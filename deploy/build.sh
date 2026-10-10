#!/usr/bin/env bash
# Builds the Lambda binaries (linux/arm64, provided.al2023) into deploy/dist/.
# Each function's directory holds one executable named "bootstrap", which is
# what the provided runtimes run.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dist="$root/deploy/dist"
rm -rf "$dist"

cd "$root/server"
for fn in ingest-lambda api-lambda; do
  mkdir -p "$dist/$fn"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w" -tags lambda.norpc \
    -o "$dist/$fn/bootstrap" "./cmd/$fn"
  printf '%-14s %s\n' "$fn" "$(du -h "$dist/$fn/bootstrap" | cut -f1)"
done
