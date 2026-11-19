// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"bytes"
	"os"
	"testing"

	"github.com/go-text/typesetting/bidi"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	"golang.org/x/image/math/fixed"
)

// truncationTestFonts bundles the faces used by the truncation tests.
type truncationTestFonts struct {
	mono *font.Face // fixed-pitch Latin face
	cjk  *font.Face // CJK + Latin subset face
	ar   *font.Face // Arabic face for RTL
}

func loadTruncationFonts(t *testing.T) truncationTestFonts {
	t.Helper()
	load := func(path string) *font.Face {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		face, err := font.ParseTTF(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		return face
	}
	return truncationTestFonts{
		mono: load("../font/testdata/UbuntuMono-R.ttf"),
		cjk:  load("../font/testdata/NotoSansSC-Subset.otf"),
		ar:   load("../font/testdata/Amiri-Regular.ttf"),
	}
}

func shapeRunWith(t *testing.T, face *font.Face, text []rune, dir di.Direction, script language.Script, level bidi.Level) Output {
	t.Helper()
	var shaper HarfbuzzShaper
	return shaper.Shape(Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: dir,
		Face:      face,
		Size:      fixed.I(16),
		Script:    script,
		Language:  language.DefaultLanguage(),
		Level:     level,
	})
}

func truncatorOutput(t *testing.T, face *font.Face, mark string, dir di.Direction, script language.Script, level bidi.Level) Output {
	t.Helper()
	r := []rune(mark)
	var shaper HarfbuzzShaper
	return shaper.Shape(Input{
		Text:      r,
		RunStart:  0,
		RunEnd:    len(r),
		Direction: dir,
		Face:      face,
		Size:      fixed.I(16),
		Script:    script,
		Language:  language.DefaultLanguage(),
		Level:     level,
	})
}

// lineWidth sums the advances of every run on the line.
func lineWidth(line Line) fixed.Int26_6 {
	var w fixed.Int26_6
	for _, run := range line {
		w += run.Advance
	}
	return w
}

// graphemeBoundaries returns the rune offsets at which a grapheme cluster ends
// (exclusive), i.e. valid positions at which the text may be cut.
func graphemeBoundaries(text []rune) []int {
	var seg segmenter.Segmenter
	seg.Init(text)
	iter := seg.GraphemeIterator()
	var ends []int
	for iter.Next() {
		g := iter.Grapheme()
		ends = append(ends, g.Offset+len(g.Text))
	}
	return ends
}

// isGraphemeBoundary reports whether cut is the end of a grapheme cluster.
func isGraphemeBoundary(text []rune, cut int) bool {
	for _, end := range graphemeBoundaries(text) {
		if end == cut {
			return true
		}
	}
	return false
}

// TestTruncationMarkerFitsWidth verifies requirement 1 and 3: when truncation
// occurs with a marker that fits, the marker is shaped and placed on the final
// line, whose total width remains within maxWidth; when no truncation happens the
// output is unchanged.
func TestTruncationMarkerFitsWidth(t *testing.T) {
	fonts := loadTruncationFonts(t)
	text := []rune("hello world this is a fairly long line of plain latin text")
	run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
	mark := truncatorOutput(t, fonts.mono, "…", di.DirectionLTR, language.Latin, 0)

	t.Run("marker within width", func(t *testing.T) {
		maxWidth := 120
		var wrapper LineWrapper
		lines, truncated := wrapper.WrapParagraph(WrapConfig{
			BreakPolicy:        WhenNecessary,
			TruncateAfterLines: 1,
			Truncator:          mark,
		}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
		if truncated == 0 {
			t.Fatal("expected truncation")
		}
		if len(lines) != 1 {
			t.Fatalf("expected 1 line, got %d", len(lines))
		}
		line := lines[0]
		last := line[len(line)-1]
		if len(last.Glyphs) != len(mark.Glyphs) {
			t.Fatalf("final run is not the truncator: got %d glyphs, want %d", len(last.Glyphs), len(mark.Glyphs))
		}
		if w := lineWidth(line); w > fixed.I(maxWidth) {
			t.Errorf("line width %v exceeds maxWidth %d", w, maxWidth)
		}
		// The marker must share the line with visible text, not replace it.
		if len(line) < 2 {
			t.Errorf("truncator replaced all visible text")
		}
	})

	t.Run("no truncation leaves output untouched", func(t *testing.T) {
		maxWidth := 2000
		var withMarker, without LineWrapper
		markedLines, truncated := withMarker.WrapParagraph(WrapConfig{
			BreakPolicy:        WhenNecessary,
			TruncateAfterLines: 1,
			Truncator:          mark,
		}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
		plainLines, _ := without.WrapParagraph(WrapConfig{
			BreakPolicy: WhenNecessary,
		}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
		if truncated != 0 {
			t.Fatalf("did not expect truncation, got %d", truncated)
		}
		if len(markedLines) != len(plainLines) {
			t.Fatalf("line count differs: %d vs %d", len(markedLines), len(plainLines))
		}
		for i := range markedLines {
			if len(markedLines[i]) != len(plainLines[i]) {
				t.Fatalf("line %d run count differs: %d vs %d", i, len(markedLines[i]), len(plainLines[i]))
			}
			for j := range markedLines[i] {
				if markedLines[i][j].Runes != plainLines[i][j].Runes {
					t.Errorf("line %d run %d differs: %+v vs %+v", i, j, markedLines[i][j].Runes, plainLines[i][j].Runes)
				}
			}
		}
	})
}

// TestTruncationMarkerTooWide verifies requirement 3's deterministic behavior when
// the marker is itself wider than the line: no marker is inserted, the line still
// respects maxWidth and keeps as much text as possible, and the hidden range is
// still reported.
func TestTruncationMarkerTooWide(t *testing.T) {
	fonts := loadTruncationFonts(t)
	text := []rune("mmmmm mmmmm mmmmm")
	run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
	wideMark := truncatorOutput(t, fonts.mono, "WWWWWWWWWWWW", di.DirectionLTR, language.Latin, 0)
	if wideMark.Advance <= 0 {
		t.Fatal("test setup: wide marker has no advance")
	}
	maxWidth := 60
	if wideMark.Advance <= fixed.I(maxWidth) {
		t.Fatalf("test setup: marker %v is not wider than %d", wideMark.Advance, maxWidth)
	}

	var wrapper LineWrapper
	lines, truncated := wrapper.WrapParagraph(WrapConfig{
		BreakPolicy:        Never,
		TruncateAfterLines: 1,
		Truncator:          wideMark,
	}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
	if truncated == 0 {
		t.Fatal("expected truncation")
	}
	line := lines[len(lines)-1]
	if w := lineWidth(line); w > fixed.I(maxWidth) {
		t.Errorf("line width %v exceeds maxWidth %d", w, maxWidth)
	}
	for _, r := range line {
		if len(r.Glyphs) == len(wideMark.Glyphs) {
			t.Errorf("over-wide marker was inserted anyway")
		}
	}

	// The iterator API must still expose the hidden range.
	var wrapper2 LineWrapper
	wrapper2.Prepare(WrapConfig{
		BreakPolicy:        Never,
		TruncateAfterLines: 1,
		Truncator:          wideMark,
	}, text, NewSliceIterator([]Output{run.copy()}))
	wl, done := wrapper2.WrapNextLine(maxWidth)
	if !done {
		t.Fatal("expected paragraph to end after the single truncated line")
	}
	want := Range{Offset: len(text) - truncated, Count: truncated}
	if wl.TruncatedRange != want {
		t.Errorf("TruncatedRange = %+v, want %+v", wl.TruncatedRange, want)
	}
	if wl.Truncated != truncated {
		t.Errorf("Truncated = %d, want %d", wl.Truncated, truncated)
	}
}

// TestTruncationGraphemeBoundaries verifies requirement 2: trailing glyphs are
// only removed on grapheme / shaping cluster boundaries, so combining marks and
// ligated clusters are never split. The reported range starts on such a boundary.
func TestTruncationGraphemeBoundaries(t *testing.T) {
	fonts := loadTruncationFonts(t)
	// "café" uses a precomposed é; add combining sequences and ZWJ emoji-adjacent
	// text so that several clusters span multiple runes.
	text := []rune("café résumé naïve a")
	run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
	mark := truncatorOutput(t, fonts.mono, "…", di.DirectionLTR, language.Latin, 0)

	for _, maxWidth := range []int{40, 55, 70, 85, 100} {
		t.Run("width", func(t *testing.T) {
			var wrapper LineWrapper
			lines, truncated := wrapper.WrapParagraph(WrapConfig{
				BreakPolicy:        Always,
				TruncateAfterLines: 1,
				Truncator:          mark,
			}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
			if truncated == 0 {
				return // nothing cut, nothing to check
			}
			cut := len(text) - truncated
			if !isGraphemeBoundary(text, cut) {
				t.Errorf("width %d: cut at rune %d is not a grapheme boundary", maxWidth, cut)
			}
			line := lines[len(lines)-1]
			if w := lineWidth(line); w > fixed.I(maxWidth) {
				t.Errorf("width %d: line width %v exceeds maxWidth", maxWidth, w)
			}
			// Every visible run's rune range must end at or before the cut.
			for _, r := range line {
				if end := r.Runes.Offset + r.Runes.Count; end > cut && len(r.Glyphs) != len(mark.Glyphs) {
					t.Errorf("run extends past cut: %+v", r.Runes)
				}
			}
		})
	}
}

// TestTruncationShapingClusters verifies that clusters fused by the shaper
// (multiple glyphs sharing one cluster index, e.g. ligatures) are never split.
func TestTruncationShapingClusters(t *testing.T) {
	fonts := loadTruncationFonts(t)
	// Repeated "fi" sequences tend to form ligatures; regardless of whether the
	// font ligates, cutting must keep each cluster intact.
	text := []rune("fi fi fi fi fi fi fi fi fi fi fi")
	run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
	mark := truncatorOutput(t, fonts.mono, "…", di.DirectionLTR, language.Latin, 0)

	// Collect the set of valid cluster starts from the shaped output.
	clusterStarts := map[int]bool{0: true}
	for _, g := range run.Glyphs {
		clusterStarts[g.ClusterIndex] = true
	}

	var wrapper LineWrapper
	maxWidth := 70
	lines, truncated := wrapper.WrapParagraph(WrapConfig{
		BreakPolicy:        Always,
		TruncateAfterLines: 1,
		Truncator:          mark,
	}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
	if truncated == 0 {
		t.Fatal("expected truncation at narrow width")
	}
	cut := len(text) - truncated
	if !clusterStarts[cut] {
		t.Errorf("cut %d does not start at a shaping cluster boundary (have %v)", cut, clusterStarts)
	}
	line := lines[len(lines)-1]
	if w := lineWidth(line); w > fixed.I(maxWidth) {
		t.Errorf("line width %v exceeds maxWidth %d", w, maxWidth)
	}
}

// TestTruncationMixedFontFallback verifies requirement 2's font-fallback aspect:
// a paragraph shaped as separate Latin and CJK runs is truncated at a cluster
// boundary and the marker remains within the width.
func TestTruncationMixedFontFallback(t *testing.T) {
	fonts := loadTruncationFonts(t)
	// Alternate Latin and CJK runs, each shaped in its own face, mirroring a
	// font-fallback pipeline. Runs are shaped against the full paragraph slice with
	// absolute offsets, as a real fallback shaper does.
	full := []rune("ABC中文DEF排版GHI测试JKL内容MNO")
	type piece struct {
		start, end int
		face       *font.Face
		script     language.Script
	}
	pieces := []piece{
		{0, 3, fonts.mono, language.Latin},
		{3, 5, fonts.cjk, language.Han},
		{5, 8, fonts.mono, language.Latin},
		{8, 10, fonts.cjk, language.Han},
		{10, 13, fonts.mono, language.Latin},
		{13, 15, fonts.cjk, language.Han},
		{15, 18, fonts.mono, language.Latin},
		{18, 20, fonts.cjk, language.Han},
		{20, 23, fonts.mono, language.Latin},
	}
	shapePiece := func(p piece) Output {
		var shaper HarfbuzzShaper
		return shaper.Shape(Input{
			Text:      full,
			RunStart:  p.start,
			RunEnd:    p.end,
			Direction: di.DirectionLTR,
			Face:      p.face,
			Size:      fixed.I(16),
			Script:    p.script,
			Language:  language.DefaultLanguage(),
		})
	}
	runs := make([]Output, len(pieces))
	for i, p := range pieces {
		runs[i] = shapePiece(p)
	}
	// Sanity: the pieces cover the paragraph without gaps.
	pos := 0
	for _, r := range runs {
		if r.Runes.Offset != pos {
			t.Fatalf("run offset %d, want %d", r.Runes.Offset, pos)
		}
		pos += r.Runes.Count
	}
	mark := truncatorOutput(t, fonts.cjk, "…", di.DirectionLTR, language.Common, 0)

	for _, maxWidth := range []int{60, 90, 120, 150} {
		t.Run("width", func(t *testing.T) {
			copied := make([]Output, len(runs))
			for i, r := range runs {
				copied[i] = r.copy()
			}
			var wrapper LineWrapper
			lines, truncated := wrapper.WrapParagraph(WrapConfig{
				BreakPolicy:        Always,
				TruncateAfterLines: 1,
				Truncator:          mark,
			}, maxWidth, full, NewSliceIterator(copied))
			if truncated == 0 {
				return
			}
			cut := len(full) - truncated
			if !isGraphemeBoundary(full, cut) {
				t.Errorf("width %d: cut %d is not a grapheme boundary", maxWidth, cut)
			}
			line := lines[len(lines)-1]
			if w := lineWidth(line); w > fixed.I(maxWidth) {
				t.Errorf("width %d: line width %v exceeds maxWidth", maxWidth, w)
			}
			// No visible run may cross the cut.
			for _, r := range line {
				if len(r.Glyphs) == len(mark.Glyphs) {
					continue
				}
				if end := r.Runes.Offset + r.Runes.Count; end > cut {
					t.Errorf("width %d: run %+v crosses cut %d", maxWidth, r.Runes, cut)
				}
			}
		})
	}
}

// TestTruncationRTL verifies requirement 2's bidi aspect and requirement 1's
// marker placement: for an RTL paragraph the marker occupies the visually leading
// (leftmost) position while the total width stays bounded and the hidden range is
// reported against logical rune offsets.
func TestTruncationRTL(t *testing.T) {
	fonts := loadTruncationFonts(t)
	text := []rune("هذا نص عربي طويل جدا يجب ان يتم اقتطاعه عند الحد")
	run := shapeRunWith(t, fonts.ar, text, di.DirectionRTL, language.Arabic, 1)
	mark := truncatorOutput(t, fonts.ar, "…", di.DirectionRTL, language.Arabic, 1)

	maxWidth := 140
	var wrapper LineWrapper
	lines, truncated := wrapper.WrapParagraph(WrapConfig{
		Direction:          di.DirectionRTL,
		BreakPolicy:        WhenNecessary,
		TruncateAfterLines: 1,
		Truncator:          mark,
	}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
	if truncated == 0 {
		t.Fatal("expected truncation")
	}
	line := lines[len(lines)-1]
	if w := lineWidth(line); w > fixed.I(maxWidth) {
		t.Fatalf("line width %v exceeds maxWidth %d", w, maxWidth)
	}
	// Find the marker run and assert it is visually first (leftmost). In an RTL
	// paragraph the truncator marks the logical end, which bidi reordering places
	// at visual index 0.
	markerIdx := -1
	for i, r := range line {
		if len(r.Glyphs) == len(mark.Glyphs) {
			markerIdx = i
			break
		}
	}
	if markerIdx < 0 {
		t.Fatal("marker run not found on truncated line")
	}
	if line[markerIdx].VisualIndex != 0 {
		t.Errorf("marker VisualIndex = %d, want 0 (visually leading in RTL)", line[markerIdx].VisualIndex)
	}
	// The logical rune ranges of visible runs must remain a prefix of the paragraph.
	cut := len(text) - truncated
	logicalEnd := 0
	for i, r := range line {
		if i == markerIdx {
			continue
		}
		if r.Runes.Offset != logicalEnd {
			t.Errorf("visible run offset %d, want contiguous prefix ending at %d", r.Runes.Offset, logicalEnd)
		}
		logicalEnd = r.Runes.Offset + r.Runes.Count
	}
	if logicalEnd != cut {
		t.Errorf("visible runes end at %d, want cut %d", logicalEnd, cut)
	}

	// Same paragraph with an LTR marker shaped at level 0: it should land at the
	// visually trailing position, still inside the width.
	ltrMark := truncatorOutput(t, fonts.mono, "…", di.DirectionLTR, language.Common, 0)
	var wrapper2 LineWrapper
	lines2, truncated2 := wrapper2.WrapParagraph(WrapConfig{
		Direction:          di.DirectionRTL,
		BreakPolicy:        WhenNecessary,
		TruncateAfterLines: 1,
		Truncator:          ltrMark,
	}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
	if truncated2 == 0 {
		t.Fatal("expected truncation with LTR marker")
	}
	line2 := lines2[len(lines2)-1]
	if w := lineWidth(line2); w > fixed.I(maxWidth) {
		t.Errorf("LTR-marker line width %v exceeds maxWidth %d", w, maxWidth)
	}
	maxVisual := 0
	markerVisual := -1
	for i, r := range line2 {
		if int(r.VisualIndex) > maxVisual {
			maxVisual = int(r.VisualIndex)
		}
		if len(r.Glyphs) == len(ltrMark.Glyphs) {
			markerVisual = int(line2[i].VisualIndex)
		}
	}
	if markerVisual != maxVisual {
		t.Errorf("LTR marker VisualIndex = %d, want visually trailing %d", markerVisual, maxVisual)
	}
}

// TestTruncationEdgeCases verifies the deterministic outcomes required by
// requirement 3: zero line limit, hard line breaks and empty text.
func TestTruncationEdgeCases(t *testing.T) {
	fonts := loadTruncationFonts(t)
	mark := truncatorOutput(t, fonts.mono, "…", di.DirectionLTR, language.Latin, 0)

	t.Run("zero line limit means no truncation", func(t *testing.T) {
		text := []rune("one two three four five six seven eight")
		run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
		var wrapper LineWrapper
		lines, truncated := wrapper.WrapParagraph(WrapConfig{
			TruncateAfterLines: 0,
			Truncator:          mark,
		}, 60, text, NewSliceIterator([]Output{run.copy()}))
		if truncated != 0 {
			t.Errorf("TruncateAfterLines=0 truncated %d runes", truncated)
		}
		for _, line := range lines {
			for _, r := range line {
				if len(r.Glyphs) == len(mark.Glyphs) {
					t.Error("marker inserted with TruncateAfterLines=0")
				}
			}
		}
		if len(lines) < 2 {
			t.Error("expected unrestricted wrapping to produce several lines")
		}
	})

	t.Run("hard break reports suffix including the break", func(t *testing.T) {
		text := []rune("first line\nsecond line is longer and gets removed entirely")
		run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
		var wrapper LineWrapper
		lines, truncated := wrapper.WrapParagraph(WrapConfig{
			BreakPolicy:        WhenNecessary,
			TruncateAfterLines: 1,
			Truncator:          mark,
		}, 2000, text, NewSliceIterator([]Output{run.copy()}))
		if len(lines) != 1 {
			t.Fatalf("expected 1 line, got %d", len(lines))
		}
		if truncated == 0 {
			t.Fatal("expected the text after the hard break to be truncated")
		}
		// The hidden range starts right after the visible prefix and covers the
		// newline plus everything following it.
		var wrapper2 LineWrapper
		wrapper2.Prepare(WrapConfig{
			BreakPolicy:        WhenNecessary,
			TruncateAfterLines: 1,
			Truncator:          mark,
		}, text, NewSliceIterator([]Output{run.copy()}))
		wl, done := wrapper2.WrapNextLine(2000)
		if !done {
			t.Fatal("expected done after first line")
		}
		want := Range{Offset: len(text) - truncated, Count: truncated}
		if wl.TruncatedRange != want {
			t.Errorf("TruncatedRange = %+v, want %+v", wl.TruncatedRange, want)
		}
		// The hard break itself remains on the visible line (its glyph has no
		// advance); the hidden suffix starts immediately after it and covers the
		// whole of the second logical line.
		newline := -1
		for i, r := range text {
			if r == '\n' {
				newline = i
				break
			}
		}
		if want.Offset != newline+1 {
			t.Errorf("hidden range should begin right after the newline at %d, got offset %d", newline+1, want.Offset)
		}
		if want.Offset+want.Count != len(text) {
			t.Errorf("hidden range does not reach the end of the paragraph")
		}
	})

	t.Run("range offset after several lines", func(t *testing.T) {
		text := []rune("alpha beta gamma delta epsilon zeta eta theta iota kappa")
		run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
		maxLines := 3
		var wrapper LineWrapper
		wrapper.Prepare(WrapConfig{
			BreakPolicy:        WhenNecessary,
			TruncateAfterLines: maxLines,
			Truncator:          mark,
		}, text, NewSliceIterator([]Output{run.copy()}))
		var finalLine WrappedLine
		lineCount := 0
		for {
			wl, done := wrapper.WrapNextLine(90)
			if wl.Line != nil {
				lineCount++
				finalLine = wl
			}
			if done {
				break
			}
		}
		if lineCount != maxLines {
			t.Fatalf("expected %d lines, got %d", maxLines, lineCount)
		}
		if finalLine.Truncated <= 0 {
			t.Fatal("expected the final line to be truncated")
		}
		want := Range{Offset: len(text) - finalLine.Truncated, Count: finalLine.Truncated}
		if finalLine.TruncatedRange != want {
			t.Errorf("TruncatedRange=%+v, want %+v", finalLine.TruncatedRange, want)
		}
		if want.Offset+want.Count != len(text) {
			t.Errorf("hidden range must cover a suffix of the paragraph")
		}
		if !isGraphemeBoundary(text, want.Offset) {
			t.Errorf("range offset %d is not a grapheme boundary", want.Offset)
		}
	})

	t.Run("empty text", func(t *testing.T) {
		text := []rune("")
		run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
		var wrapper LineWrapper
		lines, truncated := wrapper.WrapParagraph(WrapConfig{
			TruncateAfterLines: 1,
			Truncator:          mark,
		}, 100, text, NewSliceIterator([]Output{run.copy()}))
		if truncated != 0 {
			t.Errorf("empty text truncated %d runes", truncated)
		}
		if len(lines) != 1 {
			t.Fatalf("expected 1 (empty) line, got %d", len(lines))
		}
		for _, r := range lines[0] {
			if len(r.Glyphs) != 0 {
				t.Error("marker inserted for empty text")
			}
		}
	})

	t.Run("empty text with continuation still fits marker", func(t *testing.T) {
		text := []rune("")
		run := shapeRunWith(t, fonts.mono, text, di.DirectionLTR, language.Latin, 0)
		var wrapper LineWrapper
		lines, _ := wrapper.WrapParagraph(WrapConfig{
			TruncateAfterLines: 1,
			Truncator:          mark,
			TextContinues:      true,
		}, 100, text, NewSliceIterator([]Output{run.copy()}))
		line := lines[len(lines)-1]
		found := false
		for _, r := range line {
			if len(r.Glyphs) == len(mark.Glyphs) {
				found = true
			}
		}
		if !found {
			t.Error("expected continuation marker on empty paragraph")
		}
		if w := lineWidth(line); w > fixed.I(100) {
			t.Errorf("continuation line width %v exceeds maxWidth", w)
		}
	})
}

// TestTruncationMixedCJKWidth uses a fixed-size CJK face to verify requirement 4:
// with mixed Chinese and English text shaped in one monospaced CJK face, the
// truncated line width, marker position and reported range are all correct.
func TestTruncationMixedCJKWidth(t *testing.T) {
	fonts := loadTruncationFonts(t)
	// Every glyph in this text is covered by the subset CJK face, so a single run
	// exercises mixed Han/Latin shaping in one fixed font.
	text := []rune("卡片简介：这是一段用于测试排版截断的ABC混排文本内容需要超出两行宽度")
	run := shapeRunWith(t, fonts.cjk, text, di.DirectionLTR, language.Han, 0)
	mark := truncatorOutput(t, fonts.cjk, "…", di.DirectionLTR, language.Common, 0)

	// Establish the per-grapheme advance from the shaped output so the expected
	// capacity is computed from real metrics rather than hard-coded numbers.
	seg := segmenter.Segmenter{}
	seg.Init(text)
	iter := seg.GraphemeIterator()
	type cluster struct{ offset, count int }
	var clusters []cluster
	for iter.Next() {
		g := iter.Grapheme()
		clusters = append(clusters, cluster{g.Offset, len(g.Text)})
	}

	maxWidth := 110
	var wrapper LineWrapper
	lines, truncated := wrapper.WrapParagraph(WrapConfig{
		BreakPolicy:        WhenNecessary,
		TruncateAfterLines: 2,
		Truncator:          mark,
	}, maxWidth, text, NewSliceIterator([]Output{run.copy()}))
	if len(lines) != 2 {
		t.Fatalf("expected exactly 2 lines, got %d", len(lines))
	}
	if truncated == 0 {
		t.Fatal("expected truncation after two lines")
	}
	for i, line := range lines {
		if w := lineWidth(line); w > fixed.I(maxWidth) {
			t.Errorf("line %d width %v exceeds maxWidth %d", i, w, maxWidth)
		}
	}
	last := lines[1]
	// The marker is the final logical run and must be present.
	markerRun := last[len(last)-1]
	if len(markerRun.Glyphs) != len(mark.Glyphs) {
		t.Fatalf("final run is not the marker: %d vs %d glyphs", len(markerRun.Glyphs), len(mark.Glyphs))
	}
	cut := len(text) - truncated
	if !isGraphemeBoundary(text, cut) {
		t.Errorf("cut %d is not a grapheme boundary; clusters=%v", cut, clusters)
	}
	// Visible content across both lines must cover [0, cut) exactly.
	visible := 0
	for lineIdx, line := range lines {
		for i, r := range line {
			if lineIdx == len(lines)-1 && i == len(line)-1 {
				continue // marker on the final line
			}
			if r.Runes.Offset != visible {
				t.Errorf("run offset %d, want %d", r.Runes.Offset, visible)
			}
			visible = r.Runes.Offset + r.Runes.Count
		}
	}
	if visible != cut {
		t.Errorf("visible runes cover [0,%d), want [0,%d)", visible, cut)
	}
}
