# codespace-status

View resource usage of your GitHub codespaces over SSH, from a CLI, a web UI
and a VS Code extension that all share one Go backend.

Sampling is done by reading `/proc` inside the codespace through a single long
lived `gh codespace ssh` session per codespace, so there is nothing to install
on the remote side.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/codespace-status` | CLI entry point |
| `internal/metrics` | `/proc` parsing and per-second rate maths |
| `internal/ghcli` | GitHub CLI wrapper: list, SSH sessions, exec, permissions |
| `internal/config` | `codespace-status.generated.json` (git ignored) |
| `internal/monitor` | collectors, rolling 60 sample window, card ordering |
| `internal/server` | web UI server and JSON API, static assets embedded |
| `vscode` | thin VS Code wrapper that packages the binary |

Only the Go standard library and Node built-ins are used. The web UI is plain
HTML, CSS and a single script: no framework, no build step.

## Install

```sh
make build            # ./bin/codespace-status
make test             # unit tests, including a real shell sampling round trip
make vsix VERSION=0.1.0   # builds the extension, downloading vsce on demand
```

`make vsix` copies the shared web assets and the freshly built binary into the
extension and then calls `npx @vscode/vsce`. No packaging dependency is
vendored into the repository, and every artifact it produces is git ignored.

## CLI

```
codespace-status <command> [flags] [args]

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
```

Commands that need the codespace list sync automatically on startup; pass
`--no-sync` to skip that. `exec` runs against the active codespace unless
`-c` overrides it:

```sh
codespace-status select cuddly-robot-x5jx5ww7qj4xcvj47
codespace-status exec uptime
codespace-status exec -c other-codespace -- df -h /
```

### Configuration

State lives in `codespace-status.generated.json` next to the executable and is
excluded from version control. `codespace-status config` opens it in `$EDITOR`,
`config path` prints its location and `config show` dumps it. Set
`CODESPACE_STATUS_CONFIG` to use a different file.

```json
{
  "selected": "cuddly-robot-x5jx5ww7qj4xcvj47",
  "poll_ms": 2000,
  "theme": "light",
  "web_port": 7071,
  "sample_all": true,
  "codespaces": []
}
```

## Web UI

```sh
codespace-status serve      # http://127.0.0.1:7071
codespace-status show       # open it in a browser
```

The page keeps a row of buttons at the top to pick the active codespace
(clicking the active one deselects it), a poll frequency selector, sync, a
**Check permissions** button, a **Start authentication** button and a light or
dark theme toggle. Light is the default.

Every codespace gets a full width card with an identical layout: a status
indicator (active, starting, shutdown, offline), and seven metrics with a 60
sample history each - memory, CPU, network in, network out, disk IO, disk write
and disk read. The active codespace card is always first, the others follow.

The API is small enough to script against:

| Endpoint | Method | Purpose |
| --- | --- | --- |
| `/api/status` | GET | full state, cards already ordered |
| `/api/select?name=` | POST | set the active codespace |
| `/api/deselect` | POST | clear the active codespace |
| `/api/sync` | POST | refresh the codespace list |
| `/api/poll?ms=` | POST | change the sampling interval |
| `/api/theme?value=` | POST | store the theme |
| `/api/permissions` | GET | codespace permission probe |
| `/api/auth` | POST | start the GitHub login flow |

## VS Code extension

The extension reuses the same HTML, CSS and JavaScript as the web UI but talks
to the packaged binary over `codespace-status watch --json`, so it works with
the web server stopped - the two front ends never depend on each other. Inside
VS Code the panel follows the editor theme instead of the light and dark
palettes.

Commands: *Codespace Status: Open*, *Sync Codespaces*, *Select Active
Codespace*, *Check Permissions* and *Start Authentication*. Settings:
`codespaceStatus.binaryPath` and `codespaceStatus.pollMS`.

## Permissions

`codespace-status permissions` (and the web button) reports whether `gh` is
installed, authenticated, which token scopes are present and whether codespaces
are actually listable. If access is missing, run:

```sh
gh auth login --scopes codespace --web
```

`codespace-status auth` starts that flow for you.

## Development

`CODESPACE_STATUS_LOCAL=1` points the collector at the machine it runs on
instead of a codespace, which makes the CLI, the server and the UI testable
without any GitHub round trip:

```sh
make run     # web UI sampling the local machine
CODESPACE_STATUS_LOCAL=1 ./bin/codespace-status status
```

## License

MIT, see [LICENSE](LICENSE).
