package workers

import (
	"sync"
	"testing"
)

func BenchmarkEventBusPublishNoSubscriber(b *testing.B) {
	bus := NewEventBus()
	event := Event{Type: "buy.paid", OrderID: "bench"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(event)
	}
}

func BenchmarkEventBusPublishSingleSubscriber(b *testing.B) {
	bus := NewEventBus()
	ch := bus.Subscribe("buy.paid")
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	event := Event{Type: "buy.paid", OrderID: "bench"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(event)
	}
	b.StopTimer()
	close(ch)
	<-done
}

func BenchmarkEventBusPublishManySubscribers(b *testing.B) {
	bus := NewEventBus()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		ch := bus.Subscribe("buy.paid")
		wg.Add(1)
		go func(ch chan Event) {
			defer wg.Done()
			for range ch {
			}
		}(ch)
	}
	event := Event{Type: "buy.paid", OrderID: "bench"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Publish(event)
	}
	b.StopTimer()
	bus.mu.RLock()
	var subscribers []chan Event
	for _, ch := range bus.subscribers["buy.paid"] {
		subscribers = append(subscribers, ch)
	}
	bus.mu.RUnlock()
	for _, ch := range subscribers {
		close(ch)
	}
	wg.Wait()
}
