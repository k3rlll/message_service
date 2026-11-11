package app

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"main/internal/metrics"
)

const readinessTimeout = 2 * time.Second

// Check - проверка зависимости для readiness: nil значит "в порядке"
type Check func(ctx context.Context) error

// Admin - служебный HTTP-сервер: метрики и пробы здоровья.
// Живет на отдельном порту, который не публикуется наружу через балансировщик.
type Admin struct {
	checks   map[string]Check
	draining atomic.Bool
}

func NewAdmin(checks map[string]Check) *Admin {
	return &Admin{checks: checks}
}

// Drain переводит readiness в "не готов". Вызывается в начале graceful shutdown,
// чтобы балансировщик перестал слать новые запросы, пока текущие дорабатывают.
func (a *Admin) Drain() {
	a.draining.Store(true)
}

func (a *Admin) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /healthz", a.liveness)
	mux.HandleFunc("GET /readyz", a.readiness)
	return mux
}

// liveness: процесс жив и отвечает. Зависимости не проверяются: их сбой
// не лечится перезапуском сервиса, а массовый перезапуск только усугубит аварию.
func (a *Admin) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readiness: сервис готов принимать трафик - все зависимости доступны
func (a *Admin) readiness(w http.ResponseWriter, r *http.Request) {
	if a.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	status, code := "ok", http.StatusOK
	results := make(map[string]string, len(a.checks))
	for name, check := range a.checks {
		if err := check(ctx); err != nil {
			results[name] = err.Error()
			status, code = "unavailable", http.StatusServiceUnavailable
			continue
		}
		results[name] = "ok"
	}

	writeJSON(w, code, map[string]any{"status": status, "checks": results})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
