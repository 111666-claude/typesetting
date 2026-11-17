// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"reflect"
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

// mapHyphenator is a deterministic [Hyphenator] returning the absolute
// (within the provided word) offsets stored in breaks, keyed by the word
// content. Offsets are returned in the given order so that ordering handling
// can be exercised.
type mapHyphenator struct {
	breaks     map[string][]int
	calls      int
	lastWord   []rune
	lastOffset int
}

func (m *mapHyphenator) BreakOpportunities(word []rune, wordOffset int) []int {
	m.calls++
	// Keep a copy because the segmenter owns the underlying slice.
	m.lastWord = append([]rune(nil), word...)
	m.lastOffset = wordOffset
	return m.breaks[string(word)]
}

// syntheticWordOutput builds a single-run Output with one 10-unit glyph per
// rune and a 1:1 rune/glyph/cluster mapping, matching the convention used by
// the other wrapping tests.
func syntheticWordOutput(word string) Output {
	runes := []rune(word)
	out := Output{
		Advance: fixed.I(10 * len(runes)),
		LineBounds: Bounds{
			Ascent:  fixed.I(10),
			Descent: fixed.I(5),
		},
		GlyphBounds: Bounds{
			Ascent: fixed.I(10),
		},
		Glyphs: glyphs(0, len(runes)-1),
		Runes:  Range{Count: len(runes)},
	}
	return out
}

// syntheticHyphen returns a shaped hyphen made of one glyph advancing by
// hyphenAdvance units and representing no source rune.
func syntheticHyphen(hyphenAdvance int) Output {
	return Output{
		Advance: fixed.I(hyphenAdvance),
		LineBounds: Bounds{
			Ascent:  fixed.I(10),
			Descent: fixed.I(5),
		},
		GlyphBounds: Bounds{
			Ascent: fixed.I(10),
		},
		Glyphs: []Glyph{
			{
				Width:        fixed.I(hyphenAdvance),
				Height:       fixed.I(10),
				Advance:      fixed.I(hyphenAdvance),
				YBearing:     fixed.I(10),
				ClusterIndex: 0,
				GlyphCount:   1,
				RuneCount:    1,
			},
		},
		Runes: Range{Count: 0},
	}
}

// lineWidth sums the advances of the runs on a line.
func lineWidth(line Line) fixed.Int26_6 {
	var w fixed.Int26_6
	for _, run := range line {
		w += run.Advance
	}
	return w
}

// TestHyphenOpportunitiesValidation exercises the filtering performed by
// [breaker.hyphenOpportunities]: illegal indices, first/last positions,
// duplicates and unsorted inputs all have a deterministic outcome.
func TestHyphenOpportunitiesValidation(t *testing.T) {
	word := "hyphenation" // 11 runes, one segment covering offsets 0..10
	runes := []rune(word)

	type testcase struct {
		name     string
		given    []int
		expected []int // absolute breakAtRune indices (offset n maps to n-1)
	}
	for _, tc := range []testcase{
		{
			name:     "empty candidates",
			given:    nil,
			expected: nil,
		},
		{
			name:     "all valid",
			given:    []int{2, 5, 8},
			expected: []int{1, 4, 7},
		},
		{
			name:     "illegal and first/last indices are dropped",
			given:    []int{-1, 0, 11, 12, 3},
			expected: []int{2},
		},
		{
			name:     "duplicates removed and sorted",
			given:    []int{7, 3, 3, 1, 7},
			expected: []int{0, 2, 6},
		},
		{
			name:     "only illegal indices",
			given:    []int{0, 11, -4},
			expected: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seg segmenter.Segmenter
			seg.Init(runes)
			br := newBreaker(&seg)
			// Consume the only segment so that currentSegmentText is set.
			_, ok := br.nextWordRaw()
			tu.Assert(t, ok)
			h := &mapHyphenator{breaks: map[string][]int{word: tc.given}}
			got := br.hyphenOpportunities(h)
			if !reflect.DeepEqual(got, tc.expected) {
				t.Fatalf("expected %v, got %v", tc.expected, got)
			}
			// The hyphenator receives the exact UAX#14 segment and its paragraph offset.
			if string(h.lastWord) != word || h.lastOffset != 0 {
				t.Fatalf("hyphenator called with word=%q offset=%d", string(h.lastWord), h.lastOffset)
			}
			// Results are cached per segment: a second call does not invoke the
			// Hyphenator again.
			calls := h.calls
			_ = br.hyphenOpportunities(h)
			if h.calls != calls {
				t.Fatalf("expected cached opportunities, got %d calls instead of %d", h.calls, calls)
			}
		})
	}
}

// TestHyphenationSynthetic uses fixed-advance synthetic glyphs so that line
// width arithmetic is exact and verifiable.
func TestHyphenationSynthetic(t *testing.T) {
	const word = "longword" // 8 runes, each advancing 10 units
	runes := []rune(word)
	shaped := []Output{syntheticWordOutput(word)}
	hyphen := syntheticHyphen(6)

	config := func(h *mapHyphenator) WrapConfig {
		return WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphen}
	}

	t.Run("hyphenates when word break does not fit", func(t *testing.T) {
		// Width 42: the full word (80) does not fit. An offset of 4 keeps 4
		// runes (40) plus the hyphen (6) = 46, which overflows; an offset of 3
		// keeps 3 runes (30) + 6 = 36 and fits. The remainder still overflows
		// and is hyphenated in turn.
		h := &mapHyphenator{breaks: map[string][]int{word: {2, 3, 4, 6}}}
		var l LineWrapper
		lines, _ := l.WrapParagraph(config(h), 42, runes, NewSliceIterator(cloneOutputs(shaped)))
		if len(lines) < 2 {
			t.Fatalf("expected at least 2 lines, got %d", len(lines))
		}
		// First line: 3 word glyphs + the hyphen run.
		first := lines[0]
		if len(first) != 2 {
			t.Fatalf("expected 2 runs on first line (fragment + hyphen), got %d", len(first))
		}
		if len(first[0].Glyphs) != 3 {
			t.Fatalf("expected 3 fragment glyphs, got %d", len(first[0].Glyphs))
		}
		if lineWidth(first) != fixed.I(36) {
			t.Fatalf("expected first line width 36, got %d", lineWidth(first))
		}
		if lineWidth(first) > fixed.I(42) {
			t.Fatalf("first line overflows: %d > 42", lineWidth(first))
		}
		if !reflect.DeepEqual(first[1].Glyphs, hyphen.Glyphs) {
			t.Fatalf("expected the shaped hyphen as last run, got %#v", first[1])
		}
		if first[1].Runes.Count != 0 {
			t.Fatalf("hyphen run must cover no source runes, got %#v", first[1].Runes)
		}
		// The runes on the second line start at source index 3, with unchanged
		// glyph cluster indices.
		second := lines[1]
		if g := second[0].Glyphs[0]; g.ClusterIndex != 3 {
			t.Fatalf("expected first remainder glyph cluster 3, got %d", g.ClusterIndex)
		}
		if second[0].Runes.Offset != 3 {
			t.Fatalf("expected remainder offset 3, got %d", second[0].Runes.Offset)
		}
	})

	t.Run("picks largest fitting fragment", func(t *testing.T) {
		// Width 46: fragment of 4 runes (40) + hyphen (6) = 46 fits exactly;
		// the 5-rune fragment (56) overflows.
		h := &mapHyphenator{breaks: map[string][]int{word: {2, 3, 4, 5}}}
		var l LineWrapper
		lines, _ := l.WrapParagraph(config(h), 46, runes, NewSliceIterator(cloneOutputs(shaped)))
		if len(lines) < 2 {
			t.Fatalf("expected at least 2 lines, got %d", len(lines))
		}
		if got := len(lines[0][0].Glyphs); got != 4 {
			t.Fatalf("expected 4 fragment glyphs, got %d", got)
		}
		if lineWidth(lines[0]) != fixed.I(46) {
			t.Fatalf("expected width 46, got %d", lineWidth(lines[0]))
		}
	})

	t.Run("no hyphenation when word fits", func(t *testing.T) {
		h := &mapHyphenator{breaks: map[string][]int{word: {2, 3, 4}}}
		var l LineWrapper
		lines, _ := l.WrapParagraph(config(h), 200, runes, NewSliceIterator(cloneOutputs(shaped)))
		if len(lines) != 1 {
			t.Fatalf("expected a single line, got %d", len(lines))
		}
		if len(lines[0]) != 1 || lines[0][0].Runes.Count != 8 {
			t.Fatalf("expected the whole word untouched, got %#v", lines[0])
		}
		if h.calls != 0 {
			t.Fatalf("hyphenator should not be consulted when the word fits, got %d calls", h.calls)
		}
	})

	t.Run("empty candidates fall back to grapheme breaking", func(t *testing.T) {
		h := &mapHyphenator{breaks: map[string][]int{word: {}}}
		var l LineWrapper
		// BreakPolicy Always allows grapheme-level breaking; width 25 fits two
		// 10-unit glyphs.
		cfg := WrapConfig{BreakPolicy: Always, Hyphenator: h, Hyphen: hyphen}
		lines, _ := l.WrapParagraph(cfg, 25, runes, NewSliceIterator(cloneOutputs(shaped)))
		// No run should be a zero-rune hyphen.
		for i, line := range lines {
			for _, run := range line {
				if run.Runes.Count == 0 {
					t.Fatalf("line %d contains an unexpected hyphen run", i)
				}
			}
		}
		if len(lines) < 2 {
			t.Fatalf("expected grapheme fallback to produce several lines, got %d", len(lines))
		}
	})

	t.Run("hyphen itself does not fit on the line", func(t *testing.T) {
		bigHyphen := syntheticHyphen(40)
		h := &mapHyphenator{breaks: map[string][]int{word: {1, 2}}}
		var l LineWrapper
		cfg := WrapConfig{BreakPolicy: Always, Hyphenator: h, Hyphen: bigHyphen}
		// Width 25: even one glyph (10) + hyphen (40) overflows, so no
		// hyphenation break can be taken.
		lines, _ := l.WrapParagraph(cfg, 25, runes, NewSliceIterator(cloneOutputs(shaped)))
		for i, line := range lines {
			for _, run := range line {
				if run.Runes.Count == 0 {
					t.Fatalf("line %d contains an unexpected hyphen run", i)
				}
			}
		}
	})

	t.Run("extremely narrow width", func(t *testing.T) {
		// Width smaller than a single glyph plus the hyphen.
		h := &mapHyphenator{breaks: map[string][]int{word: {1, 2, 3}}}
		var l LineWrapper
		cfg := WrapConfig{BreakPolicy: Always, Hyphenator: h, Hyphen: hyphen}
		lines, _ := l.WrapParagraph(cfg, 5, runes, NewSliceIterator(cloneOutputs(shaped)))
		// Every source rune is still accounted for, and no hyphen is drawn.
		total := 0
		for i, line := range lines {
			for _, run := range line {
				if run.Runes.Count == 0 {
					t.Fatalf("line %d contains an unexpected hyphen run", i)
				}
				total += run.Runes.Count
			}
		}
		if total != len(runes) {
			t.Fatalf("lost runes: accounted %d of %d", total, len(runes))
		}
	})

	t.Run("disabled without hyphen glyph", func(t *testing.T) {
		h := &mapHyphenator{breaks: map[string][]int{word: {2, 3}}}
		var l LineWrapper
		cfg := WrapConfig{BreakPolicy: Always, Hyphenator: h} // no Hyphen
		lines, _ := l.WrapParagraph(cfg, 25, runes, NewSliceIterator(cloneOutputs(shaped)))
		for i, line := range lines {
			for _, run := range line {
				if run.Runes.Count == 0 {
					t.Fatalf("line %d contains an unexpected hyphen run", i)
				}
			}
		}
	})

	t.Run("disabled without hyphenator", func(t *testing.T) {
		var l LineWrapper
		cfg := WrapConfig{BreakPolicy: Never, Hyphen: hyphen}
		lines, _ := l.WrapParagraph(cfg, 25, runes, NewSliceIterator(cloneOutputs(shaped)))
		// With Never and no hyphenator, the single word overflows the line.
		if len(lines) != 1 || lines[0][0].Runes.Count != 8 {
			t.Fatalf("expected the unbreakable word on one line, got %#v", lines)
		}
	})

	t.Run("suppressed while truncating", func(t *testing.T) {
		// A single truncated line never hyphenates: the truncator, not the
		// hyphen, is the terminal run of the line.
		truncator := syntheticHyphen(4)
		h := &mapHyphenator{breaks: map[string][]int{word: {2, 3, 4}}}
		var l LineWrapper
		cfg := WrapConfig{
			BreakPolicy:        Never,
			Hyphenator:         h,
			Hyphen:             hyphen,
			TruncateAfterLines: 1,
			Truncator:          truncator,
		}
		lines, truncated := l.WrapParagraph(cfg, 25, runes, NewSliceIterator(cloneOutputs(shaped)))
		if truncated == 0 {
			t.Fatal("expected truncation")
		}
		lastRun := lines[len(lines)-1][len(lines[len(lines)-1])-1]
		if !reflect.DeepEqual(lastRun.Glyphs, truncator.Glyphs) {
			t.Fatalf("expected truncator as the last run, got %#v", lastRun)
		}
		// No hyphen run (zero source runes) may appear.
		for i, line := range lines {
			for _, run := range line {
				if run.Runes.Count == 0 && reflect.DeepEqual(run.Glyphs, hyphen.Glyphs) {
					t.Fatalf("line %d unexpectedly contains a hyphen while truncating", i)
				}
			}
		}
	})
}

// TestHyphenationRuneAccounting verifies that hyphenation keeps the source text
// indices and glyph cluster mapping stable: every source rune appears exactly
// once, in order, across all lines (the hyphen run contributes none).
func TestHyphenationRuneAccounting(t *testing.T) {
	word := "hyphenation" // 11 runes
	runes := []rune(word)
	shaped := []Output{syntheticWordOutput(word)}
	hyphen := syntheticHyphen(6)
	h := &mapHyphenator{breaks: map[string][]int{word: {2, 4, 6, 8}}}

	var l LineWrapper
	lines, truncated := l.WrapParagraph(
		WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphen},
		46, runes, NewSliceIterator(cloneOutputs(shaped)),
	)
	if truncated != 0 {
		t.Fatalf("did not expect truncation, got %d", truncated)
	}
	nextExpected := 0
	hyphenLines := 0
	for lineIdx, line := range lines {
		for runIdx, run := range line {
			if run.Runes.Count == 0 {
				// The only zero-rune run is the hyphen, always last on its line.
				if runIdx != len(line)-1 {
					t.Fatalf("line %d: hyphen run is not the last run", lineIdx)
				}
				hyphenLines++
				continue
			}
			if run.Runes.Offset != nextExpected {
				t.Fatalf("line %d: expected rune offset %d, got %d", lineIdx, nextExpected, run.Runes.Offset)
			}
			// Clusters of the fragment must match their source rune indices.
			for _, g := range run.Glyphs {
				if g.ClusterIndex != nextExpected {
					t.Fatalf("line %d: expected cluster %d, got %d", lineIdx, nextExpected, g.ClusterIndex)
				}
				nextExpected++
			}
		}
	}
	if nextExpected != len(runes) {
		t.Fatalf("accounted %d of %d runes", nextExpected, len(runes))
	}
	if hyphenLines == 0 {
		t.Fatal("expected at least one hyphenated line")
	}
}

// TestHyphenationOnlyAtActualBreak verifies that a hyphen is drawn solely on
// lines where a hyphenation break is taken. A multi-word paragraph where every
// word fits must contain no hyphen run.
func TestHyphenationOnlyAtActualBreak(t *testing.T) {
	text := []rune("aa bb cc")
	// 8 runes, one 10-unit glyph each.
	shaped := []Output{syntheticWordOutput(string(text))}
	hyphen := syntheticHyphen(6)
	h := &mapHyphenator{breaks: map[string][]int{
		"aa ": {1},
		"bb ": {1},
		"cc":  {1},
	}}
	var l LineWrapper
	lines, _ := l.WrapParagraph(
		WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphen},
		300, text, NewSliceIterator(cloneOutputs(shaped)),
	)
	for i, line := range lines {
		for _, run := range line {
			if run.Runes.Count == 0 {
				t.Fatalf("line %d unexpectedly contains a hyphen run", i)
			}
		}
	}
}

// TestHyphenationRTLDisabled ensures RTL segments are never hyphenated and the
// regular breaking behavior is preserved.
func TestHyphenationRTLDisabled(t *testing.T) {
	word := "longword"
	runes := []rune(word)
	out := syntheticWordOutput(word)
	out.Direction = di.DirectionRTL
	// Reverse the glyph order to mimic RTL shaping.
	for i, j := 0, len(out.Glyphs)-1; i < j; i, j = i+1, j-1 {
		out.Glyphs[i], out.Glyphs[j] = out.Glyphs[j], out.Glyphs[i]
	}
	hyphen := syntheticHyphen(6)
	h := &mapHyphenator{breaks: map[string][]int{word: {2, 3, 4}}}

	var ltr, rtl LineWrapper
	ltrLines, _ := ltr.WrapParagraph(
		WrapConfig{Direction: di.DirectionLTR, BreakPolicy: Never, Hyphenator: h, Hyphen: hyphen},
		42, runes, NewSliceIterator([]Output{syntheticWordOutput(word)}),
	)
	rtlLines, _ := rtl.WrapParagraph(
		WrapConfig{Direction: di.DirectionRTL, BreakPolicy: Never, Hyphenator: h, Hyphen: hyphen},
		42, runes, NewSliceIterator([]Output{out}),
	)
	// The LTR paragraph should have hyphenated.
	ltrHasHyphen := false
	for _, line := range ltrLines {
		for _, run := range line {
			if run.Runes.Count == 0 {
				ltrHasHyphen = true
			}
		}
	}
	if !ltrHasHyphen {
		t.Fatal("expected LTR hyphenation to occur")
	}
	// The RTL paragraph must not contain any hyphen run, and must keep the
	// single word intact because BreakPolicy is Never.
	if len(rtlLines) != 1 || len(rtlLines[0]) != 1 || rtlLines[0][0].Runes.Count != 8 {
		t.Fatalf("expected unhyphenated RTL word, got %#v", rtlLines)
	}
	if h.calls == 0 {
		t.Fatal("hyphenator should still have been consulted for the LTR case")
	}
}

// TestHyphenationE2E shapes real text with a fixed font and checks the produced
// line widths and the read-back content of each line, including ordinary spaces
// and mixed-script samples.
func TestHyphenationE2E(t *testing.T) {
	face := benchEnFace
	var shaper HarfbuzzShaper
	shape := func(text []rune) Output {
		return shaper.Shape(Input{
			Text:      text,
			RunStart:  0,
			RunEnd:    len(text),
			Direction: di.DirectionLTR,
			Face:      face,
			Size:      fixed.I(16),
			Script:    language.Latin,
			Language:  language.NewLanguage("EN"),
		})
	}
	hyphenOut := shaper.Shape(Input{
		Text:      []rune("-"),
		RunStart:  0,
		RunEnd:    1,
		Direction: di.DirectionLTR,
		Face:      face,
		Size:      fixed.I(16),
		Script:    language.Latin,
		Language:  language.NewLanguage("EN"),
	})

	t.Run("long latin word", func(t *testing.T) {
		text := []rune("internationalization")
		out := shape(text)
		// Offer a break after every rune except the first and last.
		all := make([]int, 0, len(text)-2)
		for i := 1; i < len(text); i++ {
			all = append(all, i)
		}
		h := &mapHyphenator{breaks: map[string][]int{string(text): all}}

		const maxWidth = 90
		var l LineWrapper
		lines, truncated := l.WrapParagraph(
			WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphenOut},
			maxWidth, text, NewSliceIterator([]Output{out}),
		)
		tu.Assert(t, truncated == 0)
		tu.Assert(t, len(lines) > 1)

		// Read back each line, removing the visual hyphen, and ensure the
		// concatenation reproduces the source text.
		var readBack []rune
		hyphenated := 0
		for lineIdx, line := range lines {
			if w := lineWidth(line); w > fixed.I(maxWidth) {
				t.Fatalf("line %d overflows: %d > %d", lineIdx, w, maxWidth)
			}
			lastIsHyphen := line[len(line)-1].Runes.Count == 0
			for _, run := range line {
				for _, g := range run.Glyphs {
					if run.Runes.Count == 0 {
						continue
					}
					readBack = append(readBack, text[g.ClusterIndex])
				}
			}
			if lastIsHyphen {
				hyphenated++
				if line[len(line)-1].Glyphs[0].GlyphID != hyphenOut.Glyphs[0].GlyphID {
					t.Fatalf("line %d: last run is not the configured hyphen", lineIdx)
				}
			}
		}
		if string(readBack) != string(text) {
			t.Fatalf("read-back mismatch:\nwant %q\n got %q", string(text), string(readBack))
		}
		if hyphenated == 0 {
			t.Fatal("expected at least one hyphenated line")
		}
	})

	t.Run("words separated by spaces only break where needed", func(t *testing.T) {
		text := []rune("the internationalization box")
		out := shape(text)
		// Only provide opportunities inside the long middle word.
		var segBreak []int
		middle := "internationalization "
		for i := 1; i < len(middle); i++ {
			segBreak = append(segBreak, i)
		}
		h := &mapHyphenator{breaks: map[string][]int{
			"the ": nil,
			middle: segBreak,
			"box":  nil,
		}}

		var baseline, wrapped LineWrapper
		baseLines, _ := baseline.WrapParagraph(
			WrapConfig{BreakPolicy: Never}, 400, text, NewSliceIterator([]Output{out.copy()}),
		)
		tu.Assert(t, len(baseLines) == 1)

		lines, _ := wrapped.WrapParagraph(
			WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphenOut},
			400, text, NewSliceIterator([]Output{out.copy()}),
		)
		// Everything fits at this width, so no hyphenation occurs.
		if len(lines) != 1 {
			t.Fatalf("expected one line at wide width, got %d", len(lines))
		}
		for _, run := range lines[0] {
			if run.Runes.Count == 0 {
				t.Fatal("unexpected hyphen at wide width")
			}
		}

		// At a narrow width the long word must hyphenate, while "the " and
		// "box" remain intact as whole fragments.
		var narrow LineWrapper
		narrowLines, _ := narrow.WrapParagraph(
			WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphenOut},
			95, text, NewSliceIterator([]Output{out.copy()}),
		)
		var readBack []rune
		sawHyphen := false
		for lineIdx, line := range narrowLines {
			if w := lineWidth(line); w > fixed.I(95) {
				t.Fatalf("line %d overflows: %d > 95", lineIdx, w)
			}
			for _, run := range line {
				for _, g := range run.Glyphs {
					if run.Runes.Count == 0 {
						sawHyphen = true
						continue
					}
					readBack = append(readBack, text[g.ClusterIndex])
				}
			}
		}
		if !sawHyphen {
			t.Fatal("expected hyphenation at narrow width")
		}
		if string(readBack) != string(text) {
			t.Fatalf("read-back mismatch:\nwant %q\n got %q", string(text), string(readBack))
		}
	})

	t.Run("mixed sample keeps every rune", func(t *testing.T) {
		text := []rune("internationalization 混排")
		out := shape(text)
		all := make([]int, 0, len("internationalization")-1)
		for i := 1; i < len("internationalization"); i++ {
			all = append(all, i)
		}
		h := &mapHyphenator{breaks: map[string][]int{
			"internationalization 混排": all,
		}}
		var l LineWrapper
		lines, _ := l.WrapParagraph(
			WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphenOut},
			100, text, NewSliceIterator([]Output{out}),
		)
		var readBack []rune
		for _, line := range lines {
			for _, run := range line {
				for _, g := range run.Glyphs {
					if run.Runes.Count == 0 {
						continue
					}
					readBack = append(readBack, text[g.ClusterIndex])
				}
			}
		}
		if string(readBack) != string(text) {
			t.Fatalf("read-back mismatch:\nwant %q\n got %q", string(text), string(readBack))
		}
	})
}

// cloneOutputs deep-copies shaped outputs so that repeated wrapping with the
// same input does not share mutable glyph slices.
func cloneOutputs(outs []Output) []Output {
	cp := make([]Output, len(outs))
	for i := range outs {
		cp[i] = outs[i].copy()
	}
	return cp
}

// TestHyphenationSegmentOffset verifies that opportunities requested for a word
// located after other words use the correct paragraph-relative offsets.
func TestHyphenationSegmentOffset(t *testing.T) {
	text := []rune("aa longword") // "aa " is the first segment; "longword" is
	// the second segment, starting at paragraph offset 3.
	shaped := []Output{syntheticWordOutput(string(text))}
	hyphen := syntheticHyphen(6)
	// Only the second segment is hyphenatable; offsets are relative to the
	// "longword" segment.
	h := &mapHyphenator{breaks: map[string][]int{
		"aa ":      nil,
		"longword": {3},
	}}
	var seg segmenter.Segmenter
	seg.Init(text)
	var l LineWrapper
	l.PrepareSegmented(
		WrapConfig{BreakPolicy: Never, Hyphenator: h, Hyphen: hyphen},
		&seg, NewSliceIterator(cloneOutputs(shaped)),
	)
	// Width 50: line 0 holds "aa " (30, with its trailing space trimmed at the
	// line end). "longword" (80) then overflows line 1 and is hyphenated. The
	// relative offset 3 means "keep word[:3]", a 3-rune fragment (30) that plus
	// the hyphen (6) equals 36. The fragment starts at paragraph offset 3, so
	// its last rune is absolute index 3+2 = 5 and the remainder begins at 6.
	// Width 50 also leaves room for the final 5-rune remainder (50).
	const width = 50
	var lines []Line
	for {
		line, done := l.WrapNextLine(width)
		if line.Line != nil {
			lines = append(lines, line.Line)
		}
		if done {
			break
		}
	}
	// The hyphen must appear on a line after the first, proving that the
	// paragraph-relative segment offset was handled correctly.
	hyphenLine := -1
	for i, line := range lines {
		for _, run := range line {
			if run.Runes.Count == 0 {
				hyphenLine = i
			}
		}
	}
	if hyphenLine <= 0 {
		t.Fatalf("expected a hyphen on a line after the first, got line %d", hyphenLine)
	}
	// Every source rune is accounted for exactly once and no line overflows.
	total := 0
	for _, line := range lines {
		if w := lineWidth(line); w > fixed.I(width) {
			t.Fatalf("line overflows: %d > %d", w, width)
		}
		for _, run := range line {
			total += run.Runes.Count
		}
	}
	if total != len(text) {
		t.Fatalf("accounted %d of %d runes", total, len(text))
	}
	// The hyphenator must have seen "longword" with its absolute offset 3.
	if string(h.lastWord) != "longword" || h.lastOffset != 3 {
		t.Fatalf("expected last call with longword at offset 3, got word=%q offset=%d",
			string(h.lastWord), h.lastOffset)
	}
}

// TestHyphenationPrepareSegmentedParity checks that Prepare and
// PrepareSegmented produce identical hyphenated lines.
func TestHyphenationPrepareSegmentedParity(t *testing.T) {
	text := []rune("the internationalization box")
	face := benchEnFace
	var shaper HarfbuzzShaper
	hyphenOut := shaper.Shape(Input{
		Text:      []rune("-"),
		RunStart:  0,
		RunEnd:    1,
		Direction: di.DirectionLTR,
		Face:      face,
		Size:      fixed.I(16),
		Script:    language.Latin,
		Language:  language.NewLanguage("EN"),
	})
	middle := "internationalization "
	breaks := make([]int, 0, len(middle)-1)
	for i := 1; i < len(middle); i++ {
		breaks = append(breaks, i)
	}
	newConfig := func() WrapConfig {
		return WrapConfig{
			BreakPolicy: Never,
			Hyphenator:  &mapHyphenator{breaks: map[string][]int{middle: breaks}},
			Hyphen:      hyphenOut,
		}
	}
	shapeParagraph := func() Output {
		return shaper.Shape(Input{
			Text:      text,
			RunStart:  0,
			RunEnd:    len(text),
			Direction: di.DirectionLTR,
			Face:      face,
			Size:      fixed.I(16),
			Script:    language.Latin,
			Language:  language.NewLanguage("EN"),
		})
	}

	var fresh LineWrapper
	fresh.Prepare(newConfig(), text, NewSliceIterator([]Output{shapeParagraph()}))
	expected := wrapAllLines(&fresh, []int{95})

	var seg segmenter.Segmenter
	seg.Init(text)
	var segmented LineWrapper
	segmented.PrepareSegmented(newConfig(), &seg, NewSliceIterator([]Output{shapeParagraph()}))
	got := wrapAllLines(&segmented, []int{95})

	if !reflect.DeepEqual(expected, got) {
		t.Fatalf("Prepare and PrepareSegmented disagree on hyphenated output")
	}
}
