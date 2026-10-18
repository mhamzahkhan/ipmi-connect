#!/usr/bin/env bash
set -euo pipefail

export COPYFILE_DISABLE=1

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi
VERSION="${VERSION#v}"

# Determine repo root and output directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_DIR="$REPO_ROOT/dist"

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
)

echo "==> Building ipmi-connect v${VERSION} for Linux and macOS (x86 & ARM)..."

for target in "${TARGETS[@]}"; do
  os="${target%%/*}"
  arch="${target##*/}"
  echo "--> Building ${os}/${arch}..."

  TMP_BUILD="$DIST_DIR/build_${os}_${arch}"
  mkdir -p "$TMP_BUILD"

  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o "$TMP_BUILD/ipmi-connect" \
    "$REPO_ROOT/cmd/ipmi-connect"

  cp "$REPO_ROOT/LICENSE" "$TMP_BUILD/"
  cp "$REPO_ROOT/NOTICE" "$TMP_BUILD/"
  cp "$REPO_ROOT/README.md" "$TMP_BUILD/"

  ARCHIVE_NAME="ipmi-connect_${VERSION}_${os}_${arch}.tar.gz"
  tar -czf "$DIST_DIR/$ARCHIVE_NAME" -C "$TMP_BUILD" ipmi-connect LICENSE NOTICE README.md
  rm -rf "$TMP_BUILD"
  echo "    Created $ARCHIVE_NAME"
done

echo "==> Generating sha256sums.txt..."
(
  cd "$DIST_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum *.tar.gz > sha256sums.txt
  else
    shasum -a 256 *.tar.gz > sha256sums.txt
  fi
)

echo "==> Release assets created in dist/:"
ls -lh "$DIST_DIR"
