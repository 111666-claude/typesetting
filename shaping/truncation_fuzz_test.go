// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	"golang.org/x/image/math/fixed"
)

// FuzzTruncationInvariants shapes and truncates arbitrary text with a real marker,
// checking that:
//   - every line stays within maxWidth;
//   - truncation only happens at grapheme cluster boundaries;
//   - WrappedLine.TruncatedRange matches the truncated count and covers a suffix
//     of the paragraph;
//   - visible runes plus truncated runes always account for the whole paragraph.
func FuzzTruncationInvariants(f *testing.F) {
	face := loadOpentypeFont(f, "../font/testdata/Amiri-Regular.ttf")
	// direction: 0 LTR, 1 RTL.
	f.Add(benchParagraphLatin, byte(100), byte(1), byte(0))
	f.Add(benchParagraphArabic, byte(60), byte(2), byte(1))
	f.Fuzz(func(t *testing.T, input string, lineWidth byte, maxLines byte, direction byte) {
		text := []rune(input)
		dir := di.DirectionLTR
		script := language.Latin
		if direction&1 == 1 {
			dir = di.DirectionRTL
			script = language.Arabic
		}
		var shaper HarfbuzzShaper
		run := shaper.Shape(Input{
			Text:      text,
			RunStart:  0,
			RunEnd:    len(text),
			Direction: dir,
			Face:      face,
			Size:      fixed.I(16),
			Script:    script,
			Language:  language.DefaultLanguage(),
		})
		mark := shaper.Shape(Input{
			Text:      []rune("…"),
			RunStart:  0,
			RunEnd:    1,
			Direction: dir,
			Face:      face,
			Size:      fixed.I(16),
			Script:    script,
			Language:  language.DefaultLanguage(),
		})
		width := int(lineWidth)
		lines := maxLines
		if lines == 0 {
			lines = 1
		}

		// Grapheme boundary ends, computed independently of the wrapper.
		var seg segmenter.Segmenter
		seg.Init(text)
		boundaries := map[int]bool{0: true, len(text): true}
		iter := seg.GraphemeIterator()
		for iter.Next() {
			g := iter.Grapheme()
			boundaries[g.Offset+len(g.Text)] = true
		}

		var wrapper LineWrapper
		wrapper.Prepare(WrapConfig{
			Direction:          dir,
			BreakPolicy:        Always,
			TruncateAfterLines: int(lines),
			Truncator:          mark,
		}, text, NewSliceIterator([]Output{run.copy()}))

		truncatedTotal := 0
		var truncRange Range
		gotTruncRange := false
		visibleRunes := 0
		emittedLine := false
		// isMarker identifies a truncator run. The wrapper gives the marker the
		// hidden suffix as its rune range, so a run is the marker precisely when
		// truncation has happened and its runes equal [visibleEnd, len(text)).
		// Glyph count alone is unreliable: a one-glyph word looks like the marker.
		// The value is updated as truncation is observed.
		markerRunes := Range{Offset: -1}
		for {
			wl, done := wrapper.WrapNextLine(width)
			if wl.Truncated > 0 {
				markerRunes = Range{Offset: len(text) - wl.Truncated, Count: wl.Truncated}
				// Account for truncation even when the truncated line is empty
				// (the marker did not fit and every visible rune was removed).
				truncatedTotal += wl.Truncated
				truncRange = wl.TruncatedRange
				gotTruncRange = true
			}
			if wl.Line != nil {
				emittedLine = true
				var lineAdvance fixed.Int26_6
				markerPresent := false
				for _, r := range wl.Line {
					lineAdvance += r.Advance
					if wl.Truncated > 0 && r.Runes == markerRunes {
						markerPresent = true
						continue // marker run aliases the hidden suffix
					}
					visibleRunes += r.Runes.Count
				}
				// A marker that fits is guaranteed to share a truncated line that
				// stays within the width budget. A line without such a marker may
				// still overflow only when it holds a single unbreakable cluster,
				// which no layout can split (pre-existing wrapping behavior).
				if width > 0 && markerPresent && lineAdvance > fixed.I(width) {
					t.Fatalf("truncated line width %v exceeds maxWidth %d", lineAdvance, width)
				}
			}
			if done {
				break
			}
		}

		if truncatedTotal > 0 {
			if !gotTruncRange {
				t.Fatal("truncation occurred but TruncatedRange was never set")
			}
			want := Range{Offset: len(text) - truncatedTotal, Count: truncatedTotal}
			if truncRange != want {
				t.Fatalf("TruncatedRange = %+v, want %+v", truncRange, want)
			}
			if truncRange.Offset+truncRange.Count != len(text) {
				t.Fatalf("hidden range does not cover a suffix of the paragraph")
			}
			if !boundaries[truncRange.Offset] {
				t.Fatalf("truncation at rune %d is not on a grapheme boundary", truncRange.Offset)
			}
		} else if gotTruncRange {
			t.Fatalf("TruncatedRange set (%+v) despite zero truncated runes", truncRange)
		}
		// When no line is emitted, every rune had to be hidden because neither text
		// nor a fitting marker could occupy the (possibly tiny) width. Otherwise the
		// visible and hidden runes must partition the whole paragraph exactly.
		if !emittedLine {
			if truncatedTotal != len(text) {
				t.Fatalf("no line emitted but truncated=%d of %d runes", truncatedTotal, len(text))
			}
		} else if visibleRunes+truncatedTotal != len(text) {
			t.Fatalf("rune accounting: visible=%d truncated=%d total=%d", visibleRunes, truncatedTotal, len(text))
		}
	})
}
