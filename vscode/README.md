# codespace-status for VS Code

Resource usage for your GitHub codespaces: memory, CPU, network in and out,
disk IO, disk write and disk read, sampled over SSH with the GitHub CLI.

The extension packages the `codespace-status` binary and drives it directly, so
it does not need the standalone web UI to be running.

## Commands

- **Codespace Status: Open** - show the panel
- **Codespace Status: Sync Codespaces**
- **Codespace Status: Select Active Codespace**
- **Codespace Status: Check Permissions**
- **Codespace Status: Start Authentication**

## Settings

- `codespaceStatus.binaryPath` - use a custom binary instead of the packaged one
- `codespaceStatus.pollMS` - sampling interval in milliseconds

Requires the [GitHub CLI](https://cli.github.com) authenticated with the
`codespace` scope.
