package attachment_usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domainComm "main/internal/domain"
	domain "main/internal/domain/message_entity"
	"main/internal/testutil/fakes"
)

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"photo.png":                       "photo.png",
		"../../etc/passwd":                "passwd",
		`C:\Users\me\report.pdf`:          "report.pdf",
		"  spaced name.txt  ":             "spaced name.txt",
		"bad\x00\x07name\x7f.txt":         "badname.txt",
		"..":                              "",
		"/":                               "",
		"":                                "",
		strings.Repeat("a", 300) + ".jpg": strings.Repeat("a", maxFileNameLength-4) + ".jpg",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBelongsToChat(t *testing.T) {
	cases := map[string]bool{
		"chats/A/01X/file.png":      true,
		"chats/B/01X/file.png":      false,
		"chats/A/../B/01X/file.png": false,
		"chats/A":                   false,
		"chats/AB/01X/file.png":     false, // префикс другого чата, совпадающий по началу
		"/chats/A/01X/file.png":     false,
		"chats/A//01X/file.png":     false,
	}
	for key, want := range cases {
		if got := belongsToChat(key, "A"); got != want {
			t.Errorf("belongsToChat(%q) = %v, want %v", key, got, want)
		}
	}
}

type allowAll struct{}

func (allowAll) MemberRole(context.Context, string, string) (string, error) { return "member", nil }

func TestValidate(t *testing.T) {
	ctx := context.Background()
	storage := fakes.NewStorage()
	uc := NewUsecase(storage, allowAll{}, Config{MaxFileSize: 100, UploadURLTTL: time.Minute, DownloadURLTTL: time.Minute})

	storage.Upload("chats/A/1/small.png", 10)
	storage.Upload("chats/A/2/big.bin", 101)
	storage.Upload("chats/B/3/other.png", 10)

	got, err := uc.Validate(ctx, "A", []domain.Attachment{{Key: "chats/A/1/small.png", Size: 999999}})
	if err != nil || got[0].Size != 10 || got[0].Name != "small.png" {
		t.Fatalf("size must come from storage and name from key: %+v %v", got, err)
	}

	for name, key := range map[string]string{
		"not uploaded": "chats/A/9/missing.png",
		"too large":    "chats/A/2/big.bin",
		"other chat":   "chats/B/3/other.png",
	} {
		if _, err := uc.Validate(ctx, "A", []domain.Attachment{{Key: key}}); !errors.Is(err, domainComm.ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}

	tooMany := make([]domain.Attachment, domain.MaxAttachments+1)
	if _, err := uc.Validate(ctx, "A", tooMany); !errors.Is(err, domainComm.ErrInvalidInput) {
		t.Errorf("too many: err = %v", err)
	}

	disabled := NewUsecase(nil, allowAll{}, Config{})
	if _, err := disabled.Validate(ctx, "A", []domain.Attachment{{Key: "chats/A/1/small.png"}}); !errors.Is(err, domainComm.ErrUnavailable) {
		t.Errorf("disabled storage: err = %v, want ErrUnavailable", err)
	}
	if got, err := disabled.Validate(ctx, "A", nil); err != nil || got != nil {
		t.Errorf("no attachments must pass without storage: %v %v", got, err)
	}
}
