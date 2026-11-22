package ipguard

import "net/http"

// InternalHandler is the loopback-only control surface of the Go data plane.
// The Rust control plane owns authentication, CRUD and aggregation; the Go
// worker is kept for mihomo-backed work that cannot be executed without the
// embedded proxy engine.
func (a *app) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	mux.Handle("POST /available-ips/speed-test", a.protected(http.HandlerFunc(a.speedTestAvailableIP)))
	mux.Handle("POST /networks/{id}/recheck", a.protected(http.HandlerFunc(a.recheckNetwork)))
	return mux
}
