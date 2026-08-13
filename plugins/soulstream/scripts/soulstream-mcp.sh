#!/usr/bin/env bash
# Launches the soulstream-mcp stdio server for the Claude Code plugin.
#
# Binary resolution, first hit wins:
#   1. $SOULSTREAM_MCP_BIN            — developer override, used as-is
#   2. soulstream-mcp on PATH         — an install the user manages themselves
#   3. cached copy in the plugin data dir, re-verified against its recorded sha256
#   4. download the release archive matching THIS plugin's version for this
#      OS/arch from GitHub releases, verify against checksums.txt, cache, run.
#
# Any failure exits 1 with the manual install options; a failed download or
# verification never leaves a cached binary behind.
set -euo pipefail

manual_help() {
  cat >&2 <<'EOF'

Manual install options:
  go install github.com/impire-io/soulstream-core/cmd/soulstream-mcp@latest
  # or download a binary from https://github.com/impire-io/soulstream-core/releases
  # or clone the repo and run `make build`
Then either put soulstream-mcp on PATH or set SOULSTREAM_MCP_BIN to its path.
Run /soulstream:setup in Claude Code for a guided setup.
EOF
  exit 1
}

fail() {
  echo "soulstream plugin: $*" >&2
  manual_help
}

# 1. Explicit override.
if [[ -n "${SOULSTREAM_MCP_BIN:-}" ]]; then
  exec "$SOULSTREAM_MCP_BIN" "$@"
fi

# 2. PATH.
if command -v soulstream-mcp >/dev/null 2>&1; then
  exec soulstream-mcp "$@"
fi

# The plugin's own version pins which release to fetch — plugin and binary move as
# a matched pair.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
plugin_root="$(dirname "$script_dir")"
version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$plugin_root/.claude-plugin/plugin.json" | head -1)"
[[ -n "$version" ]] || fail "cannot read plugin version from $plugin_root/.claude-plugin/plugin.json"

data_dir="${CLAUDE_PLUGIN_DATA:-${XDG_DATA_HOME:-$HOME/.local/share}/soulstream-plugin}"
bin_dir="$data_dir/bin/v$version"
cached="$bin_dir/soulstream-mcp"

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    fail "neither shasum nor sha256sum is available to verify downloads"
  fi
}

# 3. Verified cache. A recorded digest that no longer matches (truncated or
# tampered download) forces a fresh fetch instead of running the file.
if [[ -x "$cached" && -f "$cached.sha256" ]]; then
  if [[ "$(sha256 "$cached")" == "$(cat "$cached.sha256")" ]]; then
    exec "$cached" "$@"
  fi
  rm -f "$cached" "$cached.sha256"
fi

# 4. Download the matching release.
case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) fail "unsupported OS $(uname -s) — install the binary manually" ;;
esac
case "$(uname -m)" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64)  arch="amd64" ;;
  *) fail "unsupported architecture $(uname -m) — install the binary manually" ;;
esac

fetch() { # fetch <url> <outfile>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$1" -O "$2"
  else
    fail "neither curl nor wget is available to download the server binary"
  fi
}

archive="soulstream_${version}_${os}_${arch}.tar.gz"
base="https://github.com/impire-io/soulstream-core/releases/download/v$version"

mkdir -p "$data_dir"
tmp="$(mktemp -d "$data_dir/download.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

# gh reuses the credentials that already granted access to this (possibly private)
# repo — anyone who could install the marketplace has them. Anonymous curl/wget
# covers the public-repo case.
fetched=""
if command -v gh >/dev/null 2>&1; then
  if gh release download "v$version" --repo impire-io/soulstream-core \
    --pattern "$archive" --pattern "checksums.txt" --dir "$tmp" 2>/dev/null; then
    fetched="yes"
  fi
fi
if [[ -z "$fetched" ]]; then
  fetch "$base/$archive" "$tmp/$archive" \
    || fail "download failed: $base/$archive (private repo? authenticate the gh CLI and retry)"
  fetch "$base/checksums.txt" "$tmp/checksums.txt" \
    || fail "download failed: $base/checksums.txt"
fi

expected="$(awk -v f="$archive" '$2 == f {print $1}' "$tmp/checksums.txt")"
[[ -n "$expected" ]] || fail "checksums.txt has no entry for $archive"
actual="$(sha256 "$tmp/$archive")"
[[ "$actual" == "$expected" ]] \
  || fail "checksum mismatch for $archive (expected $expected, got $actual) — refusing to install"

tar -xzf "$tmp/$archive" -C "$tmp" soulstream-mcp \
  || fail "could not extract soulstream-mcp from $archive"
chmod +x "$tmp/soulstream-mcp"
sha256 "$tmp/soulstream-mcp" > "$tmp/soulstream-mcp.sha256"

mkdir -p "$bin_dir"
mv "$tmp/soulstream-mcp" "$cached"
mv "$tmp/soulstream-mcp.sha256" "$cached.sha256"

# exec never returns, so the EXIT trap cannot clean up — do it now.
rm -rf "$tmp"
trap - EXIT
exec "$cached" "$@"
