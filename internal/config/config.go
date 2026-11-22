// Package config loads all runtime configuration from the environment.
// See docs/CONFIG.md (written by scaffold) for the full env table.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// OIDCConfig holds single-SSO-provider settings. Zero value = OIDC disabled.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	DisplayName  string // default "SSO"
}

// Enabled reports whether OIDC login should be offered.
func (o OIDCConfig) Enabled() bool {
	return o.Issuer != "" && o.ClientID != ""
}

// Config is the frozen runtime configuration shape (docs/CONTRACTS.md §Config).
type Config struct {
	Port      string
	Host      string
	PublicURL string
	DataDir   string

	DatabasePath  string // default DataDir/chatgo.db
	SessionSecret string // hex; required

	OIDC         OIDCConfig
	Registration string // "open" | "approve" | "closed" (default "approve")
	TaskModel    string // "providerID:modelID" for titles/summaries; optional
	SearXNGURL   string // optional; enables web-search toggle

	TrustedURLNets []string // CIDRs allowed for provider/SearXNG URLs beyond public internet
	AllowLoopback  bool     // for in-cluster SearXNG; default true

	Version string
}

// Load reads configuration from the environment, applying contract defaults.
// It fails fast if SESSION_SECRET is missing, non-hex, or too short.
func Load() (*Config, error) {
	cfg := &Config{
		Port:      getenv("PORT", "8080"),
		Host:      getenv("HOST", "0.0.0.0"),
		PublicURL: strings.TrimRight(getenv("PUBLIC_URL", ""), "/"),
		DataDir:   getenv("DATA_DIR", "./data"),

		DatabasePath: os.Getenv("DATABASE_PATH"),

		OIDC: OIDCConfig{
			Issuer:       strings.TrimRight(os.Getenv("OIDC_ISSUER"), "/"),
			ClientID:     os.Getenv("OIDC_CLIENT_ID"),
			ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
			DisplayName:  getenv("OIDC_DISPLAY_NAME", "SSO"),
		},
		Registration:   getenv("REGISTRATION", "approve"),
		TaskModel:      os.Getenv("TASK_MODEL"),
		SearXNGURL:     strings.TrimRight(os.Getenv("SEARXNG_URL"), "/"),
		TrustedURLNets: splitList(os.Getenv("TRUSTED_URL_NETS")),
		AllowLoopback:  getenvBool("ALLOW_LOOPBACK", true),
		Version:        getenv("VERSION", "dev"),
	}

	if cfg.DatabasePath == "" {
		cfg.DatabasePath = filepath.Join(cfg.DataDir, "chatgo.db")
	}

	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		return nil, errors.New("config: SESSION_SECRET is required (generate with `openssl rand -hex 32`)")
	}
	raw, err := hex.DecodeString(secret)
	if err != nil {
		return nil, fmt.Errorf("config: SESSION_SECRET must be hex: %w", err)
	}
	if len(raw) < 16 {
		return nil, errors.New("config: SESSION_SECRET must decode to at least 16 bytes (32 hex chars)")
	}
	cfg.SessionSecret = secret

	switch cfg.Registration {
	case "open", "approve", "closed":
	default:
		return nil, fmt.Errorf("config: REGISTRATION must be open|approve|closed, got %q", cfg.Registration)
	}

	for _, c := range cfg.TrustedURLNets {
		if _, _, err := net.ParseCIDR(c); err != nil {
			return nil, fmt.Errorf("config: TRUSTED_URL_NETS contains invalid CIDR %q: %w", c, err)
		}
	}

	return cfg, nil
}

// SecretBytes decodes the hex session secret.
func (c *Config) SecretBytes() ([]byte, error) {
	return hex.DecodeString(c.SessionSecret)
}

// Addr is the listen address for http.Server.
func (c *Config) Addr() string { return c.Host + ":" + c.Port }

// ExternalURL is the best-guess absolute base URL for this instance.
// Empty when PUBLIC_URL is unset (callers fall back to relative URLs/cookies).
func (c *Config) ExternalURL() string { return c.PublicURL }

// SessionCookieSecure reports whether the session cookie should carry the Secure flag
// (true when PUBLIC_URL is https).
func (c *Config) SessionCookieSecure() bool {
	return strings.HasPrefix(strings.ToLower(c.PublicURL), "https://")
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "t", "true", "yes", "on":
		return true
	case "0", "f", "false", "no", "off":
		return false
	default:
		return def
	}
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
