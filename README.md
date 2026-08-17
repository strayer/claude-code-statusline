# Claude Code Statusline

A fast, dependency-free Go binary that renders a rich statusline for [Claude Code](https://docs.anthropic.com/en/docs/claude-code). Reimplemented from the original bash script by [jezweb/claude-skills](https://github.com/jezweb/claude-skills).

## Why?

The original bash implementation spawns ~10 sequential git subprocesses and ~15 jq invocations per render. This Go rewrite:

- **Single JSON parse** instead of many jq calls
- **3 parallel git calls** with 500ms timeout
- **Locale-independent** cost formatting (no `printf "%.2f"` locale bugs)
- **Zero dependencies** — stdlib only

## Installation

Download the latest release for your platform (macOS arm64, Linux arm64/amd64, Windows amd64):

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
curl -fL "https://github.com/strayer/claude-code-statusline/releases/latest/download/claude-statusline-$os-$arch" -o ~/.claude/claude-statusline && chmod +x ~/.claude/claude-statusline
```

On Windows, download `claude-statusline-windows-amd64.exe` from the [releases page](https://github.com/strayer/claude-code-statusline/releases/latest).

Or build from source (requires Go):

```sh
CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o claude-statusline
cp claude-statusline ~/.claude/claude-statusline
```

### Verifying releases

Release binaries are built by GitHub Actions with signed [SLSA build provenance](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations/using-artifact-attestations-to-establish-provenance-for-builds). To verify a download was built from this repository by CI (requires the [GitHub CLI](https://cli.github.com/)):

```sh
gh attestation verify ~/.claude/claude-statusline \
  --repo strayer/claude-code-statusline \
  --signer-workflow strayer/claude-code-statusline/.github/workflows/release.yml
```

The `--signer-workflow` flag ensures the attestation was produced by this repository's release workflow specifically, not just any workflow in the repository.

Each release also includes a `checksums.txt` with the binary's SHA-256 hash.

Then configure in `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/claude-statusline",
    "padding": 0
  }
}
```

## Output

Up to three lines, grouped by concern:

API usage:

```
[Opus 4.6:concise] | ~/dev/my-project
my-repo:main *↑2↓1 | [abc1234] Last commit message
[⣿⣿⣤⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀] 10% | 900k free | +42/-10 | 1h 30m | $4.50
```

Subscriber (Claude.ai Pro/Max):

```
[Opus 4.6] | @agent | ~/dev/my-project
my-repo:feature-branch | [def5678] Add new feature
[⣿⣿⣿⣿⣤⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀] 30% | 140k free | +200/-50 | 45m | 5h: 80% (3h) | 7d: 55% (4d)
```

- **Line 1** (session): Model, output style, agent, working directory (`~` for home, left-truncated when long)
- **Line 2** (git): Repo, branch, dirty/ahead/behind, commit hash + message, worktree — skipped outside git repos
- **Line 3** (metrics): Context bar, percentage (with `!` warning above 200k tokens), free tokens, lines changed, duration, cost (API) or rate limits with reset countdown (subscribers)

## License

MIT — see [LICENSE](LICENSE) for details.
