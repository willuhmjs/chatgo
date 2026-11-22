package search

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"chatgo/internal/config"
)

// Dial budget for guarded outbound connections (providers + SearXNG).
const guardDialTimeout = 10 * time.Second

var (
	// metadataIP is the cloud instance-metadata endpoint; never dialable.
	metadataIP = net.IPv4(169, 254, 169, 254)
	// cgnatNet is 100.64.0.0/10 (carrier-grade NAT); never dialable.
	cgnatNet = mustCIDR("100.64.0.0/10")

	// deny6to4Net is 6to4 and denyTeredoNet Teredo; both can embed private
	// or loopback IPv4s inside their payload (2002:7f00:1:: = 127.0.0.1)
	// and Go's IsLoopback/IsPrivate never see through them. Reaching the
	// embedded address anyway requires OS tunnel routes we never rely on,
	// so both transition ranges are denied on the literal prefix instead
	// of decoded (review L4-I4: deny defensively).
	deny6to4Net   = mustCIDR("2002::/16")
	denyTeredoNet = mustCIDR("2001::/32")

	// Injection points so tests can fake DNS + observe the final dial.
	// Production paths must go through these; never call net.Dialer directly.
	lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return net.DefaultResolver.LookupIPAddr(ctx, host)
	}
	dialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return guardTCPDialer.DialContext(ctx, network, addr)
	}
	guardTCPDialer = &net.Dialer{Timeout: guardDialTimeout, KeepAlive: 30 * time.Second}
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("search: bad built-in CIDR " + s)
	}
	return n
}

// ipPolicy decides which resolved IPs may be dialled.
type ipPolicy struct {
	allowLoopback bool
	trusted       []*net.IPNet
}

func policyFrom(cfg *config.Config) (*ipPolicy, error) {
	p := &ipPolicy{}
	if cfg == nil {
		return p, nil
	}
	p.allowLoopback = cfg.AllowLoopback
	for _, c := range cfg.TrustedURLNets {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("search: invalid TrustedURLNets entry %q: %w", c, err)
		}
		p.trusted = append(p.trusted, n)
	}
	return p, nil
}

func (p *ipPolicy) inTrusted(ip net.IP) bool {
	for _, n := range p.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// allow decides whether ip may be dialled. IPv6-mapped IPv4 forms
// (::ffff:a.b.c.d) are normalized to the embedded IPv4 address FIRST so they
// cannot smuggle loopback/private/CGNAT/metadata addresses past the filters;
// every rule below then sees the true address family.
func (p *ipPolicy) allow(ip net.IP) error {
	if ip == nil {
		return errors.New("search: nil IP")
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // unmap IPv6-mapped IPv4
	}

	// Always denied, no configuration override.
	if ip.Equal(metadataIP) {
		return fmt.Errorf("search: metadata address %s is never allowed", ip)
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("search: address %s is not dialable", ip)
	}
	// 6to4 / Teredo transition ranges are denied outright (before the
	// loopback and TrustedURLNets "allow" quirks below): a literal like
	// 2002:7f00:1:: embeds 127.0.0.1 but looks like plain global unicast to
	// every Go predicate, so only the explicit prefix denial stops it.
	if deny6to4Net.Contains(ip) || denyTeredoNet.Contains(ip) {
		return fmt.Errorf("search: 6to4/Teredo transition address %s is never allowed", ip)
	}

	if ip.IsLoopback() {
		if p.allowLoopback {
			return nil
		}
		return fmt.Errorf("search: loopback address %s blocked (ALLOW_LOOPBACK=false)", ip)
	}
	if ip.To4() != nil && cgnatNet.Contains(ip) {
		return fmt.Errorf("search: CGNAT address %s is never allowed", ip)
	}
	if p.inTrusted(ip) {
		return nil
	}
	if ip.IsPrivate() { // RFC1918 v4 + ULA fc00::/7
		return fmt.Errorf("search: private address %s requires TrustedURLNets", ip)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("search: link-local address %s requires TrustedURLNets", ip)
	}
	if !ip.IsGlobalUnicast() {
		return fmt.Errorf("search: address %s is not global unicast", ip)
	}
	return nil
}

// ResolveAndCheck is the SSRF guard shared by providers + search
// (contract §Providers client).
//
// It parses rawURL (http/https only, host required), resolves the hostname
// once via DNS, drops every address failing the policy (loopback only with
// cfg.AllowLoopback; RFC1918/ULA/link-local only inside cfg.TrustedURLNets;
// metadata 169.254.169.254, CGNAT 100.64.0.0/10, multicast and unspecified
// always rejected) and returns an http-transport-ready DialContext that dials
// ONLY the surviving pinned IPs — DNS is never re-consulted at dial time, so
// a DNS-rebinding second answer cannot smuggle a blocked address through.
//
// Because only the destination address is pinned, the http.Transport still
// derives SNI and the HTTP Host header from the original hostname (its
// targetAddr stays the URL host), which keeps TLS validation correct.
//
// Callers must not follow redirects blindly: cap CheckRedirect at 3 and
// re-validate each hop by calling ResolveAndCheck with the new URL before
// swapping in its dialer.
func ResolveAndCheck(ctx context.Context, rawURL string, cfg *config.Config) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("search: bad url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("search: scheme %q not allowed (http/https only)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("search: url has no host")
	}
	policy, err := policyFrom(cfg)
	if err != nil {
		return nil, err
	}

	resolved, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("search: resolve %q: %w", host, err)
	}
	var pinned []net.IPAddr
	for _, a := range resolved {
		if perr := policy.allow(a.IP); perr != nil {
			continue
		}
		pinned = append(pinned, a)
	}
	if len(pinned) == 0 {
		return nil, fmt.Errorf("search: no permitted address for %q (%d resolved, all blocked)", host, len(resolved))
	}

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		switch network {
		case "tcp", "tcp4", "tcp6":
		default:
			return nil, fmt.Errorf("search: network %q refused (tcp only)", network)
		}
		dHost, dPort, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("search: bad dial address %q: %w", addr, err)
		}
		if dHost != host {
			// Defense in depth: the transport must target the URL we checked.
			return nil, fmt.Errorf("search: dial of %q refused: only %q was validated", dHost, host)
		}
		var lastErr error
		for _, a := range pinned {
			if !ipMatchesNetwork(a.IP, network) {
				continue
			}
			conn, derr := dialContext(ctx, network, net.JoinHostPort(a.IP.String(), dPort))
			if derr == nil {
				return conn, nil
			}
			lastErr = derr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("search: no pinned address for %q matches network %q", host, network)
		}
		return nil, lastErr
	}
	return dial, nil
}

func ipMatchesNetwork(ip net.IP, network string) bool {
	switch network {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil
	default:
		return true
	}
}
