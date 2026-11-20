package inbox

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestHeartbeatWebhookAndStatus(t *testing.T) {
	store := &memoryStore{}
	s := testServer(store)
	s.Config.HeartbeatToken = "heartbeat-secret"
	s.Config.ForwarderToken = "sms-secret"
	s.Config.HeartbeatTimeout = 10 * time.Minute
	h := s.Handler()
	for _, token := range []string{"", "wrong", "sms-secret"} {
		w := perform(h, "POST", "/heartbeat?token="+token, "", nil)
		if w.Code != http.StatusUnauthorized || store.heartbeat != nil {
			t.Fatalf("token=%q status=%d heartbeat=%v", token, w.Code, store.heartbeat)
		}
	}
	if w := perform(h, "GET", "/heartbeat?token=heartbeat-secret", "", nil); w.Code != http.StatusMethodNotAllowed || store.heartbeat != nil {
		t.Fatal("GET must not record a heartbeat")
	}
	read := func() ForwarderStatus {
		t.Helper()
		w := perform(h, "GET", "/api/forwarder", "", nil)
		var status ForwarderStatus
		if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s error=%v", w.Code, w.Body, err)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("heartbeat status must not be cached")
		}
		return status
	}
	if status := read(); status.Status != "unknown" || status.LastSeen != nil || status.TimeoutSeconds != 600 {
		t.Fatalf("unexpected initial status: %+v", status)
	}
	// Matches the app's request: empty POST, no forwarding headers or user agent.
	w := perform(h, "POST", "/heartbeat?token=heartbeat-secret", "", map[string]string{"Content-Type": "application/json; charset=utf-8", "Sec-Fetch-Site": "cross-site"})
	if w.Code != http.StatusNoContent || store.heartbeat == nil || store.in.From != "" {
		t.Fatalf("status=%d body=%s store=%+v", w.Code, w.Body, store)
	}
	if status := read(); status.Status != "online" || status.LastSeen == nil || status.ServerTime.IsZero() {
		t.Fatalf("heartbeat not visible: %+v", status)
	}
	past := time.Now().Add(-11 * time.Minute)
	store.heartbeat = &past
	if status := read(); status.Status != "offline" || !status.LastSeen.Equal(past) {
		t.Fatalf("stale heartbeat not offline: %+v", status)
	}
	store.heartbeatErr = errors.New("database unavailable")
	if w := perform(h, "POST", "/heartbeat?token=heartbeat-secret", "", nil); w.Code != http.StatusInternalServerError {
		t.Fatal("failed persistence reported success")
	}
	if w := perform(h, "GET", "/api/forwarder", "", nil); w.Code != http.StatusInternalServerError {
		t.Fatal("failed read reported a phone status")
	}
	store.heartbeatErr = nil
	s.Config.HeartbeatToken = ""
	if w := perform(h, "POST", "/heartbeat", "", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatal("unconfigured heartbeat endpoint accepted a ping")
	}
	if status := read(); status.Status != "disabled" {
		t.Fatalf("monitoring without a token must be disabled: %+v", status)
	}
}

func TestForwarderStatusTimeout(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name string
		age  time.Duration
		want string
	}{
		{"fresh", 0, "online"},
		{"before deadline", DefaultHeartbeatTimeout - time.Nanosecond, "online"},
		{"at deadline", DefaultHeartbeatTimeout, "offline"},
		{"after deadline", DefaultHeartbeatTimeout + time.Second, "offline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			seen := now.Add(-test.age)
			if got := forwarderStatus(&seen, now, DefaultHeartbeatTimeout); got.Status != test.want {
				t.Fatalf("status=%s want=%s", got.Status, test.want)
			}
		})
	}
}

func TestHeartbeatURLTokenEncoding(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
		want  int
	}{
		{"encoded base64 characters", "token=test%2Fheartbeat%2Btoken%3D", http.StatusNoContent},
		{"unescaped plus is a space", "token=test/heartbeat+token=", http.StatusUnauthorized},
		{"double encoding is rejected", "token=test%252Fheartbeat%252Btoken%253D", http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testServer(&memoryStore{})
			s.Config.HeartbeatToken = "test/heartbeat+token="
			w := perform(s.Handler(), "POST", "/heartbeat?"+test.query, "", nil)
			if w.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, test.want, w.Body)
			}
		})
	}
}
