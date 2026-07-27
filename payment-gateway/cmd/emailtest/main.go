package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/email"
)

func main() {
	to := flag.String("to", "", "destination email")
	flag.Parse()
	if *to == "" {
		log.Fatal("use -to email@example.com")
	}

	cfg := config.LoadConfig()
	mailer := email.NewService(cfg)
	if !mailer.Enabled() {
		log.Fatal("SMTP nao configurado: defina SMTP_HOST, SMTP_PORT, SMTP_FROM_EMAIL, SMTP_USER e SMTP_PASS quando necessario")
	}

	messages := []struct {
		name string
		send func() error
	}{
		{
			name: "operational",
			send: func() error {
				return mailer.Send(email.Message{
					To:      *to,
					Subject: "ChainFX - teste operacional de email",
					Body:    "Servico de email ChainFX operacional.",
				})
			},
		},
		{
			name: "buy_receipt",
			send: func() error {
				return mailer.SendBuyCompleted(*to, email.Receipt{
					OrderID:      "buy_test_20260711",
					Asset:        "USDT",
					Network:      "BSC",
					AmountFiat:   527.50,
					FeeFiat:      27.50,
					PayoutFiat:   500.00,
					CryptoAmount: 91.74311927,
					Rate:         5.45,
					Wallet:       "0x7e3BF3FDfeF16040CE3ec60A663381766d3dB375",
					TxHash:       "0x9c7a7c77d4b9ed0f6a5b3a01f2e45d2e7c88b0a5d7f4d3f1b2c0a9e8d7c6b5a4",
					CompletedAt:  time.Now(),
				})
			},
		},
		{
			name: "sell_receipt",
			send: func() error {
				return mailer.SendSellCompleted(*to, email.Receipt{
					OrderID:      "sell_test_20260711",
					Asset:        "USDT",
					Network:      "BSC",
					AmountFiat:   459.00,
					FeeFiat:      41.00,
					PayoutFiat:   459.00,
					CryptoAmount: 100.00000000,
					Rate:         4.59,
					Wallet:       "0x7e3BF3FDfeF16040CE3ec60A663381766d3dB375",
					TxHash:       "E60711TESTCHAINFXPIXENDTOENDID000001",
					CompletedAt:  time.Now(),
				})
			},
		},
		{
			name: "marketing",
			send: func() error {
				return mailer.SendMarketing(*to, email.MarketingCampaign{
					Subject:     "Receba USDT via PIX com a ChainFX",
					Headline:    "Crypto settlement with PIX",
					Intro:       "A ChainFX conecta pagamentos PIX a liquidação em USDT na BSC para operações rápidas e rastreáveis.",
					Body:        "Use API, webhooks e recibos transacionais para acompanhar cada etapa da compra ou venda.",
					CTA:         "Abrir ChainFX",
					CTAURL:      "https://www.chainfx.store/",
					Unsubscribe: "https://www.chainfx.store/unsubscribe",
				})
			},
		},
	}

	for _, msg := range messages {
		if err := msg.send(); err != nil {
			log.Fatalf("%s failed: %v", msg.name, err)
		}
		fmt.Println("sent:", msg.name)
	}
}
