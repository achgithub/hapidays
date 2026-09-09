#!/usr/bin/env bash
# Cross-compiles pmclone for darwin/linux/windows using Docker (no local Go
# toolchain needed) and copies the binaries into ./dist.
set -euo pipefail
cd "$(dirname "$0")"

docker build --target export -o dist .

echo
echo "Built binaries:"
ls -la dist
echo
echo "On this Mac, run:"
arch=$(uname -m)
case "$arch" in
  arm64) echo "  chmod +x dist/pmclone-darwin-arm64 && ./dist/pmclone-darwin-arm64" ;;
  x86_64) echo "  chmod +x dist/pmclone-darwin-amd64 && ./dist/pmclone-darwin-amd64" ;;
  *) echo "  chmod +x dist/pmclone-darwin-* && ./dist/pmclone-darwin-<arch>" ;;
esac
echo "Then open http://127.0.0.1:8317"
