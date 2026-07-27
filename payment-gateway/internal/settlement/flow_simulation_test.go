package settlement

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"payment-gateway/internal/workers"
)

type fakeBuyOrder struct {
	ID          string
	Status      string
	ProviderID  string
	TxHashOut   string
	DeliveredAt time.Time
}

func TestPixPaidPublishesBuyPaidAndSimulatedDelivery(t *testing.T) {
	bus := workers.NewEventBus()
	store := map[string]*fakeBuyOrder{
		"buy-1": {ID: "buy-1", Status: "aguardando_pix"},
	}
	seenProviders := map[string]bool{}
	var mu sync.Mutex
	done := make(chan struct{}, 1)

	ch := bus.Subscribe("buy.paid")
	go func() {
		event := <-ch
		mu.Lock()
		defer mu.Unlock()
		order := store[event.OrderID]
		if order == nil || order.Status != StatusPaidFiat {
			t.Errorf("worker received invalid order state")
			done <- struct{}{}
			return
		}
		order.Status = "enviado"
		order.TxHashOut = "buy-sim-" + order.ID
		order.DeliveredAt = time.Now()
		done <- struct{}{}
	}()

	duplicate, err := settleFakePixWebhook(bus, store, seenProviders, &mu, "buy-1", "pix-1", "concluido")
	if err != nil {
		t.Fatalf("settle webhook: %v", err)
	}
	if duplicate {
		t.Fatal("first webhook must not be duplicate")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for simulated delivery")
	}

	mu.Lock()
	order := store["buy-1"]
	mu.Unlock()
	if order.Status != "enviado" {
		t.Fatalf("expected enviado, got %s", order.Status)
	}
	if order.TxHashOut == "" {
		t.Fatal("expected simulated tx hash")
	}

	duplicate, err = settleFakePixWebhook(bus, store, seenProviders, &mu, "buy-1", "pix-1", "concluido")
	if err != nil {
		t.Fatalf("duplicate webhook: %v", err)
	}
	if !duplicate {
		t.Fatal("expected duplicate provider id to be blocked")
	}
}

func TestPixRejectedDoesNotPublishBuyPaid(t *testing.T) {
	bus := workers.NewEventBus()
	store := map[string]*fakeBuyOrder{
		"buy-2": {ID: "buy-2", Status: "aguardando_pix"},
	}
	seenProviders := map[string]bool{}
	var mu sync.Mutex
	ch := bus.Subscribe("buy.paid")

	duplicate, err := settleFakePixWebhook(bus, store, seenProviders, &mu, "buy-2", "pix-2", "rejected")
	if err != nil {
		t.Fatalf("settle webhook: %v", err)
	}
	if duplicate {
		t.Fatal("rejected webhook should not be duplicate")
	}

	select {
	case event := <-ch:
		t.Fatalf("unexpected buy.paid event: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}

	if store["buy-2"].Status != StatusError {
		t.Fatalf("expected erro, got %s", store["buy-2"].Status)
	}
}

func settleFakePixWebhook(bus *workers.EventBus, store map[string]*fakeBuyOrder, seen map[string]bool, mu *sync.Mutex, buyID, providerID, providerStatus string) (bool, error) {
	mu.Lock()
	defer mu.Unlock()

	order := store[buyID]
	if order == nil {
		return false, fmt.Errorf("buy order not found")
	}
	key := buyID + ":" + providerID
	if seen[key] {
		return true, nil
	}
	seen[key] = true

	status := PixWebhookStatus(providerStatus)
	order.Status = status
	if status == StatusPaidFiat {
		order.ProviderID = providerID
		bus.Publish(workers.Event{Type: "buy.paid", OrderID: buyID, Payload: map[string]any{"providerId": providerID}})
	}
	return false, nil
}
