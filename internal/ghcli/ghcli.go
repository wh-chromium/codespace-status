// Package ghcli wraps the GitHub CLI so the rest of the tool can list
// codespaces, run commands inside them and inspect authentication state.
package ghcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/wh-chromium/codespace-status/internal/metrics"
)

// Codespace is the subset of `gh codespace list` output the tool needs.
type Codespace struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Repository  string `json:"repository"`
	State       string `json:"state"`
	Machine     string `json:"machineName"`
}

// Running reports whether the codespace can currently accept SSH commands.
func (c Codespace) Running() bool { return strings.EqualFold(c.State, "Available") }

// Permissions is the result of the "check permissions" action.
type Permissions struct {
	GHInstalled   bool     `json:"gh_installed"`
	Authenticated bool     `json:"authenticated"`
	Account       string   `json:"account"`
	Scopes        []string `json:"scopes"`
	CanList       bool     `json:"can_list_codespaces"`
	CodespaceOK   bool     `json:"codespace_scope"`
	Messages      []string `json:"messages"`
}

// ErrNotFound reports that the gh executable is unavailable.
var ErrNotFound = errors.New("ghcli: the GitHub CLI (gh) was not found in PATH")

// Client runs the gh executable. The zero value is ready to use.
type Client struct {
	// Bin overrides the executable name, used by tests.
	Bin string
}

// bin returns the executable to run.
func (c Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "gh"
}

// List returns every codespace visible to the authenticated user.
func (c Client) List(ctx context.Context) ([]Codespace, error) {
	out, err := c.output(ctx, "codespace", "list", "--json", "name,displayName,repository,state,machineName")
	if err != nil {
		return nil, err
	}
	var list []Codespace
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("ghcli: parsing codespace list: %w", err)
	}
	return list, nil
}

// Exec runs one command inside a codespace, wiring the caller's stdio through.
func (c Client) Exec(ctx context.Context, codespace string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	full := append([]string{"codespace", "ssh", "-c", codespace, "--"}, args...)
	cmd := exec.CommandContext(ctx, c.bin(), full...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

// Login starts an interactive gh login requesting the codespace scope.
func (c Client) Login(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, c.bin(), "auth", "login", "--scopes", "codespace", "--hostname", "github.com", "--web")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

// LoginCommand is the command a user should run to grant codespace access.
const LoginCommand = "gh auth login --scopes codespace --web"

// Permissions probes gh for authentication and codespace access.
func (c Client) Permissions(ctx context.Context) Permissions {
	p := Permissions{}
	if _, err := exec.LookPath(c.bin()); err != nil {
		p.Messages = append(p.Messages, ErrNotFound.Error())
		return p
	}
	p.GHInstalled = true

	status, err := c.combined(ctx, "auth", "status")
	if err != nil {
		p.Messages = append(p.Messages, "gh is not authenticated: run "+LoginCommand)
	} else {
		p.Authenticated = true
		p.Account, p.Scopes = parseAuthStatus(string(status))
		for _, s := range p.Scopes {
			if s == "codespace" {
				p.CodespaceOK = true
			}
		}
	}

	if p.Authenticated {
		listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := c.List(listCtx); err == nil {
			p.CanList = true
			p.CodespaceOK = true
			p.Messages = append(p.Messages, "codespaces are readable with the current credentials")
		} else {
			p.Messages = append(p.Messages, "cannot list codespaces: "+oneLine(err.Error()))
			p.Messages = append(p.Messages, "grant access with "+LoginCommand)
		}
	}
	if p.Account == "" && p.Authenticated {
		p.Account = "authenticated"
	}
	return p
}

// parseAuthStatus pulls the account name and token scopes out of gh output.
func parseAuthStatus(out string) (account string, scopes []string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "✓ Logged in to"):
			if i := strings.Index(line, "account "); i >= 0 {
				rest := strings.Fields(line[i+len("account "):])
				if len(rest) > 0 {
					account = rest[0]
				}
			}
		case strings.HasPrefix(line, "- Token scopes:"), strings.HasPrefix(line, "Token scopes:"):
			_, list, _ := strings.Cut(line, ":")
			for _, s := range strings.Split(list, ",") {
				s = strings.Trim(strings.TrimSpace(s), "'\"")
				if s != "" {
					scopes = append(scopes, s)
				}
			}
		}
	}
	return account, scopes
}

// Session is a long-lived SSH shell inside a codespace that returns one block
// of /proc counters per Sample call.
type Session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	blocks chan string
	stderr *lockedBuffer
	done   chan struct{}
	once   sync.Once
}

// Open starts a metrics session against a codespace, sampling every interval.
func (c Client) Open(ctx context.Context, codespace string, interval time.Duration) (*Session, error) {
	cmd := exec.CommandContext(ctx, c.bin(), "codespace", "ssh", "-c", codespace, "--", "sh", "-s")
	return startSession(cmd, interval)
}

// OpenLocal starts a metrics session against the local machine. It exists so
// the collector can be exercised without a codespace round trip.
func OpenLocal(ctx context.Context, interval time.Duration) (*Session, error) {
	return startSession(exec.CommandContext(ctx, "sh", "-s"), interval)
}

// startSession wires stdio around an already-built command and starts it.
func startSession(cmd *exec.Cmd, interval time.Duration) (*Session, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	buf := &lockedBuffer{}
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &Session{cmd: cmd, stdin: stdin, blocks: make(chan string, 2), stderr: buf, done: make(chan struct{})}
	if _, err := io.WriteString(stdin, metrics.RemoteScript(interval)); err != nil {
		s.Close()
		return nil, err
	}
	// The remote loop is self-timed, so stdin is closed to signal end of script.
	_ = stdin.Close()
	go s.read(stdout)
	go func() {
		_ = cmd.Wait()
		close(s.done)
	}()
	return s, nil
}

// read collects framed blocks from the remote shell until the stream ends.
func (s *Session) read(stdout io.Reader) {
	defer close(s.blocks)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var block strings.Builder
	collecting := false
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch strings.TrimSpace(line) {
		case metrics.BeginMarker:
			block.Reset()
			collecting = true
		case metrics.EndMarker:
			if collecting {
				s.blocks <- block.String()
				collecting = false
			}
		default:
			if collecting {
				block.WriteString(line)
				block.WriteByte('\n')
			}
		}
	}
}

// Sample waits for the next block emitted by the remote loop.
func (s *Session) Sample(ctx context.Context) (metrics.Snapshot, error) {
	select {
	case block, ok := <-s.blocks:
		if !ok {
			return metrics.Snapshot{}, s.wrap(errors.New("session closed"))
		}
		return metrics.Parse(block, time.Now())
	case <-ctx.Done():
		return metrics.Snapshot{}, ctx.Err()
	case <-s.done:
		return metrics.Snapshot{}, s.wrap(errors.New("session ended"))
	}
}

// wrap decorates an error with whatever the remote side printed to stderr.
func (s *Session) wrap(err error) error {
	if msg := oneLine(s.stderr.String()); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// Close terminates the SSH session.
func (s *Session) Close() error {
	s.once.Do(func() {
		_ = s.stdin.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
	return nil
}

// output runs gh and returns stdout, reporting stderr on failure.
func (c Client) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = append(os.Environ(), "GH_PAGER=cat", "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := oneLine(stderr.String()); msg != "" {
			return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// combined runs gh and returns stdout and stderr together.
func (c Client) combined(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = append(os.Environ(), "GH_PAGER=cat", "NO_COLOR=1")
	return cmd.CombinedOutput()
}

// oneLine flattens multi-line command output into a compact message.
func oneLine(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	msg := strings.Join(fields, " ")
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return msg
}

// lockedBuffer is a concurrency-safe stderr sink.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
