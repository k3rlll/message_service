package presence_test

import (
	"context"
	"testing"
	"time"

	"main/internal/presence"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newStore(t *testing.T, ttl time.Duration) (*presence.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	return presence.NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), ttl, time.Minute), mr
}

func TestMultipleConnections(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, time.Minute)

	if first, err := store.Connect(ctx, "u", "tab-1"); err != nil || !first {
		t.Fatalf("first connection must bring the user online: %v %v", first, err)
	}
	if first, _ := store.Connect(ctx, "u", "tab-2"); first {
		t.Fatal("second connection must not report online again")
	}

	if offline, _ := store.Disconnect(ctx, "u", "tab-1"); offline {
		t.Fatal("user still has tab-2")
	}
	online, _ := store.Online(ctx, []string{"u", "nobody"})
	if !online["u"] || online["nobody"] {
		t.Fatalf("unexpected online map: %v", online)
	}

	if offline, _ := store.Disconnect(ctx, "u", "tab-2"); !offline {
		t.Fatal("last disconnect must report offline")
	}
	if online, _ := store.Online(ctx, []string{"u"}); online["u"] {
		t.Fatal("user must be offline")
	}
}

func TestStaleConnectionExpires(t *testing.T) {
	// Экземпляр сервиса упал и не вызвал Disconnect: соединение протухает само
	ctx := context.Background()
	store, _ := newStore(t, 50*time.Millisecond)

	if _, err := store.Connect(ctx, "u", "dead-instance"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	if online, _ := store.Online(ctx, []string{"u"}); online["u"] {
		t.Fatal("stale connection must not count as online")
	}
	// Новое соединение после протухшего снова делает пользователя онлайн
	if first, _ := store.Connect(ctx, "u", "new"); !first {
		t.Fatal("connect after expiry must report online")
	}
}

func TestHeartbeatKeepsConnectionAlive(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, 80*time.Millisecond)

	if _, err := store.Connect(ctx, "u", "c"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		time.Sleep(40 * time.Millisecond)
		if err := store.Heartbeat(ctx, "u", "c"); err != nil {
			t.Fatal(err)
		}
	}
	if online, _ := store.Online(ctx, []string{"u"}); !online["u"] {
		t.Fatal("heartbeat must keep the connection alive")
	}
}

func TestTypingThrottle(t *testing.T) {
	ctx := context.Background()
	store, mr := newStore(t, time.Minute)

	if first, _ := store.TryTyping(ctx, "chat", "u"); !first {
		t.Fatal("first typing signal must pass")
	}
	if again, _ := store.TryTyping(ctx, "chat", "u"); again {
		t.Fatal("repeated signal within TTL must be suppressed")
	}
	if other, _ := store.TryTyping(ctx, "other-chat", "u"); !other {
		t.Fatal("typing is tracked per chat")
	}

	mr.FastForward(2 * time.Minute)
	if after, _ := store.TryTyping(ctx, "chat", "u"); !after {
		t.Fatal("signal after TTL must pass again")
	}
}
