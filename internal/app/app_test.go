package app_test

import (
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	domainChat "main/internal/domain/chat_entity"
	domain "main/internal/domain/message_entity"
	"main/internal/ratelimit"

	"github.com/coder/websocket"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/oklog/ulid/v2"
)

func TestAuth(t *testing.T) {
	e := newEnv(t)
	path := "/api/v1/chats"

	e.expectError(http.StatusUnauthorized, "UNAUTHORIZED", http.MethodGet, path, "", nil)

	publicDER, err := x509.MarshalPKIXPublicKey(&e.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	badTokens := map[string]string{
		"signed with a foreign key": signToken(t, jwtlib.SigningMethodRS256, newKey(t), e.alice, time.Hour),
		"expired":                   signToken(t, jwtlib.SigningMethodRS256, e.key, e.alice, -time.Minute),
		"subject is not a ULID":     signToken(t, jwtlib.SigningMethodRS256, e.key, "not-a-ulid", time.Hour),
		// Атака подмены алгоритма: HS256, где секретом выступает публичный ключ
		"HS256 signed with the public key": signToken(t, jwtlib.SigningMethodHS256, publicDER, e.alice, time.Hour),
	}
	for name, token := range badTokens {
		req, _ := http.NewRequest(http.MethodGet, e.baseURL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %s: status = %d, want 401", name, resp.StatusCode)
		}
	}

	// Токен в query разрешен только для WebSocket, для REST - нет
	if resp := e.do(http.MethodGet, path+"?token="+e.token(e.alice), "", nil); resp.status != http.StatusUnauthorized {
		t.Fatalf("query token on REST: status = %d, want 401", resp.status)
	}

	// WebSocket без токена
	wsURL := "ws" + strings.TrimPrefix(e.baseURL, "http") + "/api/v1/ws"
	_, wsResp, err := websocket.Dial(t.Context(), wsURL, nil)
	if err == nil || wsResp == nil || wsResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("websocket without token must be rejected with 401, got resp=%v err=%v", wsResp, err)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)

	raw := e.expect(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/messages", e.alice,
		map[string]any{"chat_id": "not-a-ulid", "type": "system", "content": "x"})

	resp := decode[struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}](t, raw)
	if resp.Error.Code != "VALIDATION_ERROR" || resp.Error.Details["ChatID"] == "" || resp.Error.Details["Type"] == "" {
		t.Fatalf("unexpected validation response: %s", raw)
	}

	// Сообщение из одних пробелов проходит валидацию DTO, но отсекается в usecase
	e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "/api/v1/messages", e.alice,
		map[string]any{"chat_id": chat.ID, "content": "   "})

	e.expect(http.StatusUnprocessableEntity, http.MethodGet, "/api/v1/messages?limit=1000&chat_id="+chat.ID, e.alice, nil)
	e.expect(http.StatusUnprocessableEntity, http.MethodDelete, "/api/v1/messages?chat_id="+chat.ID, e.alice, nil)
	e.expect(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/chats", e.alice,
		map[string]any{"type": "channel", "name": "x"})
	e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "/api/v1/chats", e.alice,
		map[string]any{"type": "group", "name": "   "})
}

func TestGroupChatRoles(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "  team  ", e.bob, e.bob, e.alice) // дубликаты и сам создатель отбрасываются
	path := "/api/v1/chats/" + chat.ID

	if chat.Name != "team" || len(chat.Members) != 2 {
		t.Fatalf("unexpected chat: %+v", chat)
	}
	got := e.getChat(e.bob, chat.ID)
	if roleOf(t, got, e.alice) != domainChat.RoleOwner || roleOf(t, got, e.bob) != domainChat.RoleMember {
		t.Fatalf("unexpected roles: %+v", got.Members)
	}

	// Посторонний не видит чат
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, path, e.carol, nil)
	e.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "/api/v1/chats/"+ulid.Make().String(), e.alice, nil)

	// Обычный участник не управляет чатом
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPatch, path, e.bob, map[string]any{"name": "mine"})
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, path+"/members", e.bob, map[string]any{"user_id": e.carol})

	raw := e.expect(http.StatusOK, http.MethodPatch, path, e.alice, map[string]any{"name": "renamed"})
	if decode[domainChat.Chat](t, raw).Name != "renamed" {
		t.Fatalf("chat was not renamed: %s", raw)
	}

	// Новый участник сразу может писать: кэш участников сброшен
	e.expect(http.StatusOK, http.MethodPost, path+"/members", e.alice, map[string]any{"user_id": e.carol})
	e.expectError(http.StatusConflict, "CONFLICT", http.MethodPost, path+"/members", e.alice, map[string]any{"user_id": e.carol})
	e.send(e.carol, chat.ID, "hi, I'm new")

	// Участник не может удалить другого, а админ - может
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodDelete, path+"/members/"+e.carol, e.bob, nil)
	e.expect(http.StatusNoContent, http.MethodPut, path+"/members/"+e.bob+"/role", e.alice, map[string]any{"role": "admin"})
	e.expect(http.StatusNoContent, http.MethodDelete, path+"/members/"+e.carol, e.bob, nil)

	// Удаленный сразу теряет доступ
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, "/api/v1/messages?chat_id="+chat.ID, e.carol, nil)
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, "/api/v1/messages", e.carol,
		map[string]any{"chat_id": chat.ID, "content": "still here?"})

	// Админ не трогает владельца и не меняет роли
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodDelete, path+"/members/"+e.alice, e.bob, nil)
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPut, path+"/members/"+e.alice+"/role", e.bob, map[string]any{"role": "member"})

	// Владелец не может просто уйти: сначала передает владение
	e.expectError(http.StatusConflict, "CONFLICT", http.MethodDelete, path+"/members/"+e.alice, e.alice, nil)
	e.expect(http.StatusNoContent, http.MethodPut, path+"/members/"+e.bob+"/role", e.alice, map[string]any{"role": "owner"})

	got = e.getChat(e.bob, chat.ID)
	if roleOf(t, got, e.bob) != domainChat.RoleOwner || roleOf(t, got, e.alice) != domainChat.RoleAdmin {
		t.Fatalf("ownership was not transferred: %+v", got.Members)
	}

	// Бывший владелец теперь админ и может уйти сам
	e.expect(http.StatusNoContent, http.MethodDelete, path+"/members/"+e.alice, e.alice, nil)
	if ids := memberIDs(e.getChat(e.bob, chat.ID).Chat); !slices.Equal(ids, []string{e.bob}) {
		t.Fatalf("unexpected members after leave: %v", ids)
	}
}

func TestDirectChat(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"type": "direct", "member_ids": []string{e.bob}}

	first := decode[domainChat.Chat](t, e.expect(http.StatusCreated, http.MethodPost, "/api/v1/chats", e.alice, body))

	// Повторное создание - с любой стороны - возвращает тот же чат
	again := decode[domainChat.Chat](t, e.expect(http.StatusOK, http.MethodPost, "/api/v1/chats", e.alice, body))
	reverse := decode[domainChat.Chat](t, e.expect(http.StatusOK, http.MethodPost, "/api/v1/chats", e.bob,
		map[string]any{"type": "direct", "member_ids": []string{e.alice}}))
	if again.ID != first.ID || reverse.ID != first.ID {
		t.Fatalf("direct chat is not unique: %s %s %s", first.ID, again.ID, reverse.ID)
	}

	for _, m := range first.Members {
		if m.Role != domainChat.RoleMember {
			t.Fatalf("direct chat has no owner, got %+v", first.Members)
		}
	}

	e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "/api/v1/chats", e.alice,
		map[string]any{"type": "direct", "member_ids": []string{e.bob, e.carol}})
	e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "/api/v1/chats", e.alice,
		map[string]any{"type": "direct", "member_ids": []string{e.alice}})
	e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/api/v1/chats/"+first.ID, e.alice,
		map[string]any{"name": "x"})
	e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodDelete,
		"/api/v1/chats/"+first.ID+"/members/"+e.bob, e.alice, nil)
}

func TestChatListAndUnread(t *testing.T) {
	e := newEnv(t)

	older := e.createGroup(e.alice, "older", e.bob)
	newer := e.createGroup(e.alice, "newer", e.bob)
	e.createGroup(e.alice, "without bob")

	m1 := e.send(e.alice, older.ID, "one")
	e.send(e.alice, older.ID, "two")
	m3 := e.send(e.alice, older.ID, "three")
	e.send(e.bob, older.ID, "my own messages are never unread")

	type chatsPage struct {
		Chats      []chatSummary `json:"chats"`
		HasMore    bool          `json:"has_more"`
		NextBefore *time.Time    `json:"next_before"`
	}
	listChats := func(query string) chatsPage {
		return decode[chatsPage](t, e.expect(http.StatusOK, http.MethodGet, "/api/v1/chats"+query, e.bob, nil))
	}

	// Чат с последней активностью - первым, у него превью и счетчик
	page := listChats("?limit=1")
	if len(page.Chats) != 1 || !page.HasMore || page.NextBefore == nil || page.Chats[0].ID != older.ID {
		t.Fatalf("unexpected first page: %+v", page)
	}
	top := page.Chats[0]
	if top.UnreadCount != 3 || top.LastMessage == nil || top.LastMessage.Content != "my own messages are never unread" {
		t.Fatalf("unexpected unread/preview: unread=%d last=%+v", top.UnreadCount, top.LastMessage)
	}

	page = listChats("?limit=1&before=" + url.QueryEscape(page.NextBefore.Format(time.RFC3339Nano)))
	if len(page.Chats) != 1 || page.HasMore || page.Chats[0].ID != newer.ID {
		t.Fatalf("unexpected second page: %+v", page)
	}

	// Прочтение двигает счетчик и не откатывается назад
	readPath := "/api/v1/chats/" + older.ID + "/read"
	e.expect(http.StatusNoContent, http.MethodPost, readPath, e.bob, map[string]any{"message_id": m1.ID})
	if got := e.getChat(e.bob, older.ID).UnreadCount; got != 2 {
		t.Fatalf("unread after reading m1 = %d, want 2", got)
	}
	e.expect(http.StatusNoContent, http.MethodPost, readPath, e.bob, map[string]any{"message_id": m3.ID})
	e.expect(http.StatusNoContent, http.MethodPost, readPath, e.bob, map[string]any{"message_id": m1.ID})
	if got := e.getChat(e.bob, older.ID).UnreadCount; got != 0 {
		t.Fatalf("unread after reading m3 = %d, want 0", got)
	}

	e.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, readPath, e.bob,
		map[string]any{"message_id": ulid.Make().String()})
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, readPath, e.carol,
		map[string]any{"message_id": m1.ID})
}

func TestMessages(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob, e.carol)
	messagesPath := "/api/v1/messages"

	var sent []domain.Message
	for i := 1; i <= 5; i++ {
		sent = append(sent, e.send(e.bob, chat.ID, fmt.Sprintf("message %d", i)))
	}

	t.Run("pagination", func(t *testing.T) {
		page := e.list(e.alice, chat.ID, "&limit=2")
		if len(page.Messages) != 2 || !page.HasMore || page.Messages[0].ID != sent[4].ID {
			t.Fatalf("unexpected first page: %+v", page)
		}
		page = e.list(e.alice, chat.ID, "&limit=2&anchor="+page.Messages[1].ID)
		if len(page.Messages) != 2 || !page.HasMore || page.Messages[0].ID != sent[2].ID {
			t.Fatalf("unexpected second page: %+v", page)
		}
		page = e.list(e.alice, chat.ID, "&limit=2&anchor="+page.Messages[1].ID)
		if len(page.Messages) != 1 || page.HasMore || page.Messages[0].ID != sent[0].ID {
			t.Fatalf("unexpected last page: %+v", page)
		}
	})

	t.Run("idempotent send", func(t *testing.T) {
		body := map[string]any{"chat_id": chat.ID, "content": "exactly once", "client_msg_id": "c-1"}
		first := e.sendBody(e.alice, body)

		// Повтор с тем же client_msg_id - 200 и то же сообщение
		raw := e.expect(http.StatusOK, http.MethodPost, messagesPath, e.alice, body)
		if again := decode[domain.Message](t, raw); again.ID != first.ID {
			t.Fatalf("retry created a new message: %s != %s", again.ID, first.ID)
		}

		// Тот же client_msg_id у другого отправителя - это другое сообщение
		if other := e.sendBody(e.bob, body); other.ID == first.ID {
			t.Fatal("client_msg_id must be scoped to the sender")
		}
	})

	t.Run("reply", func(t *testing.T) {
		reply := e.sendBody(e.alice, map[string]any{"chat_id": chat.ID, "content": "answer", "reply_to_id": sent[0].ID})
		if reply.ReplyToID != sent[0].ID {
			t.Fatalf("reply_to_id was not saved: %+v", reply)
		}

		other := e.createGroup(e.alice, "other")
		foreign := e.send(e.alice, other.ID, "in another chat")
		e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, messagesPath, e.alice,
			map[string]any{"chat_id": chat.ID, "content": "x", "reply_to_id": foreign.ID})
	})

	t.Run("edit", func(t *testing.T) {
		editPath := messagesPath + "/" + sent[0].ID
		body := map[string]any{"chat_id": chat.ID, "content": "edited"}

		e.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, editPath, e.alice, body)

		raw := e.expect(http.StatusOK, http.MethodPut, editPath, e.bob, body)
		if msg := decode[domain.Message](t, raw); msg.Content != "edited" || !msg.IsEdited {
			t.Fatalf("message was not edited: %s", raw)
		}
	})

	t.Run("reactions", func(t *testing.T) {
		path := messagesPath + "/" + sent[1].ID + "/reactions"
		body := map[string]any{"chat_id": chat.ID, "emoji": "👍"}

		e.expect(http.StatusOK, http.MethodPut, path, e.alice, body)
		e.expect(http.StatusOK, http.MethodPut, path, e.alice, body) // повтор ничего не меняет
		raw := e.expect(http.StatusOK, http.MethodPut, path, e.carol, body)
		if got := decode[domain.Message](t, raw).Reactions; len(got) != 2 {
			t.Fatalf("reactions = %+v, want 2", got)
		}

		raw = e.expect(http.StatusOK, http.MethodDelete, path+"?emoji=%F0%9F%91%8D&chat_id="+chat.ID, e.alice, nil)
		if got := decode[domain.Message](t, raw).Reactions; len(got) != 1 || got[0].UserID != e.carol {
			t.Fatalf("reaction was not removed: %+v", got)
		}
	})

	t.Run("delete", func(t *testing.T) {
		deletePath := func(ids ...string) string {
			return messagesPath + "?chat_id=" + chat.ID + "&message_ids=" + strings.Join(ids, "&message_ids=")
		}

		// Участник не удаляет чужое
		e.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, deletePath(sent[2].ID), e.carol, nil)

		// Автор удаляет свое; из смешанного списка удаляется только свое
		own := e.send(e.carol, chat.ID, "mine")
		raw := e.expect(http.StatusOK, http.MethodDelete, deletePath(own.ID, sent[2].ID), e.carol, nil)
		deleted := decode[struct {
			DeletedIDs []string `json:"deleted_ids"`
		}](t, raw).DeletedIDs
		if got := deleted; !slices.Equal(got, []string{own.ID}) {
			t.Fatalf("deleted %v, want only %s", got, own.ID)
		}

		// Владелец чата модерирует чужие сообщения
		e.expect(http.StatusOK, http.MethodDelete, deletePath(sent[2].ID), e.alice, nil)

		// Удаленное остается в истории "надгробием"
		var tombstone *domain.Message
		for _, m := range e.list(e.bob, chat.ID, "&limit=100").Messages {
			if m.ID == sent[2].ID {
				tombstone = &m
			}
		}
		if tombstone == nil || !tombstone.IsDeleted() || tombstone.Content != "" {
			t.Fatalf("expected a tombstone, got %+v", tombstone)
		}

		// Удаленное нельзя править, на него нельзя отвечать и реагировать
		e.expect(http.StatusNotFound, http.MethodPut, messagesPath+"/"+sent[2].ID, e.bob,
			map[string]any{"chat_id": chat.ID, "content": "resurrect"})
		e.expect(http.StatusBadRequest, http.MethodPost, messagesPath, e.bob,
			map[string]any{"chat_id": chat.ID, "content": "x", "reply_to_id": sent[2].ID})
		e.expect(http.StatusNotFound, http.MethodPut, messagesPath+"/"+sent[2].ID+"/reactions", e.bob,
			map[string]any{"chat_id": chat.ID, "emoji": "👍"})
	})

	t.Run("search", func(t *testing.T) {
		raw := e.expect(http.StatusOK, http.MethodGet, messagesPath+"/search?text=message&chat_id="+chat.ID, e.alice, nil)
		page := decode[messagesPage](t, raw)
		// message 1 отредактировано, message 3 удалено
		if len(page.Messages) != 3 {
			t.Fatalf("search returned %d messages, want 3: %+v", len(page.Messages), page.Messages)
		}

		raw = e.expect(http.StatusOK, http.MethodGet, messagesPath+"/search?text=nothing&chat_id="+chat.ID, e.alice, nil)
		if !strings.Contains(string(raw), `"messages":[]`) {
			t.Fatalf("empty result must be an empty array: %s", raw)
		}

		e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet,
			messagesPath+"/search?text=message&chat_id="+chat.ID, e.dave, nil)
	})
}

func TestPreviewFollowsEditsAndDeletes(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)

	e.send(e.alice, chat.ID, "first")
	last := e.send(e.alice, chat.ID, "last")

	e.expect(http.StatusOK, http.MethodPut, "/api/v1/messages/"+last.ID, e.alice,
		map[string]any{"chat_id": chat.ID, "content": "last, edited"})
	if got := e.getChat(e.bob, chat.ID).LastMessage; got == nil || got.Content != "last, edited" {
		t.Fatalf("preview did not follow the edit: %+v", got)
	}

	e.expect(http.StatusOK, http.MethodDelete, "/api/v1/messages?chat_id="+chat.ID+"&message_ids="+last.ID, e.alice, nil)
	if got := e.getChat(e.bob, chat.ID).LastMessage; got == nil || !got.IsDeleted || got.Content != "" {
		t.Fatalf("preview still shows the deleted message: %+v", got)
	}
}

func TestPins(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)
	msg := e.send(e.bob, chat.ID, "important")
	pinPath := "/api/v1/chats/" + chat.ID + "/pins/"

	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPut, pinPath+msg.ID, e.bob, nil)
	e.expect(http.StatusNoContent, http.MethodPut, pinPath+msg.ID, e.alice, nil)
	e.expect(http.StatusNoContent, http.MethodPut, pinPath+msg.ID, e.alice, nil) // идемпотентно

	pins := decode[messagesPage](t, e.expect(http.StatusOK, http.MethodGet, "/api/v1/chats/"+chat.ID+"/pins", e.bob, nil))
	if len(pins.Messages) != 1 || pins.Messages[0].ID != msg.ID {
		t.Fatalf("unexpected pins: %+v", pins)
	}

	e.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, pinPath+ulid.Make().String(), e.alice, nil)

	// Удаленное сообщение исчезает из закрепленных
	e.expect(http.StatusOK, http.MethodDelete, "/api/v1/messages?chat_id="+chat.ID+"&message_ids="+msg.ID, e.bob, nil)
	pins = decode[messagesPage](t, e.expect(http.StatusOK, http.MethodGet, "/api/v1/chats/"+chat.ID+"/pins", e.bob, nil))
	if len(pins.Messages) != 0 {
		t.Fatalf("deleted message is still pinned: %+v", pins)
	}
	e.expect(http.StatusNoContent, http.MethodDelete, pinPath+msg.ID, e.alice, nil)

	// В личном чате закреплять может любой из двоих
	direct := decode[domainChat.Chat](t, e.expect(http.StatusCreated, http.MethodPost, "/api/v1/chats", e.alice,
		map[string]any{"type": "direct", "member_ids": []string{e.bob}}))
	dm := e.send(e.alice, direct.ID, "hi")
	e.expect(http.StatusNoContent, http.MethodPut, "/api/v1/chats/"+direct.ID+"/pins/"+dm.ID, e.bob, nil)
}

func TestAttachments(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)
	other := e.createGroup(e.alice, "other")
	uploadPath := "/api/v1/chats/" + chat.ID + "/attachments"

	type signed struct {
		Key string `json:"key"`
		URL string `json:"url"`
	}

	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, uploadPath, e.carol,
		map[string]any{"file_name": "a.png", "size": 10})
	e.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, uploadPath, e.bob,
		map[string]any{"file_name": "big.bin", "size": maxFileSize + 1})

	// Путь в имени файла отбрасывается
	upload := decode[signed](t, e.expect(http.StatusCreated, http.MethodPost, uploadPath, e.bob,
		map[string]any{"file_name": "../../etc/photo.png", "size": 10}))
	if !strings.HasPrefix(upload.Key, "chats/"+chat.ID+"/") || !strings.HasSuffix(upload.Key, "/photo.png") ||
		strings.Contains(upload.Key, "..") || upload.URL == "" {
		t.Fatalf("unexpected upload: %+v", upload)
	}

	send := func(key string) response {
		return e.do(http.MethodPost, "/api/v1/messages", e.bob, map[string]any{
			"chat_id": chat.ID, "type": "image",
			"attachments": []map[string]any{{"key": key, "content_type": "image/png"}},
		})
	}

	// Файл еще не загружен
	if resp := send(upload.Key); resp.status != http.StatusBadRequest {
		t.Fatalf("not uploaded attachment: status = %d, body: %s", resp.status, resp.body)
	}

	// Загрузили больше, чем разрешено: размер берется из хранилища, а не со слов клиента
	e.storage.upload(upload.Key, maxFileSize+1)
	if resp := send(upload.Key); resp.status != http.StatusBadRequest {
		t.Fatalf("oversized attachment: status = %d, body: %s", resp.status, resp.body)
	}

	e.storage.upload(upload.Key, 10)
	resp := send(upload.Key)
	if resp.status != http.StatusCreated {
		t.Fatalf("send with attachment: status = %d, body: %s", resp.status, resp.body)
	}
	msg := decode[domain.Message](t, resp.body)
	if len(msg.Attachments) != 1 || msg.Attachments[0].Size != 10 || msg.Attachments[0].Name != "photo.png" || msg.Content != "" {
		t.Fatalf("unexpected attachment message: %+v", msg)
	}

	// Ключ из другого чата прикрепить нельзя
	foreignKey := "chats/" + other.ID + "/" + ulid.Make().String() + "/secret.pdf"
	e.storage.upload(foreignKey, 10)
	if resp := send(foreignKey); resp.status != http.StatusBadRequest {
		t.Fatalf("foreign attachment: status = %d, body: %s", resp.status, resp.body)
	}

	// Скачивание - только участникам и только файлов своего чата
	download := decode[signed](t, e.expect(http.StatusOK, http.MethodGet, uploadPath+"?key="+upload.Key, e.alice, nil))
	if download.URL == "" {
		t.Fatalf("empty download URL: %+v", download)
	}
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, uploadPath+"?key="+upload.Key, e.carol, nil)
	e.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, uploadPath+"?key="+foreignKey, e.alice, nil)
	e.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet,
		uploadPath+"?key=chats/"+chat.ID+"/../"+other.ID+"/x/secret.pdf", e.alice, nil)
}

func TestAttachmentsDisabled(t *testing.T) {
	e := newEnv(t, func(o *options) { o.noStorage = true })
	chat := e.createGroup(e.alice, "team")

	e.expectError(http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", http.MethodPost,
		"/api/v1/chats/"+chat.ID+"/attachments", e.alice, map[string]any{"file_name": "a.png", "size": 10})

	// Обычные сообщения без вложений работают
	e.send(e.alice, chat.ID, "text still works")
}

func TestRateLimits(t *testing.T) {
	t.Run("messages", func(t *testing.T) {
		e := newEnv(t, func(o *options) { o.messagesRule = ratelimit.Rule{Limit: 3, Window: time.Minute} })
		chat := e.createGroup(e.alice, "team", e.bob)

		for i := 0; i < 3; i++ {
			e.send(e.alice, chat.ID, "ok")
		}
		e.expectError(http.StatusTooManyRequests, "RATE_LIMITED", http.MethodPost, "/api/v1/messages", e.alice,
			map[string]any{"chat_id": chat.ID, "content": "one too many"})

		// Лимит общий для REST и WebSocket и считается на пользователя
		conn := e.dial(e.alice, "")
		conn.write("message.send", "ws-1", map[string]any{"chat_id": chat.ID, "text": "via ws"})
		if f := conn.next("error"); f.Ref != "ws-1" || !strings.Contains(string(f.Payload), "RATE_LIMITED") {
			t.Fatalf("expected RATE_LIMITED over websocket, got %v", f)
		}
		e.send(e.bob, chat.ID, "bob is not limited")
	})

	t.Run("http", func(t *testing.T) {
		e := newEnv(t, func(o *options) { o.httpRule = ratelimit.Rule{Limit: 2, Window: time.Minute} })

		for i := 0; i < 2; i++ {
			resp := e.do(http.MethodGet, "/api/v1/chats", e.alice, nil)
			if resp.status != http.StatusOK || resp.header.Get("X-RateLimit-Limit") != "2" {
				t.Fatalf("request %d: status = %d, headers = %v", i, resp.status, resp.header)
			}
		}

		resp := e.do(http.MethodGet, "/api/v1/chats", e.alice, nil)
		if resp.status != http.StatusTooManyRequests || resp.header.Get("Retry-After") == "" ||
			resp.header.Get("X-RateLimit-Remaining") != "0" {
			t.Fatalf("expected 429 with Retry-After, got %d %v", resp.status, resp.header)
		}

		e.expect(http.StatusOK, http.MethodGet, "/api/v1/chats", e.bob, nil)
	})
}

func TestRealtimeMessages(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)

	alice := e.dial(e.alice, "")
	bob := e.dial(e.bob, "")
	bobSecondTab := e.dial(e.bob, "")
	carol := e.dial(e.carol, "")

	if alice.hello.UserID != e.alice || alice.hello.LastEventID == "" || alice.hello.ResyncRequired {
		t.Fatalf("unexpected connected frame: %+v", alice.hello)
	}

	expectCreated := func(c *wsConn, senderID, content string) frame {
		t.Helper()
		f := c.next("message.created")
		msg := messageOf(t, f)
		if f.ID == "" || f.ChatID != chat.ID || msg.SenderID != senderID || msg.Content != content {
			t.Fatalf("unexpected frame: %v", f)
		}
		return f
	}

	// REST-сообщение приходит всем соединениям участников
	e.send(e.alice, chat.ID, "from rest")
	for _, c := range []*wsConn{alice, bob, bobSecondTab} {
		expectCreated(c, e.alice, "from rest")
	}

	// Отправка по WebSocket: ack с сообщением на ту же ref, затем событие всем
	bob.write("message.send", "r-1", map[string]any{"chat_id": chat.ID, "text": "from ws", "client_msg_id": "x-1"})
	got := bob.nextSet("ack", "message.created")
	ack := got["ack"]
	if ack.Ref != "r-1" || messageOf(t, ack).Content != "from ws" ||
		messageOf(t, got["message.created"]).ID != messageOf(t, ack).ID {
		t.Fatalf("unexpected ack/event: %v", got)
	}
	for _, c := range []*wsConn{alice, bobSecondTab} {
		expectCreated(c, e.bob, "from ws")
	}

	// Повтор с тем же client_msg_id: ack с тем же сообщением, второго события нет
	bob.write("message.send", "r-2", map[string]any{"chat_id": chat.ID, "text": "from ws", "client_msg_id": "x-1"})
	if again := bob.next("ack"); messageOf(t, again).ID != messageOf(t, ack).ID {
		t.Fatalf("retry over websocket created a new message: %v", again)
	}

	// Посторонний получает ошибку на свою ref и не видит чужих событий:
	// это первый его кадр после connected, хотя в чате уже было два сообщения
	carol.write("message.send", "c-1", map[string]any{"chat_id": chat.ID, "text": "let me in"})
	if f := carol.next("error"); f.Ref != "c-1" || !strings.Contains(string(f.Payload), "FORBIDDEN") {
		t.Fatalf("carol must get FORBIDDEN, got %v", f)
	}

	// Мусор и неизвестные команды не рвут соединение
	ctx := t.Context()
	if err := alice.conn.Write(ctx, websocket.MessageText, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if f := alice.next("error"); !strings.Contains(string(f.Payload), "INVALID_REQUEST") {
		t.Fatalf("expected INVALID_REQUEST, got %v", f)
	}
	alice.write("dance", "d-1", nil)
	if f := alice.next("error"); f.Ref != "d-1" {
		t.Fatalf("expected error for unknown frame, got %v", f)
	}

	// Следующее событие - именно новое сообщение: повтор x-1 события не породил
	e.send(e.alice, chat.ID, "still alive")
	expectCreated(bob, e.alice, "still alive")

	if got := e.list(e.alice, chat.ID, "").Messages; len(got) != 3 {
		t.Fatalf("expected 3 persisted messages, got %d", len(got))
	}
}

func TestRealtimeEvents(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)
	msg := e.send(e.bob, chat.ID, "hello")

	alice := e.dial(e.alice, "")
	bob := e.dial(e.bob, "")
	carol := e.dial(e.carol, "")

	payload := func(f frame) map[string]any {
		return decode[map[string]any](t, f.Payload)
	}

	t.Run("edit, reaction, delete", func(t *testing.T) {
		e.expect(http.StatusOK, http.MethodPut, "/api/v1/messages/"+msg.ID, e.bob,
			map[string]any{"chat_id": chat.ID, "content": "hello, edited"})
		for _, c := range []*wsConn{alice, bob} {
			if f := c.next("message.updated"); messageOf(t, f).Content != "hello, edited" {
				t.Fatalf("unexpected update event: %v", f)
			}
		}

		e.expect(http.StatusOK, http.MethodPut, "/api/v1/messages/"+msg.ID+"/reactions", e.alice,
			map[string]any{"chat_id": chat.ID, "emoji": "🔥"})
		for _, c := range []*wsConn{alice, bob} {
			if f := c.next("message.reaction_added"); payload(f)["emoji"] != "🔥" || payload(f)["user_id"] != e.alice {
				t.Fatalf("unexpected reaction event: %v", f)
			}
		}

		e.expect(http.StatusNoContent, http.MethodPut, "/api/v1/chats/"+chat.ID+"/pins/"+msg.ID, e.alice, nil)
		for _, c := range []*wsConn{alice, bob} {
			c.next("message.pinned")
		}

		e.expect(http.StatusOK, http.MethodDelete, "/api/v1/messages?chat_id="+chat.ID+"&message_ids="+msg.ID, e.bob, nil)
		for _, c := range []*wsConn{alice, bob} {
			if f := c.next("message.deleted"); !strings.Contains(string(f.Payload), msg.ID) {
				t.Fatalf("unexpected delete event: %v", f)
			}
		}
	})

	t.Run("read receipts", func(t *testing.T) {
		last := e.send(e.alice, chat.ID, "read me")
		bob.next("message.created")
		alice.next("message.created")

		bob.write("chat.read", "read-1", map[string]any{"chat_id": chat.ID, "message_id": last.ID})
		// Событие прочтения получает и сам Боб: так узнают его другие устройства
		if got := bob.nextSet("ack", "chat.read"); got["ack"].Ref != "read-1" {
			t.Fatalf("unexpected ack: %v", got)
		}
		f := alice.next("chat.read")
		if p := payload(f); p["user_id"] != e.bob || p["message_id"] != last.ID {
			t.Fatalf("unexpected read event: %v", f)
		}

		if got := e.getChat(e.bob, chat.ID).UnreadCount; got != 0 {
			t.Fatalf("unread after websocket read = %d, want 0", got)
		}
	})

	t.Run("typing", func(t *testing.T) {
		alice.write("typing", "", map[string]any{"chat_id": chat.ID})
		alice.write("typing", "", map[string]any{"chat_id": chat.ID}) // повтор в пределах TTL не рассылается

		f := bob.next("typing")
		if f.ChatID != chat.ID || payload(f)["user_id"] != e.alice {
			t.Fatalf("unexpected typing event: %v", f)
		}
	})

	t.Run("membership", func(t *testing.T) {
		// Добавленный участник получает событие с самим чатом и дальше видит сообщения
		e.expect(http.StatusOK, http.MethodPost, "/api/v1/chats/"+chat.ID+"/members", e.alice,
			map[string]any{"user_id": e.carol})

		f := carol.next("chat.member_added")
		added := decode[struct {
			UserID string          `json:"user_id"`
			Chat   domainChat.Chat `json:"chat"`
		}](t, f.Payload)
		if added.UserID != e.carol || added.Chat.ID != chat.ID || len(added.Chat.Members) != 3 {
			t.Fatalf("unexpected member_added event: %v", f)
		}

		e.send(e.bob, chat.ID, "welcome, carol")
		if got := messageOf(t, carol.next("message.created")); got.Content != "welcome, carol" {
			t.Fatalf("carol did not get the message: %+v", got)
		}

		// Удаленный узнает об этом и перестает получать сообщения
		e.expect(http.StatusNoContent, http.MethodDelete, "/api/v1/chats/"+chat.ID+"/members/"+e.carol, e.alice, nil)
		if f := carol.next("chat.member_removed"); payload(f)["user_id"] != e.carol {
			t.Fatalf("unexpected member_removed event: %v", f)
		}

		e.send(e.bob, chat.ID, "carol must not see this")

		// Маркер в отдельном чате Кэрол: если бы предыдущее сообщение дошло, оно пришло бы раньше
		own := e.createGroup(e.carol, "carol's own")
		e.send(e.carol, own.ID, "marker")
		carol.next("chat.created")
		if got := messageOf(t, carol.next("message.created")); got.Content != "marker" {
			t.Fatalf("carol got a message from a chat she left: %+v", got)
		}
	})
}

// Проверка "второго события не было" требует маркера: сообщения в том же чате после typing.
func TestTypingIsThrottledAndNotEchoed(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)

	alice := e.dial(e.alice, "")
	bob := e.dial(e.bob, "")

	alice.write("typing", "", map[string]any{"chat_id": chat.ID})
	alice.write("typing", "", map[string]any{"chat_id": chat.ID})
	alice.write("typing", "t-1", map[string]any{"chat_id": ulid.Make().String()})
	if f := alice.next("error"); f.Ref != "t-1" || !strings.Contains(string(f.Payload), "NOT_FOUND") {
		t.Fatalf("typing in unknown chat: got %v", f)
	}
	e.send(e.alice, chat.ID, "marker")

	var typing int
	for {
		f := bob.read()
		if f.Type == "typing" {
			typing++
			continue
		}
		if f.Type == "message.created" {
			break
		}
	}
	if typing != 1 {
		t.Fatalf("bob got %d typing events, want 1", typing)
	}

	// Алиса свой typing не получает: до маркера у нее не должно быть ни одного typing
	for {
		f := alice.read()
		if f.Type == "typing" {
			t.Fatalf("typing was echoed back to its author: %v", f)
		}
		if f.Type == "message.created" {
			break
		}
	}
}

func TestPresence(t *testing.T) {
	e := newEnv(t)
	chat := e.createGroup(e.alice, "team", e.bob)
	presencePath := "/api/v1/chats/" + chat.ID + "/presence"

	online := func() map[string]bool {
		raw := e.expect(http.StatusOK, http.MethodGet, presencePath, e.alice, nil)
		return decode[struct {
			Online map[string]bool `json:"online"`
		}](t, raw).Online
	}

	if got := online(); got[e.alice] || got[e.bob] || len(got) != 2 {
		t.Fatalf("nobody is connected yet: %v", got)
	}
	e.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, presencePath, e.carol, nil)

	alice := e.dial(e.alice, "")
	bob := e.dial(e.bob, "")

	expectPresence := func(c *wsConn, userID string, isOnline bool) {
		t.Helper()
		for {
			f := c.next("presence")
			p := decode[struct {
				UserID string `json:"user_id"`
				Online bool   `json:"online"`
			}](t, f.Payload)
			if p.UserID == userID {
				if p.Online != isOnline {
					t.Fatalf("presence of %s = %v, want %v", userID, p.Online, isOnline)
				}
				return
			}
		}
	}

	expectPresence(alice, e.bob, true)
	if got := online(); !got[e.alice] || !got[e.bob] {
		t.Fatalf("both must be online: %v", got)
	}

	// Вторая вкладка не меняет статус, а закрытие одной из двух не делает оффлайн
	bobSecondTab := e.dial(e.bob, "")
	bobSecondTab.close()
	waitFor(t, func() bool { return online()[e.bob] })

	bob.close()
	expectPresence(alice, e.bob, false)
	if got := online(); got[e.bob] || !got[e.alice] {
		t.Fatalf("bob must be offline: %v", got)
	}
}

func TestReconnectReplay(t *testing.T) {
	e := newEnv(t, func(o *options) { o.replayLimit = 5 })
	chat := e.createGroup(e.alice, "team", e.bob)
	carolsChat := e.createGroup(e.carol, "not for bob")

	bob := e.dial(e.bob, "")
	cursor := bob.hello.LastEventID
	bob.close()

	// Пока Боб оффлайн
	m1 := e.send(e.alice, chat.ID, "while you were away 1")
	e.send(e.carol, carolsChat.ID, "bob must not get this")
	m2 := e.send(e.alice, chat.ID, "while you were away 2")

	bob = e.dial(e.bob, cursor)
	if bob.hello.ResyncRequired || bob.hello.LastEventID == cursor {
		t.Fatalf("unexpected connected frame: %+v", bob.hello)
	}
	if len(bob.replayed) != 2 ||
		messageOf(t, bob.replayed[0]).ID != m1.ID || messageOf(t, bob.replayed[1]).ID != m2.ID {
		t.Fatalf("unexpected replay: %v", bob.replayed)
	}

	// После догрузки живые события идут как обычно
	e.send(e.alice, chat.ID, "live")
	if got := messageOf(t, bob.next("message.created")); got.Content != "live" {
		t.Fatalf("unexpected live message: %+v", got)
	}

	// Курсор из connected - позиция на момент подключения: с ним догружается только то, что пришло позже
	cursor = bob.hello.LastEventID
	bob.close()
	bob = e.dial(e.bob, cursor)
	if len(bob.replayed) != 1 || messageOf(t, bob.replayed[0]).Content != "live" {
		t.Fatalf("expected only the live message after the cursor, got %v", bob.replayed)
	}
	cursor = bob.hello.LastEventID
	bob.close()

	// Отстал сильнее лимита догрузки - нужна полная перезагрузка через REST
	for i := 0; i < 6; i++ {
		e.send(e.alice, chat.ID, fmt.Sprintf("flood %d", i))
	}
	bob = e.dial(e.bob, cursor)
	if !bob.hello.ResyncRequired || len(bob.replayed) != 0 {
		t.Fatalf("expected resync_required, got %+v with %d replayed", bob.hello, len(bob.replayed))
	}

	// Мусорный курсор - тоже resync, а не ошибка
	if garbage := e.dial(e.bob, "not-an-id"); !garbage.hello.ResyncRequired {
		t.Fatalf("garbage cursor must require resync: %+v", garbage.hello)
	}
}

func TestReplayAfterStreamTrim(t *testing.T) {
	e := newEnv(t, func(o *options) { o.streamMaxLen = 3 })
	chat := e.createGroup(e.alice, "team", e.bob)

	bob := e.dial(e.bob, "")
	cursor := bob.hello.LastEventID
	bob.close()

	// Поток обрезается до ~3 событий: событие-курсор и часть пропущенного потеряны
	for i := 0; i < 10; i++ {
		e.send(e.alice, chat.ID, fmt.Sprintf("message %d", i))
	}

	bob = e.dial(e.bob, cursor)
	if !bob.hello.ResyncRequired {
		t.Fatalf("lost events must require resync: %+v, replayed %d", bob.hello, len(bob.replayed))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
