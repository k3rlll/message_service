// Package apierror переводит доменные ошибки в то, что видит клиент.
// Используется и REST-хендлерами, и WebSocket, чтобы коды ошибок совпадали.
package apierror

import (
	"errors"
	"net/http"

	domainComm "main/internal/domain"
	domainChat "main/internal/domain/chat_entity"
)

type Info struct {
	Status  int    // HTTP статус
	Code    string // машиночитаемый код
	Message string // текст для клиента
}

var kinds = []struct {
	err  error
	info Info
}{
	{domainComm.ErrInvalidInput, Info{http.StatusBadRequest, "INVALID_REQUEST", "invalid request"}},
	{domainComm.ErrAccessDenied, Info{http.StatusForbidden, "FORBIDDEN", "you are not a member of this chat"}},
	{domainComm.ErrForbidden, Info{http.StatusForbidden, "FORBIDDEN", "insufficient permissions"}},
	{domainChat.ErrChatNotFound, Info{http.StatusNotFound, "NOT_FOUND", "chat not found"}},
	{domainComm.ErrNotFound, Info{http.StatusNotFound, "NOT_FOUND", "resource not found"}},
	{domainComm.ErrConflict, Info{http.StatusConflict, "CONFLICT", "conflict with current state"}},
	{domainComm.ErrRateLimited, Info{http.StatusTooManyRequests, "RATE_LIMITED", "too many requests, slow down"}},
	{domainComm.ErrUnavailable, Info{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "this feature is not configured on the server"}},
	{domainComm.ErrDatabaseTimeout, Info{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service temporarily unavailable"}},
}

var internal = Info{http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error"}

// Describe возвращает описание ошибки для клиента.
// known == false значит, что ошибка неожиданная: клиенту уйдет безликое
// "internal server error", а саму ошибку нужно залогировать.
func Describe(err error) (info Info, known bool) {
	for _, kind := range kinds {
		if !errors.Is(err, kind.err) {
			continue
		}

		info = kind.info
		// Если usecase приложил уточняющее сообщение, показываем его
		var detailed *domainComm.DetailedError
		if errors.As(err, &detailed) {
			info.Message = detailed.Message
		}
		return info, true
	}
	return internal, false
}
