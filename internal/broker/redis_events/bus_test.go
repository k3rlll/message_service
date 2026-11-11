package redis_events_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	events "main/internal/broker/redis_events"
	domainChat "main/internal/domain/chat_entity"
	domainEvent "main/internal/domain/event_entity"
	"main/internal/testutil/fakes"
	"main/internal/ws"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// collector собирает задачи доставки вместо Хаба
type collector struct {
	mu    sync.Mutex
	tasks []ws.DeliveryTask
}

func (c *collector) Deliver(_ context.Context, task ws.DeliveryTask) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tasks = append(c.tasks, task)
}

func (c *collector) wait(t *testing.T, n int) []ws.DeliveryTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		got := append([]ws.DeliveryTask(nil), c.tasks...)
		c.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d deliveries, want %d", len(got), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type env struct {
	bus     *events.EventBus
	members *fakes.Members
	chatID  string
}

func newEnv(t *testing.T, cfg events.Config) *env {
	t.Helper()
	chats := fakes.NewChatRepo()
	e := &env{members: &fakes.Members{Repo: chats}, chatID: "chat"}

	err := chats.CreateChat(context.Background(), &domainChat.Chat{
		ID: e.chatID, Type: domainChat.TypeGroup,
		Members: []domainChat.Member{{UserID: "alice"}, {UserID: "bob"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	e.bus = events.NewEventBus(client, e.members, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	return e
}

func (e *env) run(t *testing.T) *collector {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sink := &collector{}
	done := make(chan struct{})
	go func() { e.bus.Run(ctx, sink); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	if err := e.bus.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	return sink
}

func (e *env) publish(t *testing.T, event domainEvent.Event) {
	t.Helper()
	if event.ChatID == "" {
		event.ChatID = e.chatID
	}
	if err := e.bus.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

type frame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	ChatID  string          `json:"chat_id"`
	Payload json.RawMessage `json:"payload"`
}

func decode(t *testing.T, raw []byte) frame {
	t.Helper()
	var f frame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTargets(t *testing.T) {
	e := newEnv(t, events.Config{StreamMaxLen: 100, ReplayLimit: 100})
	sink := e.run(t)

	e.publish(t, domainEvent.Event{Type: "a", Payload: 1})                                     // участники чата
	e.publish(t, domainEvent.Event{Type: "b", Payload: 2, Except: "alice"})                    // кроме инициатора
	e.publish(t, domainEvent.Event{Type: "c", Payload: 3, ExtraTargets: []string{"carol"}})    // плюс внешний
	e.publish(t, domainEvent.Event{Type: "d", Payload: 4, Targets: []string{"dave", "dave"}})  // явный список
	e.publish(t, domainEvent.Event{Type: "e", ChatID: "deleted", ExtraTargets: []string{"x"}}) // чата нет

	tasks := sink.wait(t, 5)
	want := [][]string{{"alice", "bob"}, {"bob"}, {"alice", "bob", "carol"}, {"dave"}, {"x"}}
	for i, task := range tasks {
		got := slices.Clone(task.TargetUserIDs)
		slices.Sort(got)
		if !slices.Equal(got, want[i]) {
			t.Errorf("event %d: targets %v, want %v", i, got, want[i])
		}
		if f := decode(t, task.Payload); f.ID == "" {
			t.Errorf("durable event %d must carry its stream ID", i)
		}
	}
}

func TestMembershipEventInvalidatesCache(t *testing.T) {
	e := newEnv(t, events.Config{StreamMaxLen: 100, ReplayLimit: 100})
	sink := e.run(t)

	e.publish(t, domainEvent.Event{Type: domainEvent.TypeMessageCreated})
	e.publish(t, domainEvent.Event{Type: domainEvent.TypeChatMemberAdded})
	sink.wait(t, 2)

	if got := e.members.Invalidated(); !slices.Equal(got, []string{e.chatID}) {
		t.Fatalf("only membership events invalidate the cache, got %v", got)
	}
}

func TestEphemeralEventsHaveNoID(t *testing.T) {
	e := newEnv(t, events.Config{StreamMaxLen: 100, ReplayLimit: 100})
	sink := e.run(t)

	err := e.bus.PublishEphemeral(context.Background(), domainEvent.Event{Type: domainEvent.TypeTyping, ChatID: e.chatID, Except: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	task := sink.wait(t, 1)[0]
	if f := decode(t, task.Payload); f.ID != "" || f.Type != domainEvent.TypeTyping {
		t.Fatalf("unexpected ephemeral frame: %+v", f)
	}
	if !slices.Equal(task.TargetUserIDs, []string{"bob"}) {
		t.Fatalf("targets = %v", task.TargetUserIDs)
	}

	// И в поток они не попадают: догружать нечего
	_, _, complete, err := e.bus.Replay(context.Background(), "bob", "0-0")
	if err != nil || !complete {
		t.Fatal(err)
	}
}

func TestReplay(t *testing.T) {
	ctx := context.Background()

	t.Run("returns only events after the cursor addressed to the user", func(t *testing.T) {
		e := newEnv(t, events.Config{StreamMaxLen: 100, ReplayLimit: 100})
		e.publish(t, domainEvent.Event{Type: "before"})
		cursor, _ := e.bus.LastEventID(ctx)

		e.publish(t, domainEvent.Event{Type: "for-both"})
		e.publish(t, domainEvent.Event{Type: "not-for-bob", Except: "bob"})
		e.publish(t, domainEvent.Event{Type: "for-bob-too"})

		frames, last, complete, err := e.bus.Replay(ctx, "bob", cursor)
		if err != nil || !complete {
			t.Fatalf("complete=%v err=%v", complete, err)
		}
		var types []string
		for _, raw := range frames {
			types = append(types, decode(t, raw).Type)
		}
		if !slices.Equal(types, []string{"for-both", "for-bob-too"}) {
			t.Fatalf("replayed %v", types)
		}
		if end, _ := e.bus.LastEventID(ctx); last != end {
			t.Fatalf("cursor after replay = %s, want %s", last, end)
		}

		// С новым курсором догружать нечего
		frames, _, complete, _ = e.bus.Replay(ctx, "bob", last)
		if len(frames) != 0 || !complete {
			t.Fatalf("nothing left to replay, got %d frames", len(frames))
		}
	})

	t.Run("from an empty stream", func(t *testing.T) {
		e := newEnv(t, events.Config{StreamMaxLen: 100, ReplayLimit: 100})
		cursor, _ := e.bus.LastEventID(ctx)
		if cursor != "0-0" {
			t.Fatalf("cursor of an empty stream = %s", cursor)
		}
		e.publish(t, domainEvent.Event{Type: "first"})

		frames, _, complete, err := e.bus.Replay(ctx, "alice", cursor)
		if err != nil || !complete || len(frames) != 1 {
			t.Fatalf("frames=%d complete=%v err=%v", len(frames), complete, err)
		}
	})

	t.Run("too far behind", func(t *testing.T) {
		e := newEnv(t, events.Config{StreamMaxLen: 100, ReplayLimit: 3})
		cursor, _ := e.bus.LastEventID(ctx)
		for i := 0; i < 4; i++ {
			e.publish(t, domainEvent.Event{Type: "x"})
		}
		if _, _, complete, _ := e.bus.Replay(ctx, "alice", cursor); complete {
			t.Fatal("more events than the replay limit must require resync")
		}
	})

	t.Run("cursor trimmed away", func(t *testing.T) {
		e := newEnv(t, events.Config{StreamMaxLen: 3, ReplayLimit: 100})
		e.publish(t, domainEvent.Event{Type: "x"})
		cursor, _ := e.bus.LastEventID(ctx)
		for i := 0; i < 5; i++ {
			e.publish(t, domainEvent.Event{Type: "x"})
		}
		if _, _, complete, _ := e.bus.Replay(ctx, "alice", cursor); complete {
			t.Fatal("trimmed cursor must require resync")
		}
	})

	t.Run("malformed cursor", func(t *testing.T) {
		e := newEnv(t, events.Config{StreamMaxLen: 100, ReplayLimit: 100})
		for _, cursor := range []string{"", "abc", "1-", "-1", "1-2-3"} {
			if _, _, complete, err := e.bus.Replay(ctx, "alice", cursor); complete || err != nil {
				t.Errorf("cursor %q: complete=%v err=%v", cursor, complete, err)
			}
		}
	})
}
