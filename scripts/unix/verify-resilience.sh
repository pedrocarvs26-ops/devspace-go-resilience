#!/usr/bin/env bash
# Run on Linux with Go >= the version declared in go.mod, C compiler for -race,
# and network/module-cache access. Does not start a public tunnel or stress load.
set -euo pipefail
cd "$(dirname "$0")/../.."
command -v go >/dev/null || { echo 'Go is required; see go.mod' >&2; exit 1; }
go version
# Formatting is intentionally explicit, since the delivery sandbox had no Go tools.
find cmd internal -name '*.go' -print0 | xargs -0 gofmt -w
python3 scripts/acceptance/test_harness.py
go test -count=1 ./internal/... ./cmd/devspace
go vet ./internal/... ./cmd/devspace
go test -race -count=1 ./internal/tools ./internal/process ./internal/server
builddir=$(mktemp -d)
trap 'rm -rf "$builddir"' EXIT
for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}; arch=${target#*/}
  echo "Cross-building server: $target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -o "$builddir/devspace-$os-$arch" ./cmd/devspace
done
echo 'Automated checks complete. Real tunnel/load and native Windows limits still require target-host acceptance.'
