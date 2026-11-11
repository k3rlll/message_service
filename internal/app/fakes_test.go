package app_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	domainComm "main/internal/domain"
	domainChat "main/internal/domain/chat_entity"
	domain "main/internal/domain/message_entity"
)

// Фейки MongoDB-репозиториев и S3. Повторяют семантику настоящих реализаций,
// включая уникальные индексы и защиту от гонок, которую дают фильтры запросов.

// clone - глубокая копия через JSON, чтобы тест не видел общих срезов и мап
func clone[T any](v T) T {
	data, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}

// --- Сообщения ---

type fakeMessageRepo struct {
	mu   sync.Mutex
	msgs []domain.Message
}

func (r *fakeMessageRepo) SaveMessage(_ context.Context, msg *domain.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if msg.ClientMessageID != "" {
		for _, m := range r.msgs {
			if m.ChatID == msg.ChatID && m.SenderID == msg.SenderID && m.ClientMessageID == msg.ClientMessageID {
				return domainComm.ErrDuplicate
			}
		}
	}
	r.msgs = append(r.msgs, clone(*msg))
	return nil
}

func (r *fakeMessageRepo) findOne(match func(domain.Message) bool) (*domain.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, m := range r.msgs {
		if match(m) {
			c := clone(m)
			return &c, nil
		}
	}
	return nil, domainComm.ErrNotFound
}

func (r *fakeMessageRepo) GetMessage(_ context.Context, chatID, messageID string) (*domain.Message, error) {
	return r.findOne(func(m domain.Message) bool { return m.ID == messageID && m.ChatID == chatID })
}

func (r *fakeMessageRepo) GetMessageByClientID(_ context.Context, chatID, senderID, clientID string) (*domain.Message, error) {
	return r.findOne(func(m domain.Message) bool {
		return m.ChatID == chatID && m.SenderID == senderID && m.ClientMessageID == clientID
	})
}

func (r *fakeMessageRepo) query(chatID, anchorID string, limit int64, match func(domain.Message) bool) []domain.Message {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := []domain.Message{}
	for _, m := range r.msgs {
		if m.ChatID != chatID || (anchorID != "" && m.ID >= anchorID) || !match(m) {
			continue
		}
		out = append(out, clone(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out
}

func (r *fakeMessageRepo) GetMessages(_ context.Context, chatID string, ids []string) ([]domain.Message, error) {
	set := toSet(ids)
	return r.query(chatID, "", int64(len(ids)), func(m domain.Message) bool {
		return set[m.ID] && !m.IsDeleted()
	}), nil
}

func (r *fakeMessageRepo) ListMessages(_ context.Context, chatID, anchorID string, limit int64) ([]domain.Message, error) {
	return r.query(chatID, anchorID, limit, func(domain.Message) bool { return true }), nil
}

func (r *fakeMessageRepo) SearchMessages(_ context.Context, chatID, text, anchorID string, limit int64) ([]domain.Message, error) {
	return r.query(chatID, anchorID, limit, func(m domain.Message) bool {
		return m.Type == domain.TypeText && !m.IsDeleted() && strings.Contains(m.Content, text)
	}), nil
}

func (r *fakeMessageRepo) CountUnread(_ context.Context, chatID, userID, afterID string, limit int64) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var count int64
	for _, m := range r.msgs {
		if m.ChatID == chatID && m.SenderID != userID && !m.IsDeleted() && m.ID > afterID {
			count++
		}
	}
	return min(count, limit), nil
}

// update применяет изменение к первому подходящему сообщению и возвращает его копию
func (r *fakeMessageRepo) update(match func(domain.Message) bool, change func(*domain.Message)) (*domain.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range r.msgs {
		if match(r.msgs[i]) {
			change(&r.msgs[i])
			c := clone(r.msgs[i])
			return &c, nil
		}
	}
	return nil, domainComm.ErrNotFound
}

func (r *fakeMessageRepo) UpdateMessage(_ context.Context, userID, chatID, messageID, content string) (*domain.Message, error) {
	return r.update(
		func(m domain.Message) bool {
			return m.ID == messageID && m.ChatID == chatID && m.SenderID == userID && !m.IsDeleted()
		},
		func(m *domain.Message) {
			m.Content = content
			m.IsEdited = true
			m.UpdatedAt = time.Now().UTC()
		})
}

func (r *fakeMessageRepo) SoftDeleteMessages(_ context.Context, chatID string, ids []string, senderID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	set := toSet(ids)
	now := time.Now().UTC()

	var deleted []string
	for i := range r.msgs {
		m := &r.msgs[i]
		if !set[m.ID] || m.ChatID != chatID || m.IsDeleted() || (senderID != "" && m.SenderID != senderID) {
			continue
		}
		m.DeletedAt = &now
		m.Content = ""
		m.Attachments = nil
		m.Reactions = nil
		m.Metadata = nil
		deleted = append(deleted, m.ID)
	}
	return deleted, nil
}

func (r *fakeMessageRepo) AddReaction(_ context.Context, chatID, messageID string, reaction domain.Reaction) (*domain.Message, error) {
	return r.update(
		func(m domain.Message) bool { return m.ID == messageID && m.ChatID == chatID && !m.IsDeleted() },
		func(m *domain.Message) {
			for _, existing := range m.Reactions {
				if existing == reaction {
					return
				}
			}
			m.Reactions = append(m.Reactions, reaction)
		})
}

func (r *fakeMessageRepo) RemoveReaction(_ context.Context, chatID, messageID string, reaction domain.Reaction) (*domain.Message, error) {
	return r.update(
		func(m domain.Message) bool { return m.ID == messageID && m.ChatID == chatID && !m.IsDeleted() },
		func(m *domain.Message) {
			kept := m.Reactions[:0]
			for _, existing := range m.Reactions {
				if existing != reaction {
					kept = append(kept, existing)
				}
			}
			m.Reactions = kept
		})
}

// --- Чаты ---

type fakeChatRepo struct {
	mu    sync.Mutex
	chats map[string]*domainChat.Chat
}

func newFakeChatRepo() *fakeChatRepo {
	return &fakeChatRepo{chats: map[string]*domainChat.Chat{}}
}

func (r *fakeChatRepo) CreateChat(_ context.Context, chat *domainChat.Chat) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if chat.DirectKey != "" {
		for _, c := range r.chats {
			if c.DirectKey == chat.DirectKey {
				return domainComm.ErrDuplicate
			}
		}
	}
	c := clone(*chat)
	c.DirectKey = chat.DirectKey // в JSON ключ не попадает (json:"-"), копируем вручную
	r.chats[chat.ID] = &c
	return nil
}

func (r *fakeChatRepo) GetChat(_ context.Context, chatID string) (*domainChat.Chat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	chat, ok := r.chats[chatID]
	if !ok {
		return nil, domainChat.ErrChatNotFound
	}
	c := clone(*chat)
	return &c, nil
}

func (r *fakeChatRepo) GetDirectChat(_ context.Context, directKey string) (*domainChat.Chat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, chat := range r.chats {
		if chat.DirectKey == directKey {
			c := clone(*chat)
			return &c, nil
		}
	}
	return nil, domainChat.ErrChatNotFound
}

func (r *fakeChatRepo) ListChats(_ context.Context, userID string, before time.Time, limit int64) ([]domainChat.Chat, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := []domainChat.Chat{}
	for _, chat := range r.chats {
		if _, ok := chat.Member(userID); !ok {
			continue
		}
		if !before.IsZero() && !chat.UpdatedAt.Before(before) {
			continue
		}
		out = append(out, clone(*chat))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeChatRepo) ChatMembers(_ context.Context, chatID string) ([]domainChat.Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	chat, ok := r.chats[chatID]
	if !ok {
		return nil, domainChat.ErrChatNotFound
	}
	return clone(chat.Members), nil
}

func (r *fakeChatRepo) ContactIDs(_ context.Context, userID string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	set := map[string]bool{}
	for _, chat := range r.chats {
		if _, ok := chat.Member(userID); !ok {
			continue
		}
		for _, m := range chat.Members {
			if m.UserID != userID {
				set[m.UserID] = true
			}
		}
	}

	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out, nil
}

// modify меняет чат под мьютексом. Если чата нет, возвращает ErrNotFound,
// как UpdateOne с MatchedCount == 0.
func (r *fakeChatRepo) modify(chatID string, change func(*domainChat.Chat) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	chat, ok := r.chats[chatID]
	if !ok {
		return domainComm.ErrNotFound
	}
	return change(chat)
}

func (r *fakeChatRepo) RenameChat(_ context.Context, chatID, name string) error {
	return r.modify(chatID, func(c *domainChat.Chat) error {
		c.Name = name
		c.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (r *fakeChatRepo) AddMember(_ context.Context, chatID string, member domainChat.Member) error {
	return r.modify(chatID, func(c *domainChat.Chat) error {
		if _, exists := c.Member(member.UserID); exists {
			return domainComm.ErrConflict
		}
		c.Members = append(c.Members, member)
		c.UpdatedAt = time.Now().UTC()
		return nil
	})
}

func (r *fakeChatRepo) RemoveMember(_ context.Context, chatID, userID string) error {
	return r.modify(chatID, func(c *domainChat.Chat) error {
		kept := c.Members[:0]
		for _, m := range c.Members {
			if m.UserID != userID {
				kept = append(kept, m)
			}
		}
		if len(kept) == len(c.Members) {
			return domainComm.ErrNotFound
		}
		c.Members = kept
		return nil
	})
}

func (r *fakeChatRepo) SetRoles(_ context.Context, chatID string, roles map[string]string) error {
	err := r.modify(chatID, func(c *domainChat.Chat) error {
		for i := range c.Members {
			if role, ok := roles[c.Members[i].UserID]; ok {
				c.Members[i].Role = role
			}
		}
		return nil
	})
	if err != nil {
		return domainChat.ErrChatNotFound
	}
	return nil
}

func (r *fakeChatRepo) SetLastMessage(_ context.Context, chatID string, last domainChat.LastMessage) error {
	err := r.modify(chatID, func(c *domainChat.Chat) error {
		if c.LastMessage == nil || c.LastMessage.ID < last.ID {
			c.LastMessage = &last
			c.UpdatedAt = last.CreatedAt
		}
		return nil
	})
	if err == domainComm.ErrNotFound {
		return nil
	}
	return err
}

func (r *fakeChatRepo) PatchLastMessage(_ context.Context, chatID string, ids []string, content string, deleted bool) error {
	set := toSet(ids)
	err := r.modify(chatID, func(c *domainChat.Chat) error {
		if c.LastMessage != nil && set[c.LastMessage.ID] {
			c.LastMessage.Content = content
			c.LastMessage.IsDeleted = deleted
		}
		return nil
	})
	if err == domainComm.ErrNotFound {
		return nil
	}
	return err
}

func (r *fakeChatRepo) MarkRead(_ context.Context, chatID, userID, messageID string) (bool, error) {
	changed := false
	err := r.modify(chatID, func(c *domainChat.Chat) error {
		for i := range c.Members {
			if c.Members[i].UserID == userID && c.Members[i].LastReadMessageID < messageID {
				c.Members[i].LastReadMessageID = messageID
				changed = true
			}
		}
		return nil
	})
	if err == domainComm.ErrNotFound {
		return false, nil
	}
	return changed, err
}

func (r *fakeChatRepo) AddPin(_ context.Context, chatID, messageID string) (bool, error) {
	changed := false
	err := r.modify(chatID, func(c *domainChat.Chat) error {
		for _, id := range c.PinnedMessageIDs {
			if id == messageID {
				return nil
			}
		}
		c.PinnedMessageIDs = append(c.PinnedMessageIDs, messageID)
		changed = true
		return nil
	})
	if err == domainComm.ErrNotFound {
		return false, nil
	}
	return changed, err
}

func (r *fakeChatRepo) RemovePin(_ context.Context, chatID, messageID string) (bool, error) {
	changed := false
	err := r.modify(chatID, func(c *domainChat.Chat) error {
		kept := c.PinnedMessageIDs[:0]
		for _, id := range c.PinnedMessageIDs {
			if id == messageID {
				changed = true
				continue
			}
			kept = append(kept, id)
		}
		c.PinnedMessageIDs = kept
		return nil
	})
	if err == domainComm.ErrNotFound {
		return false, nil
	}
	return changed, err
}

// --- S3 ---

type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]int64 // key - размер
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string]int64{}}
}

// upload имитирует загрузку файла клиентом по подписанной ссылке
func (s *fakeStorage) upload(key string, size int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = size
}

func (s *fakeStorage) PresignPut(_ context.Context, key string, _ time.Duration) (string, error) {
	return "http://s3.test/put/" + key, nil
}

func (s *fakeStorage) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "http://s3.test/get/" + key, nil
}

func (s *fakeStorage) Size(_ context.Context, key string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	size, ok := s.objects[key]
	if !ok {
		return 0, domainComm.ErrNotFound
	}
	return size, nil
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
