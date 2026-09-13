#!/usr/bin/env bash
# Cross-compiles hapidays for darwin/linux/windows using Docker (no local Go
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
  arm64) echo "  chmod +x dist/hapidays-darwin-arm64 && ./dist/hapidays-darwin-arm64" ;;
  x86_64) echo "  chmod +x dist/hapidays-darwin-amd64 && ./dist/hapidays-darwin-amd64" ;;
  *) echo "  chmod +x dist/hapidays-darwin-* && ./dist/hapidays-darwin-<arch>" ;;
esac
echo "Then open http://127.0.0.1:8317"
echo
echo "Options (e.g. to avoid clashing with something else on 8317):"
echo "  --port 9000        listen on a different port"
echo "  --host 0.0.0.0     bind beyond loopback (off by default — this isn't hardened for network exposure)"
echo "  --data-dir <path>  use a specific data directory instead of ./hapidays-data next to the binary"
