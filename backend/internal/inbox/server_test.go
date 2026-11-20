package inbox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type memoryStore struct {
	in           Incoming
	agent        string
	filter       Filter
	simID        string
	alias        string
	simErr       error
	heartbeat    *time.Time
	heartbeatErr error
}

func (s *memoryStore) Insert(_ context.Context, in Incoming, agent string) (Message, bool, error) {
	s.in, s.agent = in, agent
	return Message{ID: "1"}, false, nil
}
func (s *memoryStore) List(_ context.Context, filter Filter) (Page, error) {
	s.filter = filter
	return Page{Messages: []Message{}, SIMs: []string{}}, nil
}
func (s *memoryStore) Conversations(_ context.Context, filter Filter) (ConversationPage, error) {
	s.filter = filter
	return ConversationPage{Conversations: []Conversation{}, SIMs: []string{}}, nil
}
func (s *memoryStore) SenderDetails(_ context.Context, sender, sim string) (SenderDetails, error) {
	s.filter = Filter{From: sender, SIM: sim}
	return SenderDetails{SIMs: []SIM{}}, nil
}
func (*memoryStore) Ping(context.Context) error { return nil }

func (s *memoryStore) RecordHeartbeat(context.Context) (time.Time, error) {
	now := time.Now().UTC()
	if s.heartbeatErr == nil {
		s.heartbeat = &now
	}
	return now, s.heartbeatErr
}

func (s *memoryStore) LastHeartbeat(context.Context) (*time.Time, error) {
	return s.heartbeat, s.heartbeatErr
}

func (s *memoryStore) SIMs(context.Context) ([]SIM, error) {
	return []SIM{{ID: "SIM 1", Alias: "Personal", MessageCount: 7}}, s.simErr
}
func (s *memoryStore) SetSIMAlias(_ context.Context, id, alias string) error {
	s.simID, s.alias = id, alias
	return s.simErr
}
func (s *memoryStore) DeleteSIM(_ context.Context, id string) (int64, error) {
	s.simID = id
	return 7, s.simErr
}

func testServer(store Repository) *Server {
	return &Server{Store: store, Events: NewEvents(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Context: context.Background()}
}

func perform(handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestForwarderPayloadAndValidation(t *testing.T) {
	store := &memoryStore{}
	s := testServer(store)
	s.Config.ForwarderToken = "secret-token"
	h := s.Handler()
	headers := map[string]string{"X-Forwarder-Token": "secret-token", "User-Agent": "SMS Forwarder App"}
	valid := `{"from":"+51999999999","text":"line one\nline two ✓","sentStamp":1794528000000,"receivedStamp":"2026-11-12 18:01:00","sim":2}`
	w := perform(h, "POST", "/messages", valid, headers)
	if w.Code != 201 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if store.in.SIM.Text() != "2" || store.in.ReceivedStamp.Text() != "2026-11-12 18:01:00" || store.agent != "SMS Forwarder App" {
		t.Fatalf("payload changed: %+v", store.in)
	}
	if w = perform(h, "POST", "/messages", valid, nil); w.Code != 401 {
		t.Fatalf("missing token: %d", w.Code)
	}
	for _, body := range []string{
		`{`, `null`, `[]`, `{"from":"a"}`, `{"from":" ","text":"a"}`,
		`{"from":"a","text":"b","sim":{}}`, `{"from":"a","text":"b","sentStamp":true}`,
		`{"from":"a","text":"b"} {"from":"c","text":"d"}`,
	} {
		if w = perform(h, "POST", "/messages", body, headers); w.Code != 400 {
			t.Errorf("body=%s status=%d", body, w.Code)
		}
	}
	large := `{"from":"a","text":"` + strings.Repeat("x", 140000) + `"}`
	if w = perform(h, "POST", "/messages", large, headers); w.Code != 413 {
		t.Fatalf("large body status=%d", w.Code)
	}
}

func TestViewerSessionAndLogout(t *testing.T) {
	s := testServer(&memoryStore{})
	s.Config.ViewerPassword = "private-password"
	s.Config.CookieSecure = true
	h := s.Handler()
	for _, path := range []string{"/api/messages", "/api/conversations", "/api/sender?from=test", "/api/events", "/api/forwarder"} {
		if w := perform(h, "GET", path, "", nil); w.Code != 401 {
			t.Fatalf("%s exposed without authentication", path)
		}
	}
	if w := perform(h, "POST", "/api/session", `{"password":"wrong"}`, nil); w.Code != 401 {
		t.Fatal("wrong password accepted")
	}
	w := perform(h, "POST", "/api/session", `{"password":"private-password"}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %+v", cookies)
	}
	headers := map[string]string{"Cookie": cookies[0].String()}
	if w = perform(h, "GET", "/api/messages", "", headers); w.Code != 200 {
		t.Fatal("valid session rejected")
	}
	headers["Cookie"] = sessionCookie + "=tampered"
	if w = perform(h, "GET", "/api/messages", "", headers); w.Code != 401 {
		t.Fatal("tampered session accepted")
	}
	expired := fmt.Sprint(time.Now().Add(-time.Second).Unix())
	headers["Cookie"] = sessionCookie + "=" + expired + "." + s.signature(expired)
	if w = perform(h, "GET", "/api/messages", "", headers); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
	w = perform(h, "DELETE", "/api/session", "", nil)
	if w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear cookie")
	}
	w = perform(h, "POST", "/api/session", `{"password":"private-password"}`, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if w.Code != 403 {
		t.Fatal("cross-site login accepted")
	}
}

func TestPaginationValidation(t *testing.T) {
	store := &memoryStore{}
	h := testServer(store).Handler()
	w := perform(h, "GET", "/api/messages?q=hello&sim=2&before=123&limit=25", "", nil)
	if w.Code != 200 || store.filter.Query != "hello" || store.filter.SIM != "2" || store.filter.Before != 123 || store.filter.Limit != 25 {
		t.Fatalf("filter=%+v status=%d", store.filter, w.Code)
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=x", "before=-1", "before=12garbage", "before=9223372036854775808"} {
		if w = perform(h, "GET", "/api/messages?"+query, "", nil); w.Code != 400 {
			t.Errorf("invalid pagination accepted: %s", query)
		}
	}
}

func TestConversationAndExactSenderFilters(t *testing.T) {
	store := &memoryStore{}
	h := testServer(store).Handler()
	w := perform(h, "GET", "/api/conversations?q=code&sim=2&before=123&limit=25", "", nil)
	if w.Code != 200 || store.filter.Query != "code" || store.filter.SIM != "2" || store.filter.Before != 123 || store.filter.Limit != 25 {
		t.Fatalf("filter=%+v status=%d", store.filter, w.Code)
	}
	var page ConversationPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Conversations == nil {
		t.Fatalf("invalid conversation response: %s", w.Body)
	}
	w = perform(h, "GET", "/api/messages?from=%2B51987654321&sim=1&limit=50", "", nil)
	if w.Code != 200 || store.filter.From != "+51987654321" || store.filter.Query != "" || store.filter.SIM != "1" {
		t.Fatalf("exact sender filter=%+v status=%d", store.filter, w.Code)
	}
	w = perform(h, "GET", "/api/sender?from=%2B51987654321&sim=1", "", nil)
	if w.Code != 200 || store.filter.From != "+51987654321" || store.filter.SIM != "1" {
		t.Fatalf("sender details filter=%+v status=%d", store.filter, w.Code)
	}
	if w = perform(h, "GET", "/api/sender?sim=1", "", nil); w.Code != 400 {
		t.Fatalf("sender details without a sender: %d", w.Code)
	}
	for _, query := range []string{"limit=0", "before=invalid", "q=" + strings.Repeat("a", 513), "from=" + strings.Repeat("a", 513)} {
		if w = perform(h, "GET", "/api/conversations?"+query, "", nil); w.Code != 400 {
			t.Errorf("invalid filter accepted: %s", query)
		}
	}
}

func TestSIMManagement(t *testing.T) {
	store := &memoryStore{}
	h := testServer(store).Handler()
	w := perform(h, "GET", "/api/sims", "", nil)
	var inventory struct {
		SIMs []SIM `json:"sims"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &inventory); err != nil || w.Code != 200 || len(inventory.SIMs) != 1 || inventory.SIMs[0].Alias != "Personal" || inventory.SIMs[0].MessageCount != 7 {
		t.Fatalf("SIM inventory: %d %s", w.Code, w.Body)
	}
	w = perform(h, "PUT", "/api/sims", `{"sim":"SIM +/1","alias":"  Personal ✓  "}`, nil)
	if w.Code != 204 || store.simID != "SIM +/1" || store.alias != "Personal ✓" {
		t.Fatalf("alias changed identifier or whitespace was retained: %d %s", w.Code, w.Body)
	}
	w = perform(h, "PUT", "/api/sims", `{"sim":"SIM +/1","alias":""}`, nil)
	if w.Code != 204 || store.alias != "" {
		t.Fatal("could not clear alias")
	}
	w = perform(h, "DELETE", "/api/sims?sim=SIM+%2B%2F1", "", nil)
	if w.Code != 200 || store.simID != "SIM +/1" || !strings.Contains(w.Body.String(), `"deletedMessages":7`) {
		t.Fatalf("delete must target exact SIM identifier: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`null`, `{}`, `{"sim":"SIM 1"}`, `{"sim":"SIM 1","alias":null}`, `{"sim":"SIM 1","alias":3}`, `{"sim":"","alias":"a"}`, `{"sim":"SIM 1","alias":"` + strings.Repeat("名", 129) + `"}`, `{"sim":"` + strings.Repeat("x", 129) + `","alias":"a"}`} {
		if w = perform(h, "PUT", "/api/sims", body, nil); w.Code != 400 {
			t.Errorf("invalid alias body accepted: %d %s", w.Code, body)
		}
	}
	for _, path := range []string{"/api/sims", "/api/sims?sim=", "/api/sims?sim=" + strings.Repeat("a", 129)} {
		if w = perform(h, "DELETE", path, "", nil); w.Code != 400 {
			t.Errorf("invalid delete accepted: %d %s", w.Code, path)
		}
	}
	for _, err := range []error{ErrSIMNotFound, errors.New("database unavailable")} {
		store.simErr = err
		status := 500
		if errors.Is(err, ErrSIMNotFound) {
			status = 404
		}
		for _, method := range []string{"PUT", "DELETE"} {
			if w = perform(h, method, "/api/sims?sim=SIM1", `{"sim":"SIM1","alias":"Work"}`, nil); w.Code != status {
				t.Errorf("%s failure: status=%d want=%d", method, w.Code, status)
			}
		}
	}
}

func TestSIMManagementRequiresSessionAndSameOrigin(t *testing.T) {
	s := testServer(&memoryStore{})
	s.Config.ViewerPassword = "private-password"
	h := s.Handler()
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if w := perform(h, method, "/api/sims?sim=SIM1", `{"sim":"SIM1","alias":"Work"}`, nil); w.Code != 401 {
			t.Errorf("%s exposed without authentication: %d", method, w.Code)
		}
	}
	login := perform(h, "POST", "/api/session", `{"password":"private-password"}`, nil)
	cookie := login.Result().Cookies()[0].String()
	for _, site := range []string{"cross-site", "same-site"} {
		for _, method := range []string{"PUT", "DELETE"} {
			w := perform(h, method, "/api/sims?sim=SIM1", `{"sim":"SIM1","alias":"Work"}`, map[string]string{"Cookie": cookie, "Sec-Fetch-Site": site})
			if w.Code != 403 {
				t.Errorf("%s %s request accepted: %d", site, method, w.Code)
			}
		}
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		w := perform(h, method, "/api/sims?sim=SIM1", `{"sim":"SIM1","alias":"Work"}`, map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "same-origin"})
		if w.Code != 200 && w.Code != 204 {
			t.Errorf("authenticated %s rejected: %d", method, w.Code)
		}
	}
}

func TestSSEInvalidationAndShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := testServer(&memoryStore{})
	s.Context = ctx
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(server.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("missing SSE headers")
	}
	reader := bufio.NewReader(response.Body)
	readEvent := func() string {
		t.Helper()
		var event strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			event.WriteString(line)
			if line == "\n" {
				return event.String()
			}
		}
	}
	if first := readEvent(); !strings.Contains(first, "event: ready") {
		t.Fatalf("first event=%s", first)
	}
	s.Events.Publish()
	if next := readEvent(); !strings.Contains(next, "event: messages") {
		t.Fatalf("next event=%s", next)
	}
	cancel()
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("stream did not close: %v", err)
	}
}

func TestRetryFingerprint(t *testing.T) {
	var in Incoming
	_ = json.Unmarshal([]byte(`{"from":"a","text":"b","sim":1}`), &in)
	if in.fingerprint() != nil {
		t.Fatal("timestamp-free message must not deduplicate")
	}
	_ = json.Unmarshal([]byte(`{"from":"a","text":"b","sim":1,"sentStamp":123}`), &in)
	one := fmt.Sprintf("%x", in.fingerprint())
	_ = json.Unmarshal([]byte(`{"from":"a","text":"b","sim":"1","sentStamp":"123"}`), &in)
	if one != fmt.Sprintf("%x", in.fingerprint()) {
		t.Fatal("equivalent scalar values should deduplicate")
	}
	in.Text = "different"
	if one == fmt.Sprintf("%x", in.fingerprint()) {
		t.Fatal("different message deduplicated")
	}
}

func TestStaticFrontendAndAPIRouting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>Inbox</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	s := testServer(&memoryStore{})
	s.Config.StaticDir = dir
	h := s.Handler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/", 200}, {"GET", "/api/messages", 200},
		{"GET", "/api/missing", 404}, {"GET", "/missing.js", 404},
		{"GET", "/empty/", 404}, {"POST", "/", 405},
	} {
		if w := perform(h, tc.method, tc.path, "", nil); w.Code != tc.status {
			t.Errorf("%s %s status=%d want=%d body=%s", tc.method, tc.path, w.Code, tc.status, w.Body)
		}
	}
}
