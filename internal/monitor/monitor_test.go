package monitor

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wh-chromium/codespace-status/internal/config"
)

// newTestMonitor builds a monitor backed by a temporary config file.
func newTestMonitor(t *testing.T, list []config.Codespace) *Monitor {
	t.Helper()
	cfg := config.Default()
	cfg.Codespaces = list
	m := New(filepath.Join(t.TempDir(), config.FileName), cfg)
	t.Cleanup(m.Close)
	return m
}

func TestSelectAndDeselect(t *testing.T) {
	m := newTestMonitor(t, []config.Codespace{{Name: "a", State: "Shutdown"}})
	if err := m.Select("missing"); err == nil {
		t.Fatal("selecting an unknown codespace must fail")
	}
	if err := m.Select("a"); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Target(""); err != nil || got != "a" {
		t.Fatalf("Target = %q, %v", got, err)
	}
	if got, _ := m.Target("override"); got != "override" {
		t.Fatalf("override ignored: %q", got)
	}
	if err := m.Deselect(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Target(""); err == nil {
		t.Fatal("Target must fail with no selection")
	}
}

func TestStatusPutsSelectedFirst(t *testing.T) {
	m := newTestMonitor(t, []config.Codespace{
		{Name: "zeta", State: "Available"},
		{Name: "alpha", State: "Shutdown"},
		{Name: "beta", State: "Available"},
	})
	if err := m.Select("alpha"); err != nil {
		t.Fatal(err)
	}
	status := m.Status()
	order := []string{status.Codespaces[0].Name, status.Codespaces[1].Name, status.Codespaces[2].Name}
	want := []string{"alpha", "beta", "zeta"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if !status.Codespaces[0].Selected || status.Codespaces[0].Status != "shutdown" {
		t.Fatalf("selected card wrong: %+v", status.Codespaces[0])
	}
	if status.Codespaces[1].Status != "active" {
		t.Fatalf("state mapping wrong: %+v", status.Codespaces[1])
	}
}

func TestSetPollAndThemeArePersisted(t *testing.T) {
	m := newTestMonitor(t, nil)
	if err := m.SetPollMS(100); err != nil {
		t.Fatal(err)
	}
	if m.Config().PollMS != config.MinPollMS {
		t.Fatalf("PollMS = %d, want clamped", m.Config().PollMS)
	}
	if err := m.SetTheme("dark"); err != nil {
		t.Fatal(err)
	}
	if m.Config().Theme != "dark" {
		t.Fatal("theme not stored")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"Available": "active", "Shutdown": "shutdown", "Starting": "starting",
		"Unknown": "offline", "": "unknown", "Failed": "offline",
	}
	for state, want := range cases {
		if got := classify(state); got != want {
			t.Errorf("classify(%q) = %q, want %q", state, got, want)
		}
	}
}

// TestLocalCollection exercises the real collector against the machine running
// the tests, which mirrors what happens inside a codespace over SSH.
func TestLocalCollection(t *testing.T) {
	m := newTestMonitor(t, nil)
	if err := m.SetPollMS(config.MinPollMS); err != nil {
		t.Fatal(err)
	}
	m.UseLocal()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status := m.Status()
		if len(status.Codespaces) == 1 && status.Codespaces[0].Latest != nil {
			latest := status.Codespaces[0].Latest
			if latest.MemTotalBytes == 0 {
				t.Fatalf("sample has no memory total: %+v", latest)
			}
			if latest.CPUPercent < 0 || latest.CPUPercent > 100 {
				t.Fatalf("CPU out of range: %v", latest.CPUPercent)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no sample collected: %+v", m.Status())
}
