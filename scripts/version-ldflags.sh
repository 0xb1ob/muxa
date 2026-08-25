#!/usr/bin/env bash
# stdout: -X main.version=… -X main.commit=… for go build -ldflags
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
version=dev
commit=
if command -v git >/dev/null 2>&1 && git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  commit="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || true)"
  if tag="$(git -C "$ROOT" describe --exact-match --tags HEAD 2>/dev/null)"; then
    version="${tag#v}"
  fi
fi
printf '%s' "-X main.version=${version} -X main.commit=${commit}"
