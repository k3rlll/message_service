package fakes

import (
	"context"
	"sync"

	domainComm "main/internal/domain"
	domainEvent "main/internal/domain/event_entity"
	"main/internal/ratelimit"
)

// Publisher записывает опубликованные события. Если Err задан, публикация падает.
type Publisher struct {
	mu     sync.Mutex
	events []domainEvent.Event
	Err    error
}

func (p *Publisher) Publish(_ context.Context, event domainEvent.Event) error {
	return p.record(event)
}

func (p *Publisher) PublishEphemeral(_ context.Context, event domainEvent.Event) error {
	return p.record(event)
}

func (p *Publisher) record(event domainEvent.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Err != nil {
		return p.Err
	}
	p.events = append(p.events, event)
	return nil
}

// Events возвращает копию списка опубликованных событий
func (p *Publisher) Events() []domainEvent.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domainEvent.Event(nil), p.events...)
}

// Types возвращает типы опубликованных событий по порядку
func (p *Publisher) Types() []string {
	events := p.Events()
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.Type
	}
	return types
}

// Members проверяет членство напрямую по репозиторию, без кэша.
// Считает вызовы Invalidate, чтобы тесты могли проверить сброс кэша.
type Members struct {
	Repo *ChatRepo

	mu          sync.Mutex
	invalidated []string
}

func (m *Members) MemberRole(ctx context.Context, userID, chatID string) (string, error) {
	members, err := m.Repo.ChatMembers(ctx, chatID)
	if err != nil {
		return "", err
	}
	for _, member := range members {
		if member.UserID == userID {
			return member.Role, nil
		}
	}
	return "", domainComm.ErrAccessDenied
}

func (m *Members) ChatUserIDs(ctx context.Context, chatID string) ([]string, error) {
	members, err := m.Repo.ChatMembers(ctx, chatID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(members))
	for i, member := range members {
		ids[i] = member.UserID
	}
	return ids, nil
}

func (m *Members) Invalidate(chatID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidated = append(m.invalidated, chatID)
}

// Invalidated возвращает чаты, для которых сбрасывался кэш
func (m *Members) Invalidated() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.invalidated...)
}

// Limiter - rate limiter с заданным решением
type Limiter struct {
	Deny bool
	Err  error
}

func (l *Limiter) Allow(context.Context, string, ratelimit.Rule) (ratelimit.Result, error) {
	if l.Err != nil {
		return ratelimit.Result{}, l.Err
	}
	return ratelimit.Result{Allowed: !l.Deny}, nil
}
