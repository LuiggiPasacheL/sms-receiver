package inbox

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

var accessTeam = regexp.MustCompile(`^https://[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.cloudflareaccess\.com$`)

type CloudflareAccess struct {
	verifier *oidc.IDTokenVerifier
}

func NewCloudflareAccess(ctx context.Context, teamURL, audience string) (*CloudflareAccess, error) {
	teamURL = strings.TrimRight(strings.TrimSpace(teamURL), "/")
	audience = strings.TrimSpace(audience)
	if teamURL == "" && audience == "" {
		return nil, nil
	}
	if !accessTeam.MatchString(teamURL) || audience == "" {
		return nil, errors.New("set CLOUDFLARE_ACCESS_TEAM_URL to https://<team>.cloudflareaccess.com and CLOUDFLARE_ACCESS_AUD to the application's AUD tag")
	}
	client := &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(ctx, client), teamURL+"/cdn-cgi/access/certs")
	return &CloudflareAccess{verifier: oidc.NewVerifier(teamURL, keys, &oidc.Config{
		ClientID: audience, SupportedSigningAlgs: []string{oidc.RS256},
	})}, nil
}

func (a *CloudflareAccess) email(r *http.Request) string {
	raw := r.Header.Get("Cf-Access-Jwt-Assertion")
	if raw == "" {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			return ""
		}
		raw = strings.TrimSpace(token)
	}
	if raw == "" {
		return ""
	}
	token, err := a.verifier.Verify(r.Context(), raw)
	if err != nil || token.Subject == "" {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
		Type  string `json:"type"`
	}
	if err := token.Claims(&claims); err != nil || claims.Type != "app" {
		return ""
	}
	email, _ := NormalizeAdminEmail(claims.Email)
	return email
}
