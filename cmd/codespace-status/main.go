// Command codespace-status reports resource usage for GitHub codespaces over
// SSH, through a CLI, a web UI and a VS Code extension sharing one backend.
package main

import (
	"os"

	"github.com/wh-chromium/codespace-status/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args))
}
