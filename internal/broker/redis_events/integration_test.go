//go:build integration

// Интеграционный тест шины событий против настоящего Redis (testcontainers).
// miniredis обрезает поток точно, а настоящий Redis с MAXLEN ~ - приблизительно,
// поэтому логику догрузки важно проверить и на нем.
//
//	go test -tags integration ./internal/broker/...
package redis_events_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	events "main/internal/broker/redis_events"
	domainChat "main/internal/domain/chat_entity"
	domainEvent "main/internal/domain/event_entity"
	"main/internal/testutil/fakes"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func newRealRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := redis.ParseURL(uri)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestBusOnRealRedis(t *testing.T) {
	ctx := context.Background()
	client := newRealRedis(t)

	chats := fakes.NewChatRepo()
	if err := chats.CreateChat(ctx, &domainChat.Chat{ID: "chat", Members: []domainChat.Member{{UserID: "alice"}, {UserID: "bob"}}}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("live delivery of durable and ephemeral events", func(t *testing.T) {
		bus := events.NewEventBus(client, &fakes.Members{Repo: chats}, logger, events.Config{StreamMaxLen: 1000, ReplayLimit: 100})
		runCtx, cancel := context.WithCancel(ctx)
		sink := &collector{}
		done := make(chan struct{})
		go func() { bus.Run(runCtx, sink); close(done) }()
		defer func() { cancel(); <-done }()

		if err := bus.WaitReady(ctx); err != nil {
			t.Fatal(err)
		}
		if err := bus.Publish(ctx, domainEvent.Event{Type: domainEvent.TypeMessageCreated, ChatID: "chat"}); err != nil {
			t.Fatal(err)
		}
		if err := bus.PublishEphemeral(ctx, domainEvent.Event{Type: domainEvent.TypeTyping, ChatID: "chat", Except: "alice"}); err != nil {
			t.Fatal(err)
		}
		sink.wait(t, 2)
	})

	t.Run("replay with approximate trimming", func(t *testing.T) {
		// Отдельный ключ потока здесь не настраивается, поэтому чистим общий
		if err := client.Del(ctx, "stream:chat_events").Err(); err != nil {
			t.Fatal(err)
		}
		bus := events.NewEventBus(client, &fakes.Members{Repo: chats}, logger, events.Config{StreamMaxLen: 50, ReplayLimit: 1000})

		if err := bus.Publish(ctx, domainEvent.Event{Type: "x", ChatID: "chat"}); err != nil {
			t.Fatal(err)
		}
		cursor, _ := bus.LastEventID(ctx)

		// Немного событий: обрезки еще не было, догрузка полная
		for i := 0; i < 10; i++ {
			if err := bus.Publish(ctx, domainEvent.Event{Type: "x", ChatID: "chat"}); err != nil {
				t.Fatal(err)
			}
		}
		frames, _, complete, err := bus.Replay(ctx, "bob", cursor)
		if err != nil || !complete || len(frames) != 10 {
			t.Fatalf("frames=%d complete=%v err=%v", len(frames), complete, err)
		}

		// Много событий: настоящий Redis обрезает поток целыми узлами (по ~100 записей),
		// курсор пропадает, и догрузка должна честно потребовать resync
		for i := 0; i < 500; i++ {
			if err := bus.Publish(ctx, domainEvent.Event{Type: "x", ChatID: "chat"}); err != nil {
				t.Fatal(err)
			}
		}
		length, _ := client.XLen(ctx, "stream:chat_events").Result()
		if _, _, complete, err := bus.Replay(ctx, "bob", cursor); err != nil || complete {
			t.Fatalf("after trimming (stream length %d) replay must require resync: complete=%v err=%v", length, complete, err)
		}
	})
}
