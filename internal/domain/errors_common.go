package domain

import "errors"

// Виды ошибок. Транспортный слой переводит их в HTTP статусы и коды WebSocket-ошибок.
var (
	ErrInvalidInput    = errors.New("invalid input data")
	ErrAccessDenied    = errors.New("access denied")            // пользователь не участник чата
	ErrForbidden       = errors.New("insufficient permissions") // участник, но роль не позволяет
	ErrNotFound        = errors.New("resource not found")
	ErrConflict        = errors.New("conflict with current state")
	ErrRateLimited     = errors.New("too many requests")
	ErrUnavailable     = errors.New("feature is not available")
	ErrDatabaseTimeout = errors.New("database operation timed out")
	ErrInternal        = errors.New("internal server error")

	// ErrDuplicate возвращают репозитории при нарушении уникального индекса.
	// Наружу не уходит: usecase превращает его в осмысленный результат.
	ErrDuplicate = errors.New("duplicate key")
)

// DetailedError - ошибка известного вида с сообщением, которое безопасно показать клиенту.
type DetailedError struct {
	Kind    error
	Message string
}

func (e *DetailedError) Error() string { return e.Message }
func (e *DetailedError) Unwrap() error { return e.Kind }

func Invalid(message string) error   { return &DetailedError{Kind: ErrInvalidInput, Message: message} }
func Forbidden(message string) error { return &DetailedError{Kind: ErrForbidden, Message: message} }
func NotFound(message string) error  { return &DetailedError{Kind: ErrNotFound, Message: message} }
func Conflict(message string) error  { return &DetailedError{Kind: ErrConflict, Message: message} }
