#!/usr/bin/env bash
set -euo pipefail

GO_BIN="${GO:-go}"
OUT="${OUT:-$(pwd)/key-model-router.so}"

cd "$(dirname "$0")"
"$GO_BIN" mod download
CGO_ENABLED=1 "$GO_BIN" build -buildmode=c-shared -trimpath -ldflags="-s -w" -o "$OUT" .
echo "built $OUT"
