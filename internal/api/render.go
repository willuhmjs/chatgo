package api

import (
	"net/http"

	"chatgo/internal/api/httpx"
)

// JSON/error/render helpers (contract §render: errors never leak internals).
// Delegating aliases — see internal/api/httpx for implementation.

// JSON writes a JSON response.
func JSON(w http.ResponseWriter, status int, v any) { httpx.JSON(w, status, v) }

// ErrorJSON writes {"error": msg} with a client-safe message.
func ErrorJSON(w http.ResponseWriter, status int, msg string) { httpx.ErrorJSON(w, status, msg) }

// InternalError logs err server-side and answers a generic 500.
func InternalError(w http.ResponseWriter, r *http.Request, err error) {
	httpx.InternalError(w, r, err)
}

// Decode reads a ≤1MB JSON body into dst (unknown fields rejected).
func Decode(r *http.Request, dst any) error { return httpx.Decode(r, dst) }

// DecodeLoose is Decode without unknown-field rejection.
func DecodeLoose(r *http.Request, dst any) error { return httpx.DecodeLoose(r, dst) }
