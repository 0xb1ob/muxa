#!/usr/bin/env bash
# ~/.muxa is an install cache. A depth-1 fetch after a squash-merge looks
# like unrelated histories; update must reset to origin, not merge --ff-only.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
pass=0
fail=0
tmpdir="$(mktemp -d /tmp/muxa-install-test.XXXXXX)"

cleanup() { rm -rf "$tmpdir"; }
trap cleanup EXIT

ok() { pass=$((pass + 1)); printf 'ok %s %s\n' "$pass" "$1"; }
bad() { fail=$((fail + 1)); printf 'not ok %s %s\n' "$((pass + fail))" "$1"; printf '  %s\n' "$2" >&2; }

git_ident() {
  local dir="$1"
  shift
  git -C "$dir" -c user.email=muxa@example.com -c user.name=muxa -c commit.gpgsign=false "$@"
}

# Minimal muxa tree so is_muxa_tree passes and a post-reset exec of install.sh works.
seed_muxa_tree() {
  local dest="$1"
  mkdir -p "$dest/bin" "$dest/skills/muxa-parent" "$dest/skills/muxa-worker" \
    "$dest/hooks/pi" "$dest/tests"
  cp "$ROOT/install.sh" "$dest/install.sh"
  cp "$ROOT/bin/muxa" "$dest/bin/muxa"
  cp "$ROOT/skills/muxa-parent/SKILL.md" "$dest/skills/muxa-parent/SKILL.md"
  cp "$ROOT/skills/muxa-worker/SKILL.md" "$dest/skills/muxa-worker/SKILL.md"
  cp "$ROOT/hooks/pi/muxa.ts" "$dest/hooks/pi/muxa.ts"
  : >"$dest/tests/run.sh"
  printf 'before-squash\n' >"$dest/MARKER"
  chmod +x "$dest/bin/muxa" "$dest/install.sh" "$dest/tests/run.sh"
}

run_bootstrap() {
  local home="$1" muxa_home="$2" repo="$3"
  mkdir -p "$tmpdir/bootstrap"
  cp "$ROOT/install.sh" "$tmpdir/bootstrap/install.sh"
  # Not a muxa tree, so install.sh takes the curl|bash path (ensure_muxa_home).
  HOME="$home" \
    MUXA_HOME="$muxa_home" \
    MUXA_REPO="$repo" \
    MUXA_REF=main \
    GIT_TERMINAL_PROMPT=0 \
    bash "$tmpdir/bootstrap/install.sh"
}

origin="$tmpdir/origin"
seed_muxa_tree "$origin"
git init -q "$origin"
git -C "$origin" symbolic-ref HEAD refs/heads/main
git -C "$origin" add -A
git_ident "$origin" commit -q -m init
origin_url="file://$origin"

home="$tmpdir/home"
clone="$tmpdir/muxa-home"
git clone --quiet --depth 1 --branch main "$origin_url" "$clone"

# Squash-merge stand-in: origin/main is a new root commit (unrelated to the clone).
git -C "$origin" checkout --orphan squashed >/dev/null 2>&1
printf 'after-squash\n' >"$origin/MARKER"
git -C "$origin" add -A
git_ident "$origin" commit -q -m 'squash-merge stand-in'
git -C "$origin" branch -M main

git -C "$clone" fetch --depth 1 origin main >/dev/null 2>&1
set +e
merge_err="$(git -C "$clone" merge --ff-only origin/main 2>&1)"
merge_rc=$?
set -e
if [ "$merge_rc" -ne 0 ]; then
  ok "depth-1 ff-only merge fails on unrelated squash"
else
  bad "depth-1 ff-only merge fails on unrelated squash" "merge succeeded: $merge_err"
fi
case "$merge_err" in
  *"unrelated histories"*|*"Not possible to fast-forward"*|*"refusing to merge"*|*"cannot fast-forward"*)
    ok "merge error names a non-ff / unrelated-histories failure"
    ;;
  *)
    bad "merge error names a non-ff / unrelated-histories failure" "got: $merge_err"
    ;;
esac

printf 'keep-me\n' >"$clone/untracked-cache"
old_head="$(git -C "$clone" rev-parse HEAD)"
want_head="$(git -C "$origin" rev-parse HEAD)"

set +e
install_out="$(run_bootstrap "$home" "$clone" "$origin_url" 2>&1)"
install_rc=$?
set -e
if [ "$install_rc" -eq 0 ]; then
  ok "diverged shallow clone updates via reset"
else
  bad "diverged shallow clone updates via reset" "exit=$install_rc out=$install_out"
fi

got_head="$(git -C "$clone" rev-parse HEAD)"
[ "$got_head" = "$want_head" ] && ok "reset lands on origin/main" \
  || bad "reset lands on origin/main" "want=$want_head got=$got_head old=$old_head"
[ "$got_head" != "$old_head" ] && ok "HEAD moved off the pre-squash commit" \
  || bad "HEAD moved off the pre-squash commit" "still $old_head"

marker="$(cat "$clone/MARKER" 2>/dev/null || true)"
[ "$marker" = "after-squash" ] && ok "working tree matches the squash commit" \
  || bad "working tree matches the squash commit" "MARKER=$marker"

[ -f "$clone/untracked-cache" ] && ok "diverged muxa tree is reset, not rm -rf" \
  || bad "diverged muxa tree is reset, not rm -rf" "untracked file missing; tree was replaced"

# Refuse to clobber a directory that is not a muxa checkout.
clobber="$tmpdir/not-muxa"
mkdir -p "$clobber"
printf 'secret\n' >"$clobber/secret.txt"
set +e
refuse_out="$(run_bootstrap "$home" "$clobber" "$origin_url" 2>&1)"
refuse_rc=$?
set -e
[ "$refuse_rc" -ne 0 ] && ok "non-muxa MUXA_HOME is refused" \
  || bad "non-muxa MUXA_HOME is refused" "exit=$refuse_rc out=$refuse_out"
case "$refuse_out" in
  *"not a muxa checkout"*) ok "refuse message names a non-muxa checkout" ;;
  *) bad "refuse message names a non-muxa checkout" "got: $refuse_out" ;;
esac
[ -f "$clobber/secret.txt" ] && ok "non-muxa directory is not clobbered" \
  || bad "non-muxa directory is not clobbered" "secret.txt missing"

# Install must never compile Go: the broker ships as a release asset.
if grep -nE '(^|[^_[:alnum:]])(go|\$_go|"\$_go")[[:space:]]+(build|install|mod)([^[:alnum:]]|$)' "$ROOT/install.sh" >/dev/null; then
  bad "install.sh does not compile Go" "$(grep -nE '(build|install|mod)' "$ROOT/install.sh" | grep -i go)"
else
  ok "install.sh does not compile Go"
fi

# The download is fail-closed on a bad checksum, and a broker that cannot be
# fetched must not abort the rest of the install.
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) arch="$(uname -m)" ;;
esac
case "$(uname -s)" in
  Darwin) os=darwin ;;
  *) os=linux ;;
esac
sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

serve="$tmpdir/releases/latest/download"
mkdir -p "$serve"
printf 'not-a-broker\n' >"$serve/muxa-broker-$os-$arch"
printf '0000000000000000000000000000000000000000000000000000000000000000  muxa-broker-%s-%s\n' \
  "$os" "$arch" >"$serve/SHA256SUMS"

# A throwaway tree: install.sh writes bin/muxa-broker into its own checkout.
tree="$tmpdir/tree"
seed_muxa_tree "$tree"
mkdir -p "$tree/broker"

run_install() {
  local home="$1"
  shift
  mkdir -p "$home"
  HOME="$home" MUXA_BIN_DIR="$home/bin" "$@" bash "$tree/install.sh" 2>&1
}

set +e
broker_out="$(run_install "$tmpdir/home-badsum" \
  env MUXA_BROKER_BASE_URL="file://$tmpdir/releases")"
broker_rc=$?
set -e
[ "$broker_rc" -eq 0 ] && ok "install survives a broker it cannot verify" \
  || bad "install survives a broker it cannot verify" "exit=$broker_rc out=$broker_out"
[ ! -e "$tmpdir/home-badsum/bin/muxa-broker" ] && ok "bad checksum installs no broker" \
  || bad "bad checksum installs no broker" "broker was installed anyway"
case "$broker_out" in
  *"checksum mismatch"*) ok "bad checksum is reported" ;;
  *) bad "bad checksum is reported" "out=$broker_out" ;;
esac

# A matching checksum installs the asset next to muxa.
printf '#!/bin/sh\nexit 0\n' >"$serve/muxa-broker-$os-$arch"
printf '%s  muxa-broker-%s-%s\n' \
  "$(sha256 "$serve/muxa-broker-$os-$arch")" "$os" "$arch" >"$serve/SHA256SUMS"
set +e
good_out="$(run_install "$tmpdir/home-goodsum" \
  env MUXA_BROKER_BASE_URL="file://$tmpdir/releases")"
good_rc=$?
set -e
[ "$good_rc" -eq 0 ] && ok "install succeeds with a verified broker" \
  || bad "install succeeds with a verified broker" "exit=$good_rc out=$good_out"
[ -x "$tmpdir/home-goodsum/bin/muxa-broker" ] && ok "verified broker lands next to muxa" \
  || bad "verified broker lands next to muxa" "no $tmpdir/home-goodsum/bin/muxa-broker"

printf '%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
