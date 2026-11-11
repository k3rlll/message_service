// Package redis_events - шина событий между экземплярами сервиса.
//
// Долговечные события (сообщения, изменения чатов) идут через Redis Stream:
// каждое получает ID, по которому клиент после переподключения догружает пропущенное.
// Эфемерные события (typing, presence) идут через Pub/Sub: хранить их незачем.
//
// Каждый экземпляр читает весь поток сам (без consumer group), потому что событие
// должен увидеть каждый экземпляр: получатель может быть подключен к любому из них.
package redis_events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	domainChat "main/internal/domain/chat_entity"
	domainEvent "main/internal/domain/event_entity"
	ws "main/internal/ws"

	"github.com/redis/go-redis/v9"
)

const (
	streamKey        = "stream:chat_events"
	ephemeralChannel = "channel:ephemeral_events"
	streamField      = "data"
	zeroID           = "0-0"

	readBatchSize = 100
	readBlock     = time.Second
)

var streamIDPattern = regexp.MustCompile(`^\d+-\d+$`)

// MembersProvider отдает участников чата, которым нужно доставить событие
type MembersProvider interface {
	ChatUserIDs(ctx context.Context, chatID string) ([]string, error)
	Invalidate(chatID string)
}

// Deliverer доставляет payload подключенным пользователям (ws.Hub)
type Deliverer interface {
	Deliver(ctx context.Context, task ws.DeliveryTask)
}

type Config struct {
	// Примерный размер потока. Более старые события обрезаются.
	StreamMaxLen int64
	// Сколько событий максимум догружается клиенту после переподключения
	ReplayLimit int64
}

type EventBus struct {
	client  *redis.Client
	members MembersProvider
	logger  *slog.Logger
	cfg     Config

	// Закрываются, когда читатели готовы принимать события
	streamReady    chan struct{}
	ephemeralReady chan struct{}
}

func NewEventBus(
	client *redis.Client,
	members MembersProvider,
	logger *slog.Logger,
	cfg Config) *EventBus {
	return &EventBus{
		client:  client,
		members: members,
		logger:  logger,
		cfg:     cfg,

		streamReady:    make(chan struct{}),
		ephemeralReady: make(chan struct{}),
	}
}

// WaitReady ждет, пока Run начнет принимать события.
// События, опубликованные раньше, этим экземпляром не будут доставлены вживую
// (клиенты получат долговечные события при догрузке).
func (b *EventBus) WaitReady(ctx context.Context) error {
	for _, ready := range []chan struct{}{b.streamReady, b.ephemeralReady} {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// wireEvent - событие в том виде, в каком оно лежит в Redis
type wireEvent struct {
	Type         string          `json:"type"`
	ChatID       string          `json:"chat_id,omitempty"`
	Payload      json.RawMessage `json:"payload"`
	Targets      []string        `json:"targets,omitempty"`
	ExtraTargets []string        `json:"extra_targets,omitempty"`
	Except       string          `json:"except,omitempty"`
}

// clientFrame - событие в том виде, в каком его получает клиент
type clientFrame struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	ChatID  string          `json:"chat_id,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

func encode(event domainEvent.Event) ([]byte, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	return json.Marshal(wireEvent{
		Type:         event.Type,
		ChatID:       event.ChatID,
		Payload:      payload,
		Targets:      event.Targets,
		ExtraTargets: event.ExtraTargets,
		Except:       event.Except,
	})
}

func (w wireEvent) frame(id string) []byte {
	frame, _ := json.Marshal(clientFrame{ID: id, Type: w.Type, ChatID: w.ChatID, Payload: w.Payload})
	return frame
}

// Publish записывает долговечное событие в поток
func (b *EventBus) Publish(ctx context.Context, event domainEvent.Event) error {
	const op = "EventBus.Publish"

	data, err := encode(event)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	err = b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: b.cfg.StreamMaxLen,
		Approx: true, // точная обрезка заметно дороже, а точность здесь не нужна
		Values: map[string]any{streamField: data},
	}).Err()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// PublishEphemeral рассылает событие, которое не нужно хранить
func (b *EventBus) PublishEphemeral(ctx context.Context, event domainEvent.Event) error {
	const op = "EventBus.PublishEphemeral"

	data, err := encode(event)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	if err := b.client.Publish(ctx, ephemeralChannel, data).Err(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// LastEventID возвращает ID последнего события в потоке.
// Клиент получает его при подключении как стартовую позицию для будущей догрузки.
func (b *EventBus) LastEventID(ctx context.Context) (string, error) {
	entries, err := b.client.XRevRangeN(ctx, streamKey, "+", "-", 1).Result()
	if err != nil {
		return "", fmt.Errorf("EventBus.LastEventID: %w", err)
	}
	if len(entries) == 0 {
		return zeroID, nil
	}
	return entries[0].ID, nil
}

// Run читает события из Redis и передает их на доставку.
// Блокируется, пока не отменен контекст.
func (b *EventBus) Run(ctx context.Context, deliverer Deliverer) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.runEphemeral(ctx, deliverer)
	}()

	b.runStream(ctx, deliverer)
	<-done
}

func (b *EventBus) runStream(ctx context.Context, deliverer Deliverer) {
	const op = "EventBus.runStream"

	// Читаем с текущего конца потока: все более раннее уже доставлено
	// другими экземплярами, а клиенты догрузят его через Replay
	lastID, err := b.LastEventID(ctx)
	if err != nil {
		b.logger.ErrorContext(ctx, "Failed to get stream position, reading new events only",
			"op", op, "error", err)
		lastID = "$"
	}

	b.logger.InfoContext(ctx, "Stream reader started", "op", op, "from", lastID)
	close(b.streamReady)

	for ctx.Err() == nil {
		streams, err := b.client.XRead(ctx, &redis.XReadArgs{
			Streams: []string{streamKey, lastID},
			Count:   readBatchSize,
			Block:   readBlock,
		}).Result()
		if err != nil {
			// redis.Nil - за время ожидания новых событий не появилось
			if errors.Is(err, redis.Nil) || ctx.Err() != nil {
				continue
			}

			b.logger.ErrorContext(ctx, "Failed to read stream", "op", op, "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}

		for _, stream := range streams {
			for _, entry := range stream.Messages {
				lastID = entry.ID

				event, ok := decodeEntry(entry)
				if !ok {
					b.logger.ErrorContext(ctx, "Malformed event in stream",
						"op", op, "eventID", entry.ID)
					continue
				}

				// Состав чата изменился: сбрасываем кэш ДО вычисления получателей,
				// чтобы событие ушло уже по новому составу
				if domainEvent.ChangesMembership(event.Type) {
					b.members.Invalidate(event.ChatID)
				}

				b.deliver(ctx, deliverer, event, entry.ID)
			}
		}
	}

	b.logger.InfoContext(ctx, "Stream reader stopped", "op", op)
}

func (b *EventBus) runEphemeral(ctx context.Context, deliverer Deliverer) {
	const op = "EventBus.runEphemeral"

	pubsub := b.client.Subscribe(ctx, ephemeralChannel)
	defer pubsub.Close()

	// Subscribe не ждет подтверждения от Redis, Receive - ждет
	if _, err := pubsub.Receive(ctx); err != nil {
		if ctx.Err() == nil {
			b.logger.ErrorContext(ctx, "Failed to subscribe to ephemeral events", "op", op, "error", err)
		}
		return
	}
	close(b.ephemeralReady)

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-ch:
			if !ok {
				return
			}

			var event wireEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				b.logger.ErrorContext(ctx, "Malformed ephemeral event", "op", op, "error", err)
				continue
			}

			b.deliver(ctx, deliverer, event, "")
		}
	}
}

func (b *EventBus) deliver(ctx context.Context, deliverer Deliverer, event wireEvent, id string) {
	targets, err := b.targets(ctx, event)
	if err != nil {
		b.logger.ErrorContext(ctx, "Failed to resolve event targets, event dropped",
			"chatID", event.ChatID, "type", event.Type, "error", err)
		return
	}
	if len(targets) == 0 {
		return
	}

	deliverer.Deliver(ctx, ws.DeliveryTask{
		TargetUserIDs: targets,
		Payload:       event.frame(id),
	})
}

// targets вычисляет получателей события
func (b *EventBus) targets(ctx context.Context, event wireEvent) ([]string, error) {
	candidates := event.Targets

	if len(candidates) == 0 {
		if event.ChatID != "" {
			members, err := b.members.ChatUserIDs(ctx, event.ChatID)
			// Чата уже нет - остаются только явно указанные получатели
			if err != nil && !errors.Is(err, domainChat.ErrChatNotFound) {
				return nil, err
			}
			candidates = members
		}
		candidates = append(candidates, event.ExtraTargets...)
	}

	seen := make(map[string]struct{}, len(candidates))
	targets := make([]string, 0, len(candidates))
	for _, userID := range candidates {
		if _, dup := seen[userID]; dup || userID == event.Except {
			continue
		}
		seen[userID] = struct{}{}
		targets = append(targets, userID)
	}
	return targets, nil
}

// Replay возвращает события потока после afterID, адресованные пользователю,
// и ID последнего просмотренного события.
//
// complete == false значит, что догрузить без потерь нельзя: клиент отстал слишком
// сильно или нужные события уже обрезаны. Тогда клиент должен перезагрузить
// состояние через REST.
func (b *EventBus) Replay(
	ctx context.Context,
	userID string,
	afterID string,
) (frames [][]byte, lastID string, complete bool, err error) {
	const op = "EventBus.Replay"

	if !streamIDPattern.MatchString(afterID) {
		return nil, "", false, nil
	}

	// +2: одно место под само событие afterID, еще одно - чтобы заметить превышение лимита
	entries, err := b.client.XRangeN(ctx, streamKey, afterID, "+", b.cfg.ReplayLimit+2).Result()
	if err != nil {
		return nil, "", false, fmt.Errorf("%s: %w", op, err)
	}

	if len(entries) > 0 && entries[0].ID == afterID {
		// Событие-курсор еще в потоке. Поток обрезается с начала,
		// значит все, что было после курсора, тоже на месте.
		entries = entries[1:]
	} else {
		// Курсора в потоке нет. Потерь точно нет только если поток еще ни разу
		// не обрезался, то есть его длина не дошла до лимита.
		length, err := b.client.XLen(ctx, streamKey).Result()
		if err != nil {
			return nil, "", false, fmt.Errorf("%s: %w", op, err)
		}
		if length >= b.cfg.StreamMaxLen {
			return nil, "", false, nil
		}
	}

	if int64(len(entries)) > b.cfg.ReplayLimit {
		return nil, "", false, nil
	}

	lastID = afterID
	for _, entry := range entries {
		lastID = entry.ID

		event, ok := decodeEntry(entry)
		if !ok {
			continue
		}

		targets, err := b.targets(ctx, event)
		if err != nil {
			return nil, "", false, fmt.Errorf("%s: %w", op, err)
		}

		for _, target := range targets {
			if target == userID {
				frames = append(frames, event.frame(entry.ID))
				break
			}
		}
	}

	return frames, lastID, true, nil
}

func decodeEntry(entry redis.XMessage) (wireEvent, bool) {
	var event wireEvent

	data, ok := entry.Values[streamField].(string)
	if !ok {
		return event, false
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil || event.Type == "" {
		return event, false
	}
	return event, true
}
