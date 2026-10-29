package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// SubagentInput is what Claude Code pipes to the subagentStatusLine command
// on every refresh of the subagent panel: the base hook fields, the usable
// row width and one task per visible row.
type SubagentInput struct {
	Columns int    `json:"columns"`
	Cwd     string `json:"cwd"`
	Tasks   []Task `json:"tasks"`
}

// Task describes one subagent row. Optional fields are omitted from the JSON
// when they have no value.
type Task struct {
	ID                string      `json:"id"`
	Name              string      `json:"name"`
	AgentType         string      `json:"agentType"`
	Status            string      `json:"status"`
	Description       string      `json:"description"`
	Label             string      `json:"label"`
	StartTime         int64       `json:"startTime"` // milliseconds since the epoch
	Model             string      `json:"model"`
	Effort            EffortValue `json:"effort"`
	ContextWindowSize int         `json:"contextWindowSize"`
	TokenCount        int         `json:"tokenCount"`
	TokenSamples      []int       `json:"tokenSamples"`
	Cwd               string      `json:"cwd"`
}

// EffortValue is the configured effort: a level name such as "high" or a
// numeric token budget. Either decodes to its display form.
type EffortValue string

func (e *EffortValue) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*e = EffortValue(s)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*e = EffortValue(formatTokens(int(n)))
	return nil
}

// subagentRow is one line of the command's output: the row to replace and
// its new body.
type subagentRow struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// renderSubagents writes one JSON line per task. Rows Claude Code can't match
// keep their default rendering, so an encoding error only skips that row.
func renderSubagents(w io.Writer, input SubagentInput, now time.Time) {
	enc := json.NewEncoder(w)
	for _, t := range input.Tasks {
		if t.ID == "" {
			continue
		}
		_ = enc.Encode(subagentRow{ID: t.ID, Content: renderTask(t, input.Cwd, input.Columns, now)})
	}
}

// subagentBarChars keeps the row's bar narrower than the main line's.
const subagentBarChars = 10

// renderTask formats a row:
//
//	✗ name agentType [bar] 24% 48k idle Opus 5.5:high wt description 5m
//
// The description takes whatever width the fixed segments leave and is
// dropped when that is too little to read.
func renderTask(t Task, sessionCwd string, cols int, now time.Time) string {
	var line strings.Builder

	switch t.Status {
	case "completed":
		line.WriteString(green + "✓" + reset + " ")
	case "failed":
		line.WriteString(red + "✗" + reset + " ")
	case "killed":
		line.WriteString(dim + "✗" + reset + " ")
	}

	name := t.Name
	if name == "" {
		name = t.AgentType
	}
	if name != "" {
		line.WriteString(bold + name + reset)
	}
	if t.Name != "" && t.AgentType != "" && t.AgentType != t.Name {
		line.WriteString(" " + dim + t.AgentType + reset)
	}

	if t.ContextWindowSize > 0 {
		pct := float64(t.TokenCount) * 100 / float64(t.ContextWindowSize)
		color := bold
		switch {
		case pct >= 90:
			color = red
		case pct >= 70:
			color = yellow
		}
		fmt.Fprintf(&line, " %s %s%.0f%%%s", contextBar(pct, subagentBarChars), color, pct, reset)
	}
	if t.TokenCount > 0 {
		line.WriteString(" " + dim + formatTokens(t.TokenCount) + reset)
	}

	if isIdle(t.TokenSamples) {
		line.WriteString(" " + dim + "idle" + reset)
	}

	if model := shortModel(t.Model); model != "" {
		line.WriteString(" " + cyan + model)
		if t.Effort != "" {
			line.WriteString(":" + string(t.Effort))
		}
		line.WriteString(reset)
	}

	if t.Cwd != "" && sessionCwd != "" && t.Cwd != sessionCwd {
		line.WriteString(" " + yellow + "wt" + reset)
	}

	elapsed := ""
	if t.StartTime > 0 {
		elapsed = " " + dim + formatElapsed(now.Sub(time.UnixMilli(t.StartTime))) + reset
	}

	desc := t.Label
	if desc == "" {
		desc = t.Description
	}
	if desc != "" {
		// Claude Code's own padding sits outside the row body, so the whole
		// width is usable.
		const defaultDescLen, minDescLen = 60, 10
		maxLen := defaultDescLen
		if cols > 0 {
			maxLen = cols - visibleWidth(line.String()) - visibleWidth(elapsed) - 1
		}
		if maxLen >= minDescLen {
			line.WriteString(" " + truncateWidth(desc, maxLen))
		}
	}

	line.WriteString(elapsed)
	return line.String()
}

// idleTicks is how many consecutive unchanged token readings mark a subagent
// as idle. Readings arrive one per panel refresh, so this is a few seconds.
const idleTicks = 5

// isIdle reports whether the token count has stayed flat over the last
// idleTicks readings: the subagent is waiting on a tool, a permission prompt
// or a stalled request rather than producing output.
func isIdle(samples []int) bool {
	if len(samples) < idleTicks {
		return false
	}
	recent := samples[len(samples)-idleTicks:]
	for _, s := range recent[1:] {
		if s != recent[0] {
			return false
		}
	}
	return true
}

// formatTokens renders a token count compactly: 850, 12k, 1.2M.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
	case n >= 1000:
		return strconv.Itoa(n/1000) + "k"
	default:
		return strconv.Itoa(n)
	}
}

// formatElapsed renders a duration the way the main line renders the session
// duration, with seconds while under a minute.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// shortModel turns a model ID into the family and version the main line
// shows, e.g. claude-opus-5-5 → Opus 5.5, us.anthropic.claude-sonnet-4-5-20250929-v1:0
// → Sonnet 4.5, claude-3-5-haiku-20241022 → Haiku 3.5. IDs without a
// recognizable claude- segment are returned as they are.
func shortModel(id string) string {
	if id == "" {
		return ""
	}
	_, rest, ok := strings.Cut(id, "claude-")
	if !ok {
		return id
	}
	// Drop provider and context suffixes: -v1:0, [1m], @20250929.
	if j := strings.IndexAny(rest, "[@:"); j >= 0 {
		rest = rest[:j]
	}

	var family string
	var version []string
	for part := range strings.SplitSeq(rest, "-") {
		switch {
		case part == "":
		case isDigits(part) && len(part) == 8:
			// Release date, not a version component.
		case isDigits(part):
			version = append(version, part)
		case strings.HasPrefix(part, "v") && isDigits(part[1:]):
			// Provider revision such as v1.
		case family == "":
			family = string(unicode.ToUpper(rune(part[0]))) + part[1:]
		}
	}
	if family == "" {
		return id
	}
	if len(version) == 0 {
		return family
	}
	return family + " " + strings.Join(version, ".")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
