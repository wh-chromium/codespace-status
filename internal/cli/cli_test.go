package cli

import (
	"strings"
	"testing"

	"github.com/wh-chromium/codespace-status/internal/metrics"
)

func TestParseFlags(t *testing.T) {
	opts, rest, err := parseFlags([]string{"-c", "spacey", "--json", "--port", "9000", "--no-sync", "extra"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.codespace != "spacey" || !opts.json || opts.port != 9000 || !opts.noSync {
		t.Fatalf("opts = %+v", opts)
	}
	if len(rest) != 1 || rest[0] != "extra" {
		t.Fatalf("rest = %v", rest)
	}
}

func TestParseFlagsStopsAtDoubleDash(t *testing.T) {
	_, rest, err := parseFlags([]string{"--", "ls", "-la", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 3 || rest[2] != "--json" {
		t.Fatalf("rest = %v", rest)
	}
}

func TestParseFlagsRejectsBadValues(t *testing.T) {
	if _, _, err := parseFlags([]string{"--port"}); err == nil {
		t.Fatal("missing value must fail")
	}
	if _, _, err := parseFlags([]string{"--port", "abc"}); err == nil {
		t.Fatal("non numeric port must fail")
	}
}

func TestNeedsSync(t *testing.T) {
	for _, cmd := range []string{"list", "watch", "serve", "exec"} {
		if !needsSync(cmd) {
			t.Errorf("%s should sync", cmd)
		}
	}
	for _, cmd := range []string{"config", "permissions", "auth", "theme"} {
		if needsSync(cmd) {
			t.Errorf("%s should not sync", cmd)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[float64]string{0: "0B", 1023: "1023B", 2048: "2.0KB", 5 * 1024 * 1024: "5.0MB", 3 * 1024 * 1024 * 1024: "3.00GB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatSampleCoversEveryMetric(t *testing.T) {
	line := formatSample(metrics.Sample{CPUPercent: 12.5, MemPercent: 50, MemUsedBytes: 1024, MemTotalBytes: 2048, DiskIOPS: 3})
	for _, want := range []string{"cpu", "mem", "net in", "out", "disk io", "r ", "w "} {
		if !strings.Contains(line, want) {
			t.Fatalf("%q missing from %q", want, line)
		}
	}
}

func TestTrim(t *testing.T) {
	if got := trim("abcdef", 4); got != "abc…" {
		t.Fatalf("trim = %q", got)
	}
	if got := trim("ab", 4); got != "ab" {
		t.Fatalf("trim = %q", got)
	}
}
