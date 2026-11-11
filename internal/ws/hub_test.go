package ws

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func newTestHub(t *testing.T) *Hub {
	t.Helper()
	hub := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return hub
}

// newTestClient - клиент без настоящего соединения: Хабу нужны только userID, буфер и Close
func newTestClient(userID string, buffer int) *Client {
	return NewClient(context.Background(), nil, nil, userID, buffer, nil, "", nil)
}

func receive(t *testing.T, c *Client) []byte {
	t.Helper()
	select {
	case payload := <-c.send:
		return payload
	case <-time.After(2 * time.Second):
		t.Fatalf("client %s got nothing", c.userID)
		return nil
	}
}

func TestDeliverToAllConnectionsOfTargets(t *testing.T) {
	hub := newTestHub(t)

	aliceTab1, aliceTab2 := newTestClient("alice", 4), newTestClient("alice", 4)
	bob, carol := newTestClient("bob", 4), newTestClient("carol", 4)
	for _, c := range []*Client{aliceTab1, aliceTab2, bob, carol} {
		hub.Register(c)
	}

	hub.Deliver(context.Background(), DeliveryTask{TargetUserIDs: []string{"alice", "bob", "offline"}, Payload: []byte("hi")})

	for _, c := range []*Client{aliceTab1, aliceTab2, bob} {
		if got := string(receive(t, c)); got != "hi" {
			t.Fatalf("%s got %q", c.userID, got)
		}
	}

	// Кэрол не была в списке получателей. Проверяем после следующей доставки ей:
	// задачи Хаб обрабатывает по порядку, значит первая до нее бы уже дошла.
	hub.Deliver(context.Background(), DeliveryTask{TargetUserIDs: []string{"carol"}, Payload: []byte("marker")})
	if got := string(receive(t, carol)); got != "marker" {
		t.Fatalf("carol got %q before the marker", got)
	}
}

func TestUnregisteredClientGetsNothing(t *testing.T) {
	hub := newTestHub(t)
	gone, stays := newTestClient("u", 4), newTestClient("u", 4)
	hub.Register(gone)
	hub.Register(stays)
	hub.Unregister(gone)

	hub.Deliver(context.Background(), DeliveryTask{TargetUserIDs: []string{"u"}, Payload: []byte("x")})
	receive(t, stays)

	select {
	case p := <-gone.send:
		t.Fatalf("unregistered client got %q", p)
	default:
	}
}

func TestSlowClientIsDisconnected(t *testing.T) {
	hub := newTestHub(t)
	slow, fast := newTestClient("slow", 1), newTestClient("fast", 16)
	hub.Register(slow)
	hub.Register(fast)

	// Буфер медленного клиента - одно сообщение, никто его не читает
	for i := 0; i < 3; i++ {
		hub.Deliver(context.Background(), DeliveryTask{TargetUserIDs: []string{"slow", "fast"}, Payload: []byte{byte(i)}})
	}

	select {
	case <-slow.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("slow client must be closed")
	}

	// Быстрый клиент из-за медленного ничего не потерял
	for i := 0; i < 3; i++ {
		if got := receive(t, fast); got[0] != byte(i) {
			t.Fatalf("fast client got %v, want %d", got, i)
		}
	}
}

func TestShutdownClosesClientsAndUnblocksCallers(t *testing.T) {
	hub := NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()

	c := newTestClient("u", 1)
	hub.Register(c)
	cancel()
	<-done

	select {
	case <-c.ctx.Done():
	default:
		t.Fatal("hub shutdown must close clients")
	}

	// После остановки вызовы не должны зависать
	late := newTestClient("late", 1)
	finished := make(chan struct{})
	go func() {
		hub.Register(late)
		hub.Unregister(late)
		hub.Deliver(context.Background(), DeliveryTask{TargetUserIDs: []string{"u"}, Payload: []byte("x")})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("calls after shutdown must not block")
	}
	if late.ctx.Err() == nil {
		t.Fatal("client registered after shutdown must be closed")
	}
}

// Под -race: одновременные подключения, отключения и доставки не должны гонять данные
func TestConcurrentUse(t *testing.T) {
	hub := newTestHub(t)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := newTestClient(fmt.Sprintf("user-%d", i%5), 8)
			hub.Register(c)
			for j := 0; j < 20; j++ {
				hub.Deliver(context.Background(), DeliveryTask{
					TargetUserIDs: []string{"user-0", "user-1", "user-2", "user-3", "user-4"},
					Payload:       []byte("x"),
				})
				// Читаем, чтобы клиент не считался медленным
				select {
				case <-c.send:
				default:
				}
			}
			hub.Unregister(c)
		}(i)
	}
	wg.Wait()
}
