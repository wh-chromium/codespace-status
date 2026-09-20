#!/usr/bin/env sh
# Builds the VS Code extension package. The shared web UI assets and the CLI
# binary are copied in first; vsce is fetched on demand with npx and is never
# vendored into the repository.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

version=${VERSION:-dev}
targets=${TARGETS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64"}
host_os=$(go env GOOS)
host_arch=$(go env GOARCH)

echo "copying shared web UI assets"
mkdir -p vscode/media vscode/bin
cp internal/server/static/index.html internal/server/static/style.css internal/server/static/app.js vscode/media/

echo "building codespace-status for $host_os/$host_arch"
ext=""
[ "$host_os" = "windows" ] && ext=".exe"
CGO_ENABLED=0 go build -ldflags "-s -w -X github.com/wh-chromium/codespace-status/internal/cli.Version=$version" \
  -o "vscode/bin/codespace-status$ext" ./cmd/codespace-status

if [ "${SKIP_VSCE:-0}" = "1" ]; then
  echo "skipping vsce packaging (SKIP_VSCE=1)"
  exit 0
fi

echo "packaging with vsce (downloaded on demand)"
cd vscode
npx --yes @vscode/vsce package --no-dependencies --out "../codespace-status-$version.vsix"
echo "wrote codespace-status-$version.vsix"
