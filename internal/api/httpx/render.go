// Package httpx holds the shared HTTP helpers (JSON rendering, SSE writing,
// base middleware).
//
// CONTRACT-DEVIATION (scaffold, documented): the contract places render/sse
// in package internal/api, but internal/api/server.go must call
// apichat.Register/apimgr.Register — so handlers inside apichat/apimgr cannot
// import internal/api (import cycle). The implementations therefore live here;
// internal/api/render.go + sse.go re-export them (frozen names preserved) and
// apichat/apimgr use httpx directly. No behavior changes.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// MaxJSONBody is the request-body cap for JSON endpoints (contract).
const MaxJSONBody = 1 << 20 // 1 MiB

// JSON writes v as an application/json response.
func JSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("http: encode response failed", "status", status)
	}
}

// ErrorJSON writes {"error": msg}. Callers must pass client-safe messages.
func ErrorJSON(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// InternalError logs the real error (never tokens/keys/message content) and
// answers a generic 500 — internals never leak to clients (contract).
func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		slog.Error("http: internal error", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	ErrorJSON(w, http.StatusInternalServerError, "internal error")
}

// Decode reads a size-capped JSON body into dst, rejecting unknown fields.
// A body over MaxJSONBody fails with *http.MaxBytesError.
func Decode(r *http.Request, dst any) error {
	body := http.MaxBytesReader(nil, r.Body, MaxJSONBody)
	defer body.Close()
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// DecodeLoose is Decode without the unknown-field restriction.
func DecodeLoose(r *http.Request, dst any) error {
	body := http.MaxBytesReader(nil, r.Body, MaxJSONBody)
	defer body.Close()
	return json.NewDecoder(body).Decode(dst)
}
