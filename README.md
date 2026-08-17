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
[Opus 4.6:concise:high] | ~/dev/my-project
my-repo:main *↑2↓1 | PR#13 ✓ | [abc1234] Last commit message
[⣿⣿⣤⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀] 10% | 900k free | +42/-10 | 1h 30m | $4.50
```

Subscriber (Claude.ai Pro/Max), with [fast mode](https://code.claude.com/docs/en/fast-mode) on:

```
[Opus 4.6:xhigh] | fast | @agent | ~/dev/my-project
my-repo:feature-branch | PR#14 | [def5678] Add new feature
[⣿⣿⣿⣿⣤⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀] 30% | 140k free | +200/-50 | 45m | 5h: 80% (3h) | 7d: 55% (4d)
```

In a git worktree, on a GitLab remote:

```
[Opus 4.6:high] | ~/dev/my-project
my-repo:feature-xyz * | MR!42 draft | [def5678] Add new feature | wt:feature-xyz
[⣿⣿⣤⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀] 10% | 900k free | 12m | $0.80
```

In Docker Sandbox ([sbx](https://docs.docker.com/ai/sandboxes/)):

```
sbx[my-project] | [Opus 4.6] | ~/dev/my-project
my-repo:main | [abc1234] Last commit message
[⣿⣿⣤⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀] 10% | 900k free | +42/-10 | 1h 30m | 5h: 80% (3h) | 7d: 55% (4d)
```

- **Line 1** (session): Sandbox indicator (`sbx[vm-id]` when running inside an agent sandbox, detected via `SANDBOX_VM_ID`/`IS_SANDBOX`), model, output style, reasoning effort, fast mode, agent, working directory (`~` for home, left-truncated when long)
- **Line 2** (git): Repo, branch, dirty/ahead/behind, pull request, commit hash + message, worktree — skipped outside git repos
- **Line 3** (metrics): Context bar, percentage (with `!` warning above 200k tokens), free tokens, lines changed, duration, cost (API) or rate limits with reset countdown (subscribers)

### Model chip

The bracketed chip reads `[model:style:effort]`. The output style is omitted when it is `default`, and the [reasoning effort](https://code.claude.com/docs/en/statusline#available-data) (`low`, `medium`, `high`, `xhigh`, `max`) is omitted for models without an effort parameter — so a plain session shows just `[Opus 4.6]`. Effort tracks mid-session `/effort` changes; ultracode reports as `xhigh`.

A yellow `fast` marker follows the chip while fast mode is enabled.

### Pull requests

When Claude Code finds an open pull request for the current branch, it appears after the branch status, colored by review state:

| Shown | Review state |
| --- | --- |
| `PR#13 ✓` (green) | `approved` |
| `PR#13 ✗` (red) | `changes_requested` |
| `PR#13 draft` (dim) | `draft` |
| `PR#13` (blue) | `pending`, or no review state reported |

On a GitLab remote the same segment describes the branch's open [merge request](https://code.claude.com/docs/en/interactive-mode#gitlab-merge-requests) and uses GitLab's `MR!42` notation.

### Worktrees

Inside a linked git worktree the line ends with `wt:<name>`, using the worktree name reported by Claude Code. Older versions that do not report it fall back to a bare `wt`, detected from the git directory.

## Releasing

Releases are cut by pushing a signed `v*` tag: the [release workflow](.github/workflows/release.yml) verifies the tag signature, builds the binaries, attests provenance and publishes the release with auto-generated notes. Tag creation is deliberately manual — the tag ruleset requires a signature from a maintainer key, and CI/API-created tags are unsigned, so a human signs off on every release.

Everything except the signature is automated by `mise run release`, which:

1. Requires a clean `main` in sync with `origin/main`.
2. Lists the PRs merged since the last `v*` tag with their labels, and computes the next version from those labels (the same labels that group the release notes), highest bump wins:
   - `breaking` → major (minor while still on `0.x`)
   - `feature` / `enhancement` → minor
   - anything else (`bug`, `dependencies`) → patch
3. Proposes the version and waits for confirmation — accept it, type a different `vX.Y.Z`, or abort. Only then does it create the signed annotated tag and push it.

Pass an explicit version to skip the computation: `mise run release -- v1.2.3`.

## License

MIT — see [LICENSE](LICENSE) for details.
