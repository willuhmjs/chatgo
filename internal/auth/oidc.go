package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"chatgo/internal/config"
)

// oidcFlow holds code-flow material for the single configured SSO provider.
// BE-auth owns the flow (state+nonce cookie, exchange, claims mapping);
// scaffold keeps the OIDC dependency wired and provides the lazy provider.
type oidcFlow struct {
	cfg    *config.Config
	oauth2 *oauth2.Config
}

func newOIDCFlow(cfg *config.Config) *oidcFlow {
	redirect := cfg.ExternalURL() + "/api/auth/oidc/callback"
	return &oidcFlow{
		cfg: cfg,
		oauth2: &oauth2.Config{
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			Endpoint:     oauth2.Endpoint{}, // filled lazily by provider(ctx)
			RedirectURL:  redirect,
			Scopes:       []string{"openid", "email", "profile"},
		},
	}
}

// provider resolves the OIDC discovery document lazily (boot stays offline-
// safe). BE-auth calls this from the login redirect and callback, verifies
// state+nonce and id_token claims, then upserts the user per Registration.
func (f *oidcFlow) provider(ctx context.Context) (*oidc.Provider, error) {
	p, err := oidc.NewProvider(ctx, f.cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc discovery: %w", err)
	}
	if f.oauth2.Endpoint.AuthURL == "" {
		f.oauth2.Endpoint = p.Endpoint()
	}
	return p, nil
}

// stubAuthURL exists so the scaffold compiles end-to-end; BE-auth replaces.
func (f *oidcFlow) authURL(w http.ResponseWriter, r *http.Request) (string, error) {
	_, err := f.provider(r.Context())
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("auth: oidc login not implemented yet (scaffold stub)")
}
