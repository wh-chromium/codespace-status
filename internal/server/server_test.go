package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wh-chromium/codespace-status/internal/config"
	"github.com/wh-chromium/codespace-status/internal/monitor"
)

// newTestServer wires a server to a monitor with a temporary config file.
func newTestServer(t *testing.T) (http.Handler, *monitor.Monitor) {
	t.Helper()
	cfg := config.Default()
	cfg.Codespaces = []config.Codespace{{Name: "a", State: "Shutdown"}, {Name: "b", State: "Shutdown"}}
	m := monitor.New(filepath.Join(t.TempDir(), config.FileName), cfg)
	t.Cleanup(m.Close)
	return New(m).Handler(), m
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestIndexAndAssets(t *testing.T) {
	h, _ := newTestServer(t)
	for _, path := range []string{"/", "/static/style.css", "/static/app.js"} {
		rec := do(t, h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("GET %s served nothing", path)
		}
	}
	if body := do(t, h, http.MethodGet, "/").Body.String(); !strings.Contains(body, "codespace-status") {
		t.Fatal("index page is missing its title")
	}
	if rec := do(t, h, http.MethodGet, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rec.Code)
	}
}

func TestStatusEndpoint(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var status monitor.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Codespaces) != 2 || status.PollMS != 2000 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestSelectAndDeselectEndpoints(t *testing.T) {
	h, m := newTestServer(t)
	if rec := do(t, h, http.MethodPost, "/api/select?name=b"); rec.Code != http.StatusOK {
		t.Fatalf("select = %d", rec.Code)
	}
	if m.Config().Selected != "b" {
		t.Fatalf("selected = %q", m.Config().Selected)
	}
	if rec := do(t, h, http.MethodPost, "/api/select?name=ghost"); rec.Code != http.StatusBadRequest {
		t.Fatalf("selecting an unknown codespace = %d, want 400", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/deselect"); rec.Code != http.StatusOK {
		t.Fatalf("deselect = %d", rec.Code)
	}
	if m.Config().Selected != "" {
		t.Fatal("deselect did not clear the selection")
	}
}

func TestPollAndThemeEndpoints(t *testing.T) {
	h, m := newTestServer(t)
	if rec := do(t, h, http.MethodPost, "/api/poll?ms=5000"); rec.Code != http.StatusOK {
		t.Fatalf("poll = %d", rec.Code)
	}
	if m.Config().PollMS != 5000 {
		t.Fatalf("PollMS = %d", m.Config().PollMS)
	}
	for _, bad := range []string{"/api/poll?ms=1", "/api/poll?ms=abc", "/api/poll"} {
		if rec := do(t, h, http.MethodPost, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("POST %s = %d, want 400", bad, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodPost, "/api/theme?value=dark"); rec.Code != http.StatusOK {
		t.Fatalf("theme = %d", rec.Code)
	}
	if m.Config().Theme != "dark" {
		t.Fatalf("theme = %q", m.Config().Theme)
	}
}

func TestCleanTrimsSpinnerOutput(t *testing.T) {
	got := clean("  one \r\r two  \n\n three \n")
	if got != "one\ntwo\nthree" {
		t.Fatalf("clean = %q", got)
	}
}
