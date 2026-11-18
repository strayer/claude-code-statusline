// Command claude-statusline renders a rich statusline for Claude Code.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
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
// HitRatio is nil while no input tokens have been counted. LastMissAt is zero
// until the first miss; LastMissCause stays nil when Claude Code couldn't
// identify a cause.
type PromptCache struct {
	Warm            bool       `json:"warm"`
	CachingObserved bool       `json:"caching_observed"`
	ExpiresAt       int64      `json:"expires_at"`
	HitRatio        *float64   `json:"hit_ratio"`
	LastMissAt      int64      `json:"last_miss_at"`
	LastMissCause   *MissCause `json:"last_miss_cause"`
}

// MissCause names what Claude Code identified as the likely cause of a cache
// miss, e.g. tools_changed, system_prompt_changed, ttl_expired_5m.
type MissCause struct {
	Causes []string `json:"causes"`
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

// Git info that Claude Code doesn't report

type GitInfo struct {
	RepoName      string
	Branch        string
	ShortHash     string
	Ahead         string
	Behind        string
	Dirty         bool
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

	if len(os.Args) > 1 && os.Args[1] == "--subagents" {
		var input SubagentInput
		if err := json.Unmarshal(data, &input); err != nil {
			return
		}
		input.sanitize()
		renderSubagents(os.Stdout, input, time.Now())
		return
	}

	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		return
	}

	// git needs the directory as reported; everything else is only displayed.
	dir := input.Workspace.CurrentDir
	input.sanitize()

	homeDir, _ := os.UserHomeDir()
	homeDir = sanitize(homeDir)
	gitInfo := collectGitInfo(dir, input.Workspace.Repo.Name == "")
	// Claude Code captures stdout, so the terminal width only reaches us via
	// COLUMNS. Zero means unknown and falls back to fixed limits.
	cols, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	renderOutput(os.Stdout, input, gitInfo, time.Now(), homeDir, detectSandbox(), cols)
}

// sanitize makes text from outside the program safe to print: control
// characters (Unicode category Cc, which covers ESC and the C1 controls) never
// reach the terminal. Tab, line feed and carriage return become a space, the
// others are dropped. Invalid UTF-8 comes out as U+FFFD.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t', r == '\n', r == '\r':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

// sanitize cleans every string of the payload that is displayed.
func (in *Input) sanitize() {
	fields := []*string{
		&in.Model.DisplayName,
		&in.Workspace.CurrentDir,
		&in.Workspace.GitWorktree,
		&in.Workspace.Repo.Host,
		&in.Workspace.Repo.Owner,
		&in.Workspace.Repo.Name,
		&in.OutputStyle.Name,
		&in.Agent.Name,
		&in.Effort.Level,
		&in.SessionName,
	}
	if pc := in.PromptCache; pc != nil && pc.LastMissCause != nil {
		for i := range pc.LastMissCause.Causes {
			fields = append(fields, &pc.LastMissCause.Causes[i])
		}
	}
	for _, f := range fields {
		*f = sanitize(*f)
	}
}

// detectSandbox returns a label when running inside an agent sandbox
// (e.g. Docker AI Agent Sandboxes), detected via inherited env vars.
func detectSandbox() string {
	if id := os.Getenv("SANDBOX_VM_ID"); id != "" {
		return "sbx[" + sanitize(id) + "]"
	}
	if os.Getenv("IS_SANDBOX") != "" {
		return "sbx"
	}
	return ""
}

// maxSubjectWidth caps the commit subject, in terminal columns.
const maxSubjectWidth = 70

// collectGitInfo gathers what Claude Code doesn't report about the repository.
// Every render pays for it, so the two git processes it needs run concurrently
// under one deadline and the repository name is read from the directory
// layout. needRepoName is false when Claude Code reported the name.
func collectGitInfo(dir string, needRepoName bool) GitInfo {
	if dir == "" {
		return GitInfo{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	// On a branch without commits git log fails and there is no subject.
	subject := make(chan string, 1)
	go func() {
		out, _ := gitCmd(ctx, dir, "log", "-1", "--format=%s")
		subject <- truncateWidth(strings.TrimSpace(sanitize(out)), maxSubjectWidth)
	}()

	var info GitInfo
	out, err := gitCmd(ctx, dir, "status", "--porcelain=v2", "--branch")
	if err != nil {
		return info
	}
	info.Branch, info.ShortHash, info.Ahead, info.Behind, info.Dirty = parseGitStatus(out)
	info.Branch = sanitize(info.Branch)

	if needRepoName {
		info.RepoName = sanitize(repoNameFromDir(dir))
	}
	if info.ShortHash != "" {
		info.CommitMessage = <-subject
	}
	return info
}

// repoNameFromDir names the repository after the directory that holds .git,
// searching upwards from dir. It stands in for the origin remote's name in
// repositories that have no such remote.
func repoNameFromDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Base(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

const (
	// gitTimeout bounds all git calls of a render together.
	gitTimeout = 500 * time.Millisecond
	// gitWaitDelay is how long a git process that has exited or been killed
	// may keep its output pipe open through a descendant, such as a
	// filesystem monitor it started, before the pipe is abandoned.
	gitWaitDelay = 50 * time.Millisecond
)

func gitCmd(ctx context.Context, dir string, args ...string) (string, error) {
	// Use --no-optional-locks to avoid creating .git/index.lock, which would
	// conflict with concurrent git operations by Claude Code or editors.
	fullArgs := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...) // #nosec G204 -- fixed binary, args are internal constants
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	cmd.WaitDelay = gitWaitDelay
	// ErrWaitDelay means git itself succeeded and its output is complete; only
	// a descendant was still holding the pipe.
	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
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

	if visibleWidth(dir) <= maxLen {
		return dir
	}
	// Keep the tail that fits in maxLen-1 columns, leaving room for the
	// ellipsis.
	room := max(maxLen-1, 1)
	start := len(dir)
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(dir[:start])
		w := runeWidth(r)
		if w > room {
			break
		}
		room -= w
		start -= size
	}
	cut := dir[start:]
	if i := strings.Index(cut, "/"); i >= 0 {
		return "…" + cut[i:]
	}
	return "…" + cut
}

// truncateWidth shortens s to at most n terminal columns, ellipsis included.
func truncateWidth(s string, n int) string {
	if visibleWidth(s) <= n {
		return s
	}
	room := max(n-1, 0)
	end := 0
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		w := runeWidth(r)
		if w > room {
			break
		}
		room -= w
		end += size
	}
	return s[:end] + "…"
}

// wideRanges lists the East Asian Wide/Fullwidth blocks and the emoji blocks,
// which terminals draw two columns wide. Approximate on purpose: it covers
// what turns up in session names, paths and commit messages without pulling
// in a full Unicode width table.
var wideRanges = [][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo
	{0x231A, 0x231B},   // watch, hourglass
	{0x23E9, 0x23F3},   // media control emoji
	{0x25FD, 0x25FE},   // small squares
	{0x2614, 0x2615},   // umbrella, hot beverage
	{0x2648, 0x2653},   // zodiac
	{0x26A1, 0x26A1},   // high voltage
	{0x26AA, 0x26AB},   // circles
	{0x26BD, 0x26BE},   // soccer, baseball
	{0x26C4, 0x26C5},   // snowman, sun behind cloud
	{0x26D4, 0x26D4},   // no entry
	{0x26EA, 0x26EA},   // church
	{0x26F2, 0x26F5},   // fountain … sailboat
	{0x26FA, 0x26FD},   // tent … fuel pump
	{0x2705, 0x2705},   // check mark button
	{0x270A, 0x270B},   // raised fist, hand
	{0x2728, 0x2728},   // sparkles
	{0x274C, 0x274C},   // cross mark
	{0x2753, 0x2757},   // question/exclamation marks
	{0x2795, 0x2797},   // plus, minus, divide
	{0x27B0, 0x27B0},   // curly loop
	{0x2B1B, 0x2B1C},   // large squares
	{0x2B50, 0x2B50},   // star
	{0x2E80, 0x303E},   // CJK radicals, punctuation
	{0x3041, 0x33FF},   // Hiragana, Katakana, CJK compatibility
	{0x3400, 0x4DBF},   // CJK extension A
	{0x4E00, 0x9FFF},   // CJK unified ideographs
	{0xA000, 0xA4CF},   // Yi
	{0xAC00, 0xD7A3},   // Hangul syllables
	{0xF900, 0xFAFF},   // CJK compatibility ideographs
	{0xFE30, 0xFE4F},   // CJK compatibility forms
	{0xFF00, 0xFF60},   // fullwidth forms
	{0xFFE0, 0xFFE6},   // fullwidth signs
	{0x1F004, 0x1F004}, // mahjong
	{0x1F0CF, 0x1F0CF}, // joker
	{0x1F18E, 0x1F18E}, // AB button
	{0x1F191, 0x1F19A}, // squared words
	{0x1F200, 0x1F251}, // enclosed ideographic supplement
	{0x1F300, 0x1F64F}, // pictographs, emoticons
	{0x1F680, 0x1F6FF}, // transport and map
	{0x1F7E0, 0x1F7EB}, // colored circles and squares
	{0x1F90C, 0x1F9FF}, // supplemental symbols and pictographs
	{0x1FA70, 0x1FAFF}, // symbols and pictographs extended-A
	{0x20000, 0x3FFFD}, // CJK extensions B and beyond
}

// runeWidth returns the terminal columns r occupies: 0 for combining and
// format characters (accents, zero-width joiner, variation selectors), 2 for
// wide characters, 1 otherwise.
func runeWidth(r rune) int {
	if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
		return 0
	}
	for _, rg := range wideRanges {
		if r < rg[0] {
			break
		}
		if r <= rg[1] {
			return 2
		}
	}
	return 1
}

// hyperlink wraps text in an OSC 8 link. Terminals without link support show
// the text alone. The sequences end in BEL rather than ST (ESC \\): Claude Code
// re-renders the statusline and drops ST-terminated links.
func hyperlink(url, text string) string {
	if url == "" || strings.ContainsFunc(url, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return text
	}
	return "\033]8;;" + url + "\a" + text + "\033]8;;\a"
}

// visibleWidth counts the terminal columns s occupies, skipping ANSI color
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
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n += runeWidth(r)
	}
	return n
}

// widthReserve keeps output clear of the terminal edge and Claude Code's own
// row padding, so width-sized lines don't wrap.
const widthReserve = 4

// wideSep separates the segments of the wide layout.
const wideSep = " | "

// sepWidth is the width of wideSep.
const sepWidth = len(wideSep)

const (
	// defaultNameLen caps the session name when the terminal width is unknown.
	defaultNameLen = 40
	// minNameLen and minPathLen are what the session name and the directory
	// keep while the other one shrinks.
	minNameLen = 16
	minPathLen = 12
	// minTruncated is the narrowest a truncated text is still worth showing.
	minTruncated = 4
)

// nameAndPathWidths sizes the session name and the directory for a line with
// free columns left. The name takes the room the directory leaves at its
// default length, and the directory only shrinks once the name is at its
// minimum. When both minimums don't fit, the directory goes and the name gets
// the line. Zero means the field is not shown.
func nameAndPathWidths(free int, name, path string) (nameLen, pathLen int) {
	if path != "" {
		forPath := free - sepWidth
		if name != "" {
			forPath -= sepWidth + min(minNameLen, visibleWidth(name))
		}
		if forPath < min(minPathLen, visibleWidth(path)) {
			path = ""
		}
	}
	if name != "" {
		nameLen = free - sepWidth
		if path != "" {
			nameLen = max(nameLen-sepWidth-visibleWidth(path), minNameLen)
		}
		if nameLen < min(minTruncated, visibleWidth(name)) {
			nameLen = 0
		} else {
			free -= sepWidth + min(nameLen, visibleWidth(name))
		}
	}
	if path != "" {
		pathLen = free - sepWidth
	}
	return nameLen, pathLen
}

func renderOutput(w io.Writer, input Input, git GitInfo, now time.Time, homeDir, sandbox string, cols int) {
	if cols > 0 && cols < narrowBelow {
		renderNarrow(w, input, git, now, sandbox, cols)
		return
	}

	// Without a known width nothing is dropped or shortened to fit.
	limit := math.MaxInt32
	if cols > 0 {
		limit = cols - widthReserve
	}

	// ── Line 1: sbx[id] | [Model:style:effort] | fast | @agent | name | dir ──
	var segs []segment
	if sandbox != "" {
		segs = append(segs, segment{text: yellow + sandbox + reset, drop: 1})
	}
	chip := strings.TrimPrefix(input.Model.DisplayName, "Claude ")
	if input.OutputStyle.Name != "" && input.OutputStyle.Name != "default" {
		chip += ":" + input.OutputStyle.Name
	}
	if input.Effort.Level != "" {
		chip += ":" + input.Effort.Level
	}
	segs = append(segs, segment{sep: wideSep, text: cyan + "[" + chip + "]" + reset})
	if input.FastMode {
		segs = append(segs, segment{sep: wideSep, text: yellow + "fast" + reset, drop: 3})
	}
	if input.Agent.Name != "" {
		segs = append(segs, segment{sep: wideSep, text: magenta + "@" + input.Agent.Name + reset, drop: 2})
	}
	var line strings.Builder
	line.WriteString(fitLine(segs, limit))

	// The session name and the directory share what the segments before
	// them leave.
	name, dir := input.SessionName, input.Workspace.CurrentDir
	nameLen, pathLen := defaultNameLen, defaultPathLen
	if cols > 0 {
		path := ""
		if dir != "" {
			path = shortenPath(dir, homeDir, defaultPathLen)
		}
		nameLen, pathLen = nameAndPathWidths(limit-visibleWidth(line.String()), name, path)
	}
	if name != "" && nameLen > 0 {
		line.WriteString(wideSep + bold + truncateWidth(name, nameLen) + reset)
	}
	if dir != "" && pathLen > 0 {
		line.WriteString(wideSep + shortenPath(dir, homeDir, pathLen))
	}
	fmt.Fprintln(w, line.String())

	// ── Line 2: repo:branch status | PR#n | [hash] message | wt ──
	repoName := input.Workspace.Repo.Name
	if repoName == "" {
		repoName = git.RepoName
	}
	if repoName != "" || git.Branch != "" {
		// A long branch keeps this much while the commit, the worktree and
		// the pull request can go instead.
		const keepBranch = 24
		segs = []segment{repoBranchSegment(wideSep, repoName, input.Workspace.Repo.webURL(), git.Branch, keepBranch)}

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
			segs = append(segs, segment{sep: " ", text: red + gitStatus + reset})
		}
		segs = append(segs, segment{sep: wideSep, text: widePR(input.PR), drop: 1})

		hash := -1
		if git.ShortHash != "" {
			hash = len(segs)
			segs = append(segs, segment{sep: wideSep, text: dim + "[" + reset + yellowDim + git.ShortHash + reset + dim + "]" + reset, drop: 3})
		}
		// Claude Code reports the worktree name for any linked worktree.
		if wt := input.Workspace.GitWorktree; wt != "" {
			segs = append(segs, segment{sep: wideSep, text: yellow + "wt:" + wt + reset, drop: 2})
		}

		// The commit subject takes the room that is left once everything
		// else is placed, and follows the hash.
		const minMessageLen = 10
		if msg := git.CommitMessage; hash >= 0 && msg != "" {
			free := limit - visibleWidth(fitLine(segs, limit)) - 1
			if cols <= 0 {
				segs[hash].text += " " + msg
			} else if free >= minMessageLen {
				segs[hash].text += " " + truncateWidth(msg, free)
			}
		}
		fmt.Fprintln(w, fitLine(segs, limit))
	}

	// ── Line 3: [braille bar] pct% | Nk free | +N/-N | Xh Ym | cache | $cost | limits ──
	usagePct := input.ContextWindow.UsedPercentage
	remainPct := input.ContextWindow.RemainingPercentage
	totalTokens := input.ContextWindow.ContextWindowSize
	freeK := float64(totalTokens) * remainPct / 100.0 / 1000.0

	pct := fmt.Sprintf(bold+"%.0f%%"+reset, usagePct)
	if input.Exceeds200k {
		pct = fmt.Sprintf(yellow+"%.0f%%!"+reset, usagePct)
	}
	segs = []segment{
		{text: contextBar(usagePct, 15) + " " + pct},
		{sep: wideSep, text: fmt.Sprintf(green+"%.0fk free"+reset, freeK), drop: 7},
	}

	if input.Cost.TotalLinesAdded > 0 || input.Cost.TotalLinesRemoved > 0 {
		segs = append(segs, segment{sep: wideSep, drop: 9, text: fmt.Sprintf(greenDim+"+%d"+reset+"/"+redDim+"-%d"+reset, input.Cost.TotalLinesAdded, input.Cost.TotalLinesRemoved)})
	}

	if input.Cost.TotalDurationMS > 0 {
		d := time.Duration(input.Cost.TotalDurationMS) * time.Millisecond
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		text := fmt.Sprintf("%dm", m)
		if h > 0 {
			text = fmt.Sprintf("%dh %dm", h, m)
		}
		segs = append(segs, segment{sep: wideSep, text: text, drop: 8})
	}

	segs = append(segs, promptCacheSegments(input.PromptCache, now)...)

	// Subscribers see plan windows instead of an estimated cost. A gateway
	// spend limit alone does not replace the cost.
	rl := input.RateLimits
	subscriber := rl != nil && (rl.FiveHour != nil || rl.SevenDay != nil)
	if !subscriber && input.Cost.TotalCostUSD > 0 {
		segs = append(segs, segment{sep: wideSep, text: fmt.Sprintf(yellowDim+"$%.2f"+reset, input.Cost.TotalCostUSD), drop: 3})
	}
	if rl != nil {
		segs = append(segs, rateWindowSegments("5h", rl.FiveHour, now)...)
		segs = append(segs, rateWindowSegments("7d", rl.SevenDay, now)...)
		segs = append(segs, rateWindowSegments("spend", rl.SpendLimit, now)...)
	}

	fmt.Fprintln(w, fitLine(segs, limit))
}

// contextBar draws usagePct as a bracketed bar of chars braille cells, each
// filling bottom to top in four steps.
func contextBar(usagePct float64, chars int) string {
	const unitsPerChar = 4
	totalUnits := chars * unitsPerChar
	filledUnits := int(usagePct / 100.0 * float64(totalUnits))
	if filledUnits < 0 {
		filledUnits = 0
	} else if filledUnits > totalUnits {
		filledUnits = totalUnits
	}

	fullChars := filledUnits / unitsPerChar
	partialLevel := filledUnits % unitsPerChar
	emptyChars := chars - fullChars
	if partialLevel > 0 {
		emptyChars--
	}

	// Braille characters filling bottom to top (both columns)
	braillePartial := [5]rune{'⠀', '⣀', '⣤', '⣶', '⣿'}

	var b strings.Builder
	b.WriteString("[" + cyanDim + strings.Repeat("⣿", fullChars))
	if partialLevel > 0 {
		b.WriteRune(braillePartial[partialLevel])
	}
	b.WriteString(reset + dim + strings.Repeat("⠀", emptyChars) + reset + "]")
	return b.String()
}

// widePR is the open pull request (or GitLab merge request), linked and
// colored by review state, or "" when there is none.
func widePR(pr *PRInfo) string {
	if pr == nil || pr.Number == 0 {
		return ""
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

	return color + hyperlink(pr.URL, label) + marker + reset
}

// recentMiss is how long a diagnosed cache miss stays on the statusline. The
// JSON carries only the time of the last miss, not whether the latest request
// was one, so a short window approximates "the last request missed".
const recentMiss = 2 * time.Minute

// promptCacheSegments shows when a warm cache goes cold. The statusline only
// re-runs on events, so it shows the expiry as a clock time rather than a
// countdown that would freeze while the session is idle. Claude Code re-runs
// it at expiry, which flips the segment to cold. The session hit ratio
// follows, and a fresh miss adds its cause.
func promptCacheSegments(pc *PromptCache, now time.Time) []segment {
	if pc == nil || !pc.CachingObserved {
		return nil
	}
	state := dim + "cache cold" + reset
	if pc.Warm && pc.ExpiresAt > now.Unix() {
		expires := time.Unix(pc.ExpiresAt, 0)
		color := greenDim
		if expires.Sub(now) <= 5*time.Minute {
			color = yellowDim
		}
		state = color + "cache→" + expires.Local().Format("15:04") + reset
	}
	segs := []segment{{sep: wideSep, text: state, drop: 4}}

	// The cause is the more useful of the two details, so the ratio goes
	// first.
	var ratio, cause string
	if pc.HitRatio != nil {
		ratio = fmt.Sprintf(" %s%.0f%%%s", dim, *pc.HitRatio*100, reset)
	}
	if pc.LastMissAt > 0 && now.Sub(time.Unix(pc.LastMissAt, 0)) < recentMiss {
		cause = "miss"
		if pc.LastMissCause != nil && len(pc.LastMissCause.Causes) > 0 {
			cause = strings.Join(pc.LastMissCause.Causes, "+")
		}
		cause = fmt.Sprintf(" %s✗%s%s", yellow, cause, reset)
	}
	return append(segs,
		segment{text: ratio, drop: 10, attached: true},
		segment{text: cause, drop: 6, attached: true})
}

// rateWindowSegments is a rate window as "5h: 80% (3h)": the remaining
// percentage, then the time to reset, which is dropped before the window. A
// window that is running low outlasts the others.
func rateWindowSegments(label string, rw *RateWindow, now time.Time) []segment {
	if rw == nil {
		return nil
	}
	drop := 2
	if 100-rw.UsedPercentage <= lowRateLimit {
		drop = 1
	}
	return []segment{
		{sep: wideSep, text: label + ": " + rateRemaining(rw), drop: drop},
		{text: rateReset(rw, now), drop: 5, attached: true},
	}
}

// rateRemaining is the remaining percentage of a rate window, colored by how
// little is left. A spend limit can run past 100%, which shows as the overrun
// instead.
func rateRemaining(rw *RateWindow) string {
	remaining := 100 - rw.UsedPercentage
	switch {
	case remaining < 0:
		return fmt.Sprintf("%s%.0f%% over%s", red, -remaining, reset)
	case remaining <= 10:
		return fmt.Sprintf("%s%.0f%%%s", red, remaining, reset)
	case remaining <= 30:
		return fmt.Sprintf("%s%.0f%%%s", yellow, remaining, reset)
	default:
		return fmt.Sprintf("%s%.0f%%%s", green, remaining, reset)
	}
}

// rateReset is the time until a rate window resets, with a leading space, or
// "" when the reset time is unknown.
func rateReset(rw *RateWindow, now time.Time) string {
	if rw.ResetsAt <= 0 {
		return ""
	}
	mins := max(int(rw.ResetsAt-now.Unix()), 0) / 60
	switch {
	case mins >= 1440:
		return fmt.Sprintf(" %s(%dd)%s", dim, mins/1440, reset)
	case mins >= 60:
		return fmt.Sprintf(" %s(%dh)%s", dim, mins/60, reset)
	default:
		return fmt.Sprintf(" %s(%dm)%s", dim, mins, reset)
	}
}
