package search

import (
	"context"
	"errors"
	"net"

	"chatgo/internal/config"
)

// ResolveAndCheck is the SSRF guard shared by providers + search
// (contract §Providers client).
//
// Contract: http(s) only; resolve DNS first, then dial ONLY resolved IPs in
// TrustedURLNets ∪ public ranges (plus loopback when cfg.AllowLoopback);
// reject blind redirects (caller's client must set CheckRedirect ≤ 3 and
// re-validate each hop).
//
// BE-data implements. Until then, callers get errNotImplemented — do not ship
// provider URLs without this guard.
func ResolveAndCheck(ctx context.Context, rawURL string, cfg *config.Config) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	return nil, errors.New("search: ResolveAndCheck not implemented yet (scaffold stub)")
}
