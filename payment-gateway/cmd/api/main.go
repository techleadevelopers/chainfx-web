package main

import (
	"context"
	"crypto/tls"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"payment-gateway/internal/certutil"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/logger"
	"payment-gateway/internal/mobile"
	"payment-gateway/internal/psp"
	"payment-gateway/internal/rpc"
	"payment-gateway/internal/server"
	"payment-gateway/internal/workers"
)

func main() {
	logger.Configure()
	log.Println("Iniciando o ecossistema concorrente em Go...")

	cfg := config.LoadConfig()
	if err := cfg.ValidateProduction(); err != nil {
		log.Fatalf("Configuracao invalida para producao: %v", err)
	}

	db, err := database.ConnectPostgres(cfg)
	if err != nil {
		log.Fatalf("Erro fatal ao conectar no banco de dados: %v", err)
	}
	defer db.Close()

	// 1. Contexto cancelável para desligamento ordenado.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.SeedNFCTerminals(ctx, cfg.NFCTerminals); err != nil {
		log.Fatalf("Erro fatal ao preparar terminais NFC: %v", err)
	}

	mailer := email.NewService(cfg)

	// 2. RPC pool (BSC). Nil-safe — workers self-disable gracefully if absent.
	var pool *rpc.Pool
	if cfg.BscRpcUrls != "" {
		pool, err = rpc.NewPool(cfg.BscRpcUrls)
		if err != nil {
			slog.Warn("RPC pool init failed, Gas Station + Auto-Sweeper will be disabled", "error", err)
		} else {
			pool.StartHealthChecks(ctx, 30*time.Second)
		}
	}

	// 3. Worker manager — includes Gas Station (Paymaster) + Auto-Sweeper.
	workerMgr := workers.NewWorkerManager(db, cfg, mailer, pool)

	// 3b. PSP Router (Efí adapter wired by default, swappable per environment).
	// Nil-safe: when Efí credentials/certificate aren't configured, PIX webhooks
	// fall back to the legacy inline parsing in internal/server (no behavior change).
	if pspRouter := newPSPRouter(cfg); pspRouter != nil {
		workerMgr.PSPRouter = pspRouter
	}

	workerMgr.StartAll(ctx)

	// 4. HTTP API server.
	api := server.New(cfg, db, workerMgr, mailer)

	// Wire the Paymaster service into the HTTP server for /v1/gas/* routes.
	if workerMgr.PaymasterService != nil {
		api.WithPaymaster(workerMgr.PaymasterService)
	}

	// Wire the PSP Router into the HTTP server so PIX webhooks route through it.
	if workerMgr.PSPRouter != nil {
		api.WithPSP(workerMgr.PSPRouter)
	}
	api.StartWebhooks(ctx)

	mob := mobile.New(cfg, db, workerMgr, workerMgr.BTCSvc, workerMgr.SolanaSvc)
	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mob.Wrap(api.Handler()),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("API HTTP iniciada na porta %s", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Erro fatal ao subir API HTTP: %v", err)
		}
	}()

	log.Println("API e motores em background foram disparados e isolados.")

	// 5. Espera sinal de desligamento.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("Sinal de encerramento recebido. Desligando sistemas de forma limpa...")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Erro ao desligar HTTP server: %v", err)
	}
	log.Println("Aplicação finalizada com 100% de segurança de dados.")
}

// newPSPRouter builds the Efí PIX adapter behind the PSP Router when Efí
// credentials + certificate are configured. Returns nil (never fatal) when
// they aren't — callers must treat a nil Router as "PSP layer disabled".
func newPSPRouter(cfg *config.Config) *psp.Router {
	clientID := strings.TrimSpace(cfg.EfiClientID)
	clientSecret := strings.TrimSpace(cfg.EfiClientSecret)
	pixKey := strings.TrimSpace(cfg.EfiPixKey)
	hasCert := strings.TrimSpace(cfg.EfiCertificatePath) != "" || strings.Trim(strings.TrimSpace(cfg.EfiCertificateP12), `"'`) != ""

	if clientID == "" || clientSecret == "" || pixKey == "" || !hasCert {
		slog.Info("psp: Efi nao configurado, PIX webhooks usarao o parsing legado inline")
		return nil
	}

	cert, err := certutil.LoadCertificate(cfg.EfiCertificatePath, cfg.EfiCertificateKey, cfg.EfiCertificateP12, cfg.EfiCertificatePass)
	if err != nil {
		slog.Warn("psp: falha ao carregar certificado Efi, PIX webhooks usarao o parsing legado inline", "error", err)
		return nil
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	efi := psp.NewEfiAdapter(clientID, clientSecret, pixKey, cfg.EfiApiBaseURL, tlsCfg)
	router := psp.NewRouter(efi, nil)
	slog.Info("psp: router inicializado", "activeProvider", router.ActiveProvider())
	return router
}
