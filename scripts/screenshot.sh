#!/usr/bin/env bash
# Regenerate the README screenshot. Builds the binary, pipes the example
# payloads below into it the way Claude Code would, and draws the output as a
# terminal window (scripts/ansi2svg.jq).
#
# Each example gets its own throwaway home directory with a real git
# repository in ~/dev/my-project, so branch, status and commit come from git
# as usual. Timestamps in the payloads are relative to now and the time zone
# is UTC, which keeps every rendered value the same from run to run.
#
# Needs go, git, jq, curl, rsvg-convert (librsvg) and oxipng, or optipng in
# its place. The font, Iosevka Term, is downloaded once to
# ${XDG_CACHE_HOME:-~/.cache}/claude-code-statusline and is the only font the
# renderer sees.
#
# Usage: scripts/screenshot.sh [output.png]   (default: docs/screenshot.png)
set -euo pipefail

die() { echo "error: $*" >&2; exit 1; }

FONT_FAMILY="IosevkaTerm Nerd Font Mono"
FONT_URL="https://github.com/ryanoasis/nerd-fonts/raw/v3.4.0/patched-fonts/IosevkaTerm"
FONTS=(
  "IosevkaTermNerdFontMono-Regular.ttf 7ecc07f7af0d9d3829a6d6a1beeeafeda0074332d94ce8ba49c7f675b63b770e"
  "IosevkaTermNerdFontMono-Bold.ttf a4c8f2ae0b9b7d8098f234cef578ca8fed3ded77cbfdad9a3c5a2a4d953e4e4f"
)

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-$root/docs/screenshot.png}"

for tool in go git jq curl rsvg-convert; do
  command -v "$tool" >/dev/null || die "$tool is required"
done
command -v oxipng >/dev/null || command -v optipng >/dev/null || die "oxipng or optipng is required"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

sha256() {
  if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

font_dir="${XDG_CACHE_HOME:-$HOME/.cache}/claude-code-statusline/fonts"
mkdir -p "$font_dir"
for font in "${FONTS[@]}"; do
  read -r file sum <<<"$font"
  if [[ ! -f "$font_dir/$file" || "$(sha256 "$font_dir/$file")" != "$sum" ]]; then
    echo "Downloading $file" >&2
    curl -fsSL "$FONT_URL/$file" -o "$tmp/$file"
    [[ "$(sha256 "$tmp/$file")" == "$sum" ]] || die "checksum mismatch for $file"
    mv "$tmp/$file" "$font_dir/$file"
  fi
done

bin="$tmp/claude-statusline"
(cd "$root" && go build -o "$bin" .)

# ── Example environment ──

now="$(date +%s)"
# The cache segment shows its expiry as a clock time: the next 14:32 UTC,
# skipping one that is close enough to be drawn as about to expire.
cache_expiry=$((now - now % 86400 + 14 * 3600 + 32 * 60))
((cache_expiry - now > 600)) || cache_expiry=$((cache_expiry + 86400))

home="" dir=""

# g <args>: git with a fixed identity and date and no user configuration.
# Together with the SHA-1 object format in project(), that makes the commit
# hashes the same everywhere.
g() {
  env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
    GIT_AUTHOR_NAME=Example GIT_AUTHOR_EMAIL=example@example.com GIT_AUTHOR_DATE=2026-01-01T12:00:00Z \
    GIT_COMMITTER_NAME=Example GIT_COMMITTER_EMAIL=example@example.com GIT_COMMITTER_DATE=2026-01-01T12:00:00Z \
    git "$@"
}

# project <example> <branch> <commit subject>: start the example's home
# directory with a one-commit repository, and point $home and $dir at them
project() {
  home="$tmp/$1"
  dir="$home/dev/my-project"
  mkdir -p "$dir"
  g init -q --object-format=sha1 -b "$2" "$dir"
  g -C "$dir" commit -q --allow-empty -m "$3"
}

# run [args]: the binary as Claude Code starts it, in the example's
# environment. $columns is the terminal width Claude Code reports, empty for
# none.
columns=""
run() {
  env -i PATH="$PATH" HOME="$home" TZ=UTC GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
    COLUMNS="$columns" "$bin" ${1+"$@"}
}

# The binary prints nothing for a payload it can't parse; fail instead of
# rendering a screenshot with a hole in it.
checked() {
  local out
  out="$(cat)"
  [[ -n "$out" ]] || die "no output for example in $home"
  printf '%s\n' "$out"
}

heading() { printf '\033[2;37m--- %s ---\033[0m\n' "$1"; }
statusline() { run | checked; }
# Claude Code lists the main conversation first and puts a bullet before each
# subagent row; both are drawn here, since only the row bodies come from the
# binary.
subagent_panel() {
  printf '\033[97m●\033[0m \033[1mmain\033[0m\n'
  run --subagents | jq -r '"○ " + .content' | checked
}

# ── Examples ──

{
  heading "API usage"
  project api main "Initial commit"
  # Two commits ahead of origin/main and one behind, with an untracked file.
  g -C "$dir" remote add origin https://github.com/acme/my-repo.git
  g -C "$dir" commit -q --allow-empty -m "Bump dependencies"
  g -C "$dir" update-ref refs/remotes/origin/main HEAD
  g -C "$dir" reset -q --hard HEAD~1
  g -C "$dir" branch -q --set-upstream-to=origin/main
  g -C "$dir" commit -q --allow-empty -m "Add retry budget"
  g -C "$dir" commit -q --allow-empty -m "Retry rate-limited requests with backoff"
  touch "$dir/notes.md"
  statusline <<EOF
{
  "model": {"display_name": "Opus 5.5"},
  "output_style": {"name": "concise"},
  "effort": {"level": "high"},
  "workspace": {
    "current_dir": "$dir",
    "repo": {"host": "github.com", "owner": "acme", "name": "my-repo"}
  },
  "pr": {"number": 13, "review_state": "approved", "url": "https://github.com/acme/my-repo/pull/13"},
  "context_window": {"context_window_size": 1000000, "used_percentage": 10, "remaining_percentage": 90},
  "cost": {"total_cost_usd": 4.5, "total_duration_ms": 5400000, "total_lines_added": 42, "total_lines_removed": 10}
}
EOF

  echo
  heading "Subscription"
  project subscription fix-flaky-tests "Wait for the server before the first request"
  statusline <<EOF
{
  "model": {"display_name": "Sonnet 5.5"},
  "effort": {"level": "medium"},
  "session_name": "fix flaky tests",
  "workspace": {
    "current_dir": "$dir",
    "repo": {"host": "github.com", "owner": "acme", "name": "my-repo"}
  },
  "context_window": {"context_window_size": 200000, "used_percentage": 30, "remaining_percentage": 70},
  "cost": {"total_duration_ms": 2700000},
  "prompt_cache": {"warm": true, "caching_observed": true, "expires_at": $cache_expiry, "hit_ratio": 0.91},
  "rate_limits": {
    "five_hour": {"used_percentage": 20, "resets_at": $((now + 3 * 3600 + 1800))},
    "seven_day": {"used_percentage": 78, "resets_at": $((now + 4 * 86400 + 1800))}
  }
}
EOF

  echo
  heading "Narrow terminal, 50 columns"
  project narrow narrow-layout "Add a layout for narrow terminals"
  touch "$dir/notes.md"
  columns=50 statusline <<EOF
{
  "model": {"display_name": "Fable 5.2"},
  "effort": {"level": "medium"},
  "session_name": "responsive statusline",
  "workspace": {
    "current_dir": "$dir",
    "repo": {"host": "github.com", "owner": "acme", "name": "my-repo"}
  },
  "pr": {"number": 14, "review_state": "approved", "url": "https://github.com/acme/my-repo/pull/14"},
  "context_window": {"context_window_size": 1000000, "used_percentage": 42, "remaining_percentage": 58},
  "cost": {"total_duration_ms": 2700000, "total_lines_added": 200, "total_lines_removed": 50},
  "prompt_cache": {"warm": true, "caching_observed": true, "expires_at": $cache_expiry, "hit_ratio": 0.91},
  "rate_limits": {
    "five_hour": {"used_percentage": 35, "resets_at": $((now + 3 * 3600 + 1800))},
    "seven_day": {"used_percentage": 40, "resets_at": $((now + 4 * 86400 + 1800))}
  }
}
EOF

  echo
  heading "Subagent panel"
  project subagents main "Initial commit"
  subagent_panel <<EOF
{
  "columns": 96,
  "cwd": "$dir",
  "tasks": [
    {
      "id": "a1", "agentType": "Explore", "status": "running",
      "label": "Reading renderOutput's cost section", "startTime": $(((now - 90) * 1000)),
      "model": "claude-haiku-5-5", "contextWindowSize": 1000000,
      "tokenCount": 34000, "tokenSamples": [12000, 25000, 34000], "cwd": "$dir"
    },
    {
      "id": "a2", "agentType": "general-purpose", "status": "running",
      "label": "Checking go.mod and release.sh", "startTime": $(((now - 90) * 1000)),
      "model": "claude-haiku-5-5", "contextWindowSize": 1000000,
      "tokenCount": 50000, "tokenSamples": [21000, 38000, 50000],
      "cwd": "$dir/.claude/worktrees/agent-a2"
    },
    {
      "id": "a3", "agentType": "general-purpose", "status": "running",
      "label": "Long stress-test runner", "startTime": $(((now - 150) * 1000)),
      "model": "claude-haiku-5-5", "contextWindowSize": 1000000,
      "tokenCount": 34000, "tokenSamples": [34000, 34000, 34000, 34000, 34000], "cwd": "$dir"
    }
  ]
}
EOF
} >"$tmp/output.ansi"

# ── Rendering ──

jq -nRr --arg font "$FONT_FAMILY" -f "$root/scripts/ansi2svg.jq" <"$tmp/output.ansi" >"$tmp/output.svg"

# Fontconfig is limited to the downloaded font, so a missing glyph shows up as
# a box instead of being taken from whatever the machine has installed.
cat >"$tmp/fonts.conf" <<EOF
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
  <dir>$font_dir</dir>
  <cachedir>$tmp/fontconfig</cachedir>
</fontconfig>
EOF

# Pango on macOS asks Core Text for fonts unless told to use fontconfig.
FONTCONFIG_FILE="$tmp/fonts.conf" PANGOCAIRO_BACKEND=fc \
  rsvg-convert --zoom 2 --output "$output" "$tmp/output.svg"

if command -v oxipng >/dev/null; then
  oxipng --quiet --opt max --strip safe "$output"
else
  optipng -quiet -o7 -strip all "$output"
fi
echo "Wrote $output"
