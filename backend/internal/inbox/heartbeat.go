package inbox

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const DefaultHeartbeatTimeout = 15 * time.Minute

func (s Store) RecordHeartbeat(ctx context.Context) (time.Time, error) {
	var lastSeen time.Time
	err := s.Pool.QueryRow(ctx, `INSERT INTO forwarder_heartbeat (id, last_seen)
		VALUES (TRUE, clock_timestamp()) ON CONFLICT (id) DO UPDATE
		SET last_seen = GREATEST(forwarder_heartbeat.last_seen, EXCLUDED.last_seen)
		RETURNING last_seen`).Scan(&lastSeen)
	return lastSeen, err
}

func (s Store) LastHeartbeat(ctx context.Context) (*time.Time, error) {
	var lastSeen time.Time
	err := s.Pool.QueryRow(ctx, `SELECT last_seen FROM forwarder_heartbeat WHERE id = TRUE`).Scan(&lastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &lastSeen, err
}

type ForwarderStatus struct {
	Status         string     `json:"status"`
	LastSeen       *time.Time `json:"lastSeen"`
	ServerTime     time.Time  `json:"serverTime"`
	TimeoutSeconds int64      `json:"timeoutSeconds"`
}

func forwarderStatus(lastSeen *time.Time, now time.Time, timeout time.Duration) ForwarderStatus {
	status := "unknown"
	if lastSeen != nil {
		status = "offline"
		if now.Before(lastSeen.Add(timeout)) {
			status = "online"
		}
	}
	return ForwarderStatus{Status: status, LastSeen: lastSeen, ServerTime: now, TimeoutSeconds: int64(timeout / time.Second)}
}

func (s *Server) heartbeatTimeout() time.Duration {
	if s.Config.HeartbeatTimeout > 0 {
		return s.Config.HeartbeatTimeout
	}
	return DefaultHeartbeatTimeout
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	// SMS Forwarder 2.4.0 sends an empty POST and only offers a configurable URL.
	// Use a dedicated secret rather than putting the SMS ingestion token in a URL.
	if s.Config.HeartbeatToken == "" {
		writeError(w, http.StatusServiceUnavailable, "Heartbeat monitoring is not configured")
		return
	}
	if !equalSecret(r.URL.Query().Get("token"), s.Config.HeartbeatToken) {
		writeError(w, http.StatusUnauthorized, "Invalid heartbeat token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := s.Store.RecordHeartbeat(ctx); err != nil {
		s.Logger.Error("record heartbeat failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not save heartbeat")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getForwarderStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	lastSeen, err := s.Store.LastHeartbeat(ctx)
	if err != nil {
		s.Logger.Error("load heartbeat failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load phone status")
		return
	}
	status := forwarderStatus(lastSeen, time.Now().UTC(), s.heartbeatTimeout())
	if s.Config.HeartbeatToken == "" {
		status.Status = "disabled"
	}
	writeJSON(w, http.StatusOK, status)
}
