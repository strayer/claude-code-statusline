package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func renderTasks(input SubagentInput) []subagentRow {
	var buf bytes.Buffer
	renderSubagents(&buf, input, testNow)
	var rows []subagentRow
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var row subagentRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			panic(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func sampleTask() Task {
	return Task{
		ID:                "task-1",
		AgentType:         "Explore",
		Status:            "running",
		Description:       "Searching for config loaders",
		Label:             "Searching for config loaders",
		StartTime:         testNow.Add(-2*time.Minute - 10*time.Second).UnixMilli(),
		Model:             "claude-sonnet-5-5",
		ContextWindowSize: 200000,
		TokenCount:        12400,
		TokenSamples:      []int{1000, 4000, 8000, 12400},
		Cwd:               "/home/user/project",
	}
}

func TestRenderSubagents(t *testing.T) {
	t.Run("one JSON line per task, keyed by id", func(t *testing.T) {
		a, b := sampleTask(), sampleTask()
		b.ID = "task-2"
		rows := renderTasks(SubagentInput{Tasks: []Task{a, b}})
		if len(rows) != 2 || rows[0].ID != "task-1" || rows[1].ID != "task-2" {
			t.Fatalf("expected two rows for task-1 and task-2, got %+v", rows)
		}
	})

	t.Run("task without id skipped", func(t *testing.T) {
		task := sampleTask()
		task.ID = ""
		if rows := renderTasks(SubagentInput{Tasks: []Task{task}}); len(rows) != 0 {
			t.Errorf("expected no rows, got %+v", rows)
		}
	})

	t.Run("no tasks writes nothing", func(t *testing.T) {
		var buf bytes.Buffer
		renderSubagents(&buf, SubagentInput{}, testNow)
		if buf.Len() != 0 {
			t.Errorf("expected empty output, got %q", buf.String())
		}
	})

	t.Run("decodes the panel input", func(t *testing.T) {
		data := `{"session_id":"abc","cwd":"/home/user/project","columns":120,"tasks":[{"id":"t1","type":"local_agent","agentType":"Explore","status":"running","description":"d","label":"l","startTime":1738399000000,"model":"claude-opus-5-5","effort":"high","contextWindowSize":200000,"tokenCount":5000,"tokenSamples":[1000,5000],"cwd":"/home/user/project"}]}`
		var in SubagentInput
		if err := json.Unmarshal([]byte(data), &in); err != nil {
			t.Fatal(err)
		}
		if in.Columns != 120 || len(in.Tasks) != 1 || in.Tasks[0].Effort != "high" || in.Tasks[0].TokenCount != 5000 {
			t.Errorf("unexpected decode: %+v", in)
		}
	})

	t.Run("numeric effort decodes as a token budget", func(t *testing.T) {
		var task Task
		if err := json.Unmarshal([]byte(`{"id":"t","effort":32000}`), &task); err != nil {
			t.Fatal(err)
		}
		if task.Effort != "32k" {
			t.Errorf("expected 32k, got %q", task.Effort)
		}
	})
}

func TestRenderTask(t *testing.T) {
	row := func(task Task, cols int) string {
		return renderTask(task, "/home/user/project", cols, testNow)
	}

	t.Run("full row", func(t *testing.T) {
		out := row(sampleTask(), 0)
		for _, want := range []string{
			bold + "Explore" + reset,
			"6%",
			dim + "12k" + reset,
			cyan + "Sonnet 5.5" + reset,
			"Searching for config loaders",
			dim + "2m" + reset,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q in row, got: %q", want, out)
			}
		}
		if strings.Contains(out, "wt") {
			t.Errorf("expected no worktree marker in session cwd, got: %q", out)
		}
		if strings.Contains(out, "idle") {
			t.Errorf("expected no idle marker while growing, got: %q", out)
		}
	})

	t.Run("idle marker after flat readings", func(t *testing.T) {
		task := sampleTask()
		task.TokenSamples = []int{12400, 12400, 12400, 12400, 12400}
		if out := row(task, 0); !strings.Contains(out, dim+"12k"+reset+" "+dim+"idle"+reset) {
			t.Errorf("expected idle marker after token count, got: %q", out)
		}
	})

	t.Run("name shown with agent type", func(t *testing.T) {
		task := sampleTask()
		task.Name = "scout"
		if out := row(task, 0); !strings.Contains(out, bold+"scout"+reset+" "+dim+"Explore"+reset) {
			t.Errorf("expected name then dim agent type, got: %q", out)
		}
	})

	t.Run("name equal to agent type not repeated", func(t *testing.T) {
		task := sampleTask()
		task.Name = "Explore"
		if out := row(task, 0); strings.Count(out, "Explore") != 1 {
			t.Errorf("expected a single Explore, got: %q", out)
		}
	})

	t.Run("effort appended to model", func(t *testing.T) {
		task := sampleTask()
		task.Effort = "high"
		if out := row(task, 0); !strings.Contains(out, cyan+"Sonnet 5.5:high"+reset) {
			t.Errorf("expected model:effort, got: %q", out)
		}
	})

	t.Run("status markers", func(t *testing.T) {
		for status, want := range map[string]string{
			"completed": green + "✓" + reset + " ",
			"failed":    red + "✗" + reset + " ",
			"killed":    dim + "✗" + reset + " ",
		} {
			task := sampleTask()
			task.Status = status
			if out := row(task, 0); !strings.HasPrefix(out, want) {
				t.Errorf("%s: expected prefix %q, got: %q", status, want, out)
			}
		}
		if out := row(sampleTask(), 0); !strings.HasPrefix(out, bold) {
			t.Errorf("running: expected no marker, got: %q", out)
		}
	})

	t.Run("context percentage colors", func(t *testing.T) {
		task := sampleTask()
		task.TokenCount = 150000
		if out := row(task, 0); !strings.Contains(out, yellow+"75%"+reset) {
			t.Errorf("expected yellow 75%%, got: %q", out)
		}
		task.TokenCount = 190000
		if out := row(task, 0); !strings.Contains(out, red+"95%"+reset) {
			t.Errorf("expected red 95%%, got: %q", out)
		}
	})

	t.Run("no context window size shows tokens only", func(t *testing.T) {
		task := sampleTask()
		task.ContextWindowSize = 0
		task.Model = ""
		out := row(task, 0)
		if strings.Contains(out, "%") || strings.ContainsAny(out, "⣿⠀") {
			t.Errorf("expected no bar or percentage, got: %q", out)
		}
		if !strings.Contains(out, dim+"12k"+reset) {
			t.Errorf("expected token count, got: %q", out)
		}
	})

	t.Run("worktree marker when cwd differs", func(t *testing.T) {
		task := sampleTask()
		task.Cwd = "/home/user/project/.claude/worktrees/agent-1"
		if out := row(task, 0); !strings.Contains(out, yellow+"wt"+reset) {
			t.Errorf("expected wt marker, got: %q", out)
		}
	})

	t.Run("label preferred over description", func(t *testing.T) {
		task := sampleTask()
		task.Label = "Reading main.go"
		out := row(task, 0)
		if !strings.Contains(out, "Reading main.go") || strings.Contains(out, "Searching") {
			t.Errorf("expected label, got: %q", out)
		}
	})

	t.Run("description fits the columns", func(t *testing.T) {
		task := sampleTask()
		task.Label = strings.Repeat("x", 100)
		out := row(task, 80)
		if w := visibleWidth(out); w > 80 {
			t.Errorf("row width %d exceeds 80 columns: %q", w, out)
		}
		if !strings.Contains(out, "x…") {
			t.Errorf("expected truncated description, got: %q", out)
		}
		if !strings.HasSuffix(out, dim+"2m"+reset) {
			t.Errorf("expected elapsed time kept at the end, got: %q", out)
		}
	})

	t.Run("description dropped when no room", func(t *testing.T) {
		out := row(sampleTask(), 50)
		if strings.Contains(out, "Searching") || strings.Contains(out, "…") {
			t.Errorf("expected description dropped, got: %q", out)
		}
		if !strings.HasSuffix(out, dim+"2m"+reset) {
			t.Errorf("expected elapsed time kept, got: %q", out)
		}
	})

	t.Run("unknown width uses default description length", func(t *testing.T) {
		task := sampleTask()
		task.Label = strings.Repeat("x", 100)
		if out := row(task, 0); !strings.Contains(out, strings.Repeat("x", 59)+"…") {
			t.Errorf("expected 60-column description, got: %q", out)
		}
	})
}

func TestIsIdle(t *testing.T) {
	tests := []struct {
		name    string
		samples []int
		want    bool
	}{
		{"nil", nil, false},
		{"too few samples", []int{500, 500, 500, 500}, false},
		{"flat for the window", []int{500, 500, 500, 500, 500}, true},
		{"growth before the window ignored", []int{100, 300, 500, 500, 500, 500, 500}, true},
		{"growth inside the window", []int{500, 500, 500, 500, 700}, false},
		{"still growing", []int{100, 200, 300, 400, 500}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isIdle(tt.samples); got != tt.want {
				t.Errorf("isIdle(%v) = %v, want %v", tt.samples, got, tt.want)
			}
		})
	}
}

func TestFormatTokens(t *testing.T) {
	tests := map[int]string{0: "0", 850: "850", 1000: "1k", 12400: "12k", 999999: "999k", 1200000: "1.2M"}
	for n, want := range tests {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatElapsed(t *testing.T) {
	tests := map[time.Duration]string{
		-time.Second:                   "0s",
		45 * time.Second:               "45s",
		2*time.Minute + 10*time.Second: "2m",
		65 * time.Minute:               "1h 5m",
	}
	for d, want := range tests {
		if got := formatElapsed(d); got != want {
			t.Errorf("formatElapsed(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestShortModel(t *testing.T) {
	tests := map[string]string{
		"":                           "",
		"claude-opus-5-5":            "Opus 5.5",
		"claude-sonnet-5-5":          "Sonnet 5.5",
		"claude-fable-5-1":           "Fable 5.1",
		"claude-opus-4-6":            "Opus 4.6",
		"claude-opus-4-6[1m]":        "Opus 4.6",
		"claude-sonnet-4-5-20250929": "Sonnet 4.5",
		"claude-3-5-haiku-20241022":  "Haiku 3.5",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0": "Sonnet 4.5",
		"claude-opus-4-1@20250805":                     "Opus 4.1",
		"gpt-4o":                                       "gpt-4o",
		"claude-":                                      "claude-",
	}
	for id, want := range tests {
		if got := shortModel(id); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", id, got, want)
		}
	}
}
