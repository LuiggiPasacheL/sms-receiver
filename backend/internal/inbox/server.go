package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	ViewerPassword string
	ForwarderToken string
	CookieSecure   bool
	StaticDir      string
}

type Server struct {
	Store   Repository
	Admins  AdminRepository
	Access  *CloudflareAccess
	Events  *Events
	Config  Config
	Logger  *slog.Logger
	Context context.Context
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /messages", s.ingest)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("POST /api/session", s.login)
	mux.HandleFunc("DELETE /api/session", s.logout)
	mux.HandleFunc("GET /api/messages", s.requireViewer(s.list))
	mux.HandleFunc("GET /api/conversations", s.requireViewer(s.list))
	mux.HandleFunc("GET /api/sender", s.requireViewer(s.senderDetails))
	mux.HandleFunc("GET /api/sims", s.requireViewer(s.listSIMs))
	mux.HandleFunc("PUT /api/sims", s.requireAdmin(s.setSIMAlias, true))
	mux.HandleFunc("DELETE /api/sims", s.requireAdmin(s.deleteSIM, true))
	mux.HandleFunc("GET /api/admins", s.requireAdmin(s.listAdmins, false))
	mux.HandleFunc("PUT /api/admins", s.requireAdmin(s.addAdmin, false))
	mux.HandleFunc("GET /api/events", s.requireViewer(s.stream))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, 404, "Not found") })
	if s.Config.StaticDir != "" {
		files := http.FileServer(http.Dir(s.Config.StaticDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
				return
			}
			// Do not expose directory listings or accidentally return HTML for missing APIs.
			path := filepath.Join(s.Config.StaticDir, filepath.Clean("/"+r.URL.Path))
			info, err := os.Stat(path)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if info.IsDir() {
				if _, err := os.Stat(filepath.Join(path, "index.html")); err != nil {
					http.NotFound(w, r)
					return
				}
			}
			if strings.HasPrefix(r.URL.Path, "/_next/static/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			// Browser session mutations are same-origin only. Webhooks are exempt.
			site := r.Header.Get("Sec-Fetch-Site")
			if r.URL.Path != "/messages" && site != "" && site != "same-origin" && site != "none" {
				writeError(w, http.StatusForbidden, "Cross-site request rejected")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if s.Config.ForwarderToken != "" && !equalSecret(r.Header.Get("X-Forwarder-Token"), s.Config.ForwarderToken) {
		writeError(w, http.StatusUnauthorized, "Invalid forwarding token")
		return
	}
	var in Incoming
	if err := decodeJSON(w, r, &in); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Payload is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "Expected a JSON object with from, text, sentStamp, receivedStamp, and sim")
		return
	}
	if err := in.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	m, duplicate, err := s.Store.Insert(ctx, in, r.UserAgent())
	if err != nil {
		s.Logger.Error("store message failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not save message")
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"id": m.ID, "duplicate": duplicate})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, err := validLimit(query.Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	before, err := parseCursor(query.Get("before"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(query.Get("q")) > 512 || len(query.Get("sim")) > 128 || len(query.Get("from")) > 512 {
		writeError(w, 400, "Filter is too long")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	filter := Filter{Query: query.Get("q"), SIM: query.Get("sim"), From: query.Get("from"), Before: before, Limit: limit}
	var page any
	if r.URL.Path == "/api/conversations" {
		page, err = s.Store.Conversations(ctx, filter)
	} else {
		page, err = s.Store.List(ctx, filter)
	}
	if err != nil {
		s.Logger.Error("list messages failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load messages")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) senderDetails(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sender, sim := query.Get("from"), query.Get("sim")
	if strings.TrimSpace(sender) == "" || len(sender) > 512 || len(sim) > 128 {
		writeError(w, http.StatusBadRequest, "Provide a valid sender and SIM filter")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	details, err := s.Store.SenderDetails(ctx, sender, sim)
	if err != nil {
		s.Logger.Error("load sender details failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load sender details")
		return
	}
	writeJSON(w, http.StatusOK, details)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) listSIMs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	sims, err := s.Store.SIMs(ctx)
	if err != nil {
		s.Logger.Error("list SIMs failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load SIMs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sims": sims})
}

func (s *Server) setSIMAlias(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SIM   string  `json:"sim"`
		Alias *string `json:"alias"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Alias == nil {
		writeError(w, http.StatusBadRequest, "Expected a JSON object with sim and alias strings")
		return
	}
	if in.SIM == "" || len(in.SIM) > 128 {
		writeError(w, http.StatusBadRequest, "sim must be between 1 and 128 bytes")
		return
	}
	alias := strings.TrimSpace(*in.Alias)
	if utf8.RuneCountInString(alias) > 128 {
		writeError(w, http.StatusBadRequest, "alias must be at most 128 characters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Store.SetSIMAlias(ctx, in.SIM, alias); err != nil {
		s.simMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteSIM(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("sim")
	if id == "" || len(id) > 128 {
		writeError(w, http.StatusBadRequest, "sim must be between 1 and 128 bytes")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	count, err := s.Store.DeleteSIM(ctx, id)
	if err != nil {
		s.simMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deletedMessages": count})
}

func (s *Server) simMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSIMNotFound) {
		writeError(w, http.StatusNotFound, "SIM not found")
		return
	}
	s.Logger.Error("SIM update failed", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not update SIM")
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "Streaming unavailable")
		return
	}
	updates, unsubscribe := s.Events.Subscribe()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(data string) error {
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprint(w, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	// A fresh snapshot on every connection makes reconnects lossless without
	// relying on sequence IDs whose transactions can commit out of order.
	if err := send("retry: 3000\nevent: ready\ndata: {}\n\n"); err != nil {
		return
	}
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.Context.Done():
			return
		case <-updates:
			if err := send("event: messages\ndata: {}\n\n"); err != nil {
				return
			}
		case <-heartbeat.C:
			if !s.authorized(r) {
				return
			}
			if err := send(": heartbeat\n\n"); err != nil {
				return
			}
		}
	}
}
