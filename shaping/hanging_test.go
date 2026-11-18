// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"math"
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

func TestClampHangRatio(t *testing.T) {
	for _, tc := range []struct {
		ratio    float64
		expected fixed.Int26_6
	}{
		{math.NaN(), 0},
		{math.Inf(-1), 0},
		{-0.5, 0},
		{0, 0},
		{0.5, 32},
		{1, 64},
		{2.5, 64},
		{math.Inf(1), 64},
	} {
		if got := clampHangRatio(tc.ratio); got != tc.expected {
			t.Errorf("clampHangRatio(%v) = %v, expected %v", tc.ratio, got, tc.expected)
		}
	}
}

// hangGlyph returns a glyph with the given cluster and advance, with a
// non-zero width so that it is considered as having ink.
func hangGlyph(cluster int, advance fixed.Int26_6) Glyph {
	return Glyph{
		Width:        fixed.I(10),
		Height:       fixed.I(10),
		Advance:      advance,
		ClusterIndex: cluster,
		GlyphCount:   1,
		RuneCount:    1,
	}
}

func TestHangingEdgeHang(t *testing.T) {
	text := []rune("a.,b") // indices: a=0 ,=1 .=2 b=3 -- wait: a=0 ,=1 .=2 b=3
	h := newHangingPunctuation(HangingPunctuation{
		Start:      []rune{'a'},
		End:        []rune{',', '.'},
		StartRatio: 0.5,
		EndRatio:   1,
	})
	run := Output{
		Direction: di.DirectionLTR,
		Glyphs: []Glyph{
			hangGlyph(0, 100), // a
			hangGlyph(1, 40),  // ,
			hangGlyph(2, 40),  // .
			hangGlyph(3, 100), // b
		},
	}
	// Trailing edge of "a.,b" is 'b': no hang.
	if got := h.edgeHang(run, di.DirectionLTR, text, false); got != 0 {
		t.Errorf("trailing edge of a.,b: expected 0, got %v", got)
	}
	// Leading edge is 'a' with ratio 0.5.
	if got := h.edgeHang(run, di.DirectionLTR, text, true); got != 50 {
		t.Errorf("leading edge of a.,b: expected 50, got %v", got)
	}
	// Consecutive punctuation accumulates at the trailing edge.
	run2 := run
	run2.Glyphs = []Glyph{hangGlyph(0, 100), hangGlyph(1, 40), hangGlyph(2, 40)} // a , .
	if got := h.edgeHang(run2, di.DirectionLTR, text, false); got != 80 {
		t.Errorf("trailing edge of a.,: expected 80, got %v", got)
	}
	// Zero advance and zero width glyphs at the edge are skipped.
	zero := hangGlyph(3, 0)
	zero.Width = 0
	run3 := run
	run3.Glyphs = []Glyph{hangGlyph(0, 100), hangGlyph(1, 40), zero, zero}
	if got := h.edgeHang(run3, di.DirectionLTR, text, false); got != 40 {
		t.Errorf("trailing edge with zero glyphs: expected 40, got %v", got)
	}
	// RTL runs measure the same logical edges from the other side of the
	// slice: glyphs are stored in visual order.
	rtlRun := Output{
		Direction: di.DirectionRTL,
		Glyphs: []Glyph{
			hangGlyph(2, 40),  // . (leftmost, trailing in reading order)
			hangGlyph(1, 40),  // ,
			hangGlyph(0, 100), // a (rightmost, leading in reading order)
		},
	}
	if got := h.edgeHang(rtlRun, di.DirectionRTL, text, false); got != 80 {
		t.Errorf("RTL trailing edge: expected 80, got %v", got)
	}
	if got := h.edgeHang(rtlRun, di.DirectionRTL, text, true); got != 50 {
		t.Errorf("RTL leading edge: expected 50, got %v", got)
	}
	// A run whose direction differs from the paragraph never hangs.
	if got := h.edgeHang(rtlRun, di.DirectionLTR, text, false); got != 0 {
		t.Errorf("cross direction run: expected 0, got %v", got)
	}
	// Clusters outside the text stop the walk instead of panicking.
	run4 := run
	run4.Glyphs = []Glyph{hangGlyph(0, 100), hangGlyph(99, 40)}
	if got := h.edgeHang(run4, di.DirectionLTR, text, false); got != 0 {
		t.Errorf("out of range cluster: expected 0, got %v", got)
	}
	// Empty glyph slices are stable.
	if got := h.edgeHang(Output{Direction: di.DirectionLTR}, di.DirectionLTR, text, false); got != 0 {
		t.Errorf("empty run: expected 0, got %v", got)
	}
	// A disabled configuration never hangs.
	var disabled hangingPunctuation
	if got := disabled.edgeHang(run, di.DirectionLTR, text, false); got != 0 {
		t.Errorf("disabled: expected 0, got %v", got)
	}
}

// wrapAllLines wraps text with the given config at width and returns all
// lines with their overhangs. The runs are deep copied first, since line
// wrapping mutates the shaped glyphs (trailing whitespace trim).
func wrapHangLines(config WrapConfig, width fixed.Int26_6, text []rune, runs []Output) []WrappedLine {
	copied := make([]Output, len(runs))
	for i := range runs {
		copied[i] = runs[i].copy()
	}
	var l LineWrapper
	l.Prepare(config, text, NewSliceIterator(copied))
	var lines []WrappedLine
	for {
		line, done := l.WrapNextLineF(width)
		if line.Line != nil {
			lines = append(lines, line)
		}
		if done {
			break
		}
	}
	return lines
}

func shapeLatin(text []rune) Output {
	var shaper HarfbuzzShaper
	return shaper.Shape(Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: di.DirectionLTR,
		Face:      benchEnFace,
		Size:      fixed.I(16),
		Script:    language.Latin,
		Language:  language.NewLanguage("EN"),
	})
}

func lineAdvance(line Line) fixed.Int26_6 {
	var advance fixed.Int26_6
	for _, run := range line {
		advance += run.Advance
	}
	return advance
}

func lineRunes(line Line) (start, end int) {
	start = line[0].Runes.Offset
	last := line[len(line)-1]
	end = last.Runes.Offset + last.Runes.Count
	return start, end
}

// TestHangingPunctuationDisabledUnchanged checks that a zero or ineffective
// hanging configuration produces exactly the same output as no configuration
// at all.
func TestHangingPunctuationDisabledUnchanged(t *testing.T) {
	text := []rune("Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor.")
	out := shapeLatin(text)
	reference := wrapHangLines(WrapConfig{}, fixed.I(100), text, []Output{out})
	for _, tc := range []struct {
		name   string
		config HangingPunctuation
	}{
		{"zero", HangingPunctuation{}},
		{"empty sets", HangingPunctuation{StartRatio: 1, EndRatio: 1}},
		{"zero ratios", HangingPunctuation{Start: []rune{'"'}, End: []rune{','}}},
		{"negative ratios", HangingPunctuation{End: []rune{','}, EndRatio: -2}},
		{"NaN ratios", HangingPunctuation{End: []rune{','}, EndRatio: math.NaN()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := wrapHangLines(WrapConfig{HangingPunctuation: tc.config}, fixed.I(100), text, []Output{out})
			tu.Assert(t, len(lines) == len(reference))
			for i, line := range lines {
				ref := reference[i]
				s, e := lineRunes(line.Line)
				rs, re := lineRunes(ref.Line)
				if s != rs || e != re {
					t.Fatalf("line %d: runes [%d,%d), expected [%d,%d)", i, s, e, rs, re)
				}
				tu.Assert(t, line.StartOverhang == 0)
				tu.Assert(t, line.EndOverhang == 0)
				tu.Assert(t, lineAdvance(line.Line) == lineAdvance(ref.Line))
			}
		})
	}
}

// TestHangingPunctuationEndLTR checks that a trailing comma is allowed to
// hang beyond the frame, changing the break points, and that the reported
// overhang matches the shaped comma glyph.
func TestHangingPunctuationEndLTR(t *testing.T) {
	text := []rune("aa bb, cc")
	out := shapeLatin(text)
	// The width fits "aa bb" exactly, so that the comma can only be
	// accommodated by hanging.
	width := fixed.Int26_6(2565)

	plain := wrapHangLines(WrapConfig{}, width, text, []Output{out})
	tu.Assert(t, len(plain) == 3)
	_, end := lineRunes(plain[0].Line)
	tu.Assert(t, end == 3) // "aa "
	for _, line := range plain {
		tu.Assert(t, line.EndOverhang == 0)
	}

	hang := HangingPunctuation{End: []rune{','}, EndRatio: 1}
	hung := wrapHangLines(WrapConfig{HangingPunctuation: hang}, width, text, []Output{out})
	tu.Assert(t, len(hung) == 2)
	_, end = lineRunes(hung[0].Line)
	tu.Assert(t, end == 7) // "aa bb, "

	// The comma is the last glyph with ink of the line: its advance is the
	// amount hanging beyond the frame.
	var commaAdvance fixed.Int26_6
	for _, g := range hung[0].Line[0].Glyphs {
		if g.Advance != 0 {
			commaAdvance = g.Advance
		}
	}
	tu.Assert(t, commaAdvance > 0)
	tu.Assert(t, hung[0].EndOverhang == commaAdvance)
	// The line visually overflows the frame by exactly the overhang.
	tu.Assert(t, lineAdvance(hung[0].Line) == width+commaAdvance)
	// The second line does not overflow.
	tu.Assert(t, hung[1].EndOverhang == 0)
}

// TestHangingPunctuationStartLTR checks that an opening quote at the start
// of a line hangs by the configured ratio.
func TestHangingPunctuationStartLTR(t *testing.T) {
	text := []rune(`xx "yy" zz`)
	out := shapeLatin(text)
	width := fixed.Int26_6(1400)

	plain := wrapHangLines(WrapConfig{}, width, text, []Output{out})
	for _, line := range plain {
		tu.Assert(t, line.StartOverhang == 0)
	}

	hang := HangingPunctuation{Start: []rune{'"'}, StartRatio: 0.5}
	hung := wrapHangLines(WrapConfig{HangingPunctuation: hang}, width, text, []Output{out})
	tu.Assert(t, len(hung) == len(plain))
	// Find the lines starting with a quote and check their overhang against
	// the shaped quote glyph.
	quoteLines := 0
	for _, line := range hung {
		start, _ := lineRunes(line.Line)
		if text[start] != '"' {
			tu.Assert(t, line.StartOverhang == 0)
			continue
		}
		quoteLines++
		quoteAdvance := line.Line[0].Glyphs[0].Advance
		tu.Assert(t, line.StartOverhang == quoteAdvance/2)
	}
	tu.Assert(t, quoteLines == 2)
}

// TestHangingPunctuationRTL checks hanging punctuation on an RTL paragraph:
// the trailing edge of the line is on the left, and the allowance is
// measured on the first glyph of the run.
func TestHangingPunctuationRTL(t *testing.T) {
	text := []rune("الحب سماء، جميلة")
	var shaper HarfbuzzShaper
	out := shaper.Shape(Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: di.DirectionRTL,
		Face:      benchArFace,
		Size:      fixed.I(16),
		Script:    language.Arabic,
		Language:  language.NewLanguage("AR"),
	})
	width := fixed.Int26_6(3688)
	config := WrapConfig{
		Direction:          di.DirectionRTL,
		HangingPunctuation: HangingPunctuation{End: []rune{'،'}, EndRatio: 1},
	}

	plain := wrapHangLines(WrapConfig{Direction: di.DirectionRTL}, width, text, []Output{out})
	tu.Assert(t, len(plain) == 2)
	_, end := lineRunes(plain[0].Line)
	tu.Assert(t, end == 5) // the comma does not fit without hanging

	hung := wrapHangLines(config, width, text, []Output{out})
	tu.Assert(t, len(hung) == 2)
	_, end = lineRunes(hung[0].Line)
	tu.Assert(t, end == 11) // "الحب سماء، "

	// The line overflows the frame by less than the full comma advance:
	// the reported overhang is the actual visual overflow.
	lineWidth := lineAdvance(hung[0].Line)
	tu.Assert(t, lineWidth > width)
	tu.Assert(t, hung[0].EndOverhang == lineWidth-width)
	// The trailing edge (reading order) of an RTL run is the start of its
	// glyph slice; the first glyph with ink there is the arabic comma (the
	// trailing space was trimmed to a zero advance by the wrapper).
	var trailing Glyph
	for _, g := range hung[0].Line[0].Glyphs {
		if g.Advance != 0 {
			trailing = g
			break
		}
	}
	tu.Assert(t, text[trailing.ClusterIndex] == '،')
	tu.Assert(t, hung[0].EndOverhang <= trailing.Advance)
}

// TestHangingPunctuationConsecutive checks that consecutive punctuation
// glyphs accumulate their hang allowance.
func TestHangingPunctuationConsecutive(t *testing.T) {
	text := []rune("ab.. cd")
	out := shapeLatin(text)
	width := fixed.Int26_6(1140) // exactly "ab"

	hang := HangingPunctuation{End: []rune{'.'}, EndRatio: 1}
	hung := wrapHangLines(WrapConfig{HangingPunctuation: hang}, width, text, []Output{out})
	tu.Assert(t, len(hung) == 2)
	_, end := lineRunes(hung[0].Line)
	tu.Assert(t, end == 5) // "ab.. "

	// Both periods hang: the overhang is the sum of their advances.
	var sum fixed.Int26_6
	count := 0
	for i := len(hung[0].Line[0].Glyphs) - 1; i >= 0 && count < 2; i-- {
		g := hung[0].Line[0].Glyphs[i]
		if g.Advance == 0 {
			continue
		}
		sum += g.Advance
		count++
	}
	tu.Assert(t, hung[0].EndOverhang == sum)
	tu.Assert(t, hung[0].EndOverhang == lineAdvance(hung[0].Line)-width)
}

// TestHangingPunctuationZeroWidth checks that zero advance glyphs (like
// combining marks) and zero width glyphs (like spaces) at the line edge do
// not prevent the punctuation before them from hanging.
func TestHangingPunctuationZeroWidth(t *testing.T) {
	// U+0301 COMBINING ACUTE ACCENT follows the period.
	text := []rune("ab.́ cd")
	out := shapeLatin(text)
	width := fixed.Int26_6(1140) // exactly "ab"

	hang := HangingPunctuation{End: []rune{'.'}, EndRatio: 1}
	hung := wrapHangLines(WrapConfig{HangingPunctuation: hang}, width, text, []Output{out})
	tu.Assert(t, len(hung) == 2)
	_, end := lineRunes(hung[0].Line)
	tu.Assert(t, end == 5) // "ab.́ "

	// The combining mark has a zero advance: the period is the glyph
	// effectively hanging.
	var periodAdvance fixed.Int26_6
	for _, g := range hung[0].Line[0].Glyphs {
		if g.Advance != 0 {
			periodAdvance = g.Advance
		}
	}
	tu.Assert(t, hung[0].EndOverhang == periodAdvance)

	// A zero width space at the edge is stable and does not hang.
	zwsp := []rune("ab​ cd")
	outZWSP := shapeLatin(zwsp)
	lines := wrapHangLines(WrapConfig{HangingPunctuation: hang}, width, zwsp, []Output{outZWSP})
	for _, line := range lines {
		tu.Assert(t, line.StartOverhang == 0)
		tu.Assert(t, line.EndOverhang == 0)
	}
}

// TestHangingPunctuationNarrowLine checks that extremely narrow lines still
// terminate and consume the whole text when hanging is enabled.
func TestHangingPunctuationNarrowLine(t *testing.T) {
	text := []rune("a., a., a.,")
	out := shapeLatin(text)
	hang := HangingPunctuation{End: []rune{',', '.'}, EndRatio: 1, Start: []rune{'a'}, StartRatio: 1}
	lines := wrapHangLines(WrapConfig{HangingPunctuation: hang}, 1, text, []Output{out})
	tu.Assert(t, len(lines) > 0)
	// The whole text must be consumed.
	_, end := lineRunes(lines[len(lines)-1].Line)
	tu.Assert(t, end == len(text))
}

// TestHangingPunctuationInvalidRatios checks that out of range ratios are
// clamped, behaving exactly like ratio 0 or 1.
func TestHangingPunctuationInvalidRatios(t *testing.T) {
	text := []rune("aa bb, cc")
	out := shapeLatin(text)
	width := fixed.Int26_6(2565)

	breakPoints := func(config HangingPunctuation) []int {
		lines := wrapHangLines(WrapConfig{HangingPunctuation: config}, width, text, []Output{out})
		var points []int
		for _, line := range lines {
			_, end := lineRunes(line.Line)
			points = append(points, end)
		}
		return points
	}
	zero := breakPoints(HangingPunctuation{})
	one := breakPoints(HangingPunctuation{End: []rune{','}, EndRatio: 1})
	// Clamped to 0: same as disabled.
	for _, ratio := range []float64{-1, math.NaN(), math.Inf(-1)} {
		got := breakPoints(HangingPunctuation{End: []rune{','}, EndRatio: ratio})
		if len(got) != len(zero) {
			t.Fatalf("ratio %v: got %v, expected %v", ratio, got, zero)
		}
		for i := range got {
			tu.Assert(t, got[i] == zero[i])
		}
	}
	// Clamped to 1: same as ratio 1.
	for _, ratio := range []float64{2.5, math.Inf(1)} {
		got := breakPoints(HangingPunctuation{End: []rune{','}, EndRatio: ratio})
		if len(got) != len(one) {
			t.Fatalf("ratio %v: got %v, expected %v", ratio, got, one)
		}
		for i := range got {
			tu.Assert(t, got[i] == one[i])
		}
	}
	// And ratio 1 actually differs from disabled here.
	tu.Assert(t, len(one) != len(zero) || one[0] != zero[0])
}

// TestHangingPunctuationMissingGlyph checks that a hangable rune missing
// from the font (shaped as .notdef) still hangs by its shaped advance,
// without panicking.
func TestHangingPunctuationMissingGlyph(t *testing.T) {
	text := []rune("ab世 cd") // U+4E16 is missing from the test font
	out := shapeLatin(text)
	// Sanity check: the missing rune is shaped as .notdef (GID 0) with a
	// non zero advance.
	var notdefAdvance fixed.Int26_6
	for _, g := range out.Glyphs {
		if g.ClusterIndex == 2 {
			tu.Assert(t, g.GlyphID == 0)
			notdefAdvance = g.Advance
		}
	}
	tu.Assert(t, notdefAdvance > 0)

	// The width fits "ab" plus part of the missing glyph: it can only stay
	// on the first line by hanging.
	width := fixed.Int26_6(1500)
	hang := HangingPunctuation{End: []rune{'世'}, EndRatio: 1}
	hung := wrapHangLines(WrapConfig{HangingPunctuation: hang}, width, text, []Output{out})
	_, end := lineRunes(hung[0].Line)
	tu.Assert(t, end == 4) // "ab世 "
	// The missing glyph hangs beyond the frame by the actual overflow,
	// capped by its .notdef advance.
	tu.Assert(t, hung[0].EndOverhang > 0)
	tu.Assert(t, hung[0].EndOverhang <= notdefAdvance)
	tu.Assert(t, hung[0].EndOverhang == lineAdvance(hung[0].Line)-width)
	// Without hanging, the missing glyph does not fit on the first line.
	plain := wrapHangLines(WrapConfig{}, width, text, []Output{out})
	_, end = lineRunes(plain[0].Line)
	tu.Assert(t, end == 2) // "ab"
}

// TestHangingPunctuationMixedFonts checks that the hang is measured on the
// glyphs of the run containing the punctuation, whatever its font and size.
func TestHangingPunctuationMixedFonts(t *testing.T) {
	text := []rune("ab, cd")
	var shaper HarfbuzzShaper
	first := shaper.Shape(Input{
		Text: text, RunStart: 0, RunEnd: 3, // "ab,"
		Direction: di.DirectionLTR, Face: benchEnFace, Size: fixed.I(16),
		Script: language.Latin, Language: language.NewLanguage("EN"),
	})
	second := shaper.Shape(Input{
		Text: text, RunStart: 3, RunEnd: 6, // " cd" in a different font and size
		Direction: di.DirectionLTR, Face: benchArFace, Size: fixed.I(24),
		Script: language.Latin, Language: language.NewLanguage("EN"),
	})
	runs := []Output{first, second}

	// Width fitting "ab" exactly: the comma must hang to stay on the line.
	width := first.Glyphs[0].Advance + first.Glyphs[1].Advance
	hang := HangingPunctuation{End: []rune{','}, EndRatio: 1}
	hung := wrapHangLines(WrapConfig{HangingPunctuation: hang}, width, text, runs)
	tu.Assert(t, len(hung) >= 1)
	start, end := lineRunes(hung[0].Line)
	tu.Assert(t, start == 0 && end == 4) // "ab, "

	commaAdvance := first.Glyphs[2].Advance
	tu.Assert(t, hung[0].EndOverhang == commaAdvance)
	tu.Assert(t, lineAdvance(hung[0].Line) == width+commaAdvance)
}

// TestHangingPunctuationWrapParagraph checks that the WrapParagraph entry
// point is stable with hanging enabled, including its single run fast path.
func TestHangingPunctuationWrapParagraph(t *testing.T) {
	short := []rune("Hi, there.")
	out := shapeLatin(short)
	hang := HangingPunctuation{End: []rune{',', '.'}, EndRatio: 1}
	var l LineWrapper
	lines, truncated := l.WrapParagraph(WrapConfig{HangingPunctuation: hang}, 1000, short, NewSliceIterator([]Output{out}))
	tu.Assert(t, truncated == 0)
	tu.Assert(t, len(lines) == 1)

	// A paragraph which only fits thanks to the hang is wrapped on one line.
	text := []rune("aa bb,")
	out = shapeLatin(text)
	width := fixed.Int26_6(2565) // "aa bb"
	lines, _ = l.WrapParagraphF(WrapConfig{HangingPunctuation: hang}, width, text, NewSliceIterator([]Output{out}))
	tu.Assert(t, len(lines) == 1)
	// Without hanging it takes two lines.
	lines, _ = l.WrapParagraphF(WrapConfig{}, width, text, NewSliceIterator([]Output{out}))
	tu.Assert(t, len(lines) == 2)
}
