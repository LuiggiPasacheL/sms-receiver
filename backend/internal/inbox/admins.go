package inbox

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type AdminRepository interface {
	IsAdmin(context.Context, string) (bool, error)
	AdminEmails(context.Context) ([]string, error)
	AddAdmin(context.Context, string) error
}

func NormalizeAdminEmail(value string) (string, error) {
	email := strings.TrimSpace(value)
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 {
		return "", errors.New("Enter a valid email address")
	}
	return strings.ToLower(email), nil
}

func (s Store) IsAdmin(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM admin_emails WHERE email = $1)`, email).Scan(&exists)
	return exists, err
}

func (s Store) AdminEmails(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT email FROM admin_emails ORDER BY email`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (s Store) AddAdmin(ctx context.Context, email string) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO admin_emails (email) VALUES ($1) ON CONFLICT (email) DO NOTHING`, email)
	return err
}

func (s *Server) checkAdmin(r *http.Request, email string) (bool, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	return s.Admins.IsAdmin(ctx, email)
}

func (s *Server) requireAdmin(next http.HandlerFunc, legacyAllowed bool) http.HandlerFunc {
	if s.Access == nil && legacyAllowed {
		return s.requireViewer(next)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Access == nil {
			writeError(w, http.StatusForbidden, "Administrator management requires Cloudflare Access")
			return
		}
		email := s.Access.email(r)
		if email == "" {
			writeError(w, http.StatusUnauthorized, "Sign in through Cloudflare Access")
			return
		}
		admin, err := s.checkAdmin(r, email)
		if err != nil {
			s.adminError(w, err)
			return
		}
		if !admin {
			writeError(w, http.StatusForbidden, "Administrator access required")
			return
		}
		next(w, r)
	}
}

func (s *Server) adminError(w http.ResponseWriter, err error) {
	s.Logger.Error("administrator operation failed", "error", err)
	writeError(w, http.StatusServiceUnavailable, "Administrator list is unavailable")
}

func (s *Server) listAdmins(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	emails, err := s.Admins.AdminEmails(ctx)
	if err != nil {
		s.adminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"emails": emails})
}

func (s *Server) addAdmin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "Expected a JSON object with an email address")
		return
	}
	email, err := NormalizeAdminEmail(input.Email)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Admins.AddAdmin(ctx, email); err != nil {
		s.adminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": email})
}
