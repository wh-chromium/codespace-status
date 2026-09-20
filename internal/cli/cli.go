// Package cli implements the codespace-status command line, which is also the
// backend used by the web UI and the VS Code extension.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wh-chromium/codespace-status/internal/config"
	"github.com/wh-chromium/codespace-status/internal/ghcli"
	"github.com/wh-chromium/codespace-status/internal/metrics"
	"github.com/wh-chromium/codespace-status/internal/monitor"
	"github.com/wh-chromium/codespace-status/internal/server"
)

// Version is stamped at build time.
var Version = "dev"

const usage = `codespace-status - resource usage for your GitHub codespaces

Usage: codespace-status <command> [flags] [args]

Commands:
  sync                     refresh the codespace list from the GitHub CLI
  list                     list known codespaces and their state
  select <name>            make a codespace the active one (only one at a time)
  deselect                 clear the active codespace
  status [--json]          sample the active codespace once
  watch [--json] [-c name] stream samples until interrupted
  exec <command>...        run a command in the active codespace
  serve [--port n]         start the web UI server
  show                     open the web UI in a browser
  permissions [--json]     check codespace permissions
  auth                     start GitHub authentication
  poll <ms>                set the sampling interval (500-60000)
  theme <light|dark>       set the web UI theme
  config [path|show|edit]  open or inspect the generated config
  version                  print the version

Flags:
  -c, --codespace <name>   override the active codespace for this command
      --json               machine readable output
      --port <n>           web UI port (serve)
      --no-sync            skip the automatic startup sync
`

// Run executes one command and returns the process exit code.
func Run(args []string) int {
	if len(args) < 2 {
		fmt.Print(usage)
		return 2
	}

	cmd := args[1]
	opts, rest, err := parseFlags(args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "version", "--version":
		fmt.Println("codespace-status", Version)
		return 0
	}

	path, err := config.Path()
	if err != nil {
		return fail(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fail(err)
	}
	mon := monitor.New(path, cfg)
	defer mon.Close()
	if os.Getenv("CODESPACE_STATUS_LOCAL") == "1" {
		mon.UseLocal()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Every command that needs the codespace list syncs on startup.
	if needsSync(cmd) && !opts.noSync {
		syncCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err := mon.Sync(syncCtx)
		cancel()
		if err != nil && cmd != "serve" && cmd != "watch" {
			fmt.Fprintln(os.Stderr, "warning: sync failed:", err)
		}
	}

	switch cmd {
	case "sync":
		fmt.Printf("synced %d codespaces\n", len(mon.Config().Codespaces))
		return runList(mon, opts)
	case "list":
		return runList(mon, opts)
	case "select":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: codespace-status select <name>")
			return 2
		}
		if err := mon.Select(rest[0]); err != nil {
			return fail(err)
		}
		fmt.Println("selected", rest[0])
		return 0
	case "deselect":
		if err := mon.Deselect(); err != nil {
			return fail(err)
		}
		fmt.Println("deselected")
		return 0
	case "status":
		return runStatus(ctx, mon, opts)
	case "watch":
		return runWatch(ctx, mon, opts)
	case "exec":
		return runExec(ctx, mon, opts, rest)
	case "serve":
		return runServe(ctx, mon, opts)
	case "show":
		return runShow(mon, opts)
	case "permissions":
		return runPermissions(ctx, mon, opts)
	case "auth":
		return runAuth(ctx, mon)
	case "poll":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: codespace-status poll <ms>")
			return 2
		}
		ms, err := strconv.Atoi(rest[0])
		if err != nil {
			return fail(err)
		}
		if err := mon.SetPollMS(ms); err != nil {
			return fail(err)
		}
		fmt.Println("poll interval", mon.Config().PollMS, "ms")
		return 0
	case "theme":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: codespace-status theme <light|dark>")
			return 2
		}
		if err := mon.SetTheme(rest[0]); err != nil {
			return fail(err)
		}
		fmt.Println("theme", mon.Config().Theme)
		return 0
	case "config":
		return runConfig(path, rest)
	default:
		fmt.Print(usage)
		return 2
	}
}

// options holds parsed command flags.
type options struct {
	codespace string
	json      bool
	port      int
	noSync    bool
}

// parseFlags splits flags from positional arguments.
func parseFlags(args []string) (options, []string, error) {
	var o options
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("missing value for %s", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "-c", "--codespace":
			v, err := next()
			if err != nil {
				return o, nil, err
			}
			o.codespace = v
		case "--json":
			o.json = true
		case "--no-sync":
			o.noSync = true
		case "--port":
			v, err := next()
			if err != nil {
				return o, nil, err
			}
			p, err := strconv.Atoi(v)
			if err != nil {
				return o, nil, fmt.Errorf("invalid port %q", v)
			}
			o.port = p
		case "--":
			rest = append(rest, args[i+1:]...)
			return o, rest, nil
		default:
			rest = append(rest, a)
		}
	}
	return o, rest, nil
}

// needsSync reports whether a command depends on a fresh codespace list.
func needsSync(cmd string) bool {
	switch cmd {
	case "sync", "list", "select", "status", "watch", "serve", "exec":
		return true
	}
	return false
}

// runList prints known codespaces, active one first.
func runList(mon *monitor.Monitor, opts options) int {
	status := mon.Status()
	if opts.json {
		return printJSON(status.Codespaces)
	}
	if len(status.Codespaces) == 0 {
		fmt.Println("no codespaces found")
		return 0
	}
	for _, cs := range status.Codespaces {
		mark := " "
		if cs.Selected {
			mark = "*"
		}
		fmt.Printf("%s %-36s %-10s %-10s %s\n", mark, cs.Name, cs.Status, cs.State, cs.Repository)
	}
	return 0
}

// runStatus samples the target codespace once and prints the result.
func runStatus(ctx context.Context, mon *monitor.Monitor, opts options) int {
	target, err := mon.Target(opts.codespace)
	if err != nil {
		if opts.json {
			return printJSON(mon.Status())
		}
		return fail(err)
	}

	sample, err := sampleOnce(ctx, mon, target)
	if err != nil {
		return fail(err)
	}
	if opts.json {
		return printJSON(map[string]any{"codespace": target, "latest": sample})
	}
	fmt.Println("codespace:", target)
	fmt.Println(formatSample(sample))
	return 0
}

// sampleOnce opens a session and takes the two readings a delta needs.
func sampleOnce(ctx context.Context, mon *monitor.Monitor, target string) (metrics.Sample, error) {
	interval := time.Duration(mon.Config().PollMS) * time.Millisecond
	session, err := mon.OpenSession(ctx, target, interval)
	if err != nil {
		return metrics.Sample{}, err
	}
	defer session.Close()

	first, err := session.Sample(ctx)
	if err != nil {
		return metrics.Sample{}, err
	}
	second, err := session.Sample(ctx)
	if err != nil {
		return metrics.Sample{}, err
	}
	return metrics.Delta(first, second), nil
}

// runWatch streams the full status until interrupted.
func runWatch(ctx context.Context, mon *monitor.Monitor, opts options) int {
	if opts.codespace != "" {
		if err := mon.Select(opts.codespace); err != nil {
			return fail(err)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	ticker := time.NewTicker(time.Duration(mon.Config().PollMS) * time.Millisecond)
	defer ticker.Stop()
	lastSync := time.Now()

	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
		_ = mon.ReloadFromDisk()
		if time.Since(lastSync) > 30*time.Second {
			syncCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			_ = mon.Sync(syncCtx)
			cancel()
			lastSync = time.Now()
		}
		status := mon.Status()
		if opts.json {
			if err := enc.Encode(status); err != nil {
				return fail(err)
			}
		} else {
			printStatusTable(status, opts.codespace)
		}
		ticker.Reset(time.Duration(status.PollMS) * time.Millisecond)
	}
}

// printStatusTable renders one line per sampled codespace.
func printStatusTable(status monitor.Status, filter string) {
	for _, cs := range status.Codespaces {
		if filter != "" && cs.Name != filter {
			continue
		}
		if cs.Latest == nil {
			continue
		}
		mark := " "
		if cs.Selected {
			mark = "*"
		}
		fmt.Printf("%s %-30s %s\n", mark, trim(cs.Name, 30), formatSample(*cs.Latest))
	}
}

// runExec runs a command inside the target codespace.
func runExec(ctx context.Context, mon *monitor.Monitor, opts options, rest []string) int {
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: codespace-status exec <command> [-c <codespace>]")
		return 2
	}
	target, err := mon.Target(opts.codespace)
	if err != nil {
		return fail(err)
	}
	err = mon.Client().Exec(ctx, target, rest, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return fail(err)
	}
	return 0
}

// runServe starts the web UI and blocks until interrupted.
func runServe(ctx context.Context, mon *monitor.Monitor, opts options) int {
	port := opts.port
	if port == 0 {
		port = mon.Config().WebPort
	}
	srv := server.New(mon)
	addr, err := srv.Listen(ctx, port)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("codespace-status web UI on http://%s\n", addr)
	<-ctx.Done()
	return 0
}

// runShow opens the web UI in a browser.
func runShow(mon *monitor.Monitor, opts options) int {
	port := opts.port
	if port == 0 {
		port = mon.Config().WebPort
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	fmt.Println("opening", url)
	return fail(openBrowser(url))
}

// runPermissions prints the result of the permission probe.
func runPermissions(ctx context.Context, mon *monitor.Monitor, opts options) int {
	p := mon.Client().Permissions(ctx)
	if opts.json {
		return printJSON(p)
	}
	fmt.Println("gh installed:    ", p.GHInstalled)
	fmt.Println("authenticated:   ", p.Authenticated, p.Account)
	fmt.Println("token scopes:    ", strings.Join(p.Scopes, ", "))
	fmt.Println("codespace access:", p.CanList)
	for _, m := range p.Messages {
		fmt.Println(" -", m)
	}
	if !p.CanList {
		fmt.Println("run:", ghcli.LoginCommand)
	}
	return 0
}

// runAuth starts an interactive gh login.
func runAuth(ctx context.Context, mon *monitor.Monitor) int {
	fmt.Println("starting", ghcli.LoginCommand)
	return fail(mon.Client().Login(ctx, os.Stdin, os.Stdout, os.Stderr))
}

// runConfig opens or inspects the generated config file.
func runConfig(path string, rest []string) int {
	action := "edit"
	if len(rest) > 0 {
		action = rest[0]
	}
	switch action {
	case "path":
		fmt.Println(path)
		return 0
	case "show":
		data, err := os.ReadFile(path)
		if err != nil {
			return fail(err)
		}
		_, _ = os.Stdout.Write(data)
		return 0
	case "edit":
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			fmt.Println(path)
			return fail(openBrowser(path))
		}
		cmd := exec.Command(editor, path)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return fail(cmd.Run())
	default:
		fmt.Fprintln(os.Stderr, "usage: codespace-status config [path|show|edit]")
		return 2
	}
}

// openBrowser opens target with the platform handler.
func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", target)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return fmt.Errorf("open %s manually", target)
		}
		cmd = exec.Command("xdg-open", target)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Start()
}

// printJSON writes v as indented JSON.
func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(err)
	}
	return 0
}

// trim shortens s to n characters.
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// fail reports err and maps it to an exit code.
func fail(err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
