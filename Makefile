# codespace-status build entry points. Everything below produces artifacts in
# bin/ or vscode/, all of which are ignored by git.

VERSION ?= dev
LDFLAGS := -s -w -X github.com/wh-chromium/codespace-status/internal/cli.Version=$(VERSION)

.PHONY: build test vet fmt run web vsix clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/codespace-status ./cmd/codespace-status

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal

# Runs the web UI against the local machine, which needs no codespace.
run: build
	CODESPACE_STATUS_LOCAL=1 ./bin/codespace-status serve

web: build
	./bin/codespace-status serve

vsix:
	VERSION=$(VERSION) ./scripts/build-vscode.sh

clean:
	rm -rf bin vscode/bin vscode/media/index.html vscode/media/style.css vscode/media/app.js *.vsix
