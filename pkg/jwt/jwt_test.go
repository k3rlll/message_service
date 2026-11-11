package jwt_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "main/internal/domain/token_entity"
	"main/pkg/jwt"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

const userID = "01JD8X3Q5N7ZK2W4M6R8T0V9YA"

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func sign(t *testing.T, method jwtlib.SigningMethod, key any, claims jwtlib.Claims) string {
	t.Helper()
	token, err := jwtlib.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestVerifyAccessToken(t *testing.T) {
	key := newKey(t)
	verifier := jwt.NewVerifier(&key.PublicKey)
	exp := jwtlib.NewNumericDate(time.Now().Add(time.Hour))

	got, err := verifier.VerifyAccessToken(sign(t, jwtlib.SigningMethodRS256, key,
		jwtlib.RegisteredClaims{Subject: userID, ExpiresAt: exp}))
	if err != nil || got != userID {
		t.Fatalf("valid token: %q %v", got, err)
	}

	publicDER, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)

	cases := map[string]struct {
		token string
		want  error
	}{
		"expired": {sign(t, jwtlib.SigningMethodRS256, key,
			jwtlib.RegisteredClaims{Subject: userID, ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(-time.Minute))}),
			domain.ErrTokenExpired},
		"without exp": {sign(t, jwtlib.SigningMethodRS256, key,
			jwtlib.RegisteredClaims{Subject: userID}), domain.ErrInvalidToken},
		"foreign key": {sign(t, jwtlib.SigningMethodRS256, newKey(t),
			jwtlib.RegisteredClaims{Subject: userID, ExpiresAt: exp}), domain.ErrInvalidToken},
		"subject is not a ULID": {sign(t, jwtlib.SigningMethodRS256, key,
			jwtlib.RegisteredClaims{Subject: "admin", ExpiresAt: exp}), domain.ErrInvalidToken},
		// Подмена алгоритма: HS256 с публичным ключом в роли секрета
		"HS256 with the public key": {sign(t, jwtlib.SigningMethodHS256, publicDER,
			jwtlib.RegisteredClaims{Subject: userID, ExpiresAt: exp}), domain.ErrInvalidToken},
		"garbage": {"not.a.token", domain.ErrInvalidToken},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.VerifyAccessToken(tc.token); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewVerifierFromFile(t *testing.T) {
	key := newKey(t)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	path := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := jwt.NewVerifierFromFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.NewVerifierFromFile(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("missing file must fail")
	}
}
