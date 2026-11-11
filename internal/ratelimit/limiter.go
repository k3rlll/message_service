// Package ratelimit - ограничение частоты запросов на Redis.
// Счетчики общие для всех экземпляров сервиса.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Rule - не больше Limit действий за Window
type Rule struct {
	Limit  int           `yaml:"limit"`
	Window time.Duration `yaml:"window"`
}

type Result struct {
	Allowed   bool
	Limit     int
	Remaining int
	// Через сколько окно сбросится и счетчик обнулится
	RetryAfter time.Duration
}

// Алгоритм фиксированного окна: окно открывается первым запросом и живет Window.
// INCR и установка TTL выполняются атомарно в одном скрипте, иначе при сбое
// между ними счетчик остался бы без TTL и заблокировал пользователя навсегда.
var incrScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {count, redis.call('PTTL', KEYS[1])}
`)

type Limiter struct {
	client *redis.Client
}

func NewLimiter(client *redis.Client) *Limiter {
	return &Limiter{client: client}
}

// Allow учитывает одно действие по ключу и сообщает, укладывается ли оно в лимит.
// Правило с Limit <= 0 отключает ограничение.
func (l *Limiter) Allow(ctx context.Context, key string, rule Rule) (Result, error) {
	if rule.Limit <= 0 {
		return Result{Allowed: true}, nil
	}

	values, err := incrScript.Run(ctx, l.client,
		[]string{"ratelimit:" + key}, rule.Window.Milliseconds()).Int64Slice()
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit.Allow: %w", err)
	}
	if len(values) != 2 {
		return Result{}, fmt.Errorf("ratelimit.Allow: unexpected script result %v", values)
	}

	count, ttl := int(values[0]), time.Duration(values[1])*time.Millisecond

	return Result{
		Allowed:    count <= rule.Limit,
		Limit:      rule.Limit,
		Remaining:  max(rule.Limit-count, 0),
		RetryAfter: max(ttl, 0),
	}, nil
}
