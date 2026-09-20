package config

import (
	"path/filepath"
	"testing"
)

func TestLoadCreatesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollMS != 2000 || cfg.Theme != "light" || !cfg.SampleAll {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("reloading written defaults: %v", err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	cfg := Default()
	cfg.Codespaces = []Codespace{{Name: "a", State: "Available"}}
	cfg.Selected = "a"
	cfg.Theme = "dark"
	cfg.PollMS = 5000
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Selected != "a" || got.Theme != "dark" || got.PollMS != 5000 {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestNormalizeClampsValues(t *testing.T) {
	cfg := Config{PollMS: 10, Theme: "neon", WebPort: 0, Selected: "ghost"}
	cfg.Normalize()
	if cfg.PollMS != MinPollMS || cfg.Theme != "light" || cfg.WebPort != Default().WebPort {
		t.Fatalf("normalize failed: %+v", cfg)
	}
	if cfg.Selected != "" {
		t.Fatal("selection of an unknown codespace must be cleared")
	}
	cfg.PollMS = 999999
	cfg.Normalize()
	if cfg.PollMS != MaxPollMS {
		t.Fatalf("PollMS = %d, want %d", cfg.PollMS, MaxPollMS)
	}
}

func TestMergeKeepsValidSelection(t *testing.T) {
	cfg := Default()
	cfg.Selected = "a"
	cfg.Merge([]Codespace{{Name: "a"}, {Name: "b"}})
	if cfg.Selected != "a" || len(cfg.Codespaces) != 2 {
		t.Fatalf("merge lost state: %+v", cfg)
	}
	if cfg.Codespaces[0].LastSync == "" {
		t.Fatal("merge must stamp the sync time")
	}
	cfg.Merge([]Codespace{{Name: "b"}})
	if cfg.Selected != "" {
		t.Fatal("selection must clear when the codespace disappears")
	}
}

func TestFindAndHas(t *testing.T) {
	cfg := Config{Codespaces: []Codespace{{Name: "x", State: "Shutdown"}}}
	if !cfg.Has("x") || cfg.Has("") || cfg.Has("y") {
		t.Fatal("Has is wrong")
	}
	cs, ok := cfg.Find("x")
	if !ok || cs.State != "Shutdown" {
		t.Fatalf("Find = %+v, %v", cs, ok)
	}
	if _, ok := cfg.Find("y"); ok {
		t.Fatal("Find must report missing entries")
	}
}
