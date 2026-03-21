package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseGitStatus(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		branch    string
		shortHash string
		ahead     string
		behind    string
		dirty     bool
	}{
		{
			name: "clean repo on main",
			input: `# branch.oid abc1234def5678
# branch.head main
# branch.ab +0 -0`,
			branch:    "main",
			shortHash: "abc1234",
			dirty:     false,
		},
		{
			name: "dirty with modified file",
			input: `# branch.oid abc1234def5678
# branch.head feature
# branch.ab +0 -0
1 .M N... 100644 100644 100644 abc123 def456 file.go`,
			branch:    "feature",
			shortHash: "abc1234",
			dirty:     true,
		},
		{
			name: "ahead and behind",
			input: `# branch.oid abc1234def5678
# branch.head main
# branch.ab +3 -2`,
			branch:    "main",
			shortHash: "abc1234",
			ahead:     "3",
			behind:    "2",
		},
		{
			name: "only ahead",
			input: `# branch.oid abc1234def5678
# branch.head main
# branch.ab +5 -0`,
			branch:    "main",
			shortHash: "abc1234",
			ahead:     "5",
		},
		{
			name: "untracked file marks dirty",
			input: `# branch.oid abc1234def5678
# branch.head main
? newfile.go`,
			branch:    "main",
			shortHash: "abc1234",
			dirty:     true,
		},
		{
			name: "renamed file marks dirty",
			input: `# branch.oid abc1234def5678
# branch.head main
2 R. N... 100644 100644 100644 abc123 def456 R100 new.go	old.go`,
			branch:    "main",
			shortHash: "abc1234",
			dirty:     true,
		},
		{
			name: "unmerged file marks dirty",
			input: `# branch.oid abc1234def5678
# branch.head main
u UU N... 100644 100644 100644 100644 abc123 def456 ghi789 file.go`,
			branch:    "main",
			shortHash: "abc1234",
			dirty:     true,
		},
		{
			name:      "short hex oid kept as-is",
			input:     `# branch.oid abc`,
			shortHash: "abc",
		},
		{
			name:      "initial commit oid is ignored",
			input:     `# branch.oid (initial)`,
			shortHash: "",
		},
		{
			name:      "non-hex oid is ignored",
			input:     `# branch.oid notahash`,
			shortHash: "",
		},
		{
			name:   "no branch.ab line means no ahead/behind",
			input:  `# branch.head main`,
			branch: "main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branch, shortHash, ahead, behind, dirty := parseGitStatus(tt.input)
			if branch != tt.branch {
				t.Errorf("branch = %q, want %q", branch, tt.branch)
			}
			if shortHash != tt.shortHash {
				t.Errorf("shortHash = %q, want %q", shortHash, tt.shortHash)
			}
			if ahead != tt.ahead {
				t.Errorf("ahead = %q, want %q", ahead, tt.ahead)
			}
			if behind != tt.behind {
				t.Errorf("behind = %q, want %q", behind, tt.behind)
			}
			if dirty != tt.dirty {
				t.Errorf("dirty = %v, want %v", dirty, tt.dirty)
			}
		})
	}
}


func defaultInput() Input {
	return Input{
		Model: ModelInfo{DisplayName: "Claude Sonnet 4"},
		ContextWindow: ContextWindow{
			ContextWindowSize:   200000,
			UsedPercentage:      10,
			RemainingPercentage: 90,
		},
	}
}

func render(input Input, git GitInfo) string {
	var buf bytes.Buffer
	renderOutput(&buf, input, git)
	return buf.String()
}

func TestRenderOutput(t *testing.T) {
	t.Run("model line with style", func(t *testing.T) {
		in := defaultInput()
		in.OutputStyle = OutputStyle{Name: "concise"}
		in.ContextWindow.UsedPercentage = 50
		in.ContextWindow.RemainingPercentage = 50
		out := render(in, GitInfo{})

		if !strings.Contains(out, "Sonnet 4:concise") {
			t.Errorf("expected model:style in output, got: %q", out)
		}
	})

	t.Run("default style omitted from model line", func(t *testing.T) {
		in := defaultInput()
		in.OutputStyle = OutputStyle{Name: "default"}
		out := render(in, GitInfo{})

		if strings.Contains(out, ":default") {
			t.Errorf("default style should not appear in output, got: %q", out)
		}
		if !strings.Contains(out, "[Sonnet 4]") {
			t.Errorf("expected model without style suffix, got: %q", out)
		}
	})

	t.Run("git line with dirty and ahead", func(t *testing.T) {
		out := render(defaultInput(), GitInfo{
			RepoName:  "myrepo",
			Branch:    "main",
			ShortHash: "abc1234",
			Ahead:     "2",
			Dirty:     true,
		})

		if !strings.Contains(out, "myrepo") {
			t.Error("expected repo name in output")
		}
		if !strings.Contains(out, "main") {
			t.Error("expected branch in output")
		}
		if !strings.Contains(out, "*") {
			t.Error("expected dirty marker in output")
		}
		if !strings.Contains(out, "↑2") {
			t.Error("expected ahead indicator in output")
		}
	})

	t.Run("no git line when repo empty", func(t *testing.T) {
		out := render(defaultInput(), GitInfo{})

		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != 2 {
			t.Errorf("expected 2 lines without git info, got %d", len(lines))
		}
	})

	t.Run("git line shown without repo name", func(t *testing.T) {
		out := render(defaultInput(), GitInfo{
			Branch:    "main",
			ShortHash: "abc1234",
			Dirty:     true,
		})

		if !strings.Contains(out, "main") {
			t.Error("expected branch in output")
		}
		if !strings.Contains(out, "*") {
			t.Error("expected dirty marker in output")
		}
	})

	t.Run("context bar percentage", func(t *testing.T) {
		in := defaultInput()
		in.ContextWindow.UsedPercentage = 75
		in.ContextWindow.RemainingPercentage = 25
		out := render(in, GitInfo{})

		if !strings.Contains(out, "75%") {
			t.Errorf("expected 75%% in output, got: %q", out)
		}
		if !strings.Contains(out, "50k free") {
			t.Errorf("expected 50k free in output, got: %q", out)
		}
	})

	t.Run("exceeds 200k warning", func(t *testing.T) {
		in := defaultInput()
		in.Exceeds200k = true
		in.ContextWindow.UsedPercentage = 95
		in.ContextWindow.RemainingPercentage = 5
		out := render(in, GitInfo{})

		if !strings.Contains(out, "95%!") {
			t.Errorf("expected 95%%! warning in output, got: %q", out)
		}
	})

	t.Run("cost shown when nonzero", func(t *testing.T) {
		in := defaultInput()
		in.Cost.TotalCostUSD = 1.50
		out := render(in, GitInfo{})

		if !strings.Contains(out, "$1.50") {
			t.Errorf("expected $1.50 in output, got: %q", out)
		}
	})

	t.Run("lines added/removed", func(t *testing.T) {
		in := defaultInput()
		in.Cost.TotalLinesAdded = 42
		in.Cost.TotalLinesRemoved = 10
		out := render(in, GitInfo{})

		if !strings.Contains(out, "+42") || !strings.Contains(out, "-10") {
			t.Errorf("expected +42/-10 in output, got: %q", out)
		}
	})

	t.Run("duration hidden when zero", func(t *testing.T) {
		out := render(defaultInput(), GitInfo{})

		if strings.Contains(out, "0h 0m") {
			t.Errorf("expected no duration at zero, got: %q", out)
		}
	})

	t.Run("duration minutes only when under 1h", func(t *testing.T) {
		in := defaultInput()
		in.Cost.TotalDurationMS = 5 * 60 * 1000
		out := render(in, GitInfo{})

		if !strings.Contains(out, "5m") {
			t.Errorf("expected 5m in output, got: %q", out)
		}
		if strings.Contains(out, "0h") {
			t.Errorf("expected no 0h prefix, got: %q", out)
		}
	})

	t.Run("duration hours and minutes", func(t *testing.T) {
		in := defaultInput()
		in.Cost.TotalDurationMS = 90 * 60 * 1000
		out := render(in, GitInfo{})

		if !strings.Contains(out, "1h 30m") {
			t.Errorf("expected 1h 30m in output, got: %q", out)
		}
	})

	t.Run("agent name shown", func(t *testing.T) {
		in := defaultInput()
		in.Agent = AgentInfo{Name: "myagent"}
		out := render(in, GitInfo{})

		if !strings.Contains(out, "@myagent") {
			t.Errorf("expected @myagent in output, got: %q", out)
		}
	})

	t.Run("worktree indicator", func(t *testing.T) {
		out := render(defaultInput(), GitInfo{
			RepoName:   "myrepo",
			Branch:     "main",
			IsWorktree: true,
		})

		if !strings.Contains(out, "wt") {
			t.Errorf("expected wt indicator in output, got: %q", out)
		}
	})

	t.Run("negative percentage does not panic", func(t *testing.T) {
		in := defaultInput()
		in.ContextWindow.UsedPercentage = -10
		in.ContextWindow.RemainingPercentage = 110
		out := render(in, GitInfo{})

		if !strings.Contains(out, "Sonnet 4") {
			t.Errorf("expected model in output, got: %q", out)
		}
	})
}

func TestTruncateCommitMessage(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short message unchanged", "hello", "hello"},
		{"exact 70 runes unchanged", strings.Repeat("a", 70), strings.Repeat("a", 70)},
		{"long ascii truncated to 70 with ellipsis", strings.Repeat("a", 75), strings.Repeat("a", 70) + "…"},
		{"emoji preserved at boundary", strings.Repeat("a", 69) + "🎉", strings.Repeat("a", 69) + "🎉"},
		{"emoji not split", strings.Repeat("a", 70) + "🎉", strings.Repeat("a", 70) + "…"},
		{"multi-byte truncation preserves valid utf8", strings.Repeat("a", 68) + "🎉🎉🎉", strings.Repeat("a", 68) + "🎉🎉" + "…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateCommitMessage(tt.input)
			if got != tt.want {
				t.Errorf("truncateCommitMessage(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
