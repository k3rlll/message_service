// Package user_entity. Пользователями (регистрация, логин, пароли) владеет
// отдельный сервис авторизации. Здесь пользователь - это только его ID из токена.
package user_entity

import "errors"

var (
	ErrInvalidUser = errors.New("invalid user")
)
