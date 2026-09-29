#!/usr/bin/env bash
# Builds every ARW download into dist/.
#
#   scripts/build-release.sh v1.0.0
#
# Mac:     one universal ARW.app (Apple silicon + Intel) in a zip. On a Mac the
#          two builds are merged with lipo and the app is ad-hoc signed; on
#          other systems each architecture gets its own zip.
# Windows: arw.exe for x64 and ARM64, zipped.
# Linux:   arw for x64 and ARM64, as tar.gz.
# Every package carries QUICKSTART.txt and LICENSE. SHA256SUMS.txt lists all.
set -euo pipefail

VERSION="${1:-dev}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
rm -rf "$DIST" && mkdir -p "$DIST"
cd "$ROOT"

build() { # os arch out
  GOOS="$1" GOARCH="$2" CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$3" .
}

extras() { cp packaging/QUICKSTART.txt LICENSE "$1/"; }

# ---- macOS ------------------------------------------------------------------
mac_app() { # dir binary
  local app="$1/ARW.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  cp "$2" "$app/Contents/MacOS/arw"
  cp packaging/macos/arw.icns "$app/Contents/Resources/arw.icns"
  sed "s/@VERSION@/${VERSION#v}/g" packaging/macos/Info.plist.in > "$app/Contents/Info.plist"
  if command -v codesign >/dev/null; then codesign --force --deep --sign - "$app"; fi
}
build darwin arm64 "$WORK/arw-darwin-arm64"
build darwin amd64 "$WORK/arw-darwin-amd64"
if command -v lipo >/dev/null; then
  d="$WORK/ARW-$VERSION-macOS"; mkdir -p "$d"
  lipo -create -output "$WORK/arw-universal" "$WORK/arw-darwin-arm64" "$WORK/arw-darwin-amd64"
  mac_app "$d" "$WORK/arw-universal"; extras "$d"
  (cd "$d" && zip -qry "$DIST/ARW-$VERSION-macOS.zip" .)
else
  for arch in arm64 amd64; do
    d="$WORK/ARW-$VERSION-macOS-$arch"; mkdir -p "$d"
    mac_app "$d" "$WORK/arw-darwin-$arch"; extras "$d"
    (cd "$d" && zip -qry "$DIST/ARW-$VERSION-macOS-$arch.zip" .)
  done
fi

# ---- Windows ----------------------------------------------------------------
for arch in amd64 arm64; do
  label=x64; [ "$arch" = arm64 ] && label=arm64
  d="$WORK/ARW-$VERSION-windows-$label"; mkdir -p "$d"
  build windows "$arch" "$d/arw.exe"; extras "$d"
  (cd "$d" && zip -qr "$DIST/ARW-$VERSION-windows-$label.zip" .)
done

# ---- Linux ------------------------------------------------------------------
for arch in amd64 arm64; do
  label=x64; [ "$arch" = arm64 ] && label=arm64
  d="$WORK/ARW-$VERSION-linux-$label"; mkdir -p "$d"
  build linux "$arch" "$d/arw"; extras "$d"
  tar -C "$WORK" -czf "$DIST/ARW-$VERSION-linux-$label.tar.gz" "$(basename "$d")"
done

# Checksums go to a temp file first so the list never includes itself.
(cd "$DIST" && { if command -v sha256sum >/dev/null; then sha256sum -- *; else shasum -a 256 -- *; fi; } > "$WORK/sums" && mv "$WORK/sums" SHA256SUMS.txt)
echo "Built into $DIST:"
ls -lh "$DIST"
