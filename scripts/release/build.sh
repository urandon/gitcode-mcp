#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

VERSION="${RELEASE_VERSION:-}"
if [[ -z "$VERSION" ]]; then
  VERSION="$(git describe --tags --exact-match 2>/dev/null || true)"
fi
if [[ -z "$VERSION" ]]; then
  VERSION="0.0.0-dev"
fi

BINARY_VERSION="${VERSION#v}"
COMMIT="${RELEASE_COMMIT:-$(git rev-parse HEAD)}"
DATE="${RELEASE_DATE:-$(git show -s --format=%cI "$COMMIT")}"
DIST_DIR="${DIST_DIR:-$ROOT/dist}"
CHECKSUMS="$DIST_DIR/checksums.txt"
TARGETS="${RELEASE_TARGETS:-darwin/arm64 linux/amd64 linux/arm64 windows/amd64}"
if [[ -n "${GOOS:-}" && -n "${GOARCH:-}" && -z "${RELEASE_TARGETS:-}" ]]; then
  TARGETS="$GOOS/$GOARCH"
fi

mkdir -p "$DIST_DIR"
: > "$CHECKSUMS"

for target in $TARGETS; do
  TARGET_OS="${target%/*}"
  TARGET_ARCH="${target#*/}"
  PACKAGE="gitcode-mcp_${VERSION}_${TARGET_OS}_${TARGET_ARCH}"
  PACKAGE_DIR="$DIST_DIR/$PACKAGE"
  BINARY="gitcode-mcp"
  ARCHIVE="$DIST_DIR/$PACKAGE.tar.gz"

  if [[ "$TARGET_OS" == "windows" ]]; then
    BINARY="gitcode-mcp.exe"
    ARCHIVE="$DIST_DIR/$PACKAGE.zip"
  fi

  rm -rf "$PACKAGE_DIR" "$ARCHIVE"
  mkdir -p "$PACKAGE_DIR"

  CGO_ENABLED=0 GOOS="$TARGET_OS" GOARCH="$TARGET_ARCH" go build \
    -trimpath \
    -ldflags "-s -w -X gitcode-mcp/internal/buildinfo.Version=$BINARY_VERSION -X gitcode-mcp/internal/buildinfo.Commit=$COMMIT -X gitcode-mcp/internal/buildinfo.Date=$DATE" \
    -o "$PACKAGE_DIR/$BINARY" \
    ./cmd/gitcode-mcp

  cat > "$PACKAGE_DIR/README.txt" <<README
gitcode-mcp $VERSION

Install:
  macOS/Linux: install -m 0755 gitcode-mcp /usr/local/bin/gitcode-mcp
  Windows: add gitcode-mcp.exe to a directory on PATH

Verify:
  gitcode-mcp --version
README

  if [[ "$TARGET_OS" == "windows" ]]; then
    (cd "$DIST_DIR" && zip -qr "$(basename "$ARCHIVE")" "$PACKAGE")
  else
    tar -C "$DIST_DIR" -czf "$ARCHIVE" "$PACKAGE"
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$DIST_DIR" && sha256sum "$(basename "$ARCHIVE")" >> "$CHECKSUMS")
  else
    (cd "$DIST_DIR" && shasum -a 256 "$(basename "$ARCHIVE")" >> "$CHECKSUMS")
  fi

  echo "$ARCHIVE"
done
