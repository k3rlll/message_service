package user_entity

import "errors"

var (
	ErrInvalidUser = errors.New("invalid user")
)

type User struct {
	ID       string `bson:"_id,omitempty"`
	Username string `bson:"username"`
	Email    string `bson:"email"`
	Password string `bson:"password"` // hashed password

}