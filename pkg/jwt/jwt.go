package jwt

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"os"

	domain "main/internal/domain/token_entity"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/oklog/ulid/v2"
)

// Verifier проверяет access-токены, выпущенные сервисом авторизации.
//
// Токены подписаны RS256: сервис авторизации подписывает приватным ключом,
// а этот сервис знает только публичный, поэтому проверить токен может,
// а выпустить - нет.
//
// Ожидаемые claims: sub - ID пользователя (ULID), exp - время истечения.
type Verifier struct {
	publicKey *rsa.PublicKey
}

func NewVerifier(publicKey *rsa.PublicKey) *Verifier {
	return &Verifier{publicKey: publicKey}
}

// NewVerifierFromFile читает публичный RSA-ключ из PEM-файла
func NewVerifierFromFile(path string) (*Verifier, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read JWT public key: %w", err)
	}

	publicKey, err := jwtlib.ParseRSAPublicKeyFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse JWT public key %s: %w", path, err)
	}

	return NewVerifier(publicKey), nil
}

// VerifyAccessToken verifies the access token and returns the user ID if the token is valid.
// Returns token_entity.ErrTokenExpired or token_entity.ErrInvalidToken otherwise.
func (v *Verifier) VerifyAccessToken(tokenString string) (string, error) {
	claims := &jwtlib.RegisteredClaims{}

	_, err := jwtlib.ParseWithClaims(
		tokenString,
		claims,
		func(*jwtlib.Token) (any, error) { return v.publicKey, nil },
		// Жестко фиксируем алгоритм: иначе возможна подмена на HS256,
		// где публичный ключ сработал бы как секрет
		jwtlib.WithValidMethods([]string{jwtlib.SigningMethodRS256.Alg()}),
		jwtlib.WithExpirationRequired(),
	)
	if err != nil {
		if errors.Is(err, jwtlib.ErrTokenExpired) {
			return "", domain.ErrTokenExpired
		}
		return "", domain.ErrInvalidToken
	}

	if _, err := ulid.ParseStrict(claims.Subject); err != nil {
		return "", domain.ErrInvalidToken
	}

	return claims.Subject, nil
}
