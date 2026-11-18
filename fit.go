package main

import (
	"slices"
	"strings"
)

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
	// attached marks a detail of the nearest segment before it that is not
	// attached itself, such as the reset time of a rate limit. It is never
	// shown without that one, and its drop must be the higher of the two.
	attached bool
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
		owner := i
		for owner > 0 && segs[owner].attached {
			owner--
		}
		if owner != i && !shown[owner] {
			continue
		}
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
