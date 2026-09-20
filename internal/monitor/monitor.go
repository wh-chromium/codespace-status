// Package monitor keeps a live view of resource usage for every known
// codespace and owns the persisted configuration.
package monitor

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wh-chromium/codespace-status/internal/config"
	"github.com/wh-chromium/codespace-status/internal/ghcli"
	"github.com/wh-chromium/codespace-status/internal/metrics"
)

// WindowSize is the number of one-sample buckets kept per codespace, which
// covers the last 60 samples shown by every card.
const WindowSize = 60

// Opener starts a metrics session for a codespace.
type Opener func(ctx context.Context, codespace string, interval time.Duration) (*ghcli.Session, error)

// CodespaceStatus is the per-card payload shared by the web UI, the CLI and
// the VS Code extension.
type CodespaceStatus struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"display_name,omitempty"`
	Repository  string           `json:"repository,omitempty"`
	Machine     string           `json:"machine,omitempty"`
	State       string           `json:"state"`
	Status      string           `json:"status"`
	Selected    bool             `json:"selected"`
	Error       string           `json:"error,omitempty"`
	Latest      *metrics.Sample  `json:"latest,omitempty"`
	Samples     []metrics.Sample `json:"samples"`
}

// Status is the complete state served to any front end.
type Status struct {
	Selected   string            `json:"selected"`
	PollMS     int               `json:"poll_ms"`
	Theme      string            `json:"theme"`
	SyncedAt   string            `json:"synced_at,omitempty"`
	SyncError  string            `json:"sync_error,omitempty"`
	Codespaces []CodespaceStatus `json:"codespaces"`
}

// Monitor coordinates configuration, syncing and per-codespace collectors.
type Monitor struct {
	mu         sync.Mutex
	cfg        config.Config
	path       string
	client     ghcli.Client
	open       Opener
	collectors map[string]*collector
	syncedAt   string
	syncErr    string
	local      bool
	closed     bool
}

// New builds a monitor bound to a config file path.
func New(path string, cfg config.Config) *Monitor {
	client := ghcli.Client{}
	m := &Monitor{
		cfg:        cfg,
		path:       path,
		client:     client,
		collectors: map[string]*collector{},
	}
	m.open = client.Open
	return m
}

// SetOpener overrides how metrics sessions are started, used by tests and by
// the local self-check mode.
func (m *Monitor) SetOpener(o Opener) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.open = o
}

// OpenSession starts a metrics session using the configured opener.
func (m *Monitor) OpenSession(ctx context.Context, codespace string, interval time.Duration) (*ghcli.Session, error) {
	m.mu.Lock()
	open := m.open
	m.mu.Unlock()
	return open(ctx, codespace, interval)
}

// Client exposes the underlying gh wrapper.
func (m *Monitor) Client() ghcli.Client { return m.client }

// Config returns a copy of the current configuration.
func (m *Monitor) Config() config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// Sync refreshes the codespace list from gh and reconciles collectors.
func (m *Monitor) Sync(ctx context.Context) error {
	m.mu.Lock()
	local := m.local
	m.mu.Unlock()
	if local {
		return nil
	}

	list, err := m.client.List(ctx)

	m.mu.Lock()
	if err != nil {
		m.syncErr = err.Error()
		m.mu.Unlock()
		return err
	}
	entries := make([]config.Codespace, 0, len(list))
	for _, cs := range list {
		entries = append(entries, config.Codespace{
			Name:        cs.Name,
			DisplayName: cs.DisplayName,
			Repository:  cs.Repository,
			Machine:     cs.Machine,
			State:       cs.State,
		})
	}
	m.cfg.Merge(entries)
	m.syncErr = ""
	m.syncedAt = time.Now().UTC().Format(time.RFC3339)
	m.mu.Unlock()

	m.reconcile()
	return m.save()
}

// Select makes one codespace the active one.
func (m *Monitor) Select(name string) error {
	m.mu.Lock()
	if !m.cfg.Has(name) {
		m.mu.Unlock()
		return errors.New("unknown codespace: " + name)
	}
	m.cfg.Selected = name
	m.mu.Unlock()
	m.reconcile()
	return m.save()
}

// Deselect clears the active codespace.
func (m *Monitor) Deselect() error {
	m.mu.Lock()
	m.cfg.Selected = ""
	m.mu.Unlock()
	m.reconcile()
	return m.save()
}

// SetPollMS changes the sampling interval for every collector.
func (m *Monitor) SetPollMS(ms int) error {
	m.mu.Lock()
	m.cfg.PollMS = ms
	m.cfg.Normalize()
	m.mu.Unlock()
	return m.save()
}

// SetTheme stores the preferred web UI theme.
func (m *Monitor) SetTheme(theme string) error {
	m.mu.Lock()
	m.cfg.Theme = theme
	m.cfg.Normalize()
	m.mu.Unlock()
	return m.save()
}

// Target returns the codespace a command should run against, preferring an
// explicit override over the active selection.
func (m *Monitor) Target(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Selected == "" {
		return "", errors.New("no codespace selected: run `codespace-status select <name>`")
	}
	return m.cfg.Selected, nil
}

// pollInterval reports the configured sampling interval.
func (m *Monitor) pollInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Duration(m.cfg.PollMS) * time.Millisecond
}

// save persists the configuration to disk.
func (m *Monitor) save() error {
	m.mu.Lock()
	cfg, path := m.cfg, m.path
	m.mu.Unlock()
	if path == "" {
		return nil
	}
	return config.Save(path, cfg)
}

// reconcile starts collectors for codespaces that should be sampled and stops
// the rest.
func (m *Monitor) reconcile() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	wanted := map[string]bool{}
	for _, cs := range m.cfg.Codespaces {
		if !strings.EqualFold(cs.State, "Available") {
			continue
		}
		if m.cfg.SampleAll || cs.Name == m.cfg.Selected {
			wanted[cs.Name] = true
		}
	}
	var stop []*collector
	for name, c := range m.collectors {
		if !wanted[name] {
			stop = append(stop, c)
			delete(m.collectors, name)
		}
	}
	var start []string
	for name := range wanted {
		if _, ok := m.collectors[name]; !ok {
			start = append(start, name)
		}
	}
	open := m.open
	for _, name := range start {
		c := newCollector(name, open, m.pollIntervalLocked)
		m.collectors[name] = c
	}
	m.mu.Unlock()

	for _, c := range stop {
		c.stop()
	}
	for _, name := range start {
		m.mu.Lock()
		c := m.collectors[name]
		m.mu.Unlock()
		if c != nil {
			c.start()
		}
	}
}

// pollIntervalLocked is the interval getter handed to collectors.
func (m *Monitor) pollIntervalLocked() time.Duration { return m.pollInterval() }

// Status renders the current state with the active codespace first.
func (m *Monitor) Status() Status {
	m.mu.Lock()
	cfg := m.cfg
	syncedAt, syncErr := m.syncedAt, m.syncErr
	collectors := make(map[string]*collector, len(m.collectors))
	for k, v := range m.collectors {
		collectors[k] = v
	}
	m.mu.Unlock()

	out := Status{
		Selected:   cfg.Selected,
		PollMS:     cfg.PollMS,
		Theme:      cfg.Theme,
		SyncedAt:   syncedAt,
		SyncError:  syncErr,
		Codespaces: make([]CodespaceStatus, 0, len(cfg.Codespaces)),
	}
	for _, cs := range cfg.Codespaces {
		item := CodespaceStatus{
			Name:        cs.Name,
			DisplayName: cs.DisplayName,
			Repository:  cs.Repository,
			Machine:     cs.Machine,
			State:       cs.State,
			Status:      classify(cs.State),
			Selected:    cs.Name == cfg.Selected,
			Samples:     []metrics.Sample{},
		}
		if c := collectors[cs.Name]; c != nil {
			samples, latest, err := c.snapshot()
			item.Samples = samples
			item.Latest = latest
			if err != "" {
				item.Error = err
			}
		}
		out.Codespaces = append(out.Codespaces, item)
	}
	sortCodespaces(out.Codespaces, cfg.Selected)
	return out
}

// Close stops every collector.
func (m *Monitor) Close() {
	m.mu.Lock()
	m.closed = true
	var all []*collector
	for name, c := range m.collectors {
		all = append(all, c)
		delete(m.collectors, name)
	}
	m.mu.Unlock()
	for _, c := range all {
		c.stop()
	}
}

// classify maps a gh codespace state to a coarse UI status.
func classify(state string) string {
	switch strings.ToLower(state) {
	case "available":
		return "active"
	case "shutdown", "shuttingdown":
		return "shutdown"
	case "starting", "queued", "provisioning", "awaiting", "rebuilding", "exporting":
		return "starting"
	case "":
		return "unknown"
	default:
		return "offline"
	}
}

// sortCodespaces puts the selected card first, then active ones, then names.
func sortCodespaces(list []CodespaceStatus, selected string) {
	rank := func(c CodespaceStatus) int {
		switch {
		case c.Name == selected && selected != "":
			return 0
		case c.Status == "active":
			return 1
		case c.Status == "starting":
			return 2
		case c.Status == "shutdown":
			return 3
		default:
			return 4
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		ri, rj := rank(list[i]), rank(list[j])
		if ri != rj {
			return ri < rj
		}
		return list[i].Name < list[j].Name
	})
}

// ReloadFromDisk picks up selection, poll and theme changes written by another
// process, such as the CLI or the VS Code extension.
func (m *Monitor) ReloadFromDisk() error {
	if m.path == "" {
		return nil
	}
	disk, err := config.Load(m.path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	local := m.local
	m.mu.Unlock()
	if local {
		return nil
	}
	m.mu.Lock()
	changed := m.cfg.Selected != disk.Selected || m.cfg.PollMS != disk.PollMS || m.cfg.Theme != disk.Theme
	if changed {
		if m.cfg.Has(disk.Selected) || disk.Selected == "" {
			m.cfg.Selected = disk.Selected
		}
		m.cfg.PollMS = disk.PollMS
		m.cfg.Theme = disk.Theme
		m.cfg.Normalize()
	}
	m.mu.Unlock()
	if changed {
		m.reconcile()
	}
	return nil
}

// UseLocal points the monitor at the machine it runs on instead of a remote
// codespace. It backs the CODESPACE_STATUS_LOCAL development mode and keeps
// the collector, server and UI exercisable without a GitHub round trip.
func (m *Monitor) UseLocal() {
	m.mu.Lock()
	m.local = true
	m.open = func(ctx context.Context, _ string, interval time.Duration) (*ghcli.Session, error) {
		return ghcli.OpenLocal(ctx, interval)
	}
	m.cfg.Codespaces = []config.Codespace{{
		Name:        "local",
		DisplayName: "local machine",
		Repository:  "local",
		Machine:     "host",
		State:       "Available",
	}}
	if m.cfg.Selected == "" {
		m.cfg.Selected = "local"
	}
	m.syncedAt = time.Now().UTC().Format(time.RFC3339)
	m.mu.Unlock()
	m.reconcile()
}
