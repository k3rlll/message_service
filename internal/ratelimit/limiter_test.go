package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"main/internal/ratelimit"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLimiter(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	limiter := ratelimit.NewLimiter(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	rule := ratelimit.Rule{Limit: 3, Window: time.Minute}

	for i := 1; i <= 3; i++ {
		res, err := limiter.Allow(ctx, "user", rule)
		if err != nil || !res.Allowed || res.Remaining != 3-i {
			t.Fatalf("request %d: %+v %v", i, res, err)
		}
	}

	res, err := limiter.Allow(ctx, "user", rule)
	if err != nil || res.Allowed || res.Remaining != 0 {
		t.Fatalf("4th request must be denied: %+v %v", res, err)
	}
	if res.RetryAfter <= 0 || res.RetryAfter > time.Minute {
		t.Fatalf("RetryAfter = %v", res.RetryAfter)
	}

	// Другой ключ считается отдельно
	if res, _ := limiter.Allow(ctx, "other", rule); !res.Allowed {
		t.Fatal("limits must be per key")
	}

	// Окно истекло - счетчик сброшен
	mr.FastForward(time.Minute + time.Second)
	if res, _ := limiter.Allow(ctx, "user", rule); !res.Allowed || res.Remaining != 2 {
		t.Fatalf("window must reset: %+v", res)
	}
}

func TestLimiterDisabled(t *testing.T) {
	// Нулевой лимит отключает ограничение и даже не ходит в Redis
	limiter := ratelimit.NewLimiter(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	res, err := limiter.Allow(context.Background(), "user", ratelimit.Rule{})
	if err != nil || !res.Allowed {
		t.Fatalf("disabled rule: %+v %v", res, err)
	}
}

func TestLimiterKeyAlwaysExpires(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	limiter := ratelimit.NewLimiter(redis.NewClient(&redis.Options{Addr: mr.Addr()}))

	for i := 0; i < 5; i++ {
		if _, err := limiter.Allow(ctx, "user", ratelimit.Rule{Limit: 1, Window: time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	// Счетчик без TTL заблокировал бы пользователя навсегда
	if ttl := mr.TTL("ratelimit:user"); ttl <= 0 {
		t.Fatalf("counter must have TTL, got %v", ttl)
	}
}
