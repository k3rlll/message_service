// devtoken - заглушка сервиса авторизации для локальной разработки.
//
// Выпускает access-токен в том же формате, который будет выпускать настоящий
// сервис авторизации: RS256, claim sub - ID пользователя (ULID).
// При первом запуске генерирует пару RSA-ключей; публичный ключ читает message_service.
//
//	go run ./cmd/devtoken                  # новый пользователь
//	go run ./cmd/devtoken -user <ULID>     # токен для существующего пользователя
//
// НЕ для продакшена: приватный ключ лежит на диске без защиты.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/oklog/ulid/v2"
)

const (
	privateKeyFile = "jwt_private.pem"
	publicKeyFile  = "jwt_public.pem"
)

func main() {
	userID := flag.String("user", "", "ID пользователя (ULID); по умолчанию генерируется новый")
	ttl := flag.Duration("ttl", 24*time.Hour, "время жизни токена")
	keysDir := flag.String("keys", "keys", "каталог с ключами")
	flag.Parse()

	if err := run(*userID, *ttl, *keysDir); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(userID string, ttl time.Duration, keysDir string) error {
	if userID == "" {
		userID = ulid.Make().String()
	} else if _, err := ulid.ParseStrict(userID); err != nil {
		return fmt.Errorf("user ID must be a ULID: %w", err)
	}

	privateKey, err := loadOrCreateKeys(keysDir)
	if err != nil {
		return err
	}

	now := time.Now()
	token, err := jwtlib.NewWithClaims(jwtlib.SigningMethodRS256, jwtlib.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwtlib.NewNumericDate(now),
		ExpiresAt: jwtlib.NewNumericDate(now.Add(ttl)),
	}).SignedString(privateKey)
	if err != nil {
		return fmt.Errorf("sign token: %w", err)
	}

	// Служебная информация идет в stderr, чтобы в stdout был только токен:
	// TOKEN=$(go run ./cmd/devtoken)
	fmt.Fprintln(os.Stderr, "user_id:", userID)
	fmt.Fprintln(os.Stderr, "expires:", now.Add(ttl).Format(time.RFC3339))
	fmt.Println(token)
	return nil
}

// loadOrCreateKeys читает приватный ключ, а если его нет - генерирует новую пару
func loadOrCreateKeys(dir string) (*rsa.PrivateKey, error) {
	privatePath := filepath.Join(dir, privateKeyFile)
	publicPath := filepath.Join(dir, publicKeyFile)

	pemBytes, err := os.ReadFile(privatePath)
	switch {
	case err == nil:
		privateKey, err := jwtlib.ParseRSAPrivateKeyFromPEM(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", privatePath, err)
		}
		return privateKey, nil

	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read %s: %w", privatePath, err)
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("encode public key: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	if err := writePEM(privatePath, "PRIVATE KEY", privateDER, 0o600); err != nil {
		return nil, err
	}
	if err := writePEM(publicPath, "PUBLIC KEY", publicDER, 0o644); err != nil {
		return nil, err
	}

	fmt.Fprintln(os.Stderr, "generated new key pair in", dir)
	return privateKey, nil
}

func writePEM(path, blockType string, der []byte, perm fs.FileMode) error {
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
