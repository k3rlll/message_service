package ctxutils

import "context"

type contextKey string

const RequestIDKey contextKey = "request_id"

// Helper для удобного извлечения Request ID 
func GetRequestID(ctx context.Context) string {
	if reqID, ok := ctx.Value(RequestIDKey).(string); ok {
		return reqID
	}
	return ""
}
