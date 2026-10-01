// Command claude-statusline renders a rich statusline for Claude Code.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// JSON input structs matching the Claude Code statusline schema

type Input struct {
	Model         ModelInfo     `json:"model"`
	Workspace     WorkspaceInfo `json:"workspace"`
	Cost          CostInfo      `json:"cost"`
	ContextWindow ContextWindow `json:"context_window"`
	OutputStyle   OutputStyle   `json:"output_style"`
	Agent         AgentInfo     `json:"agent"`
	Effort        EffortInfo    `json:"effort"`
	PR            *PRInfo       `json:"pr"`
	RateLimits    *RateLimits   `json:"rate_limits"`
	PromptCache   *PromptCache  `json:"prompt_cache"`
	Vim           VimInfo       `json:"vim"`
	SessionName   string        `json:"session_name"`
	Exceeds200k   bool          `json:"exceeds_200k_tokens"`
	FastMode      bool          `json:"fast_mode"`
}

// RateLimits holds the subscription windows and, behind a Claude apps gateway,
// the spend limit. Any of them may be absent.
type RateLimits struct {
	FiveHour   *RateWindow `json:"five_hour"`
	SevenDay   *RateWindow `json:"seven_day"`
	SpendLimit *RateWindow `json:"spend_limit"`
}

// PromptCache summarizes the main conversation's prompt cache. ExpiresAt is
// zero (null in the JSON) when the last response reported no cache tokens.
type PromptCache struct {
	Warm            bool  `json:"warm"`
	CachingObserved bool  `json:"caching_observed"`
	ExpiresAt       int64 `json:"expires_at"`
}

// VimInfo carries the vim mode (NORMAL, INSERT, VISUAL, VISUAL LINE), absent
// unless vim mode is enabled.
type VimInfo struct {
	Mode string `json:"mode"`
}

type RateWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

type ModelInfo struct {
	DisplayName string `json:"display_name"`
}

type WorkspaceInfo struct {
	CurrentDir string `json:"current_dir"`
	// GitWorktree is the worktree name, absent in the main working tree.
	GitWorktree string `json:"git_worktree"`
	// Repo is parsed from the origin remote, absent without one.
	Repo RepoInfo `json:"repo"`
}

// RepoInfo identifies the repository, e.g. github.com / anthropics / claude-code.
// For nested GitLab groups Owner is the full namespace path.
type RepoInfo struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// webURL returns the repository's web page, or "" when the identity is
// incomplete.
func (r RepoInfo) webURL() string {
	if r.Host == "" || r.Owner == "" || r.Name == "" {
		return ""
	}
	return "https://" + r.Host + "/" + r.Owner + "/" + r.Name
}

type CostInfo struct {
	TotalCostUSD      float64 `json:"total_cost_usd"`
	TotalDurationMS   int64   `json:"total_duration_ms"`
	TotalLinesAdded   int     `json:"total_lines_added"`
	TotalLinesRemoved int     `json:"total_lines_removed"`
}

type ContextWindow struct {
	ContextWindowSize   int     `json:"context_window_size"`
	UsedPercentage      float64 `json:"used_percentage"`
	RemainingPercentage float64 `json:"remaining_percentage"`
}

type OutputStyle struct {
	Name string `json:"name"`
}

type AgentInfo struct {
	Name string `json:"name"`
}

// EffortInfo carries the reasoning effort level (low, medium, high, xhigh,
// max). Absent when the model does not support the effort parameter.
type EffortInfo struct {
	Level string `json:"level"`
}

// PRInfo describes the open pull request for the current branch, or the open
// merge request when Kind is "mr". Absent when there is none.
type PRInfo struct {
	Number      int    `json:"number"`
	ReviewState string `json:"review_state"`
	Kind        string `json:"kind"`
	URL         string `json:"url"`
}

// Git info collected from parallel commands

type GitInfo struct {
	RepoName      string
	Branch        string
	ShortHash     string
	Ahead         string
	Behind        string
	Dirty         bool
	IsWorktree    bool
	CommitMessage string
}

const (
	reset     = "\033[0m"
	bold      = "\033[1m"
	dim       = "\033[2;37m"
	cyan      = "\033[1;36m"
	cyanDim   = "\033[0;36m"
	green     = "\033[1;32m"
	greenDim  = "\033[0;32m"
	red       = "\033[1;31m"
	redDim    = "\033[0;31m"
	blue      = "\033[1;34m"
	yellow    = "\033[1;33m"
	yellowDim = "\033[0;33m"
	magenta   = "\033[1;35m"
)

func main() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		return
	}

	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		return
	}

	homeDir, _ := os.UserHomeDir()
	gitInfo := collectGitInfo(input.Workspace.CurrentDir)
	// Claude Code captures stdout, so the terminal width only reaches us via
	// COLUMNS. Zero means unknown and falls back to fixed limits.
	cols, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	renderOutput(os.Stdout, input, gitInfo, time.Now(), homeDir, detectSandbox(), cols)
}

// detectSandbox returns a label when running inside an agent sandbox
// (e.g. Docker AI Agent Sandboxes), detected via inherited env vars.
func detectSandbox() string {
	if id := os.Getenv("SANDBOX_VM_ID"); id != "" {
		return "sbx[" + id + "]"
	}
	if os.Getenv("IS_SANDBOX") != "" {
		return "sbx"
	}
	return ""
}

func collectGitInfo(dir string) GitInfo {
	if dir == "" {
		return GitInfo{}
	}

	var info GitInfo
	var mu sync.Mutex
	var wg sync.WaitGroup

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Command 1: git status --porcelain=v2 --branch
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, err := gitCmd(ctx, dir, "status", "--porcelain=v2", "--branch")
		if err != nil {
			return
		}
		branch, hash, ahead, behind, dirty := parseGitStatus(out)
		mu.Lock()
		info.Branch = branch
		info.ShortHash = hash
		info.Ahead = ahead
		info.Behind = behind
		info.Dirty = dirty
		mu.Unlock()
	}()

	// Command 2: git rev-parse --show-toplevel --git-dir
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, err := gitCmd(ctx, dir, "rev-parse", "--show-toplevel", "--git-dir")
		if err != nil {
			return
		}
		lines := strings.SplitN(strings.TrimSpace(out), "\n", 2)
		if len(lines) >= 1 {
			mu.Lock()
			info.RepoName = filepath.Base(lines[0])
			mu.Unlock()
		}
		if len(lines) >= 2 {
			mu.Lock()
			info.IsWorktree = strings.Contains(lines[1], "/worktrees/")
			mu.Unlock()
		}
	}()

	// Command 3: git log -1 --format=%s
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, err := gitCmd(ctx, dir, "log", "-1", "--format=%s")
		if err != nil {
			return
		}
		msg := truncateCommitMessage(strings.TrimSpace(out))
		mu.Lock()
		info.CommitMessage = msg
		mu.Unlock()
	}()

	wg.Wait()
	return info
}

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	// Use --no-optional-locks to avoid creating .git/index.lock, which would
	// conflict with concurrent git operations by Claude Code or editors.
	fullArgs := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...) // #nosec G204 -- fixed binary, args are internal constants
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func parseGitStatus(output string) (branch, shortHash, ahead, behind string, dirty bool) {
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.oid "):
			oid := strings.TrimPrefix(line, "# branch.oid ")
			if isHexString(oid) {
				if len(oid) >= 7 {
					shortHash = oid[:7]
				} else {
					shortHash = oid
				}
			}
		case strings.HasPrefix(line, "# branch.ab "):
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if parts[2] != "+0" {
					ahead = parts[2][1:] // strip the +
				}
				if parts[3] != "-0" {
					behind = parts[3][1:] // strip the -
				}
			}
		case len(line) > 0 && (line[0] == '1' || line[0] == '2' || line[0] == 'u' || line[0] == '?'):
			dirty = true
		}
	}
	return
}

func truncateCommitMessage(msg string) string {
	const maxRunes = 70
	if runes := []rune(msg); len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return msg
}

func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// defaultPathLen caps the directory when the terminal width is unknown.
const defaultPathLen = 50

// shortenPath replaces the home directory with ~ and truncates from the left,
// at a slash boundary where possible, to at most maxLen characters.
func shortenPath(dir, homeDir string, maxLen int) string {
	// Normalize to forward slashes for display (Windows compat)
	dir = filepath.ToSlash(dir)
	homeDir = filepath.ToSlash(homeDir)

	// Replace home directory prefix with ~
	if homeDir != "" && (dir == homeDir || strings.HasPrefix(dir, homeDir+"/")) {
		dir = "~" + dir[len(homeDir):]
	}

	runes := []rune(dir)
	if len(runes) <= maxLen {
		return dir
	}
	// Keep the last maxLen-1 characters, leaving room for the ellipsis.
	cut := string(runes[len(runes)-max(maxLen-1, 1):])
	if i := strings.Index(cut, "/"); i >= 0 {
		return "…" + cut[i:]
	}
	return "…" + cut
}

// truncateRunes shortens s to at most n characters, ellipsis included.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}

// hyperlink wraps text in an OSC 8 link. Terminals without link support show
// the text alone.
func hyperlink(url, text string) string {
	if url == "" || strings.ContainsFunc(url, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return text
	}
	return "\033]8;;" + url + "\033\\" + text + "\033]8;;\033\\"
}

// visibleWidth counts the characters a terminal displays, skipping ANSI color
// (CSI) and OSC hyperlink sequences.
func visibleWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[': // CSI: ends at a final byte in @–~
				j := i + 2
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				i = j + 1
				continue
			case ']': // OSC: ends at ST (ESC \) or BEL
				j := i + 2
				for j < len(s) && s[j] != 0x07 && (s[j] != 0x1b || j+1 >= len(s) || s[j+1] != '\\') {
					j++
				}
				if j < len(s) && s[j] == 0x1b {
					j++
				}
				i = j + 1
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

// widthReserve keeps output clear of the terminal edge and Claude Code's own
// row padding, so width-sized lines don't wrap.
const widthReserve = 4

// budget returns the characters left on a line of width cols after used, or -1
// when the width is unknown.
func budget(cols, used int) int {
	if cols <= 0 {
		return -1
	}
	return cols - widthReserve - used
}

func vimModeColor(mode string) string {
	switch mode {
	case "INSERT":
		return green
	case "NORMAL":
		return blue
	default: // VISUAL, VISUAL LINE
		return magenta
	}
}

func renderOutput(w io.Writer, input Input, git GitInfo, now time.Time, homeDir, sandbox string, cols int) {

	// ── Line 1: sbx[id] | MODE | [Model:style:effort] | fast | @agent | name | dir ──
	var line strings.Builder
	if sandbox != "" {
		line.WriteString(yellow + sandbox + reset + " | ")
	}

	if mode := input.Vim.Mode; mode != "" {
		line.WriteString(vimModeColor(mode) + mode + reset + " | ")
	}

	chip := strings.TrimPrefix(input.Model.DisplayName, "Claude ")
	if input.OutputStyle.Name != "" && input.OutputStyle.Name != "default" {
		chip += ":" + input.OutputStyle.Name
	}
	if input.Effort.Level != "" {
		chip += ":" + input.Effort.Level
	}
	fmt.Fprintf(&line, cyan+"[%s]"+reset, chip)

	if input.FastMode {
		line.WriteString(" | " + yellow + "fast" + reset)
	}

	if input.Agent.Name != "" {
		fmt.Fprintf(&line, " | "+magenta+"@%s"+reset, input.Agent.Name)
	}

	// The session name takes the room the directory leaves at its default
	// length; the directory only shrinks once the name is at its minimum.
	if name := input.SessionName; name != "" {
		const defaultNameLen, minNameLen = 40, 16
		maxLen := defaultNameLen
		if b := budget(cols, visibleWidth(line.String())+len(" | ")); b >= 0 {
			if dir := input.Workspace.CurrentDir; dir != "" {
				b -= len(" | ") + utf8.RuneCountInString(shortenPath(dir, homeDir, defaultPathLen))
			}
			maxLen = max(b, minNameLen)
		}
		line.WriteString(" | " + bold + truncateRunes(name, maxLen) + reset)
	}

	if dir := input.Workspace.CurrentDir; dir != "" {
		maxLen := defaultPathLen
		if b := budget(cols, visibleWidth(line.String())+len(" | ")); b >= 0 {
			// Never squeeze the path below a recognizable tail.
			maxLen = max(b, 12)
		}
		line.WriteString(" | " + shortenPath(dir, homeDir, maxLen))
	}

	fmt.Fprintln(w, line.String())

	// ── Line 2: repo:branch status | PR#n | [hash] message | wt ──
	repoName := input.Workspace.Repo.Name
	if repoName == "" {
		repoName = git.RepoName
	}
	if repoName != "" || git.Branch != "" {
		line.Reset()
		if repoName != "" {
			line.WriteString(green + hyperlink(input.Workspace.Repo.webURL(), repoName) + reset)
		}
		if git.Branch != "" {
			if repoName != "" {
				line.WriteString(":")
			}
			line.WriteString(blue + git.Branch + reset)
		}

		gitStatus := ""
		if git.Dirty {
			gitStatus += "*"
		}
		if git.Ahead != "" {
			gitStatus += "↑" + git.Ahead
		}
		if git.Behind != "" {
			gitStatus += "↓" + git.Behind
		}
		if gitStatus != "" {
			line.WriteString(" " + red + gitStatus + reset)
		}

		renderPR(&line, input.PR)

		// Claude Code reports the worktree name for any linked worktree; the
		// git-dir probe is the fallback for versions that omit the field.
		suffix := ""
		if wt := input.Workspace.GitWorktree; wt != "" {
			suffix = " | " + yellow + "wt:" + wt + reset
		} else if git.IsWorktree {
			suffix = " | " + yellow + "wt" + reset
		}

		if git.ShortHash != "" {
			line.WriteString(" | " + dim + "[" + reset + yellowDim + git.ShortHash + reset + dim + "]" + reset)
			if msg := git.CommitMessage; msg != "" {
				b := budget(cols, visibleWidth(line.String())+visibleWidth(suffix)+1)
				const minMessageLen = 10
				switch {
				case b < 0:
					line.WriteString(" " + msg)
				case b >= minMessageLen:
					line.WriteString(" " + truncateRunes(msg, b))
				}
			}
		}

		line.WriteString(suffix)
		fmt.Fprintln(w, line.String())
	}

	// ── Line 3: [braille bar] pct% | Nk free | +N/-N | Xh Ym | $cost ──
	usagePct := input.ContextWindow.UsedPercentage
	remainPct := input.ContextWindow.RemainingPercentage
	totalTokens := input.ContextWindow.ContextWindowSize
	freeK := float64(totalTokens) * remainPct / 100.0 / 1000.0

	const unitsPerChar = 4
	const barChars = 15
	const totalUnits = barChars * unitsPerChar // 60
	filledUnits := int(usagePct / 100.0 * float64(totalUnits))
	if filledUnits < 0 {
		filledUnits = 0
	} else if filledUnits > totalUnits {
		filledUnits = totalUnits
	}

	fullChars := filledUnits / unitsPerChar
	partialLevel := filledUnits % unitsPerChar
	emptyChars := barChars - fullChars
	if partialLevel > 0 {
		emptyChars--
	}

	// Braille characters filling bottom to top (both columns)
	braillePartial := [5]rune{'⠀', '⣀', '⣤', '⣶', '⣿'}

	fmt.Fprint(w, "[")
	fmt.Fprint(w, cyanDim+strings.Repeat("⣿", fullChars))
	if partialLevel > 0 {
		fmt.Fprintf(w, "%c"+reset, braillePartial[partialLevel])
	} else {
		fmt.Fprint(w, reset)
	}
	fmt.Fprint(w, dim+strings.Repeat("⠀", emptyChars)+reset)
	fmt.Fprint(w, "] ")

	if input.Exceeds200k {
		fmt.Fprintf(w, yellow+"%.0f%%!"+reset, usagePct)
	} else {
		fmt.Fprintf(w, bold+"%.0f%%"+reset, usagePct)
	}

	fmt.Fprintf(w, " | "+green+"%.0fk free"+reset, freeK)

	if input.Cost.TotalLinesAdded > 0 || input.Cost.TotalLinesRemoved > 0 {
		fmt.Fprintf(w, " | "+greenDim+"+%d"+reset+"/"+redDim+"-%d"+reset, input.Cost.TotalLinesAdded, input.Cost.TotalLinesRemoved)
	}

	if input.Cost.TotalDurationMS > 0 {
		d := time.Duration(input.Cost.TotalDurationMS) * time.Millisecond
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if h > 0 {
			fmt.Fprintf(w, " | %dh %dm", h, m)
		} else {
			fmt.Fprintf(w, " | %dm", m)
		}
	}

	renderPromptCache(w, input.PromptCache, now)

	// Subscribers see plan windows instead of an estimated cost. A gateway
	// spend limit alone does not replace the cost.
	rl := input.RateLimits
	subscriber := rl != nil && (rl.FiveHour != nil || rl.SevenDay != nil)
	if !subscriber && input.Cost.TotalCostUSD > 0 {
		fmt.Fprintf(w, " | "+yellowDim+"$%.2f"+reset, input.Cost.TotalCostUSD)
	}
	if rl != nil {
		renderRateLimits(w, rl, now)
	}

	fmt.Fprintln(w)
}

// renderPR appends the open pull request (or GitLab merge request), colored by
// review state.
func renderPR(w io.Writer, pr *PRInfo) {
	if pr == nil || pr.Number == 0 {
		return
	}

	label := fmt.Sprintf("PR#%d", pr.Number)
	if pr.Kind == "mr" {
		// GitLab refers to merge requests as !123.
		label = fmt.Sprintf("MR!%d", pr.Number)
	}

	color, marker := blue, ""
	switch pr.ReviewState {
	case "approved":
		color, marker = green, " ✓"
	case "changes_requested":
		color, marker = red, " ✗"
	case "draft":
		color, marker = dim, " draft"
	}

	fmt.Fprintf(w, " | %s%s%s%s", color, hyperlink(pr.URL, label), marker, reset)
}

// renderPromptCache shows when a warm cache goes cold. The statusline only
// re-runs on events, so it shows the expiry as a clock time rather than a
// countdown that would freeze while the session is idle. Claude Code re-runs
// it at expiry, which flips the segment to cold.
func renderPromptCache(w io.Writer, pc *PromptCache, now time.Time) {
	if pc == nil || !pc.CachingObserved {
		return
	}
	if pc.Warm && pc.ExpiresAt > now.Unix() {
		expires := time.Unix(pc.ExpiresAt, 0)
		color := greenDim
		if expires.Sub(now) <= 5*time.Minute {
			color = yellowDim
		}
		fmt.Fprintf(w, " | %scache→%s%s", color, expires.Local().Format("15:04"), reset)
		return
	}
	fmt.Fprint(w, " | "+dim+"cache cold"+reset)
}

func renderRateLimits(w io.Writer, rl *RateLimits, now time.Time) {
	if rl.FiveHour != nil {
		fmt.Fprintf(w, " | 5h: %s", formatRateWindow(rl.FiveHour, now))
	}
	if rl.SevenDay != nil {
		fmt.Fprintf(w, " | 7d: %s", formatRateWindow(rl.SevenDay, now))
	}
	if rl.SpendLimit != nil {
		fmt.Fprintf(w, " | spend: %s", formatRateWindow(rl.SpendLimit, now))
	}
}

// formatRateWindow shows the remaining percentage and the time to reset. A
// spend limit can run past 100%, which shows as the overrun instead.
func formatRateWindow(rw *RateWindow, now time.Time) string {
	remaining := 100 - rw.UsedPercentage

	var s string
	switch {
	case remaining < 0:
		s = fmt.Sprintf("%s%.0f%% over%s", red, -remaining, reset)
	case remaining <= 10:
		s = fmt.Sprintf("%s%.0f%%%s", red, remaining, reset)
	case remaining <= 30:
		s = fmt.Sprintf("%s%.0f%%%s", yellow, remaining, reset)
	default:
		s = fmt.Sprintf("%s%.0f%%%s", green, remaining, reset)
	}

	if rw.ResetsAt > 0 {
		mins := int(rw.ResetsAt - now.Unix())
		if mins < 0 {
			mins = 0
		}
		mins /= 60
		switch {
		case mins >= 1440:
			s += fmt.Sprintf(" %s(%dd)%s", dim, mins/1440, reset)
		case mins >= 60:
			s += fmt.Sprintf(" %s(%dh)%s", dim, mins/60, reset)
		default:
			s += fmt.Sprintf(" %s(%dm)%s", dim, mins, reset)
		}
	}

	return s
}
