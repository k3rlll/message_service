package chat_usecase_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	domainComm "main/internal/domain"
	domain "main/internal/domain/chat_entity"
	domainEvent "main/internal/domain/event_entity"
	domainMsg "main/internal/domain/message_entity"
	"main/internal/testutil/fakes"
	chatUC "main/internal/usecase/chat_usecase"

	"github.com/oklog/ulid/v2"
)

type env struct {
	uc       *chatUC.ChatUsecase
	chats    *fakes.ChatRepo
	messages *fakes.MessageRepo
	members  *fakes.Members
	events   *fakes.Publisher
}

func newEnv() *env {
	chats := fakes.NewChatRepo()
	e := &env{
		chats:    chats,
		messages: fakes.NewMessageRepo(),
		members:  &fakes.Members{Repo: chats},
		events:   &fakes.Publisher{},
	}
	e.uc = chatUC.NewUsecase(e.chats, e.messages, e.members, e.events,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return e
}

// group создает групповой чат owner + участники с заданными ролями
func (e *env) group(t *testing.T, owner string, roles map[string]string) *domain.Chat {
	t.Helper()
	ctx := context.Background()

	var ids []string
	for id := range roles {
		ids = append(ids, id)
	}
	chat, _, err := e.uc.CreateChat(ctx, owner, chatUC.CreateParams{Type: domain.TypeGroup, Name: "g", MemberIDs: ids})
	if err != nil {
		t.Fatal(err)
	}

	set := map[string]string{}
	for id, role := range roles {
		if role != domain.RoleMember {
			set[id] = role
		}
	}
	if len(set) > 0 {
		if err := e.chats.SetRoles(ctx, chat.ID, set); err != nil {
			t.Fatal(err)
		}
	}
	return chat
}

func id() string { return ulid.Make().String() }

func TestCreateChat(t *testing.T) {
	ctx := context.Background()
	alice, bob := id(), id()

	t.Run("group normalizes name and members", func(t *testing.T) {
		e := newEnv()
		chat, created, err := e.uc.CreateChat(ctx, alice, chatUC.CreateParams{
			Type: domain.TypeGroup, Name: "  team  ", MemberIDs: []string{bob, bob, alice},
		})
		if err != nil || !created {
			t.Fatalf("created=%v err=%v", created, err)
		}
		if chat.Name != "team" || len(chat.Members) != 2 {
			t.Fatalf("unexpected chat: %+v", chat)
		}
		if owner, _ := chat.Member(alice); owner.Role != domain.RoleOwner {
			t.Fatalf("creator must be owner: %+v", chat.Members)
		}
		if !slices.Equal(e.events.Types(), []string{domainEvent.TypeChatCreated}) {
			t.Fatalf("events: %v", e.events.Types())
		}
	})

	t.Run("direct chat is idempotent from both sides", func(t *testing.T) {
		e := newEnv()
		first, created, err := e.uc.CreateChat(ctx, alice, chatUC.CreateParams{Type: domain.TypeDirect, MemberIDs: []string{bob}})
		if err != nil || !created {
			t.Fatalf("created=%v err=%v", created, err)
		}
		again, created, err := e.uc.CreateChat(ctx, bob, chatUC.CreateParams{Type: domain.TypeDirect, MemberIDs: []string{alice}})
		if err != nil || created || again.ID != first.ID {
			t.Fatalf("second create: created=%v err=%v id=%s want %s", created, err, again.ID, first.ID)
		}
		if len(e.events.Events()) != 1 {
			t.Fatalf("existing direct chat must not publish an event: %v", e.events.Types())
		}
	})

	invalid := map[string]chatUC.CreateParams{
		"direct without member":   {Type: domain.TypeDirect},
		"direct with self only":   {Type: domain.TypeDirect, MemberIDs: []string{alice}},
		"direct with two members": {Type: domain.TypeDirect, MemberIDs: []string{bob, id()}},
		"group without name":      {Type: domain.TypeGroup, Name: "   "},
		"unknown type":            {Type: "channel", Name: "x"},
	}
	for name, p := range invalid {
		t.Run(name, func(t *testing.T) {
			_, _, err := newEnv().uc.CreateChat(ctx, alice, p)
			if !errors.Is(err, domainComm.ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestRemoveMemberPermissions(t *testing.T) {
	ctx := context.Background()

	owner, admin, admin2, member, member2 := "owner", "admin", "admin2", "member", "member2"
	roles := map[string]string{
		admin: domain.RoleAdmin, admin2: domain.RoleAdmin,
		member: domain.RoleMember, member2: domain.RoleMember,
	}

	cases := []struct {
		actor, target string
		want          error // nil - разрешено
	}{
		{owner, admin, nil},
		{owner, member, nil},
		{owner, owner, domainComm.ErrConflict}, // владелец не уходит, не передав владение
		{admin, member, nil},
		{admin, admin2, domainComm.ErrForbidden}, // админ не удаляет админа
		{admin, owner, domainComm.ErrForbidden},
		{admin, admin, nil}, // выйти сам может
		{member, member2, domainComm.ErrForbidden},
		{member, member, nil},
		{"stranger", member, domainComm.ErrAccessDenied},
		{owner, "stranger", domainComm.ErrNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.actor+" removes "+tc.target, func(t *testing.T) {
			e := newEnv()
			chat := e.group(t, owner, roles)

			err := e.uc.RemoveMember(ctx, tc.actor, chat.ID, tc.target)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				got, _ := e.chats.GetChat(ctx, chat.ID)
				if _, still := got.Member(tc.target); still {
					t.Fatal("member was not removed")
				}
				if !slices.Contains(e.members.Invalidated(), chat.ID) {
					t.Fatal("members cache was not invalidated")
				}
				last := e.events.Events()[len(e.events.Events())-1]
				if last.Type != domainEvent.TypeChatMemberRemoved || !slices.Contains(last.ExtraTargets, tc.target) {
					t.Fatalf("removed user must be notified: %+v", last)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSetRole(t *testing.T) {
	ctx := context.Background()
	owner, admin, member := "owner", "admin", "member"
	roles := map[string]string{admin: domain.RoleAdmin, member: domain.RoleMember}

	t.Run("only owner changes roles", func(t *testing.T) {
		e := newEnv()
		chat := e.group(t, owner, roles)
		if err := e.uc.SetRole(ctx, admin, chat.ID, member, domain.RoleAdmin); !errors.Is(err, domainComm.ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})

	t.Run("owner cannot change own role", func(t *testing.T) {
		e := newEnv()
		chat := e.group(t, owner, roles)
		if err := e.uc.SetRole(ctx, owner, chat.ID, owner, domain.RoleMember); !errors.Is(err, domainComm.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("ownership transfer is atomic", func(t *testing.T) {
		e := newEnv()
		chat := e.group(t, owner, roles)
		if err := e.uc.SetRole(ctx, owner, chat.ID, member, domain.RoleOwner); err != nil {
			t.Fatal(err)
		}

		got, _ := e.chats.GetChat(ctx, chat.ID)
		owners := 0
		for _, m := range got.Members {
			if m.Role == domain.RoleOwner {
				owners++
			}
		}
		newOwner, _ := got.Member(member)
		oldOwner, _ := got.Member(owner)
		if owners != 1 || newOwner.Role != domain.RoleOwner || oldOwner.Role != domain.RoleAdmin {
			t.Fatalf("unexpected roles: %+v", got.Members)
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		e := newEnv()
		chat := e.group(t, owner, roles)
		if err := e.uc.SetRole(ctx, owner, chat.ID, member, "king"); !errors.Is(err, domainComm.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})
}

func TestMarkRead(t *testing.T) {
	ctx := context.Background()
	e := newEnv()
	alice, bob := id(), id()
	chat := e.group(t, alice, map[string]string{bob: domain.RoleMember})

	var msgs []string
	for i := 0; i < 3; i++ {
		m := &domainMsg.Message{ID: id(), ChatID: chat.ID, SenderID: alice, Type: domainMsg.TypeText, Content: "x"}
		if err := e.messages.SaveMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m.ID)
	}

	if err := e.uc.MarkRead(ctx, bob, chat.ID, msgs[2]); err != nil {
		t.Fatal(err)
	}
	// Отметка назад - не ошибка, но и не событие
	if err := e.uc.MarkRead(ctx, bob, chat.ID, msgs[0]); err != nil {
		t.Fatal(err)
	}

	reads := 0
	for _, ev := range e.events.Events() {
		if ev.Type == domainEvent.TypeChatRead {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("expected exactly one chat.read event, got %d", reads)
	}

	summary, err := e.uc.GetChat(ctx, bob, chat.ID)
	if err != nil || summary.UnreadCount != 0 {
		t.Fatalf("unread = %d, err = %v", summary.UnreadCount, err)
	}
	if err := e.uc.MarkRead(ctx, bob, chat.ID, id()); !errors.Is(err, domainComm.ErrNotFound) {
		t.Fatalf("unknown message: err = %v", err)
	}
}

func TestPinRules(t *testing.T) {
	ctx := context.Background()
	e := newEnv()
	owner, member := id(), id()
	chat := e.group(t, owner, map[string]string{member: domain.RoleMember})

	msg := &domainMsg.Message{ID: id(), ChatID: chat.ID, SenderID: member, Type: domainMsg.TypeText, Content: "pin me"}
	if err := e.messages.SaveMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}

	if err := e.uc.SetPin(ctx, member, chat.ID, msg.ID, true); !errors.Is(err, domainComm.ErrForbidden) {
		t.Fatalf("member pin: err = %v", err)
	}
	if err := e.uc.SetPin(ctx, owner, chat.ID, msg.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.uc.SetPin(ctx, owner, chat.ID, msg.ID, true); err != nil {
		t.Fatal(err)
	}

	pins := 0
	for _, ev := range e.events.Events() {
		if ev.Type == domainEvent.TypeMessagePinned {
			pins++
		}
	}
	if pins != 1 {
		t.Fatalf("repeated pin must not publish again, got %d events", pins)
	}

	if _, err := e.messages.SoftDeleteMessages(ctx, chat.ID, []string{msg.ID}, ""); err != nil {
		t.Fatal(err)
	}
	list, err := e.uc.ListPins(ctx, member, chat.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("deleted message must not be listed: %v %v", list, err)
	}
}
