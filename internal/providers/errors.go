package providers

import "fmt"

// UpstreamError reports a non-2xx answer from a provider. Msg is always a
// user-safe English string: the upstream response body is deliberately NOT
// included (it can carry keys, stack traces or internal URLs) — only the
// status code is surfaced. internal/api can key off Status for mapping.
type UpstreamError struct {
	Status int
	Msg    string
}

func (e *UpstreamError) Error() string { return e.Msg }

// upstreamErrorFor maps a provider HTTP status to a safe message.
func upstreamErrorFor(status int) *UpstreamError {
	switch {
	case status == 401 || status == 403:
		return &UpstreamError{Status: status,
			Msg: fmt.Sprintf("provider rejected the credentials (HTTP %d) — check this provider's API key", status)}
	case status == 404:
		return &UpstreamError{Status: status,
			Msg: fmt.Sprintf("provider endpoint or model not found (HTTP %d) — check the provider base URL and model id", status)}
	case status == 429:
		return &UpstreamError{Status: status,
			Msg: "provider is rate limiting requests (HTTP 429) — try again in a moment"}
	case status >= 500:
		return &UpstreamError{Status: status,
			Msg: fmt.Sprintf("provider is having server trouble (HTTP %d) — try again shortly", status)}
	default:
		return &UpstreamError{Status: status,
			Msg: fmt.Sprintf("provider returned an unexpected HTTP status (%d)", status)}
	}
}
