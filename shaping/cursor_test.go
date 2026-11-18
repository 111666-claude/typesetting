package shaping

import (
	"fmt"
	"testing"

	"github.com/go-text/typesetting/bidi"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

// shapeRun shapes text[start:end] as a single run.
func shapeRun(text []rune, start, end int, face *font.Face, dir di.Direction, script language.Script, level bidi.Level) Output {
	return (&HarfbuzzShaper{}).Shape(Input{
		Text:      text,
		RunStart:  start,
		RunEnd:    end,
		Direction: dir,
		Face:      face,
		Size:      16 * 72 * 10,
		Script:    script,
		Level:     level,
	})
}

// assertRoundTrip checks that every caret position of m resolves back to a
// position on the same line at the same X coordinate.
func assertRoundTrip(t *testing.T, m CursorMap) {
	t.Helper()
	for line := 0; line < m.LineCount(); line++ {
		for _, p := range m.Positions(line) {
			got := m.Closest(p.Line, p.X)
			tu.AssertC(t, got.Line == p.Line && got.X == p.X,
				fmt.Sprintf("round trip of %+v gave %+v", p, got))
			// The resolved rune index must itself have a position at the
			// same coordinate.
			found := false
			for _, q := range m.All(got.RuneIndex) {
				if q.Line == got.Line && q.X == got.X {
					found = true
					break
				}
			}
			tu.AssertC(t, found, fmt.Sprintf("round trip of %+v: no position back for rune %d", p, got.RuneIndex))
		}
	}
}

// assertStrictRoundTrip checks that every caret position resolves back to
// its own rune index (only valid when no two positions of a line share a
// coordinate).
func assertStrictRoundTrip(t *testing.T, m CursorMap) {
	t.Helper()
	for line := 0; line < m.LineCount(); line++ {
		for _, p := range m.Positions(line) {
			got := m.Closest(p.Line, p.X)
			tu.AssertC(t, got == p, fmt.Sprintf("strict round trip of %+v gave %+v", p, got))
		}
	}
}

// TestCursorMapMonospaceASCII checks caret positions on a fixed-width font,
// where every rune advances by the same amount, including across a line
// break created by wrapping.
func TestCursorMapMonospaceASCII(t *testing.T) {
	monoFont := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	text := []rune("Hello world!") // 12 runes
	out := simpleShape(text, monoFont, di.DirectionLTR)
	charAdvance := out.Advance / fixed.Int26_6(len(text))
	tu.Assert(t, charAdvance > 0)

	// Wrap after the 6th rune ("Hello "), keeping trailing whitespace
	// advances so that every caret coordinate is distinct.
	config := WrapConfig{Direction: di.DirectionLTR, DisableTrailingWhitespaceTrim: true}
	var l LineWrapper
	l.Prepare(config, text, NewSliceIterator([]Output{out}))
	var lines []Line
	for {
		wl, done := l.WrapNextLineF(charAdvance * 6)
		if wl.Line != nil {
			lines = append(lines, wl.Line)
		}
		if done {
			break
		}
	}
	tu.Assert(t, len(lines) == 2)

	m := NewCursorMap(di.DirectionLTR, lines...)
	tu.Assert(t, m.LineCount() == 2)

	// Line 0 holds runes 0..5 ("Hello ") : 7 caret positions on a regular grid.
	line0 := m.Positions(0)
	tu.Assert(t, len(line0) == 7)
	for i, p := range line0 {
		tu.AssertC(t, p.RuneIndex == i, fmt.Sprintf("line 0 position %d has rune %d", i, p.RuneIndex))
		tu.AssertC(t, p.X == fixed.Int26_6(i)*charAdvance, fmt.Sprintf("line 0 position %d has X %d", i, p.X))
		tu.Assert(t, p.Direction == di.DirectionLTR)
	}
	// Line 1 holds runes 6..11 ("world!") : 7 caret positions, starting at 0.
	line1 := m.Positions(1)
	tu.Assert(t, len(line1) == 7)
	for i, p := range line1 {
		tu.AssertC(t, p.RuneIndex == i+6, fmt.Sprintf("line 1 position %d has rune %d", i, p.RuneIndex))
		tu.AssertC(t, p.X == fixed.Int26_6(i)*charAdvance, fmt.Sprintf("line 1 position %d has X %d", i, p.X))
	}

	// The wrap point (rune 6) has one caret at the end of line 0 and one at
	// the start of line 1.
	all := m.All(6)
	tu.Assert(t, len(all) == 2)
	tu.Assert(t, all[0].Line == 0 && all[0].X == 6*charAdvance)
	tu.Assert(t, all[1].Line == 1 && all[1].X == 0)
	// The canonical position is on the earliest line.
	canonical, ok := m.At(6)
	tu.Assert(t, ok && canonical.Line == 0 && canonical.X == 6*charAdvance)

	// Every coordinate is distinct on each line : the round trip is exact.
	assertStrictRoundTrip(t, m)

	// Out-of-range queries clamp to the nearest valid position.
	tu.Assert(t, m.Closest(0, -fixed.I(10)) == line0[0])
	tu.Assert(t, m.Closest(0, fixed.I(1)<<20) == line0[len(line0)-1])
	tu.Assert(t, m.Closest(-1, 0) == line0[0]) // line clamped to 0
	tu.Assert(t, m.Closest(99, 0) == line1[0]) // line clamped to last
	tu.Assert(t, m.Closest(1, charAdvance*3) == line1[3])
}

// TestCursorMapBidi checks caret positions on mixed Latin/Arabic text,
// including the visual reordering of adjacent RTL runs and the double caret
// positions at direction boundaries.
func TestCursorMapBidi(t *testing.T) {
	latinFont := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	arabicFont := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")

	// "ab" + "لمن" + "قص" + "cd" : the two RTL runs are reordered visually
	// as a single sequence, so the visual order of runs is 0, 2, 1, 3.
	text := []rune("abلمنقصcd")
	runs := []Output{
		shapeRun(text, 0, 2, latinFont, di.DirectionLTR, language.Latin, 0),
		shapeRun(text, 2, 5, arabicFont, di.DirectionRTL, language.Arabic, 1),
		shapeRun(text, 5, 7, arabicFont, di.DirectionRTL, language.Arabic, 1),
		shapeRun(text, 7, 9, latinFont, di.DirectionLTR, language.Latin, 0),
	}
	var l LineWrapper
	lines, _ := l.WrapParagraph(WrapConfig{Direction: di.DirectionLTR}, 10000, text, NewSliceIterator(runs))
	tu.Assert(t, len(lines) == 1)
	line := lines[0]
	// Check the expected visual reordering.
	tu.Assert(t, line[1].VisualIndex == 2 && line[2].VisualIndex == 1)

	m := NewCursorMap(di.DirectionLTR, lines...)
	tu.Assert(t, m.LineCount() == 1)

	// Every rune boundary has at least one caret.
	for i := 0; i <= len(text); i++ {
		_, ok := m.At(i)
		tu.AssertC(t, ok, fmt.Sprintf("no caret for rune %d", i))
	}

	// Direction boundaries produce two positions; same-direction run
	// boundaries and run interiors produce one.
	tu.Assert(t, len(m.All(2)) == 2) // LTR "ab" | RTL "لمن"
	tu.Assert(t, len(m.All(7)) == 2) // RTL "قص" | LTR "cd"
	tu.Assert(t, len(m.All(5)) == 1) // RTL "لمن" | RTL "قص" : merged
	for _, r := range []int{0, 1, 3, 4, 6, 8, 9} {
		tu.AssertC(t, len(m.All(r)) == 1, fmt.Sprintf("rune %d should have one caret", r))
	}

	// Within the RTL runs, the caret X decreases as the rune index increases.
	rtlX := func(runeIndex int) fixed.Int26_6 {
		for _, p := range m.All(runeIndex) {
			if p.Direction == di.DirectionRTL {
				return p.X
			}
		}
		t.Fatalf("no RTL caret for rune %d", runeIndex)
		return 0
	}
	tu.Assert(t, rtlX(2) > rtlX(3) && rtlX(3) > rtlX(4) && rtlX(4) > rtlX(5))
	tu.Assert(t, rtlX(5) > rtlX(6) && rtlX(6) > rtlX(7))
	// Visual reordering : the logically later run "قص" (runes 5..7) is
	// displayed to the left of "لمن" (runes 2..5).
	tu.Assert(t, rtlX(5) < rtlX(2))

	// The two positions of a direction boundary sit at different coordinates.
	b2 := m.All(2)
	tu.Assert(t, b2[0].X != b2[1].X)
	// The canonical position prefers the paragraph direction.
	canonical, _ := m.At(2)
	tu.Assert(t, canonical.Direction == di.DirectionLTR)

	// At the shared coordinate of the boundary, two rune indices (2 and 7)
	// are candidates; the smallest rune index wins deterministically.
	var ltrBoundaryX fixed.Int26_6
	for _, p := range b2 {
		if p.Direction == di.DirectionLTR {
			ltrBoundaryX = p.X
		}
	}
	tu.Assert(t, m.Closest(0, ltrBoundaryX).RuneIndex == 2)

	// Positions are sorted by X on the line.
	positions := m.Positions(0)
	for i := 1; i < len(positions); i++ {
		tu.Assert(t, positions[i-1].X <= positions[i].X)
	}
	assertRoundTrip(t, m)
}

// TestCursorMapLigature checks that a ligature cluster offers no insertion
// point between its runes.
func TestCursorMapLigature(t *testing.T) {
	latinFont := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	text := []rune("fi") // shaped as a single ligature cluster by Roboto
	out := simpleShape(text, latinFont, di.DirectionLTR)
	tu.Assert(t, len(out.Glyphs) == 1 && out.Glyphs[0].RuneCount == 2)

	m := NewCursorMap(di.DirectionLTR, Line{out})
	// Only the cluster edges are legal insertion points.
	tu.Assert(t, len(m.Positions(0)) == 2)
	_, ok := m.At(1)
	tu.Assert(t, !ok) // no caret inside the ligature
	start, ok := m.At(0)
	tu.Assert(t, ok && start.X == 0)
	end, ok := m.At(2)
	tu.Assert(t, ok && end.X == out.Advance)

	// A click in the middle of the ligature resolves to the nearest edge,
	// with the left edge winning an exact tie.
	tu.Assert(t, m.Closest(0, out.Advance/4).RuneIndex == 0)
	tu.Assert(t, m.Closest(0, out.Advance/2).RuneIndex == 0)
	tu.Assert(t, m.Closest(0, 3*out.Advance/4).RuneIndex == 2)
	assertStrictRoundTrip(t, m)
}

// TestCursorMapCombiningMarks checks that a base character and its combining
// marks form a single cluster with no insertion point inside.
func TestCursorMapCombiningMarks(t *testing.T) {
	latinFont := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	text := []rune("éx") // e + combining acute accent, then x
	out := simpleShape(text, latinFont, di.DirectionLTR)
	// The first two runes shape into a single cluster.
	tu.Assert(t, out.Glyphs[0].ClusterIndex == 0 && out.Glyphs[0].RuneCount == 2)

	m := NewCursorMap(di.DirectionLTR, Line{out})
	_, ok := m.At(1)
	tu.Assert(t, !ok) // no caret between the base and its accent
	_, ok = m.At(0)
	tu.Assert(t, ok)
	_, ok = m.At(2)
	tu.Assert(t, ok)
	_, ok = m.At(3)
	tu.Assert(t, ok)
	assertStrictRoundTrip(t, m)
}

// TestCursorMapNewlineBoundary checks caret positions around a mandatory
// line break.
func TestCursorMapNewlineBoundary(t *testing.T) {
	monoFont := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	text := []rune("ab\ncd")
	out := simpleShape(text, monoFont, di.DirectionLTR)
	var l LineWrapper
	lines, _ := l.WrapParagraph(WrapConfig{Direction: di.DirectionLTR}, 10000, text, NewSliceIterator([]Output{out}))
	tu.Assert(t, len(lines) == 2)

	m := NewCursorMap(di.DirectionLTR, lines...)
	tu.Assert(t, m.LineCount() == 2)

	// Rune 3 (just after the newline) is the end of line 0 and the start of
	// line 1 : it has one caret on each line.
	all := m.All(3)
	tu.Assert(t, len(all) == 2)
	tu.Assert(t, all[0].Line == 0 && all[1].Line == 1)
	tu.Assert(t, all[1].X == 0)
	// Line 0 ends after the newline rune, line 1 starts at rune 3.
	line0 := m.Positions(0)
	tu.Assert(t, line0[len(line0)-1].RuneIndex == 3)
	line1 := m.Positions(1)
	tu.Assert(t, line1[0].RuneIndex == 3 && line1[len(line1)-1].RuneIndex == 5)

	assertRoundTrip(t, m)
}

// TestCursorMapEdgeCases checks the deterministic rules for empty inputs,
// zero-advance glyphs and out-of-range coordinates.
func TestCursorMapEdgeCases(t *testing.T) {
	// An empty paragraph has exactly one caret at the origin.
	m := NewCursorMap(di.DirectionLTR)
	tu.Assert(t, m.LineCount() == 1)
	pos, ok := m.At(0)
	tu.Assert(t, ok && pos.Line == 0 && pos.X == 0 && pos.Direction == di.DirectionLTR)
	tu.Assert(t, m.Closest(4, fixed.I(100)) == pos) // line and X clamped

	// An empty line has exactly one caret at its origin, using the rune
	// offset of its first run when available.
	m = NewCursorMap(di.DirectionLTR, Line{}, Line{{Runes: Range{Offset: 5}}})
	tu.Assert(t, m.LineCount() == 2)
	tu.Assert(t, len(m.Positions(0)) == 1 && m.Positions(0)[0].RuneIndex == 0)
	tu.Assert(t, len(m.Positions(1)) == 1 && m.Positions(1)[0].RuneIndex == 5)

	// Zero-advance glyphs stack several carets on the same coordinate;
	// the smallest rune index wins.
	zeroRun := Output{
		Direction: di.DirectionLTR,
		Runes:     Range{Count: 2},
		Glyphs: []Glyph{
			{ClusterIndex: 0, RuneCount: 1, GlyphCount: 1},
			{ClusterIndex: 1, RuneCount: 1, GlyphCount: 1},
		},
	}
	m = NewCursorMap(di.DirectionLTR, Line{zeroRun})
	positions := m.Positions(0)
	tu.Assert(t, len(positions) == 3)
	for _, p := range positions {
		tu.Assert(t, p.X == 0)
	}
	tu.Assert(t, m.Closest(0, 0).RuneIndex == 0)
	// Beyond the line extent, the nearest (here: last) position wins.
	tu.Assert(t, m.Closest(0, fixed.I(1000)).RuneIndex == 2)
	tu.Assert(t, m.Closest(0, -fixed.I(1000)).RuneIndex == 0)

	// Out-of-range lines and rune indices are reported, not panics.
	tu.Assert(t, m.Positions(-1) == nil && m.Positions(1) == nil)
	_, ok = m.At(99)
	tu.Assert(t, !ok)
}
