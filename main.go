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
	"strings"
	"sync"
	"time"
)

// JSON input structs matching the Claude Code statusline schema

type Input struct {
	Model         ModelInfo     `json:"model"`
	Workspace     WorkspaceInfo `json:"workspace"`
	Cost          CostInfo      `json:"cost"`
	ContextWindow ContextWindow `json:"context_window"`
	OutputStyle   OutputStyle   `json:"output_style"`
	Agent         AgentInfo     `json:"agent"`
	RateLimits    *RateLimits   `json:"rate_limits"`
	Exceeds200k   bool          `json:"exceeds_200k_tokens"`
}

type RateLimits struct {
	FiveHour *RateWindow `json:"five_hour"`
	SevenDay *RateWindow `json:"seven_day"`
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
	renderOutput(os.Stdout, input, gitInfo, time.Now(), homeDir)
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
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
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
		if strings.HasPrefix(line, "# branch.head ") {
			branch = strings.TrimPrefix(line, "# branch.head ")
		} else if strings.HasPrefix(line, "# branch.oid ") {
			oid := strings.TrimPrefix(line, "# branch.oid ")
			if isHexString(oid) {
				if len(oid) >= 7 {
					shortHash = oid[:7]
				} else {
					shortHash = oid
				}
			}
		} else if strings.HasPrefix(line, "# branch.ab ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				if parts[2] != "+0" {
					ahead = parts[2][1:] // strip the +
				}
				if parts[3] != "-0" {
					behind = parts[3][1:] // strip the -
				}
			}
		} else if len(line) > 0 && (line[0] == '1' || line[0] == '2' || line[0] == 'u' || line[0] == '?') {
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
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func shortenPath(dir, homeDir string) string {
	// Normalize to forward slashes for display (Windows compat)
	dir = filepath.ToSlash(dir)
	homeDir = filepath.ToSlash(homeDir)

	// Replace home directory prefix with ~
	if homeDir != "" && (dir == homeDir || strings.HasPrefix(dir, homeDir+"/")) {
		dir = "~" + dir[len(homeDir):]
	}

	// Truncate from the left if too long
	const maxLen = 50
	if len(dir) > maxLen {
		// Find a slash boundary to cut at
		cut := dir[len(dir)-maxLen:]
		if i := strings.Index(cut, "/"); i >= 0 {
			dir = "…" + cut[i:]
		} else {
			dir = "…" + cut
		}
	}

	return dir
}

func renderOutput(w io.Writer, input Input, git GitInfo, now time.Time, homeDir string) {

	// ── Line 1: [Model:style] | @agent | dir ──
	model := strings.TrimPrefix(input.Model.DisplayName, "Claude ")
	if input.OutputStyle.Name != "" && input.OutputStyle.Name != "default" {
		fmt.Fprintf(w, cyan+"[%s:%s]"+reset, model, input.OutputStyle.Name)
	} else {
		fmt.Fprintf(w, cyan+"[%s]"+reset, model)
	}

	if input.Agent.Name != "" {
		fmt.Fprintf(w, " | "+magenta+"@%s"+reset, input.Agent.Name)
	}

	if dir := input.Workspace.CurrentDir; dir != "" {
		fmt.Fprintf(w, " | %s", shortenPath(dir, homeDir))
	}

	fmt.Fprintln(w)

	// ── Line 2: repo:branch status | [hash] message | wt ──
	if git.RepoName != "" || git.Branch != "" {
		if git.RepoName != "" {
			fmt.Fprint(w, green+git.RepoName+reset)
		}
		if git.Branch != "" {
			if git.RepoName != "" {
				fmt.Fprint(w, ":")
			}
			fmt.Fprintf(w, blue+"%s"+reset, git.Branch)
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
			fmt.Fprint(w, " "+red+gitStatus+reset)
		}

		if git.ShortHash != "" {
			fmt.Fprint(w, " | "+dim+"["+reset+yellowDim+git.ShortHash+reset+dim+"]"+reset)
			if git.CommitMessage != "" {
				fmt.Fprintf(w, " %s", git.CommitMessage)
			}
		}

		if git.IsWorktree {
			fmt.Fprint(w, " | "+yellow+"wt"+reset)
		}

		fmt.Fprintln(w)
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

	if input.RateLimits != nil {
		renderRateLimits(w, input.RateLimits, now)
	} else if input.Cost.TotalCostUSD > 0 {
		fmt.Fprintf(w, " | "+yellowDim+"$%.2f"+reset, input.Cost.TotalCostUSD)
	}

	fmt.Fprintln(w)
}

func renderRateLimits(w io.Writer, rl *RateLimits, now time.Time) {
	if rl.FiveHour != nil {
		fmt.Fprintf(w, " | 5h: %s", formatRateWindow(rl.FiveHour, now))
	}
	if rl.SevenDay != nil {
		fmt.Fprintf(w, " | 7d: %s", formatRateWindow(rl.SevenDay, now))
	}
}

func formatRateWindow(rw *RateWindow, now time.Time) string {
	remaining := 100 - rw.UsedPercentage
	if remaining < 0 {
		remaining = 0
	}

	var color string
	switch {
	case remaining <= 10:
		color = red
	case remaining <= 30:
		color = yellow
	default:
		color = green
	}

	s := fmt.Sprintf("%s%.0f%%%s", color, remaining, reset)

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
