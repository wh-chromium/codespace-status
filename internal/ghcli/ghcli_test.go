package ghcli

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseAuthStatus(t *testing.T) {
	out := `github.com
  ✓ Logged in to github.com account octocat (keyring)
  - Active account: true
  - Token scopes: 'codespace', 'repo', 'read:org'`
	account, scopes := parseAuthStatus(out)
	if account != "octocat" {
		t.Fatalf("account = %q", account)
	}
	if strings.Join(scopes, ",") != "codespace,repo,read:org" {
		t.Fatalf("scopes = %v", scopes)
	}
}

func TestParseAuthStatusWithoutScopes(t *testing.T) {
	account, scopes := parseAuthStatus("You are not logged into any GitHub hosts.")
	if account != "" || len(scopes) != 0 {
		t.Fatalf("account = %q, scopes = %v", account, scopes)
	}
}

func TestOneLineCollapsesAndTruncates(t *testing.T) {
	if got := oneLine(" a \n b \t c \n"); got != "a b c" {
		t.Fatalf("oneLine = %q", got)
	}
	long := oneLine(strings.Repeat("x", 500))
	if len(long) != 303 || !strings.HasSuffix(long, "...") {
		t.Fatalf("long message not truncated: %d", len(long))
	}
}

func TestRunningState(t *testing.T) {
	if !(Codespace{State: "Available"}).Running() || (Codespace{State: "Shutdown"}).Running() {
		t.Fatal("Running is wrong")
	}
}

// TestOpenLocalStreamsSamples verifies the session framing against a real
// shell, which is exactly what runs inside a codespace over SSH.
func TestOpenLocalStreamsSamples(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	session, err := OpenLocal(ctx, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	first, err := session.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.Sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.MemTotalBytes == 0 || second.MemTotalBytes == 0 {
		t.Fatal("sessions returned empty snapshots")
	}
	if !second.Time.After(first.Time) {
		t.Fatal("samples are not ordered in time")
	}
}
