package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// narrowBelow is the terminal width under which the narrow layout replaces
// the wide one. A phone shows about 50 columns.
const narrowBelow = 60

// narrowSep separates the groups of a narrow line. Within a group, such as
// the model with its effort, a space is enough.
const narrowSep = " " + dim + "|" + reset + " "

// segment is one piece of a fitted line.
type segment struct {
	// sep is printed before the segment unless it starts the line.
	sep  string
	text string
	// drop is 0 for a segment that always stays. Among the others the
	// highest goes first when the line is too long, and of two
	// with the same value the later one.
	drop int
	// fit makes the segment elastic: it renders into the given width and
	// returns "" when it can't. A line has at most one elastic segment.
	fit func(width int) string
	// min is the width an elastic segment keeps while there are other
	// segments left to drop.
	min int
}

// fitLine joins segs into a line of at most limit columns. It drops segments
// by priority until the rest fits, takes back those dropped ones that fit
// after all, most important first, and gives the elastic segment the columns
// that are left. A line of segments that all stay can still exceed limit.
func fitLine(segs []segment, limit int) string {
	segs = slices.DeleteFunc(slices.Clone(segs), func(s segment) bool {
		return s.text == "" && s.fit == nil
	})
	shown := make([]bool, len(segs))
	for i := range shown {
		shown[i] = true
	}

	// measure returns the width of the shown fixed segments and their
	// separators, and whether the line fits.
	measure := func() (fixed int, fits bool) {
		first, need := true, 0
		for i, s := range segs {
			if !shown[i] {
				continue
			}
			if !first {
				fixed += visibleWidth(s.sep)
			}
			first = false
			if s.fit != nil {
				need = s.min
			} else {
				fixed += visibleWidth(s.text)
			}
		}
		return fixed, limit-fixed >= need
	}

	var dropped []int
	for {
		if _, fits := measure(); fits {
			break
		}
		victim := -1
		for i, s := range segs {
			if shown[i] && s.drop > 0 && (victim < 0 || s.drop >= segs[victim].drop) {
				victim = i
			}
		}
		if victim < 0 {
			break
		}
		shown[victim] = false
		dropped = append(dropped, victim)
	}
	for _, i := range slices.Backward(dropped) {
		shown[i] = true
		if _, fits := measure(); !fits {
			shown[i] = false
		}
	}

	fixed, _ := measure()
	var line strings.Builder
	for i, s := range segs {
		if !shown[i] {
			continue
		}
		text := s.text
		if s.fit != nil {
			text = s.fit(limit - fixed)
		}
		if text == "" {
			continue
		}
		if line.Len() > 0 {
			line.WriteString(s.sep)
		}
		line.WriteString(text)
	}
	return line.String()
}

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
		segs = append(segs, repoBranchSegment(repoName, input.Workspace.Repo.webURL(), git.Branch))
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

// repoBranchSegment is the elastic repo:branch. The branch shrinks first, and
// the repository name goes when even a short branch doesn't fit next to it.
func repoBranchSegment(repo, repoURL, branch string) segment {
	const minBranchLen = 6
	color := func(name, c string) string { return c + name + reset }
	link := func(name string) string { return green + hyperlink(repoURL, name) + reset }

	if repo == "" || branch == "" {
		name, render := repo, link
		if repo == "" {
			name, render = branch, func(s string) string { return color(s, blue) }
		}
		need := min(minTruncated, visibleWidth(name))
		return segment{sep: narrowSep, min: need, fit: func(width int) string {
			if width < need {
				return ""
			}
			return render(truncateWidth(name, width))
		}}
	}

	repoWidth := visibleWidth(repo) + 1
	shortBranch := min(minBranchLen, visibleWidth(branch))
	return segment{sep: narrowSep, min: repoWidth + shortBranch, fit: func(width int) string {
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
	return []segment{window, {text: rateReset(rw, now), drop: 11}}
}
