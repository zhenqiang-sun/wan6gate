#!/usr/bin/env bash
# Cross-compile wan6gate for aarch64 Linux (router target).
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="build/wan6gate"
mkdir -p build

# Build identity injected via ldflags so the running binary can self-report.
#
# Number comes from `git describe --tags --always --dirty`:
#   - At a tag (e.g. v0.2.2)         → "0.2.2"
#   - N commits past last tag         → "0.2.2-3-gabc1234"
#   - Uncommitted working tree        → suffix "-dirty"
#   - No tag yet (only commits)       → "gabc1234"
#   - No git at all                   → fall back to in-package default
#
# To bump a real release: `git tag v0.2.3 && ./scripts/build.sh` — no code edit.
COMMIT="$(git rev-parse --short=10 HEAD 2>/dev/null || echo dev)"
if [ "$COMMIT" != "dev" ] && ! git diff --quiet HEAD 2>/dev/null; then
    COMMIT="${COMMIT}-dirty"
fi
# Only override Number when git can find a v-prefixed semver tag (current or
# ancestor). Otherwise fall back to the in-package default — better than
# showing a bare SHA on the UI.
NUMBER=""
if RAW="$(git describe --tags --dirty --match 'v[0-9]*' 2>/dev/null)"; then
    case "$RAW" in
        v[0-9]*) NUMBER="${RAW#v}" ;;
    esac
fi
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

LDFLAGS="-s -w \
 -X wan6gate/internal/version.Commit=${COMMIT} \
 -X wan6gate/internal/version.BuildTime=${BUILD_TIME}"
if [ -n "$NUMBER" ]; then
    LDFLAGS="${LDFLAGS} -X wan6gate/internal/version.Number=${NUMBER}"
fi

GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" \
    -o "$OUT" ./cmd/wan6gate

ls -lh "$OUT"
file "$OUT" 2>/dev/null || true
SHA=$(shasum -a 256 "$OUT" | awk '{print $1}')
echo "sha256: $SHA"
echo "version: ${NUMBER:-<package default>}  commit: $COMMIT  built: $BUILD_TIME"
