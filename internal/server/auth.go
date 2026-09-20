package server

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// authTimeout bounds how long a background login attempt may run.
const authTimeout = 10 * time.Minute

// authWatch is how long StartAuth waits for gh to print its instructions.
const authWatch = 20 * time.Second

var authMu sync.Mutex

// StartAuth launches `gh auth login` in the background and returns the first
// instructions it prints, which contain the one-time code and the URL to open.
func StartAuth(ctx context.Context) (string, error) {
	authMu.Lock()
	defer authMu.Unlock()

	runCtx, cancel := context.WithTimeout(context.Background(), authTimeout)
	buf := &syncBuffer{}
	cmd := exec.CommandContext(runCtx, "gh", "auth", "login", "--scopes", "codespace", "--hostname", "github.com", "--web")
	cmd.Stdout, cmd.Stderr = buf, buf
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return "", err
	}
	// gh waits for a keypress before opening a browser; the device code is
	// printed either way.
	_, _ = stdin.Write([]byte("\n"))

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		cancel()
		close(done)
	}()

	deadline := time.After(authWatch)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if text := buf.String(); strings.Contains(text, "http") || strings.Contains(text, "code:") {
				return clean(text), nil
			}
		case <-done:
			return clean(buf.String()), nil
		case <-deadline:
			return clean(buf.String()), nil
		case <-ctx.Done():
			return clean(buf.String()), ctx.Err()
		}
	}
}

// clean trims control characters gh uses for spinners.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\r", "\n")
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// syncBuffer is a concurrency-safe output sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
