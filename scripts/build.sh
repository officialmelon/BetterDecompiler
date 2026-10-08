#!/usr/bin/env bash
# Cross-compiles release archives for every supported platform into dist/.
# Usage: scripts/build.sh [version]
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
TARGETS=(
  windows/amd64 windows/arm64
  darwin/amd64 darwin/arm64
  linux/amd64 linux/arm64 linux/arm
)

rm -rf dist && mkdir -p dist
for target in "${TARGETS[@]}"; do
  os="${target%/*}"; arch="${target#*/}"
  name="betterdecompiler-${VERSION}-${os}-${arch}"
  stage="dist/${name}"
  mkdir -p "$stage"
  bin="betterdecompiler"; [[ "$os" == windows ]] && bin="betterdecompiler.exe"
  echo "building ${os}/${arch}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM=7 \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "${stage}/${bin}" ./cmd/betterdecompiler
  cp README.md client/betterdecompiler.lua "$stage/"
  if [[ "$os" == windows ]]; then
    (cd dist && zip -qr "${name}.zip" "${name}")
  else
    tar -C dist -czf "dist/${name}.tar.gz" "${name}"
  fi
  rm -rf "$stage"
done
cp client/betterdecompiler.lua dist/betterdecompiler.lua
(cd dist && sha256sum -- * > SHA256SUMS.txt)
ls -lh dist
