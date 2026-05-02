package domain

import "errors"

var (
	ErrInvalidInput    = errors.New("invalid input data")
	ErrAccessDenied    = errors.New("access denied")
	ErrNotFound        = errors.New("resource not found")
	ErrDatabaseTimeout = errors.New("database operation timed out")
	ErrInternal        = errors.New("internal server error")
)
