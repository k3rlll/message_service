package app_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"main/internal/app"
	events "main/internal/broker/redis_events"
	"main/internal/cache"
	"main/internal/configs"
	domainChat "main/internal/domain/chat_entity"
	domain "main/internal/domain/message_entity"
	"main/internal/presence"
	"main/internal/ratelimit"
	chatHandlers "main/internal/transport/http/chat_handlers"
	msgHandlers "main/internal/transport/http/message_handlers"
	wsHandler "main/internal/transport/http/ws_handler"
	attachUC "main/internal/usecase/attachment_usecase"
	cacheUC "main/internal/usecase/cache_usecase"
	chatUC "main/internal/usecase/chat_usecase"
	msgUC "main/internal/usecase/message_usecase"
	presenceUC "main/internal/usecase/presence_usecase"
	"main/internal/ws"
	"main/pkg/jwt"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
)

// Сквозные тесты: настоящие HTTP-сервер, WebSocket, Хаб, кэш, все usecase,
// шина событий, presence и rate limit на Redis (через miniredis).
// Подменены только MongoDB и S3.

const (
	testTimeout = 5 * time.Second
	maxFileSize = 1000
)

type options struct {
	noStorage    bool
	messagesRule ratelimit.Rule
	httpRule     ratelimit.Rule
	replayLimit  int64
	streamMaxLen int64
}

type env struct {
	t       *testing.T
	baseURL string
	key     *rsa.PrivateKey // ключ "сервиса авторизации"
	storage *fakeStorage

	alice, bob, carol, dave string
}

func newEnv(t *testing.T, opts ...func(*options)) *env {
	t.Helper()

	o := options{replayLimit: 100, streamMaxLen: 10000}
	for _, opt := range opts {
		opt(&o)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())

	e := &env{
		t:       t,
		key:     newKey(t),
		storage: newFakeStorage(),
		alice:   ulid.Make().String(),
		bob:     ulid.Make().String(),
		carol:   ulid.Make().String(),
		dave:    ulid.Make().String(),
	}

	redisClient := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	chatRepo := newFakeChatRepo()
	messageRepo := &fakeMessageRepo{}

	membersCache := cacheUC.NewInMemoryCache(
		cache.NewCache[string, cacheUC.ChatMembers](
			configs.InMemoryCacheConfig{MaximumSize: 100, InitialCapacity: 10, TTL: time.Minute}, logger),
		logger, chatRepo)
	bus := events.NewEventBus(redisClient, membersCache, logger, events.Config{
		StreamMaxLen: o.streamMaxLen,
		ReplayLimit:  o.replayLimit,
	})
	limiter := ratelimit.NewLimiter(redisClient)

	var storage attachUC.Storage
	if !o.noStorage {
		storage = e.storage
	}

	attachments := attachUC.NewUsecase(storage, membersCache, attachUC.Config{
		UploadURLTTL: time.Minute, DownloadURLTTL: time.Minute, MaxFileSize: maxFileSize,
	})
	messages := msgUC.NewUsecase(messageRepo, chatRepo, membersCache, bus,
		attachments, limiter, o.messagesRule, logger)
	chats := chatUC.NewUsecase(chatRepo, messageRepo, membersCache, bus, logger)
	presenceUsecase := presenceUC.NewUsecase(
		presence.NewStore(redisClient, time.Minute, time.Minute),
		chatRepo, membersCache, bus, logger)

	hub := ws.NewHub(logger)

	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); hub.Run(ctx) }()
	go func() { defer workers.Done(); bus.Run(ctx, hub) }()

	server := httptest.NewServer(app.NewServer(logger,
		app.Handlers{
			Messages: msgHandlers.NewHandler(logger, messages),
			Chats:    chatHandlers.NewHandler(logger, chats, presenceUsecase, attachments),
			WS: wsHandler.NewHandler(logger, hub,
				wsHandler.NewBackend(messages, chats, presenceUsecase, bus),
				configs.WebsocketConfig{
					InsecureSkipVerify: true, ClientChanSize: 64,
					MaxMessageSize: 16384, CompressionThreshold: 512,
				}),
		},
		app.RouteDeps{
			Verifier: jwt.NewVerifier(&e.key.PublicKey),
			Limiter:  limiter,
			HTTPRule: o.httpRule,
			Logger:   logger,
		},
	))
	e.baseURL = server.URL

	t.Cleanup(func() {
		cancel()
		workers.Wait() // Хаб закрывает WebSocket соединения, иначе server.Close зависнет
		server.Close()
		redisClient.Close()
	})

	readyCtx, readyCancel := context.WithTimeout(ctx, testTimeout)
	defer readyCancel()
	if err := bus.WaitReady(readyCtx); err != nil {
		t.Fatalf("event bus is not ready: %v", err)
	}

	return e
}

// --- Токены ---

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// signToken выпускает токен так же, как это делает сервис авторизации
func signToken(t *testing.T, method jwtlib.SigningMethod, key any, userID string, ttl time.Duration) string {
	t.Helper()
	token, err := jwtlib.NewWithClaims(method, jwtlib.RegisteredClaims{
		Subject:   userID,
		ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(ttl)),
	}).SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func (e *env) token(userID string) string {
	e.t.Helper()
	return signToken(e.t, jwtlib.SigningMethodRS256, e.key, userID, time.Hour)
}

// --- HTTP ---

type response struct {
	status int
	body   []byte
	header http.Header
}

func (e *env) do(method, path, userID string, body any) response {
	e.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, e.baseURL+path, reader)
	if err != nil {
		e.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		req.Header.Set("Authorization", "Bearer "+e.token(userID))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, body: raw, header: resp.Header}
}

// expect выполняет запрос и проверяет статус
func (e *env) expect(want int, method, path, userID string, body any) []byte {
	e.t.Helper()
	resp := e.do(method, path, userID, body)
	if resp.status != want {
		e.t.Fatalf("%s %s as %s: status = %d, want %d, body: %s",
			method, path, e.name(userID), resp.status, want, resp.body)
	}
	return resp.body
}

// expectError проверяет статус и код ошибки в теле ответа
func (e *env) expectError(status int, code, method, path, userID string, body any) {
	e.t.Helper()
	raw := e.expect(status, method, path, userID, body)

	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Error.Code != code {
		e.t.Fatalf("%s %s: error code = %q, want %q, body: %s", method, path, resp.Error.Code, code, raw)
	}
}

func (e *env) name(userID string) string {
	switch userID {
	case e.alice:
		return "alice"
	case e.bob:
		return "bob"
	case e.carol:
		return "carol"
	case e.dave:
		return "dave"
	case "":
		return "anonymous"
	}
	return userID
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %T from %s: %v", v, raw, err)
	}
	return v
}

type chatSummary struct {
	domainChat.Chat
	UnreadCount int64 `json:"unread_count"`
}

type messagesPage struct {
	Messages []domain.Message `json:"messages"`
	HasMore  bool             `json:"has_more"`
}

func (e *env) createGroup(ownerID, name string, memberIDs ...string) domainChat.Chat {
	e.t.Helper()
	raw := e.expect(http.StatusCreated, http.MethodPost, "/api/v1/chats", ownerID,
		map[string]any{"type": "group", "name": name, "member_ids": memberIDs})
	return decode[domainChat.Chat](e.t, raw)
}

func (e *env) getChat(userID, chatID string) chatSummary {
	e.t.Helper()
	raw := e.expect(http.StatusOK, http.MethodGet, "/api/v1/chats/"+chatID, userID, nil)
	return decode[chatSummary](e.t, raw)
}

func (e *env) send(userID, chatID, content string) domain.Message {
	e.t.Helper()
	return e.sendBody(userID, map[string]any{"chat_id": chatID, "content": content})
}

func (e *env) sendBody(userID string, body map[string]any) domain.Message {
	e.t.Helper()
	raw := e.expect(http.StatusCreated, http.MethodPost, "/api/v1/messages", userID, body)
	return decode[domain.Message](e.t, raw)
}

func (e *env) list(userID, chatID, query string) messagesPage {
	e.t.Helper()
	raw := e.expect(http.StatusOK, http.MethodGet,
		"/api/v1/messages?chat_id="+chatID+query, userID, nil)
	return decode[messagesPage](e.t, raw)
}

// --- WebSocket ---

type frame struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	ChatID  string          `json:"chat_id"`
	Ref     string          `json:"ref"`
	Payload json.RawMessage `json:"payload"`
}

func (f frame) String() string {
	return fmt.Sprintf("{id:%s type:%s chat:%s ref:%s payload:%s}", f.ID, f.Type, f.ChatID, f.Ref, f.Payload)
}

type connected struct {
	UserID         string `json:"user_id"`
	LastEventID    string `json:"last_event_id"`
	ResyncRequired bool   `json:"resync_required"`
}

type wsConn struct {
	t    *testing.T
	conn *websocket.Conn
	// Кадры, полученные до connected (догруженные события)
	replayed []frame
	hello    connected
}

// dial подключается по WebSocket с токеном в query-параметре, как это делает браузер,
// и дожидается кадра connected
func (e *env) dial(userID, lastEventID string) *wsConn {
	e.t.Helper()

	query := url.Values{"token": {e.token(userID)}}
	if lastEventID != "" {
		query.Set("last_event_id", lastEventID)
	}
	wsURL := "ws" + strings.TrimPrefix(e.baseURL, "http") + "/api/v1/ws?" + query.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		e.t.Fatalf("dial websocket: %v", err)
	}
	e.t.Cleanup(func() { conn.CloseNow() })

	c := &wsConn{t: e.t, conn: conn}
	for {
		f := c.read()
		if f.Type == "connected" {
			c.hello = decode[connected](e.t, f.Payload)
			return c
		}
		if f.Type != "presence" {
			c.replayed = append(c.replayed, f)
		}
	}
}

func (c *wsConn) close() {
	c.conn.Close(websocket.StatusNormalClosure, "")
}

// read читает следующий кадр без фильтрации
func (c *wsConn) read() frame {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, raw, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("read websocket frame: %v", err)
	}
	return decode[frame](c.t, raw)
}

// next читает следующий кадр ожидаемого типа. Кадры presence и typing, если их
// не ждут, пропускаются: они приходят в недетерминированные моменты.
// Любой другой неожиданный кадр - ошибка теста.
func (c *wsConn) next(want string) frame {
	c.t.Helper()
	for {
		f := c.read()
		if f.Type == want {
			return f
		}
		if f.Type == "presence" || f.Type == "typing" {
			continue
		}
		c.t.Fatalf("got frame %v, want type %s", f, want)
	}
}

// nextSet читает кадры, пока не получит по одному кадру каждого типа, в любом порядке.
// Нужен там, где порядок не гарантирован: например, ack на команду и событие,
// которое эта же команда породила, идут к клиенту разными путями.
func (c *wsConn) nextSet(types ...string) map[string]frame {
	c.t.Helper()
	got := make(map[string]frame, len(types))
	for len(got) < len(types) {
		f := c.read()
		if slices.Contains(types, f.Type) {
			if _, dup := got[f.Type]; dup {
				c.t.Fatalf("duplicate frame of type %s: %v", f.Type, f)
			}
			got[f.Type] = f
			continue
		}
		if f.Type == "presence" || f.Type == "typing" {
			continue
		}
		c.t.Fatalf("got frame %v, want one of %v", f, types)
	}
	return got
}

func (c *wsConn) write(frameType, ref string, payload any) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	raw, _ := json.Marshal(map[string]any{"type": frameType, "ref": ref, "payload": payload})
	if err := c.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		c.t.Fatalf("write websocket frame: %v", err)
	}
}

func messageOf(t *testing.T, f frame) domain.Message {
	t.Helper()
	return decode[domain.Message](t, f.Payload)
}

func memberIDs(chat domainChat.Chat) []string {
	ids := make([]string, len(chat.Members))
	for i, m := range chat.Members {
		ids[i] = m.UserID
	}
	slices.Sort(ids)
	return ids
}

func roleOf(t *testing.T, chat chatSummary, userID string) string {
	t.Helper()
	member, ok := chat.Member(userID)
	if !ok {
		t.Fatalf("user %s is not a member of chat %s", userID, chat.ID)
	}
	return member.Role
}
