// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"sort"

	"github.com/go-text/typesetting/di"
	"golang.org/x/image/math/fixed"
)

// CursorPosition describes a legal text insertion point (a caret) within a
// wrapped paragraph.
type CursorPosition struct {
	// RuneIndex is the index in the paragraph text at which new text would
	// be inserted. It always falls on a glyph cluster boundary: ligatures
	// and combining character sequences have no insertion points inside
	// them.
	RuneIndex int
	// Line is the index of the wrapped line containing the caret.
	Line int
	// X is the horizontal distance from the origin of its line to the caret.
	X fixed.Int26_6
	// Direction is the direction of the text run this caret position was
	// computed from. At a boundary between runs of different directions a
	// single rune index maps to two visual caret positions (one for each
	// side of the boundary); this field tells them apart.
	Direction di.Direction
}

// CursorMap maps between logical rune indices in a paragraph and the visual
// caret positions of its wrapped lines. It is built from the shaped and
// wrapped output of a paragraph (see [LineWrapper] and [WrappedLine]) and
// answers both directions of the mapping:
//
//   - [CursorMap.At] and [CursorMap.All] resolve a rune index to its caret
//     position(s).
//   - [CursorMap.Closest] resolves a point on a line (for instance a mouse
//     click) to the nearest legal insertion point.
//
// Only horizontal layout is supported: for vertical text the meaning of the
// X coordinate is undefined.
//
// The zero value is not usable; build a map with [NewCursorMap].
type CursorMap struct {
	direction di.Direction
	// positions holds every caret position of the paragraph, sorted by
	// (Line, X, RuneIndex, Direction).
	positions []CursorPosition
	// lineStart has LineCount()+1 entries: lineStart[i] is the index in
	// positions of the first caret of line i, and the last entry is
	// len(positions).
	lineStart []int
	// byRune maps a rune index to the indices of its caret positions in
	// positions, in increasing (Line, X) order. A rune index has several
	// positions when it sits on a line boundary or on a boundary between
	// runs of different directions.
	byRune map[int][]int
}

// NewCursorMap builds the caret positions of a wrapped paragraph laid out
// with the given paragraph direction (the [WrapConfig.Direction] used when
// wrapping). Each element of lines is one wrapped line, as found in
// [WrappedLine.Line] or returned by [LineWrapper.WrapParagraph].
//
// The following rules make the mapping deterministic:
//
//   - Insertion points are generated at both edges of every glyph cluster,
//     so ligatures and combining sequences offer no caret inside them.
//   - In RTL runs the logically first rune of a cluster is displayed on its
//     right edge; its caret is placed accordingly.
//   - A rune index on a boundary between two runs of different directions
//     yields two positions (one per direction), usually at different X
//     coordinates. Identical positions produced by adjacent same-direction
//     runs are merged, preferring the paragraph direction.
//   - An empty line (no runs, or no glyphs) has exactly one caret, at its
//     origin. An empty paragraph has a single caret at the origin.
//   - Within a line, positions are ordered by X, then by rune index, then
//     by direction; [CursorMap.Closest] uses this order to break ties.
func NewCursorMap(direction di.Direction, lines ...Line) CursorMap {
	m := CursorMap{
		direction: direction,
		byRune:    make(map[int][]int),
	}
	type key struct {
		line, rune int
		x          fixed.Int26_6
	}
	seen := make(map[key]int) // key -> index in m.positions
	add := func(pos CursorPosition) {
		k := key{pos.Line, pos.RuneIndex, pos.X}
		if idx, ok := seen[k]; ok {
			// Adjacent runs may produce the same caret coordinate for the
			// same rune index (for instance two same-direction runs split
			// by font fallback). Keep a single position, preferring the one
			// whose direction matches the paragraph direction.
			if m.positions[idx].Direction != direction && pos.Direction == direction {
				m.positions[idx].Direction = pos.Direction
			}
			return
		}
		seen[k] = len(m.positions)
		m.positions = append(m.positions, pos)
	}

	// nextRune tracks the rune index following the last line seen so far;
	// it is used to place the caret of empty lines.
	nextRune := 0
	for lineIdx, line := range lines {
		lineStart := len(m.positions)
		m.appendLinePositions(lineIdx, line, add)
		if len(m.positions) == lineStart {
			// An empty line still has exactly one caret, at its origin.
			runeIndex := nextRune
			if len(line) > 0 {
				runeIndex = line[0].Runes.Offset
			}
			add(CursorPosition{RuneIndex: runeIndex, Line: lineIdx, Direction: direction})
		}
		for _, pos := range m.positions[lineStart:] {
			if pos.RuneIndex > nextRune {
				nextRune = pos.RuneIndex
			}
		}
		m.lineStart = append(m.lineStart, lineStart)
	}
	if len(m.positions) == 0 {
		// An empty paragraph has a single caret at the origin.
		m.lineStart = append(m.lineStart, 0)
		add(CursorPosition{Direction: direction})
	}

	// Sort each line's positions by (X, RuneIndex, Direction) so that
	// lookups and tie-breaking are deterministic.
	for lineIdx := range m.lineStart {
		segment := m.positions[m.lineStart[lineIdx]:]
		if lineIdx+1 < len(m.lineStart) {
			segment = m.positions[m.lineStart[lineIdx]:m.lineStart[lineIdx+1]]
		}
		sort.SliceStable(segment, func(i, j int) bool {
			a, b := segment[i], segment[j]
			if a.X != b.X {
				return a.X < b.X
			}
			if a.RuneIndex != b.RuneIndex {
				return a.RuneIndex < b.RuneIndex
			}
			return a.Direction < b.Direction
		})
	}
	m.lineStart = append(m.lineStart, len(m.positions))
	for idx, pos := range m.positions {
		m.byRune[pos.RuneIndex] = append(m.byRune[pos.RuneIndex], idx)
	}
	return m
}

// appendLinePositions generates the caret positions of every run of the
// line. Runs are laid out left to right in visual order (by [Output.VisualIndex]),
// accumulating each run's Advance.
func (m *CursorMap) appendLinePositions(lineIdx int, line Line, add func(CursorPosition)) {
	if len(line) == 0 {
		return
	}
	// Compute the horizontal start of each run by accumulating advances in
	// visual (left to right) order. The sort is stable so that runs without
	// a computed VisualIndex keep their logical order.
	visualOrder := make([]int, len(line))
	for i := range visualOrder {
		visualOrder[i] = i
	}
	sort.SliceStable(visualOrder, func(i, j int) bool {
		return line[visualOrder[i]].VisualIndex < line[visualOrder[j]].VisualIndex
	})
	runStart := make([]fixed.Int26_6, len(line))
	var pen fixed.Int26_6
	for _, runIdx := range visualOrder {
		runStart[runIdx] = pen
		pen += line[runIdx].Advance
	}
	for runIdx, run := range line {
		appendRunPositions(lineIdx, run, runStart[runIdx], add)
	}
}

// appendRunPositions generates the caret positions at both edges of every
// glyph cluster of run, whose visual box starts at startX.
func appendRunPositions(lineIdx int, run Output, startX fixed.Int26_6, add func(CursorPosition)) {
	rtl := run.Direction.Progression() == di.TowardTopLeft
	x := startX
	glyphs := run.Glyphs
	for i := 0; i < len(glyphs); {
		// Gather the glyph cluster: consecutive glyphs sharing a cluster index.
		j := i + 1
		for j < len(glyphs) && glyphs[j].ClusterIndex == glyphs[i].ClusterIndex {
			j++
		}
		var advance fixed.Int26_6
		for _, g := range glyphs[i:j] {
			advance += g.Advance
		}
		clusterStart := glyphs[i].ClusterIndex
		runeCount := glyphs[i].RuneCount
		if runeCount <= 0 {
			runeCount = 1
		}
		clusterEnd := clusterStart + runeCount
		if rtl {
			// In right-to-left text the logically first rune of a cluster
			// is displayed on its right edge.
			add(CursorPosition{RuneIndex: clusterStart, Line: lineIdx, X: x + advance, Direction: run.Direction})
			add(CursorPosition{RuneIndex: clusterEnd, Line: lineIdx, X: x, Direction: run.Direction})
		} else {
			add(CursorPosition{RuneIndex: clusterStart, Line: lineIdx, X: x, Direction: run.Direction})
			add(CursorPosition{RuneIndex: clusterEnd, Line: lineIdx, X: x + advance, Direction: run.Direction})
		}
		x += advance
		i = j
	}
}

// LineCount returns the number of wrapped lines in the paragraph. It is at
// least one, even for an empty paragraph.
func (m CursorMap) LineCount() int { return len(m.lineStart) - 1 }

// Positions returns the caret positions of the given line, ordered by
// increasing X (then rune index, then direction). It returns nil if line is
// out of range.
func (m CursorMap) Positions(line int) []CursorPosition {
	if line < 0 || line >= m.LineCount() {
		return nil
	}
	return m.positions[m.lineStart[line]:m.lineStart[line+1]]
}

// At returns the canonical caret position for the given rune index, and
// whether the rune index has any position at all (rune indices inside a
// ligature or a combining sequence have none).
//
// When several positions exist for the rune index (line or direction
// boundaries), the canonical one is the first one matching the paragraph
// direction, falling back to the position on the earliest line with the
// smallest X. Use [CursorMap.All] to access every candidate.
func (m CursorMap) At(runeIndex int) (CursorPosition, bool) {
	indices := m.byRune[runeIndex]
	if len(indices) == 0 {
		return CursorPosition{}, false
	}
	best := m.positions[indices[0]]
	if best.Direction != m.direction {
		for _, idx := range indices[1:] {
			if m.positions[idx].Direction == m.direction {
				best = m.positions[idx]
				break
			}
		}
	}
	return best, true
}

// All returns every caret position for the given rune index, ordered by
// (Line, X). A rune index on a line boundary has one position per line; a
// rune index on a direction boundary has one position per direction.
func (m CursorMap) All(runeIndex int) []CursorPosition {
	indices := m.byRune[runeIndex]
	out := make([]CursorPosition, len(indices))
	for i, idx := range indices {
		out[i] = m.positions[idx]
	}
	return out
}

// Closest returns the caret position on the given line nearest to the
// horizontal coordinate x, for instance to resolve a mouse click into an
// insertion point.
//
// The rules are deterministic:
//
//   - A line index outside [0, LineCount) is clamped to the nearest valid
//     line, and an x outside the line's extent resolves to the first or
//     last position of the line.
//   - If x falls exactly halfway between two positions, the leftmost one
//     wins.
//   - If several positions share the winning coordinate (zero-advance
//     clusters, direction boundaries), the one with the smallest rune
//     index wins.
func (m CursorMap) Closest(line int, x fixed.Int26_6) CursorPosition {
	if line < 0 {
		line = 0
	} else if line >= m.LineCount() {
		line = m.LineCount() - 1
	}
	positions := m.positions[m.lineStart[line]:m.lineStart[line+1]]
	// Find the first position with X >= x.
	i := sort.Search(len(positions), func(i int) bool { return positions[i].X >= x })
	switch {
	case i == 0:
		return positions[0]
	case i >= len(positions):
		return positions[len(positions)-1]
	default:
		before, after := positions[i-1], positions[i]
		if x-before.X <= after.X-x {
			return before
		}
		return after
	}
}
