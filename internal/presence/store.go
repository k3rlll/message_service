// Package presence хранит в Redis, кто сейчас онлайн и кто печатает.
package presence

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store хранит онлайн-статус в sorted set на пользователя:
// элемент - ID соединения, score - время, до которого соединение считается живым.
//
// Так корректно учитываются несколько устройств и несколько экземпляров сервиса,
// а соединения упавшего экземпляра сами "протухают" по score.
type Store struct {
	client    *redis.Client
	ttl       time.Duration
	typingTTL time.Duration
}

func NewStore(client *redis.Client, ttl, typingTTL time.Duration) *Store {
	return &Store{
		client:    client,
		ttl:       ttl,
		typingTTL: typingTTL,
	}
}

func presenceKey(userID string) string {
	return "presence:" + userID
}

func score(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}

// Connect регистрирует соединение. Возвращает true, если пользователь
// только что стал онлайн, то есть других живых соединений у него не было.
func (s *Store) Connect(ctx context.Context, userID, connID string) (bool, error) {
	now := time.Now()
	key := presenceKey(userID)

	var alive *redis.IntCmd
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZRemRangeByScore(ctx, key, "-inf", score(now))
		alive = pipe.ZCard(ctx, key)
		pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.Add(s.ttl).UnixMilli()), Member: connID})
		pipe.Expire(ctx, key, s.ttl)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("presence.Connect: %w", err)
	}
	return alive.Val() == 0, nil
}

// Heartbeat продлевает жизнь соединения. Вызывается периодически, пока соединение открыто.
func (s *Store) Heartbeat(ctx context.Context, userID, connID string) error {
	key := presenceKey(userID)

	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZAdd(ctx, key, redis.Z{Score: float64(time.Now().Add(s.ttl).UnixMilli()), Member: connID})
		pipe.Expire(ctx, key, s.ttl)
		return nil
	})
	if err != nil {
		return fmt.Errorf("presence.Heartbeat: %w", err)
	}
	return nil
}

// Disconnect убирает соединение. Возвращает true, если пользователь
// стал оффлайн, то есть живых соединений не осталось.
func (s *Store) Disconnect(ctx context.Context, userID, connID string) (bool, error) {
	key := presenceKey(userID)

	var alive *redis.IntCmd
	_, err := s.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZRem(ctx, key, connID)
		pipe.ZRemRangeByScore(ctx, key, "-inf", score(time.Now()))
		alive = pipe.ZCard(ctx, key)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("presence.Disconnect: %w", err)
	}
	return alive.Val() == 0, nil
}

// Online возвращает онлайн-статус для каждого из пользователей
func (s *Store) Online(ctx context.Context, userIDs []string) (map[string]bool, error) {
	now := score(time.Now())

	counts := make([]*redis.IntCmd, len(userIDs))
	_, err := s.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, userID := range userIDs {
			// "(" - строгое неравенство: соединение со score == now уже не живое
			counts[i] = pipe.ZCount(ctx, presenceKey(userID), "("+now, "+inf")
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("presence.Online: %w", err)
	}

	online := make(map[string]bool, len(userIDs))
	for i, userID := range userIDs {
		online[userID] = counts[i].Val() > 0
	}
	return online, nil
}

// TryTyping отмечает, что пользователь печатает в чате.
// Возвращает false, если отметка уже стоит: тогда рассылать событие повторно не нужно.
// Отметка живет typingTTL, клиент должен повторять сигнал, пока пользователь печатает.
func (s *Store) TryTyping(ctx context.Context, chatID, userID string) (bool, error) {
	key := "typing:" + chatID + ":" + userID

	set, err := s.client.SetNX(ctx, key, 1, s.typingTTL).Result()
	if err != nil {
		return false, fmt.Errorf("presence.TryTyping: %w", err)
	}
	return set, nil
}
