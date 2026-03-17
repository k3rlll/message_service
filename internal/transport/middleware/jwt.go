package middleware

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/oklog/ulid/v2"
)

type JWTManager struct {
	secretKey      string
	accessTokenTTL int
}

func NewJWTManager(secretKey string, tokenTTL int) *JWTManager {
	return &JWTManager{
		secretKey:      secretKey,
		accessTokenTTL: tokenTTL,
	}
}

// NewAccessToken generates a new JWT access token for the given user ID.
func (Manager *JWTManager) NewAccessToken(userID ulid.ULID) (string, error) {
	jwtClaims := jwt.NewWithClaims(jwt.SigningMethodHS256, &jwt.MapClaims{
		"user_id": userID,
		"exp":     time.Now().Add(time.Duration(Manager.accessTokenTTL) * time.Minute).Unix(),
		"iat":     time.Now().Unix(),
	})
	tokenString, err := jwtClaims.SignedString([]byte(Manager.secretKey))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

// VerifyAccessToken verifies the access token and returns the user ID if the token is valid.
func (Manager *JWTManager) VerifyAccessToken(tokenString string) (userID ulid.ULID, err error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrTokenMalformed
		}
		return []byte(Manager.secretKey), nil
	})
	if err != nil {
		return ulid.Zero, err
	}
	sub, err := token.Claims.GetSubject()
	if err != nil || sub == "" {
		return ulid.Zero, jwt.ErrTokenMalformed
	}

	userID, err = ulid.Parse(sub)
	if err != nil {
		return ulid.Zero, err
	}

	return userID, nil
}
