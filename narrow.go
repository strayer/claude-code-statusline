package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// narrowBelow is the terminal width under which the narrow layout replaces
// the wide one. A phone shows about 50 columns.
const narrowBelow = 60

// narrowSep separates the groups of a narrow line. Within a group, such as
// the model with its effort, a space is enough.
const narrowSep = " " + dim + "|" + reset + " "

// renderNarrow prints the layout for phone-width terminals: where the session
// is, then the model and the gauges. Everything is quiet while it is fine and
// gains detail when it needs attention. The session name is left to Claude
// Code, which shows it above the prompt.
func renderNarrow(w io.Writer, input Input, git GitInfo, now time.Time, sandbox string, cols int) {
	limit := cols - widthReserve

	// ── Line 1: sbx | repo:branch*↑2 | #13✓ | wt ──
	var segs []segment
	if sandbox != "" {
		segs = append(segs, segment{text: yellow + "sbx" + reset, drop: 4})
	}
	repoName := input.Workspace.Repo.Name
	if repoName == "" {
		repoName = git.RepoName
	}
	if repoName != "" || git.Branch != "" {
		segs = append(segs, repoBranchSegment(narrowSep, repoName, input.Workspace.Repo.webURL(), git.Branch, minBranchLen))
		status := ""
		if git.Dirty {
			status += "*"
		}
		if git.Ahead != "" {
			status += "↑" + git.Ahead
		}
		if git.Behind != "" {
			status += "↓" + git.Behind
		}
		if status != "" {
			segs = append(segs, segment{text: red + status + reset})
		}
		segs = append(segs, segment{sep: narrowSep, text: narrowPR(input.PR), drop: 2})
		if input.Workspace.GitWorktree != "" {
			segs = append(segs, segment{sep: narrowSep, text: yellow + "wt" + reset, drop: 5})
		}
	} else if dir := input.Workspace.CurrentDir; dir != "" {
		base := filepath.Base(filepath.ToSlash(dir))
		segs = append(segs, segment{sep: narrowSep, min: min(minTruncated, visibleWidth(base)), fit: func(width int) string {
			if width < min(minTruncated, visibleWidth(base)) {
				return ""
			}
			return truncateWidth(base, width)
		}})
	}
	if line := fitLine(segs, limit); line != "" {
		fmt.Fprintln(w, line)
	}

	// ── Line 2: Model effort fast @agent | ▂14% | ● | 5h 65% | 7d 93% ──
	segs = segs[:0]
	segs = append(segs, segment{text: cyan + strings.TrimPrefix(input.Model.DisplayName, "Claude ") + reset})
	if level := input.Effort.Level; level != "" {
		if level == "medium" {
			level = "med"
		}
		segs = append(segs, segment{sep: " ", text: cyanDim + level + reset, drop: 6})
	}
	if input.FastMode {
		segs = append(segs, segment{sep: " ", text: yellow + "fast" + reset, drop: 10})
	}
	if input.Agent.Name != "" {
		segs = append(segs, segment{sep: " ", text: magenta + "@" + truncateWidth(input.Agent.Name, 12) + reset, drop: 9})
	}
	segs = append(segs, segment{sep: narrowSep, text: narrowContext(input)})
	segs = append(segs, narrowCache(input.PromptCache, now))

	rl := input.RateLimits
	subscriber := rl != nil && (rl.FiveHour != nil || rl.SevenDay != nil)
	if !subscriber && input.Cost.TotalCostUSD > 0 {
		segs = append(segs, segment{sep: narrowSep, text: fmt.Sprintf(yellowDim+"$%.2f"+reset, input.Cost.TotalCostUSD), drop: 4})
	}
	if rl != nil {
		segs = append(segs, narrowRateWindow("5h", rl.FiveHour, now, 7)...)
		segs = append(segs, narrowRateWindow("7d", rl.SevenDay, now, 8)...)
		segs = append(segs, narrowRateWindow("spend", rl.SpendLimit, now, 7)...)
	}
	fmt.Fprintln(w, fitLine(segs, limit))
}

// minBranchLen is the narrowest a branch is shown next to the repository
// name.
const minBranchLen = 6

// repoBranchSegment is the elastic repo:branch. The branch shrinks first, and
// the repository name goes when even a short branch doesn't fit next to it.
// The branch keeps keepBranch columns while other segments can be dropped
// instead.
func repoBranchSegment(sep, repo, repoURL, branch string, keepBranch int) segment {
	color := func(name, c string) string { return c + name + reset }
	link := func(name string) string { return green + hyperlink(repoURL, name) + reset }

	if repo == "" || branch == "" {
		name, render := repo, link
		if repo == "" {
			name, render = branch, func(s string) string { return color(s, blue) }
		}
		need := min(minTruncated, visibleWidth(name))
		return segment{sep: sep, min: min(max(keepBranch, need), visibleWidth(name)), fit: func(width int) string {
			if width < need {
				return ""
			}
			return render(truncateWidth(name, width))
		}}
	}

	repoWidth := visibleWidth(repo) + 1
	shortBranch := min(minBranchLen, visibleWidth(branch))
	return segment{sep: sep, min: repoWidth + min(max(keepBranch, shortBranch), visibleWidth(branch)), fit: func(width int) string {
		switch {
		case width-repoWidth >= shortBranch:
			return link(repo) + ":" + color(truncateWidth(branch, width-repoWidth), blue)
		case width >= min(minTruncated, visibleWidth(branch)):
			return color(truncateWidth(branch, width), blue)
		}
		return ""
	}}
}

// narrowPR is the pull or merge request as a linked #13 with its review mark.
func narrowPR(pr *PRInfo) string {
	if pr == nil || pr.Number == 0 {
		return ""
	}
	label := fmt.Sprintf("#%d", pr.Number)
	if pr.Kind == "mr" {
		label = fmt.Sprintf("!%d", pr.Number)
	}
	color, marker := blue, ""
	switch pr.ReviewState {
	case "approved":
		color, marker = green, "✓"
	case "changes_requested":
		color, marker = red, "✗"
	case "draft":
		color = dim
	}
	return color + hyperlink(pr.URL, label) + marker + reset
}

// tightContext is the used percentage from which the context shows what is
// left instead of what is used.
const tightContext = 80

// narrowContext is a one-cell gauge, then the percentage used, or the tokens
// left once the window is nearly full.
func narrowContext(input Input) string {
	cw := input.ContextWindow
	color := cyanDim
	switch {
	case cw.UsedPercentage >= 90:
		color = red
	case cw.UsedPercentage >= 70:
		color = yellow
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	level := min(max(int(cw.UsedPercentage/100*float64(len(levels))), 0), len(levels)-1)
	gauge := color + string(levels[level]) + reset + " "

	if cw.UsedPercentage >= tightContext {
		freeK := float64(cw.ContextWindowSize) * cw.RemainingPercentage / 100 / 1000
		return gauge + fmt.Sprintf("%s%.0fk free%s", color, freeK, reset)
	}
	if input.Exceeds200k {
		return gauge + fmt.Sprintf(yellow+"%.0f%%!"+reset, cw.UsedPercentage)
	}
	return gauge + fmt.Sprintf(bold+"%.0f%%"+reset, cw.UsedPercentage)
}

// narrowCache is a dot while the prompt cache is warm, the expiry time in its
// last five minutes and "cold" once it is gone. A fresh miss adds a mark
// without its cause.
func narrowCache(pc *PromptCache, now time.Time) segment {
	if pc == nil || !pc.CachingObserved {
		return segment{}
	}
	s := segment{sep: narrowSep, text: greenDim + "●" + reset, drop: 12}
	if !pc.Warm || pc.ExpiresAt <= now.Unix() {
		s.text, s.drop = dim+"cold"+reset, 4
	} else if expires := time.Unix(pc.ExpiresAt, 0); expires.Sub(now) <= 5*time.Minute {
		s.text, s.drop = yellowDim+"→"+expires.Local().Format("15:04")+reset, 4
	}
	if pc.LastMissAt > 0 && now.Sub(time.Unix(pc.LastMissAt, 0)) < recentMiss {
		s.text += yellow + "✗" + reset
	}
	return s
}

// lowRateLimit is the remaining percentage from which a rate window shows its
// reset time and outlasts the other segments.
const lowRateLimit = 30

// narrowRateWindow is a rate window as its label and the remaining
// percentage. A window that is running low adds its reset time, which is
// dropped long before the window itself.
func narrowRateWindow(label string, rw *RateWindow, now time.Time, drop int) []segment {
	if rw == nil {
		return nil
	}
	remaining := 100 - rw.UsedPercentage
	window := segment{sep: narrowSep, text: label + " " + rateRemaining(rw), drop: drop}
	if remaining > lowRateLimit {
		return []segment{window}
	}
	window.drop = 2
	if remaining <= 10 {
		window.drop = 1
	}
	return []segment{window, {text: rateReset(rw, now), drop: 11, attached: true}}
}
