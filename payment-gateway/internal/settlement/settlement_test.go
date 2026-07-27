package settlement

import "testing"

func TestPixWebhookStatus(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{name: "concluido", input: "concluido", expect: StatusPaidFiat},
		{name: "concluida", input: "CONCLUIDA", expect: StatusPaidFiat},
		{name: "pending", input: "pending", expect: StatusError},
		{name: "empty", input: "", expect: StatusError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PixWebhookStatus(tt.input); got != tt.expect {
				t.Fatalf("expected %s, got %s", tt.expect, got)
			}
		})
	}
}

func TestShouldPublishBuyPaid(t *testing.T) {
	if !ShouldPublishBuyPaid(StatusPaidFiat) {
		t.Fatal("expected pago_fiat to publish buy.paid")
	}
	if ShouldPublishBuyPaid(StatusError) {
		t.Fatal("expected erro not to publish buy.paid")
	}
}
