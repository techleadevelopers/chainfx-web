package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/security"
)

type SweepWorker struct {
	bus    *EventBus
	db     *database.DB
	cfg    *config.Config
	client *http.Client
}

type SweepPayload struct {
	DerivationIndex int    `json:"derivationIndex"`
	To              string `json:"to"`
	Amount          string `json:"amount"`
	TokenContract   string `json:"tokenContract"`
	Network         string `json:"network"`
	IdempotencyKey  string `json:"idempotencyKey"`
}

func NewSweepWorker(bus *EventBus, db *database.DB, cfg *config.Config) *SweepWorker {
	return &SweepWorker{
		bus:    bus,
		db:     db,
		cfg:    cfg,
		client: httpclient.Default(),
	}
}

func (sw *SweepWorker) Start(ctx context.Context) {
	slog.Info("SweepWorker inicializado com segurança anti-replay.")

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando SweepWorker...")
			return
		case <-ticker.C:
			sw.executeSweeps(ctx)
		}
	}
}

func (sw *SweepWorker) executeSweeps(ctx context.Context) {
	// Stub mode for testing
	if sw.cfg.EnableSweepStub {
		if sw.cfg.IsProduction() || !sw.cfg.AllowSimulations {
			slog.Error("Sweep stub bloqueado por configuracao de producao")
			return
		}
		pending, err := sw.db.ListPendingSweeps(ctx)
		if err != nil {
			slog.Error("Erro ao listar sweeps pendentes", "error", err)
			return
		}
		for _, sweep := range pending {
			txHash := "sweep-sim-" + sweep.ID
			if err := sw.db.MarkSweep(ctx, sweep.ID, "sent", txHash); err != nil {
				slog.Error("Erro ao marcar sweep simulado", "sweep_id", sweep.ID, "error", err)
				continue
			}
			slog.Info("Sweep simulado concluído", "sweep_id", sweep.ID, "tx_hash", txHash)
		}
		return
	}

	// Validar configurações
	if sw.cfg.SignerUrl == "" || sw.cfg.TreasuryHot == "" {
		slog.Warn("SweepWorker suspenso: SIGNER_URL ou TREASURY_HOT ausentes.")
		return
	}

	// Buscar ordens para sweep
	orders, err := sw.db.OrdersToSweep(ctx)
	if err != nil {
		slog.Error("Erro ao buscar ordens para sweep", "error", err)
		return
	}

	// Criar sweeps para ordens elegíveis
	for _, order := range orders {
		if order.DerivationIndex == nil {
			continue
		}
		amount := order.AmountUSDT
		if order.DepositAmount != nil && *order.DepositAmount > 0 {
			amount = *order.DepositAmount
		}
		orderID := order.ID
		if _, err := sw.db.CreateSweep(ctx, *order.DerivationIndex, order.Address, sw.cfg.TreasuryHot, amount, &orderID); err != nil {
			slog.Error("Erro ao criar sweep", "order_id", order.ID, "error", err)
		}
	}

	// Processar sweeps pendentes
	pending, err := sw.db.ListPendingSweeps(ctx)
	if err != nil {
		slog.Error("Erro ao listar sweeps pendentes", "error", err)
		return
	}

	for _, sweep := range pending {
		sw.sendSweep(ctx, sweep)
	}
}

func (sw *SweepWorker) sendSweep(ctx context.Context, sweep database.Sweep) {
	// Usar timeout específico para cada sweep
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	payload := SweepPayload{
		DerivationIndex: sweep.ChildIndex,
		To:              sweep.ToAddr,
		Amount:          fmt.Sprintf("%.8f", sweep.Amount),
		TokenContract:   sw.cfg.BscUsdtContract,
		Network:         "BSC",
		IdempotencyKey:  sweep.ID,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Erro ao serializar payload de sweep", "sweep_id", sweep.ID, "error", err)
		_ = sw.db.MarkSweep(ctx, sweep.ID, "failed", "")
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", sw.cfg.SignerUrl+"/hd/transfer", bytes.NewBuffer(bodyBytes))
	if err != nil {
		slog.Error("Erro ao criar request de sweep", "sweep_id", sweep.ID, "error", err)
		_ = sw.db.MarkSweep(ctx, sweep.ID, "failed", "")
		return
	}

	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, sw.cfg.SignerHmacSecret, bodyBytes)

	slog.Info("Disparando sweep para signer", "index", payload.DerivationIndex, "sweep_id", sweep.ID, "amount", sweep.Amount)

	resp, err := sw.client.Do(req)
	if err != nil {
		slog.Error("Falha na comunicação com o Signer", "sweep_id", sweep.ID, "error", err)
		_ = sw.db.MarkSweep(ctx, sweep.ID, "failed", "")
		return
	}
	defer resp.Body.Close()

	// Processar resposta
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Tentar extrair txHash da resposta
		var result struct {
			TxHash string `json:"txHash"`
		}
		txHash := "signer-accepted-" + sweep.ID
		if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.TxHash != "" {
			txHash = result.TxHash
		}

		if err := sw.db.MarkSweep(ctx, sweep.ID, "sent", txHash); err != nil {
			slog.Error("Erro ao marcar sweep como enviado", "sweep_id", sweep.ID, "error", err)
			return
		}

		slog.Info("Sweep executado com sucesso", "sweep_id", sweep.ID, "tx_hash", txHash)
		orderID := ""
		if sweep.OrderID != nil {
			orderID = *sweep.OrderID
		}
		sw.bus.Publish(Event{Type: "sweep.sent", OrderID: orderID, Payload: map[string]any{"txHash": txHash}})
	} else {
		// Ler corpo do erro para diagnóstico
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		errMsg := fmt.Sprintf("signer status %d", resp.StatusCode)
		if errBody.Error != "" {
			errMsg = errBody.Error
		}

		_ = sw.db.MarkSweep(ctx, sweep.ID, "failed", errMsg)
		slog.Error("Signer rejeitou sweep", "sweep_id", sweep.ID, "status", resp.StatusCode, "error", errMsg)
	}
}
