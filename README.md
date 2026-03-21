# Claude Code Statusline

A fast, dependency-free Go binary that renders a rich statusline for [Claude Code](https://docs.anthropic.com/en/docs/claude-code). Reimplemented from the original bash script by [jezweb/claude-skills](https://github.com/jezweb/claude-skills).

## Why?

The original bash implementation spawns ~10 sequential git subprocesses and ~15 jq invocations per render. This Go rewrite:

- **Single JSON parse** instead of many jq calls
- **3 parallel git calls** with 500ms timeout
- **Locale-independent** cost formatting (no `printf "%.2f"` locale bugs)
- **Zero dependencies** — stdlib only

## Installation

Download the latest release:

```sh
curl -L https://github.com/strayer/claude-code-statusline/releases/latest/download/claude-statusline -o ~/.claude/claude-statusline && chmod +x ~/.claude/claude-statusline
```

Or build from source (requires Go):

```sh
CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o claude-statusline
cp claude-statusline ~/.claude/claude-statusline
```

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
