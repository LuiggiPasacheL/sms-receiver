package inbox

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "sms_session"

func equalSecret(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

func validForwarderSignature(signature string, body []byte, secret string) bool {
	if len(signature) != sha256.Size*2 {
		return false
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func (s *Server) signature(value string) string {
	mac := hmac.New(sha256.New, []byte(s.Config.ViewerPassword))
	_, _ = mac.Write([]byte("sms-viewer-session:" + value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Access != nil {
		return s.Access.email(r) != ""
	}
	if s.Config.ViewerPassword == "" {
		return true
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	expires, sig, ok := strings.Cut(cookie.Value, ".")
	if !ok || !equalSecret(sig, s.signature(expires)) {
		return false
	}
	timestamp, err := strconv.ParseInt(expires, 10, 64)
	return err == nil && time.Now().Unix() < timestamp
}

func (s *Server) requireViewer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "Sign in to view messages")
			return
		}
		next(w, r)
	}
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if s.Access != nil {
		status := struct {
			Authenticated bool   `json:"authenticated"`
			Provider      string `json:"provider"`
			Email         string `json:"email,omitempty"`
			Admin         bool   `json:"admin"`
		}{Provider: "cloudflare", Email: s.Access.email(r)}
		if status.Email != "" {
			status.Authenticated = true
			admin, err := s.checkAdmin(r, status.Email)
			if err != nil {
				s.adminError(w, err)
				return
			}
			status.Admin = admin
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.authorized(r), "passwordRequired": s.Config.ViewerPassword != ""})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.Access != nil {
		writeError(w, http.StatusForbidden, "Sign in through Cloudflare Access")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if s.Config.ViewerPassword != "" && !equalSecret(body.Password, s.Config.ViewerPassword) {
		writeError(w, http.StatusUnauthorized, "Incorrect password")
		return
	}
	expires := time.Now().Add(7 * 24 * time.Hour)
	value := strconv.FormatInt(expires.Unix(), 10)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value + "." + s.signature(value), Path: "/", Expires: expires, MaxAge: 7 * 24 * 3600, HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.Access != nil {
		writeError(w, http.StatusForbidden, "Sign out through Cloudflare Access")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Config.CookieSecure, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}
