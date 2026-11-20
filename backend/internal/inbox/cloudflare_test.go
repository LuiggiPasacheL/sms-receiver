package inbox

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type testAdmins map[string]bool

func (a testAdmins) IsAdmin(_ context.Context, email string) (bool, error) { return a[email], nil }
func (a testAdmins) AddAdmin(_ context.Context, email string) error {
	a[email] = true
	return nil
}
func (a testAdmins) AdminEmails(context.Context) ([]string, error) {
	emails := make([]string, 0, len(a))
	for email := range a {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	return emails, nil
}

func TestCloudflareAccessPermissions(t *testing.T) {
	const issuer = "https://test.cloudflareaccess.com"
	const audience = "test-audience"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	signToken := func(email string, overrides map[string]any) string {
		t.Helper()
		claims := map[string]any{
			"email": email, "type": "app", "sub": "user-id", "iss": issuer,
			"aud": []string{audience}, "exp": now.Add(time.Hour).Unix(),
		}
		for field, value := range overrides {
			claims[field] = value
		}
		body, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		encode := base64.RawURLEncoding.EncodeToString
		parts := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(body)
		digest := sha256.Sum256([]byte(parts))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return parts + "." + encode(sig)
	}
	store := &memoryStore{}
	adminEmails := testAdmins{"owner@example.com": true}
	s := testServer(store)
	s.Admins = adminEmails
	s.Access = &CloudflareAccess{verifier: oidc.NewVerifier(issuer, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{
		ClientID: audience, SupportedSigningAlgs: []string{oidc.RS256},
	})}
	s.Config.ViewerPassword = "legacy-password"
	s.Config.ForwarderToken = "forwarder-token"
	h := s.Handler()
	admin := map[string]string{"Cf-Access-Jwt-Assertion": signToken("Owner@Example.COM", nil)}
	viewer := map[string]string{"Authorization": "Bearer " + signToken("viewer@example.com", nil)}

	assertResponse := func(method, path, body string, headers map[string]string, status int, contents string) {
		t.Helper()
		w := perform(h, method, path, body, headers)
		if w.Code != status || !strings.Contains(w.Body.String(), contents) {
			t.Fatalf("%s %s: got %d %s, want %d containing %q", method, path, w.Code, w.Body.String(), status, contents)
		}
	}
	forged := admin["Cf-Access-Jwt-Assertion"]
	separator := strings.LastIndexByte(forged, '.')
	forged = forged[:separator+1] + "A" + forged[separator+2:]
	if forged == admin["Cf-Access-Jwt-Assertion"] {
		forged = forged[:separator+1] + "B" + forged[separator+2:]
	}
	for _, token := range []string{
		"", forged, signToken("", nil),
		signToken("owner@example.com", map[string]any{"aud": []string{"different-app"}}),
		signToken("owner@example.com", map[string]any{"iss": "https://other.cloudflareaccess.com"}),
		signToken("owner@example.com", map[string]any{"exp": now.Add(-time.Hour).Unix()}),
		signToken("owner@example.com", map[string]any{"type": "org"}),
		signToken("owner@example.com", map[string]any{"sub": ""}),
	} {
		assertResponse(http.MethodGet, "/api/messages", "", map[string]string{
			"Cf-Access-Jwt-Assertion": token, "Cf-Access-Authenticated-User-Email": "owner@example.com",
		}, http.StatusUnauthorized, "")
	}
	assertResponse(http.MethodGet, "/api/events", "", nil, http.StatusUnauthorized, "")
	assertResponse(http.MethodPost, "/api/session", `{"password":"legacy-password"}`, nil, http.StatusForbidden, "Cloudflare")
	assertResponse(http.MethodGet, "/api/session", "", admin, http.StatusOK, `"admin":true`)
	assertResponse(http.MethodGet, "/api/session", "", viewer, http.StatusOK, `"admin":false`)
	assertResponse(http.MethodGet, "/api/messages", "", viewer, http.StatusOK, `"messages"`)
	assertResponse(http.MethodGet, "/api/sims", "", viewer, http.StatusOK, `"sims"`)
	assertResponse(http.MethodGet, "/api/admins", "", viewer, http.StatusForbidden, "")
	assertResponse(http.MethodPut, "/api/admins", `{"email":"viewer@example.com"}`, viewer, http.StatusForbidden, "")
	assertResponse(http.MethodPut, "/api/sims", `{"sim":"SIM 1","alias":"Work"}`, viewer, http.StatusForbidden, "")
	assertResponse(http.MethodDelete, "/api/sims?sim=SIM+1", "", viewer, http.StatusForbidden, "")
	if store.simID != "" || adminEmails["viewer@example.com"] {
		t.Fatal("a denied request changed the store")
	}
	assertResponse(http.MethodPut, "/api/admins", `{"email":"Name <viewer@example.com>"}`, admin, http.StatusBadRequest, "valid email")
	assertResponse(http.MethodPut, "/api/admins", `{"email":" Viewer@Example.com "}`, admin, http.StatusOK, `"viewer@example.com"`)
	assertResponse(http.MethodGet, "/api/admins", "", viewer, http.StatusOK, `"viewer@example.com"`)
	assertResponse(http.MethodPut, "/api/sims", `{"sim":"SIM 1","alias":"Work"}`, viewer, http.StatusNoContent, "")
	if store.alias != "Work" {
		t.Fatal("administrator did not update the SIM alias")
	}
	assertResponse(http.MethodDelete, "/api/sims?sim=SIM+1", "", viewer, http.StatusOK, `"deletedMessages":7`)
	assertResponse(http.MethodPost, "/messages", `{"from":"test","text":"message"}`, nil, http.StatusUnauthorized, "")
	assertResponse(http.MethodPost, "/messages", `{"from":"test","text":"message"}`, map[string]string{"X-Forwarder-Token": "forwarder-token"}, http.StatusCreated, "")
}
