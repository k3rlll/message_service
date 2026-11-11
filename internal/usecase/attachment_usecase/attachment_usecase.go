package attachment_usecase

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	domainComm "main/internal/domain"
	domain "main/internal/domain/message_entity"

	"github.com/oklog/ulid/v2"
)

const maxFileNameLength = 200

// Storage - объектное хранилище. nil означает, что вложения не настроены.
type Storage interface {
	PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Size(ctx context.Context, key string) (int64, error)
}

type membershipChecker interface {
	MemberRole(ctx context.Context, userID, chatID string) (string, error)
}

type Config struct {
	UploadURLTTL   time.Duration
	DownloadURLTTL time.Duration
	MaxFileSize    int64
}

type AttachmentUsecase struct {
	storage Storage
	members membershipChecker
	cfg     Config
}

func NewUsecase(storage Storage, members membershipChecker, cfg Config) *AttachmentUsecase {
	return &AttachmentUsecase{
		storage: storage,
		members: members,
		cfg:     cfg,
	}
}

// SignedURL - временная подписанная ссылка на объект в хранилище
type SignedURL struct {
	Key       string    `json:"key"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateUpload выдает ссылку для загрузки файла в чат.
// Клиент загружает файл по ней методом PUT, а затем отправляет сообщение,
// указав полученный key во вложениях.
func (u *AttachmentUsecase) CreateUpload(
	ctx context.Context,
	userID string,
	chatID string,
	fileName string,
	size int64,
) (*SignedURL, error) {
	const op = "AttachmentUsecase.CreateUpload"

	if u.storage == nil {
		return nil, fmt.Errorf("%s: %w", op, domainComm.ErrUnavailable)
	}
	if _, err := u.members.MemberRole(ctx, userID, chatID); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	fileName = sanitizeFileName(fileName)
	if fileName == "" {
		return nil, domainComm.Invalid("file name is empty or invalid")
	}
	if size <= 0 || size > u.cfg.MaxFileSize {
		return nil, domainComm.Invalid(
			fmt.Sprintf("file size must be between 1 and %d bytes", u.cfg.MaxFileSize))
	}

	// Префикс чата в ключе - основа проверки доступа: по ключу видно, какому чату
	// принадлежит файл. Случайная часть не дает угадать или перезаписать чужой файл.
	key := path.Join(chatPrefix(chatID), ulid.Make().String(), fileName)

	url, err := u.storage.PresignPut(ctx, key, u.cfg.UploadURLTTL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	return &SignedURL{Key: key, URL: url, ExpiresAt: time.Now().Add(u.cfg.UploadURLTTL)}, nil
}

// CreateDownload выдает ссылку для скачивания файла. Доступна только участникам чата.
func (u *AttachmentUsecase) CreateDownload(
	ctx context.Context,
	userID string,
	chatID string,
	key string,
) (*SignedURL, error) {
	const op = "AttachmentUsecase.CreateDownload"

	if u.storage == nil {
		return nil, fmt.Errorf("%s: %w", op, domainComm.ErrUnavailable)
	}
	if _, err := u.members.MemberRole(ctx, userID, chatID); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if !belongsToChat(key, chatID) {
		return nil, domainComm.NotFound("attachment not found in this chat")
	}

	url, err := u.storage.PresignGet(ctx, key, u.cfg.DownloadURLTTL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	return &SignedURL{Key: key, URL: url, ExpiresAt: time.Now().Add(u.cfg.DownloadURLTTL)}, nil
}

// Validate проверяет вложения перед сохранением сообщения: файл должен принадлежать
// этому чату, быть реально загружен и укладываться в лимит размера.
// Размер берется из хранилища, а не со слов клиента: подписанная PUT-ссылка
// сама размер не ограничивает.
func (u *AttachmentUsecase) Validate(
	ctx context.Context,
	chatID string,
	attachments []domain.Attachment,
) ([]domain.Attachment, error) {
	const op = "AttachmentUsecase.Validate"

	if len(attachments) == 0 {
		return nil, nil
	}
	if u.storage == nil {
		return nil, fmt.Errorf("%s: %w", op, domainComm.ErrUnavailable)
	}
	if len(attachments) > domain.MaxAttachments {
		return nil, domainComm.Invalid(
			fmt.Sprintf("a message can have at most %d attachments", domain.MaxAttachments))
	}

	validated := make([]domain.Attachment, 0, len(attachments))
	for _, attachment := range attachments {
		if !belongsToChat(attachment.Key, chatID) {
			return nil, domainComm.Invalid("attachment does not belong to this chat: " + attachment.Key)
		}

		size, err := u.storage.Size(ctx, attachment.Key)
		if err != nil {
			if errors.Is(err, domainComm.ErrNotFound) {
				return nil, domainComm.Invalid("attachment is not uploaded: " + attachment.Key)
			}
			return nil, fmt.Errorf("%s: %w", op, err)
		}
		if size > u.cfg.MaxFileSize {
			return nil, domainComm.Invalid("attachment is too large: " + attachment.Key)
		}

		attachment.Size = size
		if attachment.Name == "" {
			attachment.Name = path.Base(attachment.Key)
		}
		validated = append(validated, attachment)
	}

	return validated, nil
}

func chatPrefix(chatID string) string {
	return "chats/" + chatID
}

func belongsToChat(key, chatID string) bool {
	// path.Clean отсекает попытки выйти из префикса через ".."
	return key == path.Clean(key) && strings.HasPrefix(key, chatPrefix(chatID)+"/")
}

// sanitizeFileName оставляет только имя файла без пути и управляющих символов
func sanitizeFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)

	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)

	if name == "." || name == ".." || name == "/" {
		return ""
	}
	if runes := []rune(name); len(runes) > maxFileNameLength {
		name = string(runes[len(runes)-maxFileNameLength:]) // хвост сохраняет расширение
	}
	return name
}
