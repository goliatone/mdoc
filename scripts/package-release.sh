#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
MODULE_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
VERSION=$(tr -d '[:space:]' < "$MODULE_DIR/VERSION")
MODULE_PATH=$(cd "$MODULE_DIR" && go list -m -f '{{.Path}}')
PACKAGE_NAME="mdoc-v${VERSION}-darwin-arm64"
DIST_DIR="$MODULE_DIR/dist"
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/mdoc-release.XXXXXX")
PACKAGE_DIR="$WORK_DIR/$PACKAGE_NAME"

cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM

mkdir -p "$PACKAGE_DIR/bin" "$PACKAGE_DIR/share/mdoc/filters" "$DIST_DIR"

(
  cd "$MODULE_DIR"
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
    -trimpath \
    -buildvcs=false \
    -ldflags "-s -w -X ${MODULE_PATH}/internal/cli.Version=v${VERSION}" \
    -o "$PACKAGE_DIR/bin/mdoc" \
    ./cmd/mdoc
)

cp "$MODULE_DIR/README.md" "$PACKAGE_DIR/README.md"
cp "$MODULE_DIR/RELEASE_NOTES.md" "$PACKAGE_DIR/RELEASE_NOTES.md"
cp "$MODULE_DIR/example.mdoc.yaml" "$PACKAGE_DIR/share/mdoc/mdoc.yaml.example"
cp "$MODULE_DIR/internal/render/filters/title-style.lua" "$PACKAGE_DIR/share/mdoc/filters/title-style.lua"

(
  cd "$PACKAGE_DIR"
  find . -type f ! -name SHA256SUMS -print | LC_ALL=C sort | while IFS= read -r file; do
    shasum -a 256 "$file"
  done > SHA256SUMS
  shasum -a 256 -c SHA256SUMS
)

ARCHIVE="$DIST_DIR/$PACKAGE_NAME.tar.gz"
(
  cd "$WORK_DIR"
  COPYFILE_DISABLE=1 tar -cf - "$PACKAGE_NAME" | gzip -n > "$ARCHIVE"
)

(
  cd "$DIST_DIR"
  shasum -a 256 "$(basename "$ARCHIVE")" > SHA256SUMS
  shasum -a 256 -c SHA256SUMS
)

printf '%s\n' "$ARCHIVE"
