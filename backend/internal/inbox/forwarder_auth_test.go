package inbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestForwarderSignatureRFC4231(t *testing.T) {
	// RFC 4231 test case 2: independently checks HMAC-SHA256 and hexadecimal input.
	const signature = "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"
	if !validForwarderSignature(signature, []byte("what do ya want for nothing?"), "Jefe") {
		t.Fatal("standard HMAC-SHA256 vector rejected")
	}
}

func TestForwarderSignatureAndLegacyFallback(t *testing.T) {
	const secret = "test/signing+secret="
	const body = "{\n  \"from\": \"Test phone\", \"text\": \"Código ✓\\nline two\", \"sim\": 2\n}\n"
	sign := func(value, key string) string {
		mac := hmac.New(sha256.New, []byte(key))
		_, _ = mac.Write([]byte(value))
		return hex.EncodeToString(mac.Sum(nil))
	}
	signature := sign(body, secret)
	for _, test := range []struct {
		name      string
		signature string
		legacy    string
		body      string
		want      int
	}{
		{"valid app signature only", signature, "", body, http.StatusCreated},
		{"signature takes priority over wrong legacy token", signature, "wrong", body, http.StatusCreated},
		{"uppercase hex", strings.ToUpper(signature), "", body, http.StatusCreated},
		{"legacy only", "", secret, body, http.StatusCreated},
		{"invalid signature falls back to legacy", sign(body, "wrong"), secret, body, http.StatusCreated},
		{"malformed signature falls back to legacy", "not-hex", secret, body, http.StatusCreated},
		{"missing both credentials", "", "", body, http.StatusUnauthorized},
		{"wrong signing secret", sign(body, "wrong"), "", body, http.StatusUnauthorized},
		{"signature is not a plaintext token", secret, "", body, http.StatusUnauthorized},
		{"tampered message", signature, "", strings.Replace(body, "Código", "Changed", 1), http.StatusUnauthorized},
		{"whitespace changes signature", signature, "", strings.TrimSpace(body), http.StatusUnauthorized},
		{"malformed hex", strings.Repeat("z", 64), "", body, http.StatusUnauthorized},
		{"truncated signature", signature[:62], "", body, http.StatusUnauthorized},
		{"invalid signature and legacy", "invalid", "wrong", body, http.StatusUnauthorized},
		{"valid signature still requires valid JSON", sign("{", secret), "", "{", http.StatusBadRequest},
		{"signed JSON must be one object", sign(body+body, secret), "", body + body, http.StatusBadRequest},
		{"signature read respects size limit", signature, "", strings.Repeat("x", 128*1024+1), http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{}
			s := testServer(store)
			s.Config.ForwarderToken = secret
			w := perform(s.Handler(), "POST", "/messages", test.body, map[string]string{
				"X-Signature": test.signature, "X-Forwarder-Token": test.legacy,
			})
			if w.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, test.want, w.Body)
			}
			if test.want == http.StatusCreated {
				if store.in.From != "Test phone" || store.in.Text != "Código ✓\nline two" || store.in.SIM.Text() != "2" {
					t.Fatalf("signed payload changed: %+v", store.in)
				}
			} else if store.in.From != "" {
				t.Fatal("rejected request reached storage")
			}
		})
	}
}

func TestForwarderSignatureWithoutConfiguredToken(t *testing.T) {
	s := testServer(&memoryStore{})
	w := perform(s.Handler(), "POST", "/messages", `{"from":"Test phone","text":"test"}`, map[string]string{"X-Signature": "invalid"})
	if w.Code != http.StatusCreated {
		t.Fatalf("unconfigured token changed public ingestion behavior: %d %s", w.Code, w.Body)
	}
}
