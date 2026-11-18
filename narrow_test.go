package main

import (
	"strings"
	"testing"
)

// plain strips colors and hyperlinks.
func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += strings.IndexByte(s[i:], 'm') + 1
			continue
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == ']' {
			i += strings.IndexByte(s[i:], '\a') + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func narrowLines(in Input, git GitInfo, cols int) []string {
	return strings.Split(strings.TrimSuffix(plain(renderCols(in, git, cols)), "\n"), "\n")
}

func busyInput() (Input, GitInfo) {
	in := defaultInput()
	in.Model.DisplayName = "Opus 5.5"
	in.Effort.Level = "medium"
	in.FastMode = true
	in.Agent.Name = "reviewer"
	in.SessionName = "a rather long session name that will not fit on a phone"
	in.Workspace.CurrentDir = "/home/user/dev/claude-code-statusline"
	in.Workspace.GitWorktree = "feat"
	in.Workspace.Repo = RepoInfo{Host: "github.com", Owner: "acme", Name: "claude-code-statusline"}
	in.PR = &PRInfo{Number: 44, ReviewState: "approved", URL: "https://github.com/acme/claude-code-statusline/pull/44"}
	in.PromptCache = &PromptCache{Warm: true, CachingObserved: true, ExpiresAt: testNow.Unix() + 3000}
	in.RateLimits = &RateLimits{
		FiveHour: &RateWindow{UsedPercentage: 35, ResetsAt: testNow.Unix() + 7200},
		SevenDay: &RateWindow{UsedPercentage: 7, ResetsAt: testNow.Unix() + 5*86400},
	}
	git := GitInfo{Branch: "feature/responsive-layout", ShortHash: "abc1234", Dirty: true, Ahead: "2", CommitMessage: "Subject"}
	return in, git
}

func TestNarrowLayout(t *testing.T) {
	t.Run("typical session at phone width", func(t *testing.T) {
		in := defaultInput()
		in.Model.DisplayName = "Fable 5.2"
		in.Effort.Level = "medium"
		in.SessionName = "Zig vs Rust rebuild benchmark"
		in.Workspace.CurrentDir = "/home/user/dev/statusline"
		in.Workspace.Repo = RepoInfo{Host: "github.com", Owner: "acme", Name: "statusline"}
		in.Cost.TotalLinesAdded = 10
		in.Cost.TotalDurationMS = 60000
		in.ContextWindow.UsedPercentage = 14
		in.PromptCache = &PromptCache{Warm: true, CachingObserved: true, ExpiresAt: testNow.Unix() + 3000}
		in.RateLimits = &RateLimits{
			FiveHour: &RateWindow{UsedPercentage: 35, ResetsAt: testNow.Unix() + 7200},
			SevenDay: &RateWindow{UsedPercentage: 7, ResetsAt: testNow.Unix() + 5*86400},
		}
		git := GitInfo{Branch: "main", ShortHash: "abc1234", Dirty: true, CommitMessage: "Subject"}

		// The session name is left to Claude Code, which shows it above
		// the prompt.
		want := []string{
			"statusline:main*",
			"Fable 5.2 med | ▂ 14% | ● | 5h 65% | 7d 93%",
		}
		got := narrowLines(in, git, 50)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	t.Run("wide layout from the breakpoint", func(t *testing.T) {
		in, git := busyInput()
		if got := narrowLines(in, git, narrowBelow)[0]; !strings.Contains(got, "[Opus 5.5:medium]") {
			t.Errorf("expected the wide layout at %d columns, got: %q", narrowBelow, got)
		}
		if got := narrowLines(in, git, 0)[0]; !strings.Contains(got, "[Opus 5.5:medium]") {
			t.Errorf("expected the wide layout without a width, got: %q", got)
		}
	})

	t.Run("links stay", func(t *testing.T) {
		in, git := busyInput()
		out := renderCols(in, git, 50)
		for _, url := range []string{"https://github.com/acme/claude-code-statusline\a", in.PR.URL + "\a#44"} {
			if !strings.Contains(out, "\033]8;;"+url) {
				t.Errorf("expected a link to %q, got: %q", url, out)
			}
		}
	})

	t.Run("branch shrinks before the repository goes", func(t *testing.T) {
		in, git := busyInput()
		if got := narrowLines(in, git, 50)[0]; got != "claude-code-statusline:feature…*↑2 | #44✓ | wt" {
			t.Errorf("at 50 columns got: %q", got)
		}
		if got := narrowLines(in, git, 30)[0]; got != "feature/responsive-lay…*↑2" {
			t.Errorf("at 30 columns got: %q", got)
		}
	})

	t.Run("directory name outside a repository", func(t *testing.T) {
		in := defaultInput()
		in.Workspace.CurrentDir = "/home/user/scratch"
		if got := narrowLines(in, GitInfo{}, 50)[0]; got != "scratch" {
			t.Errorf("got: %q", got)
		}
	})

	t.Run("merge request and review states", func(t *testing.T) {
		tests := []struct {
			pr   PRInfo
			want string
		}{
			{PRInfo{Number: 7}, "#7"},
			{PRInfo{Number: 7, ReviewState: "changes_requested"}, "#7✗"},
			{PRInfo{Number: 7, ReviewState: "draft"}, "#7"},
			{PRInfo{Number: 42, Kind: "mr"}, "!42"},
		}
		for _, tt := range tests {
			if got := plain(narrowPR(&tt.pr)); got != tt.want {
				t.Errorf("narrowPR(%+v) = %q, want %q", tt.pr, got, tt.want)
			}
		}
	})
}

func TestNarrowContext(t *testing.T) {
	tests := []struct {
		used    float64
		exceeds bool
		want    string
	}{
		{0, false, bold + "0%" + reset},
		{14, false, bold + "14%" + reset},
		{50, false, bold + "50%" + reset},
		{30, true, yellow + "30%!" + reset},
		{79, false, bold + "79%" + reset},
		{80, false, yellow + "40k free" + reset},
		{95, false, red + "10k free" + reset},
		{100, false, red + "0k free" + reset},
	}
	gauges := map[float64]string{0: "▁", 14: "▂", 30: "▃", 50: "▅", 79: "▇", 80: "▇", 95: "█", 100: "█"}
	for _, tt := range tests {
		in := defaultInput()
		in.ContextWindow = ContextWindow{ContextWindowSize: 200000, UsedPercentage: tt.used, RemainingPercentage: 100 - tt.used}
		in.Exceeds200k = tt.exceeds
		got := narrowContext(in)
		if !strings.HasSuffix(got, tt.want) || !strings.Contains(got, gauges[tt.used]) {
			t.Errorf("narrowContext(%v) = %q, want gauge %q and suffix %q", tt.used, got, gauges[tt.used], tt.want)
		}
	}
}

func TestNarrowCache(t *testing.T) {
	now := testNow.Unix()
	tests := []struct {
		name string
		pc   *PromptCache
		want string
	}{
		{"absent", nil, ""},
		{"no caching", &PromptCache{Warm: true, ExpiresAt: now + 3000}, ""},
		{"warm", &PromptCache{CachingObserved: true, Warm: true, ExpiresAt: now + 3000}, "●"},
		{"expired", &PromptCache{CachingObserved: true, Warm: true, ExpiresAt: now - 1}, "cold"},
		{"cold", &PromptCache{CachingObserved: true}, "cold"},
		{"fresh miss", &PromptCache{CachingObserved: true, Warm: true, ExpiresAt: now + 3000, LastMissAt: now - 30}, "●✗"},
		{"old miss", &PromptCache{CachingObserved: true, Warm: true, ExpiresAt: now + 3000, LastMissAt: now - 300}, "●"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plain(narrowCache(tt.pc, testNow).text); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("expiry time in the last five minutes", func(t *testing.T) {
		pc := &PromptCache{CachingObserved: true, Warm: true, ExpiresAt: now + 200}
		got := plain(narrowCache(pc, testNow).text)
		if !strings.HasPrefix(got, "→") || len(got) != len("→00:00") {
			t.Errorf("got %q, want an expiry time", got)
		}
	})
}

func TestNarrowRateLimits(t *testing.T) {
	in := defaultInput()
	in.RateLimits = &RateLimits{
		FiveHour:   &RateWindow{UsedPercentage: 93, ResetsAt: testNow.Unix() + 7200},
		SevenDay:   &RateWindow{UsedPercentage: 20, ResetsAt: testNow.Unix() + 5*86400},
		SpendLimit: &RateWindow{UsedPercentage: 105},
	}
	in.Cost.TotalCostUSD = 4.5

	if got, want := narrowLines(in, GitInfo{}, 59)[0], "Sonnet 4 | ▁ 10% | 5h 7% (2h) | 7d 80% | spend 5% over"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The windows that are running low outlast the others, and the reset
	// time goes before either.
	if got, want := narrowLines(in, GitInfo{}, 44)[0], "Sonnet 4 | ▁ 10% | 5h 7% | spend 5% over"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	in.RateLimits = &RateLimits{SpendLimit: &RateWindow{UsedPercentage: 40}}
	if got, want := narrowLines(in, GitInfo{}, 50)[0], "Sonnet 4 | ▁ 10% | $4.50 | spend 60%"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Every line of the narrow layout fits, whatever the payload carries.
func TestNarrowFitsAnyWidth(t *testing.T) {
	busy, busyGit := busyInput()
	low := busy
	low.ContextWindow.UsedPercentage, low.ContextWindow.RemainingPercentage = 92, 8
	low.RateLimits = &RateLimits{
		FiveHour:   &RateWindow{UsedPercentage: 95, ResetsAt: testNow.Unix() + 600},
		SevenDay:   &RateWindow{UsedPercentage: 80, ResetsAt: testNow.Unix() + 86400},
		SpendLimit: &RateWindow{UsedPercentage: 130, ResetsAt: testNow.Unix() + 86400},
	}
	low.PromptCache = &PromptCache{CachingObserved: true, Warm: true, ExpiresAt: testNow.Unix() + 100, LastMissAt: testNow.Unix()}
	wide := busy
	wide.SessionName = strings.Repeat("会話", 30)
	wideGit := busyGit
	wideGit.Branch = strings.Repeat("ブランチ", 10)

	cases := []struct {
		name string
		in   Input
		git  GitInfo
	}{
		{"plain", defaultInput(), GitInfo{}},
		{"busy", busy, busyGit},
		{"running low", low, busyGit},
		{"wide characters", wide, wideGit},
	}
	for _, c := range cases {
		for cols := 30; cols < narrowBelow; cols++ {
			var buf strings.Builder
			renderOutput(&buf, c.in, c.git, testNow, "/home/user", "sbx[vm]", cols)
			for i, l := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
				if w := visibleWidth(l); w > cols-widthReserve {
					t.Fatalf("%s, cols=%d: line %d is %d wide, limit %d: %q", c.name, cols, i+1, w, cols-widthReserve, plain(l))
				}
			}
		}
	}
}

func TestFitLine(t *testing.T) {
	elastic := func(s string) segment {
		return segment{sep: " ", min: 4, fit: func(width int) string {
			if width < 4 {
				return ""
			}
			return truncateWidth(s, width)
		}}
	}
	tests := []struct {
		name  string
		segs  []segment
		limit int
		want  string
	}{
		{"everything fits", []segment{{text: "a"}, {sep: " ", text: "bb", drop: 1}}, 10, "a bb"},
		{"empty segments leave no separator", []segment{{text: ""}, {sep: " | ", text: "a"}, {sep: " | "}, {sep: " | ", text: "b"}}, 10, "a | b"},
		{"highest drop goes first", []segment{{text: "keep"}, {sep: " ", text: "two", drop: 2}, {sep: " ", text: "one", drop: 1}}, 8, "keep one"},
		{"later of equals goes first", []segment{{text: "keep"}, {sep: " ", text: "aaa", drop: 1}, {sep: " ", text: "bbb", drop: 1}}, 8, "keep aaa"},
		{"a dropped segment returns when it fits", []segment{{text: "keep"}, {sep: " ", text: "b", drop: 2}, {sep: " ", text: "longer", drop: 1}}, 6, "keep b"},
		{"elastic takes the rest", []segment{{text: "keep"}, elastic("abcdefghij")}, 12, "keep abcdef…"},
		{"others go before the elastic falls under its minimum", []segment{elastic("abcdefghij"), {sep: " ", text: "gone", drop: 1}}, 7, "abcdef…"},
		{"elastic gives up without room", []segment{{text: "keepkeep"}, elastic("abcdefghij")}, 10, "keepkeep"},
		{"what always stays can overflow", []segment{{text: "keepkeep"}, {sep: " ", text: "x", drop: 1}}, 4, "keepkeep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fitLine(tt.segs, tt.limit); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
