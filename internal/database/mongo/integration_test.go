//go:build integration

// Интеграционные тесты репозиториев против настоящего MongoDB (testcontainers).
// Проверяют то, что не покрывают in-memory фейки: индексы, $text, $max, array filters.
//
//	go test -tags integration ./internal/database/...
package mongo_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"main/internal/configs"
	mongodb "main/internal/database/mongo"
	chatRepo "main/internal/database/mongo/chat_repository"
	msgRepo "main/internal/database/mongo/message_repository"
	domainComm "main/internal/domain"
	domainChat "main/internal/domain/chat_entity"
	domain "main/internal/domain/message_entity"

	"github.com/oklog/ulid/v2"
	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/mongo"
)

func newDB(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()

	container, err := tcmongo.Run(ctx, "mongo:7.0")
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}

	client, err := mongodb.ConnectMongoDB(ctx, configs.MongoConfig{URI: uri, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	return client.Database("test_" + ulid.Make().String())
}

func newRepos(t *testing.T) (*chatRepo.ChatRepository, *msgRepo.MessageRepository) {
	t.Helper()
	db := newDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	chats := chatRepo.NewChatRepository(db, logger)
	messages := msgRepo.NewMessageRepository(db, logger)

	ctx := context.Background()
	if err := chats.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := messages.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	// Повторный вызов не должен падать: индексы создаются при каждом старте сервиса
	if err := messages.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes must be idempotent: %v", err)
	}
	return chats, messages
}

func id() string { return ulid.Make().String() }

func newGroup(t *testing.T, repo *chatRepo.ChatRepository, members ...domainChat.Member) *domainChat.Chat {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	chat := &domainChat.Chat{ID: id(), Type: domainChat.TypeGroup, Name: "g", Members: members, CreatedAt: now, UpdatedAt: now}
	if err := repo.CreateChat(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	return chat
}

func save(t *testing.T, repo *msgRepo.MessageRepository, chatID, sender, content string) *domain.Message {
	t.Helper()
	now := time.Now().UTC()
	msg := &domain.Message{ID: id(), ChatID: chatID, SenderID: sender, Type: domain.TypeText, Content: content, CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestChatRepository(t *testing.T) {
	ctx := context.Background()
	chats, _ := newRepos(t)
	alice, bob, carol := id(), id(), id()

	t.Run("direct chat is unique per pair", func(t *testing.T) {
		key := domainChat.DirectKey(alice, bob)
		chat := &domainChat.Chat{ID: id(), Type: domainChat.TypeDirect, DirectKey: key}
		if err := chats.CreateChat(ctx, chat); err != nil {
			t.Fatal(err)
		}
		dup := &domainChat.Chat{ID: id(), Type: domainChat.TypeDirect, DirectKey: domainChat.DirectKey(bob, alice)}
		if err := chats.CreateChat(ctx, dup); !errors.Is(err, domainComm.ErrDuplicate) {
			t.Fatalf("err = %v, want ErrDuplicate", err)
		}
		got, err := chats.GetDirectChat(ctx, key)
		if err != nil || got.ID != chat.ID {
			t.Fatalf("GetDirectChat: %+v %v", got, err)
		}
		// Групповые чаты без direct_key уникальный индекс не задевает
		newGroup(t, chats)
		newGroup(t, chats)
	})

	t.Run("members and roles", func(t *testing.T) {
		chat := newGroup(t, chats,
			domainChat.Member{UserID: alice, Role: domainChat.RoleOwner},
			domainChat.Member{UserID: bob, Role: domainChat.RoleMember})

		if err := chats.AddMember(ctx, chat.ID, domainChat.Member{UserID: carol, Role: domainChat.RoleMember}); err != nil {
			t.Fatal(err)
		}
		if err := chats.AddMember(ctx, chat.ID, domainChat.Member{UserID: carol, Role: domainChat.RoleMember}); !errors.Is(err, domainComm.ErrConflict) {
			t.Fatalf("duplicate member: err = %v", err)
		}

		// Передача владения одной операцией через array filters
		if err := chats.SetRoles(ctx, chat.ID, map[string]string{bob: domainChat.RoleOwner, alice: domainChat.RoleAdmin}); err != nil {
			t.Fatal(err)
		}
		got, _ := chats.GetChat(ctx, chat.ID)
		roles := map[string]string{}
		for _, m := range got.Members {
			roles[m.UserID] = m.Role
		}
		if roles[bob] != domainChat.RoleOwner || roles[alice] != domainChat.RoleAdmin || roles[carol] != domainChat.RoleMember {
			t.Fatalf("roles = %v", roles)
		}

		if err := chats.RemoveMember(ctx, chat.ID, carol); err != nil {
			t.Fatal(err)
		}
		if err := chats.RemoveMember(ctx, chat.ID, carol); !errors.Is(err, domainComm.ErrNotFound) {
			t.Fatalf("remove twice: err = %v", err)
		}

		contacts, _ := chats.ContactIDs(ctx, alice)
		if !slices.Contains(contacts, bob) || slices.Contains(contacts, alice) {
			t.Fatalf("contacts = %v", contacts)
		}
	})

	t.Run("read mark only moves forward", func(t *testing.T) {
		chat := newGroup(t, chats, domainChat.Member{UserID: alice, Role: domainChat.RoleMember})
		older, newer := id(), id()

		if changed, err := chats.MarkRead(ctx, chat.ID, alice, newer); err != nil || !changed {
			t.Fatalf("first mark: %v %v", changed, err)
		}
		if changed, _ := chats.MarkRead(ctx, chat.ID, alice, older); changed {
			t.Fatal("older mark must not move the read marker back")
		}
		got, _ := chats.GetChat(ctx, chat.ID)
		if m, _ := got.Member(alice); m.LastReadMessageID != newer {
			t.Fatalf("read marker = %s, want %s", m.LastReadMessageID, newer)
		}
	})

	t.Run("last message preview ignores out of order updates", func(t *testing.T) {
		chat := newGroup(t, chats)
		older := domainChat.LastMessage{ID: id(), Content: "older", CreatedAt: time.Now().UTC()}
		newer := domainChat.LastMessage{ID: id(), Content: "newer", CreatedAt: time.Now().UTC()}

		for _, last := range []domainChat.LastMessage{newer, older} {
			if err := chats.SetLastMessage(ctx, chat.ID, last); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := chats.GetChat(ctx, chat.ID)
		if got.LastMessage.Content != "newer" {
			t.Fatalf("preview = %+v", got.LastMessage)
		}

		if err := chats.PatchLastMessage(ctx, chat.ID, []string{newer.ID}, "", true); err != nil {
			t.Fatal(err)
		}
		got, _ = chats.GetChat(ctx, chat.ID)
		if !got.LastMessage.IsDeleted || got.LastMessage.Content != "" {
			t.Fatalf("preview after delete = %+v", got.LastMessage)
		}
	})

	t.Run("list by activity with cursor", func(t *testing.T) {
		user := id()
		var created []*domainChat.Chat
		for i := 0; i < 3; i++ {
			chat := newGroup(t, chats, domainChat.Member{UserID: user, Role: domainChat.RoleMember})
			last := domainChat.LastMessage{ID: id(), CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second)}
			if err := chats.SetLastMessage(ctx, chat.ID, last); err != nil {
				t.Fatal(err)
			}
			created = append(created, chat)
		}

		page, err := chats.ListChats(ctx, user, time.Time{}, 2)
		if err != nil || len(page) != 2 || page[0].ID != created[2].ID || page[1].ID != created[1].ID {
			t.Fatalf("first page: %v %v", ids(page), err)
		}
		page, _ = chats.ListChats(ctx, user, page[1].UpdatedAt, 2)
		if len(page) != 1 || page[0].ID != created[0].ID {
			t.Fatalf("second page: %v", ids(page))
		}
	})

	t.Run("pins", func(t *testing.T) {
		chat := newGroup(t, chats)
		msg := id()
		if added, _ := chats.AddPin(ctx, chat.ID, msg); !added {
			t.Fatal("first pin must change the chat")
		}
		if added, _ := chats.AddPin(ctx, chat.ID, msg); added {
			t.Fatal("repeated pin must not change the chat")
		}
		if removed, _ := chats.RemovePin(ctx, chat.ID, msg); !removed {
			t.Fatal("unpin must change the chat")
		}
	})
}

func TestMessageRepository(t *testing.T) {
	ctx := context.Background()
	_, messages := newRepos(t)
	chatID, alice, bob := id(), id(), id()

	var sent []*domain.Message
	for _, text := range []string{"hello world", "golang is fun", "hello again", "something else"} {
		sent = append(sent, save(t, messages, chatID, alice, text))
	}
	save(t, messages, id(), alice, "hello from another chat")

	t.Run("client message id is unique per sender and chat", func(t *testing.T) {
		msg := &domain.Message{ID: id(), ChatID: chatID, SenderID: bob, Type: domain.TypeText, Content: "x", ClientMessageID: "c-1"}
		if err := messages.SaveMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
		dup := &domain.Message{ID: id(), ChatID: chatID, SenderID: bob, Type: domain.TypeText, Content: "x", ClientMessageID: "c-1"}
		if err := messages.SaveMessage(ctx, dup); !errors.Is(err, domainComm.ErrDuplicate) {
			t.Fatalf("err = %v, want ErrDuplicate", err)
		}
		other := &domain.Message{ID: id(), ChatID: chatID, SenderID: alice, Type: domain.TypeText, Content: "x", ClientMessageID: "c-1"}
		if err := messages.SaveMessage(ctx, other); err != nil {
			t.Fatalf("same client id from another sender must be allowed: %v", err)
		}
		got, err := messages.GetMessageByClientID(ctx, chatID, bob, "c-1")
		if err != nil || got.ID != msg.ID {
			t.Fatalf("GetMessageByClientID: %+v %v", got, err)
		}
	})

	t.Run("pagination by ULID cursor", func(t *testing.T) {
		page, err := messages.ListMessages(ctx, chatID, sent[3].ID, 2)
		if err != nil || len(page) != 2 || page[0].ID != sent[2].ID || page[1].ID != sent[1].ID {
			t.Fatalf("page: %v %v", msgIDs(page), err)
		}
	})

	t.Run("full-text search within a chat", func(t *testing.T) {
		found, err := messages.SearchMessages(ctx, chatID, "hello", "", 10)
		if err != nil || len(found) != 2 {
			t.Fatalf("found %v, err %v", contents(found), err)
		}
	})

	t.Run("unread count skips own messages and stops at the limit", func(t *testing.T) {
		if n, _ := messages.CountUnread(ctx, chatID, bob, sent[1].ID, 100); n != 3 { // sent[2], sent[3] и сообщение alice с c-1
			t.Fatalf("unread = %d, want 3", n)
		}
		if n, _ := messages.CountUnread(ctx, chatID, bob, "", 2); n != 2 {
			t.Fatalf("capped unread = %d, want 2", n)
		}
	})

	t.Run("edit only own messages", func(t *testing.T) {
		if _, err := messages.UpdateMessage(ctx, bob, chatID, sent[0].ID, "hijack"); !errors.Is(err, domainComm.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
		got, err := messages.UpdateMessage(ctx, alice, chatID, sent[0].ID, "hello edited")
		if err != nil || got.Content != "hello edited" || !got.IsEdited {
			t.Fatalf("edit: %+v %v", got, err)
		}
	})

	t.Run("reactions", func(t *testing.T) {
		r := domain.Reaction{Emoji: "👍", UserID: bob}
		messages.AddReaction(ctx, chatID, sent[1].ID, r)
		got, _ := messages.AddReaction(ctx, chatID, sent[1].ID, r)
		if len(got.Reactions) != 1 {
			t.Fatalf("$addToSet must deduplicate: %+v", got.Reactions)
		}
		got, _ = messages.RemoveReaction(ctx, chatID, sent[1].ID, r)
		if len(got.Reactions) != 0 {
			t.Fatalf("$pull must remove: %+v", got.Reactions)
		}
	})

	t.Run("soft delete", func(t *testing.T) {
		deleted, err := messages.SoftDeleteMessages(ctx, chatID, []string{sent[2].ID, sent[3].ID}, bob)
		if err != nil || len(deleted) != 0 {
			t.Fatalf("bob must not delete alice's messages: %v %v", deleted, err)
		}
		deleted, err = messages.SoftDeleteMessages(ctx, chatID, []string{sent[2].ID}, "")
		if err != nil || !slices.Equal(deleted, []string{sent[2].ID}) {
			t.Fatalf("moderator delete: %v %v", deleted, err)
		}

		got, _ := messages.GetMessage(ctx, chatID, sent[2].ID)
		if !got.IsDeleted() || got.Content != "" {
			t.Fatalf("tombstone expected: %+v", got)
		}
		if found, _ := messages.SearchMessages(ctx, chatID, "again", "", 10); len(found) != 0 {
			t.Fatalf("search must skip deleted: %v", contents(found))
		}
		if again, _ := messages.SoftDeleteMessages(ctx, chatID, []string{sent[2].ID}, ""); len(again) != 0 {
			t.Fatal("already deleted message must not be deleted twice")
		}
		if _, err := messages.UpdateMessage(ctx, alice, chatID, sent[2].ID, "resurrect"); !errors.Is(err, domainComm.ErrNotFound) {
			t.Fatalf("deleted message must not be editable: %v", err)
		}
	})
}

func ids(chats []domainChat.Chat) []string {
	out := make([]string, len(chats))
	for i, c := range chats {
		out[i] = c.ID
	}
	return out
}

func msgIDs(msgs []domain.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func contents(msgs []domain.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}
