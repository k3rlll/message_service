package message_usecase_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	domainComm "main/internal/domain"
	domainChat "main/internal/domain/chat_entity"
	domainEvent "main/internal/domain/event_entity"
	domain "main/internal/domain/message_entity"
	"main/internal/ratelimit"
	"main/internal/testutil/fakes"
	msgUC "main/internal/usecase/message_usecase"

	"github.com/oklog/ulid/v2"
)

// attachments пропускает вложения, если они есть в allowed
type attachments struct{ allowed map[string]bool }

func (a attachments) Validate(_ context.Context, _ string, list []domain.Attachment) ([]domain.Attachment, error) {
	for _, item := range list {
		if !a.allowed[item.Key] {
			return nil, domainComm.Invalid("attachment is not uploaded")
		}
	}
	return list, nil
}

type env struct {
	uc       *msgUC.MessageUsecase
	chats    *fakes.ChatRepo
	messages *fakes.MessageRepo
	events   *fakes.Publisher
	limiter  *fakes.Limiter

	chatID              string
	owner, admin, alice string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		chats:    fakes.NewChatRepo(),
		messages: fakes.NewMessageRepo(),
		events:   &fakes.Publisher{},
		limiter:  &fakes.Limiter{},
		chatID:   ulid.Make().String(),
		owner:    ulid.Make().String(),
		admin:    ulid.Make().String(),
		alice:    ulid.Make().String(),
	}

	now := time.Now().UTC()
	err := e.chats.CreateChat(context.Background(), &domainChat.Chat{
		ID: e.chatID, Type: domainChat.TypeGroup, Name: "g", CreatedAt: now, UpdatedAt: now,
		Members: []domainChat.Member{
			{UserID: e.owner, Role: domainChat.RoleOwner},
			{UserID: e.admin, Role: domainChat.RoleAdmin},
			{UserID: e.alice, Role: domainChat.RoleMember},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	e.uc = msgUC.NewUsecase(e.messages, e.chats, &fakes.Members{Repo: e.chats}, e.events,
		attachments{allowed: map[string]bool{"chats/" + e.chatID + "/ok.png": true}},
		e.limiter, ratelimit.Rule{Limit: 100, Window: time.Minute},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return e
}

func (e *env) send(t *testing.T, sender string, p msgUC.SendParams) *domain.Message {
	t.Helper()
	if p.ChatID == "" {
		p.ChatID = e.chatID
	}
	msg, created, err := e.uc.SendMessage(context.Background(), sender, p)
	if err != nil || !created {
		t.Fatalf("send: created=%v err=%v", created, err)
	}
	return msg
}

func TestSendMessage(t *testing.T) {
	ctx := context.Background()

	t.Run("stores, updates preview and publishes", func(t *testing.T) {
		e := newEnv(t)
		msg := e.send(t, e.alice, msgUC.SendParams{Content: "  hello  "})

		if msg.Content != "hello" || msg.Type != domain.TypeText || msg.SenderID != e.alice {
			t.Fatalf("unexpected message: %+v", msg)
		}
		chat, _ := e.chats.GetChat(ctx, e.chatID)
		if chat.LastMessage == nil || chat.LastMessage.ID != msg.ID {
			t.Fatalf("preview not updated: %+v", chat.LastMessage)
		}
		if !slices.Equal(e.events.Types(), []string{domainEvent.TypeMessageCreated}) {
			t.Fatalf("events: %v", e.events.Types())
		}
	})

	t.Run("retry with client_msg_id returns stored message without a new event", func(t *testing.T) {
		e := newEnv(t)
		first := e.send(t, e.alice, msgUC.SendParams{Content: "once", ClientMessageID: "c-1"})

		again, created, err := e.uc.SendMessage(ctx, e.alice, msgUC.SendParams{ChatID: e.chatID, Content: "once", ClientMessageID: "c-1"})
		if err != nil || created || again.ID != first.ID {
			t.Fatalf("retry: created=%v err=%v id=%s", created, err, again.ID)
		}
		if len(e.events.Events()) != 1 {
			t.Fatalf("retry must not publish: %v", e.events.Types())
		}
	})

	t.Run("published event failure does not fail the send", func(t *testing.T) {
		e := newEnv(t)
		e.events.Err = errors.New("redis is down")
		e.send(t, e.alice, msgUC.SendParams{Content: "still saved"})

		if list, _ := e.messages.ListMessages(ctx, e.chatID, "", 10); len(list) != 1 {
			t.Fatalf("message must be stored, got %d", len(list))
		}
	})

	t.Run("attachments allow empty text", func(t *testing.T) {
		e := newEnv(t)
		msg := e.send(t, e.alice, msgUC.SendParams{
			Type:        domain.TypeImage,
			Attachments: []domain.Attachment{{Key: "chats/" + e.chatID + "/ok.png"}},
		})
		if msg.Content != "" || len(msg.Attachments) != 1 {
			t.Fatalf("unexpected message: %+v", msg)
		}
	})

	t.Run("rate limiter outage lets messages through", func(t *testing.T) {
		e := newEnv(t)
		e.limiter.Err = errors.New("redis is down")
		e.send(t, e.alice, msgUC.SendParams{Content: "allowed"})
	})

	rejected := []struct {
		name   string
		sender func(*env) string
		params func(*env) msgUC.SendParams
		want   error
	}{
		{"empty text", func(e *env) string { return e.alice },
			func(e *env) msgUC.SendParams { return msgUC.SendParams{Content: "   "} }, domainComm.ErrInvalidInput},
		{"too long", func(e *env) string { return e.alice },
			func(e *env) msgUC.SendParams {
				return msgUC.SendParams{Content: strings.Repeat("я", domain.MaxContentLength+1)}
			}, domainComm.ErrInvalidInput},
		{"client_msg_id too long", func(e *env) string { return e.alice },
			func(e *env) msgUC.SendParams {
				return msgUC.SendParams{Content: "x", ClientMessageID: strings.Repeat("a", 65)}
			}, domainComm.ErrInvalidInput},
		{"reply to unknown message", func(e *env) string { return e.alice },
			func(e *env) msgUC.SendParams { return msgUC.SendParams{Content: "x", ReplyToID: ulid.Make().String()} },
			domainComm.ErrInvalidInput},
		{"attachment not uploaded", func(e *env) string { return e.alice },
			func(e *env) msgUC.SendParams {
				return msgUC.SendParams{Attachments: []domain.Attachment{{Key: "chats/" + e.chatID + "/missing.png"}}}
			}, domainComm.ErrInvalidInput},
		{"not a member", func(*env) string { return ulid.Make().String() },
			func(e *env) msgUC.SendParams { return msgUC.SendParams{Content: "x"} }, domainComm.ErrAccessDenied},
		{"unknown chat", func(e *env) string { return e.alice },
			func(e *env) msgUC.SendParams { return msgUC.SendParams{ChatID: ulid.Make().String(), Content: "x"} },
			domainChat.ErrChatNotFound},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			p := tc.params(e)
			if p.ChatID == "" {
				p.ChatID = e.chatID
			}
			_, _, err := e.uc.SendMessage(ctx, tc.sender(e), p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(e.events.Events()) != 0 {
				t.Fatalf("rejected message must not publish: %v", e.events.Types())
			}
		})
	}

	t.Run("rate limited", func(t *testing.T) {
		e := newEnv(t)
		e.limiter.Deny = true
		_, _, err := e.uc.SendMessage(ctx, e.alice, msgUC.SendParams{ChatID: e.chatID, Content: "x"})
		if !errors.Is(err, domainComm.ErrRateLimited) {
			t.Fatalf("err = %v, want ErrRateLimited", err)
		}
	})
}

func TestDeleteMessages(t *testing.T) {
	ctx := context.Background()

	t.Run("member deletes only own messages", func(t *testing.T) {
		e := newEnv(t)
		own := e.send(t, e.alice, msgUC.SendParams{Content: "mine"})
		foreign := e.send(t, e.owner, msgUC.SendParams{Content: "owner's"})

		deleted, err := e.uc.DeleteMessages(ctx, e.alice, e.chatID, []string{own.ID, foreign.ID})
		if err != nil || !slices.Equal(deleted, []string{own.ID}) {
			t.Fatalf("deleted=%v err=%v", deleted, err)
		}

		_, err = e.uc.DeleteMessages(ctx, e.alice, e.chatID, []string{foreign.ID})
		if !errors.Is(err, domainComm.ErrNotFound) {
			t.Fatalf("foreign delete: err = %v, want ErrNotFound", err)
		}
	})

	for _, moderator := range []string{"owner", "admin"} {
		t.Run(moderator+" moderates foreign messages", func(t *testing.T) {
			e := newEnv(t)
			msg := e.send(t, e.alice, msgUC.SendParams{Content: "spam"})

			actor := e.owner
			if moderator == "admin" {
				actor = e.admin
			}
			deleted, err := e.uc.DeleteMessages(ctx, actor, e.chatID, []string{msg.ID})
			if err != nil || len(deleted) != 1 {
				t.Fatalf("deleted=%v err=%v", deleted, err)
			}

			stored, _ := e.messages.GetMessage(ctx, e.chatID, msg.ID)
			if !stored.IsDeleted() || stored.Content != "" {
				t.Fatalf("message must become a tombstone: %+v", stored)
			}
			chat, _ := e.chats.GetChat(ctx, e.chatID)
			if !chat.LastMessage.IsDeleted {
				t.Fatalf("preview must show deletion: %+v", chat.LastMessage)
			}
			types := e.events.Types()
			if types[len(types)-1] != domainEvent.TypeMessageDeleted {
				t.Fatalf("events: %v", types)
			}
		})
	}
}

func TestUpdateAndReactions(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	msg := e.send(t, e.alice, msgUC.SendParams{Content: "draft"})

	if _, err := e.uc.UpdateMessage(ctx, e.owner, e.chatID, msg.ID, "hijack"); !errors.Is(err, domainComm.ErrNotFound) {
		t.Fatalf("foreign edit: err = %v", err)
	}
	updated, err := e.uc.UpdateMessage(ctx, e.alice, e.chatID, msg.ID, " final ")
	if err != nil || updated.Content != "final" || !updated.IsEdited {
		t.Fatalf("edit: %+v %v", updated, err)
	}
	chat, _ := e.chats.GetChat(ctx, e.chatID)
	if chat.LastMessage.Content != "final" {
		t.Fatalf("preview must follow the edit: %+v", chat.LastMessage)
	}

	for i := 0; i < 2; i++ {
		if _, err := e.uc.SetReaction(ctx, e.owner, e.chatID, msg.ID, "👍", true); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.uc.SetReaction(ctx, e.admin, e.chatID, msg.ID, "👍", true)
	if err != nil || len(got.Reactions) != 2 {
		t.Fatalf("reactions: %+v %v", got, err)
	}
	got, _ = e.uc.SetReaction(ctx, e.owner, e.chatID, msg.ID, "👍", false)
	if len(got.Reactions) != 1 || got.Reactions[0].UserID != e.admin {
		t.Fatalf("reaction not removed: %+v", got.Reactions)
	}

	if _, err := e.uc.SetReaction(ctx, e.owner, e.chatID, msg.ID, strings.Repeat("x", 33), true); !errors.Is(err, domainComm.ErrInvalidInput) {
		t.Fatalf("long emoji: err = %v", err)
	}
}
