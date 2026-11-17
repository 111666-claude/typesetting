package shaping

import (
	"reflect"
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	"golang.org/x/image/math/fixed"
)

// FuzzE2E shapes and wraps large strings looking for unshapable text or failures
// in rune accounting.
func FuzzE2E(f *testing.F) {
	face := loadOpentypeFont(f, "../font/testdata/Amiri-Regular.ttf")
	f.Add(benchParagraphLatin)
	f.Add(benchParagraphArabic)
	f.Fuzz(func(t *testing.T, input string) {
		textInput := []rune(input)
		var shaper HarfbuzzShaper
		out := []Output{shaper.Shape(Input{
			Text:      textInput,
			RunStart:  0,
			RunEnd:    len(textInput),
			Direction: di.DirectionRTL,
			Face:      face,
			Size:      16 * 72,
			Script:    language.Arabic,
			Language:  language.NewLanguage("AR"),
		})}
		if out[0].Runes.Count != len(textInput) {
			t.Errorf("expected %d shaped runes, got %d", len(textInput), out[0].Runes.Count)
		}
		var l LineWrapper
		outs, _ := l.WrapParagraph(WrapConfig{}, 100, textInput, NewSliceIterator(out))
		totalRunes := 0
		for _, l := range outs {
			for _, run := range l {
				if run.Runes.Offset != totalRunes {
					t.Errorf("expected rune offset %d, got %d", totalRunes, run.Runes.Offset)
				}
				totalRunes += run.Runes.Count
			}
		}
		if totalRunes != len(textInput) {
			t.Errorf("mismatched runes! expected %d, but wrapped output only contains %d", len(textInput), totalRunes)
		}
		_ = outs
	})
}

// FuzzE2EVariableSize shapes and wraps large strings at varying text sizes and line widths.
func FuzzE2EVariableSize(f *testing.F) {
	face := loadOpentypeFont(f, "../font/testdata/Amiri-Regular.ttf")
	f.Add(benchParagraphLatin, byte(16), byte(100), byte(0))
	f.Add(benchParagraphArabic, byte(16), byte(100), byte(0))
	f.Fuzz(func(t *testing.T, input string, textSize byte, lineWidth byte, truncateBreakCont byte) {
		// We pack the wrapconfig fields into a byte in order to ensure that the fuzzer spends more time
		// exploring interesting parts of the space instead of varying bits that don't matter.
		wc := WrapConfig{
			TruncateAfterLines: int(truncateBreakCont & 0b11111),
			TextContinues:      truncateBreakCont&0b100000 != 0,
			BreakPolicy:        LineBreakPolicy((truncateBreakCont & 0b11000000 >> 5) % 3),
		}
		textInput := []rune(input)
		var shaper HarfbuzzShaper
		out := []Output{shaper.Shape(Input{
			Text:      textInput,
			RunStart:  0,
			RunEnd:    len(textInput),
			Direction: di.DirectionRTL,
			Face:      face,
			Size:      fixed.I(int(textSize)),
			Script:    language.Arabic,
			Language:  language.NewLanguage("AR"),
		})}
		if out[0].Runes.Count != len(textInput) {
			t.Errorf("expected %d shaped runes, got %d", len(textInput), out[0].Runes.Count)
		}
		var l LineWrapper
		outs, truncated := l.WrapParagraph(wc, int(lineWidth), textInput, NewSliceIterator(out))
		totalRunes := 0
		for _, l := range outs {
			for _, run := range l {
				if run.Runes.Offset != totalRunes {
					if reflect.DeepEqual(run, wc.Truncator) {
						continue
					}
					t.Errorf("expected rune offset %d, got %d", totalRunes, run.Runes.Offset)
				}
				totalRunes += run.Runes.Count
			}
		}
		totalRunes += truncated
		if totalRunes != len(textInput) {
			t.Errorf("mismatched runes! expected %d, but wrapped output only contains %d", len(textInput), totalRunes)
		}
		_ = outs
	})
}

// fuzzHyphenator derives deterministic but deliberately messy break
// opportunities from each word: it includes interior offsets, but also
// duplicates, out-of-range values and unsorted order to exercise the wrapper's
// validation. The exact pattern is a pure function of word content, so the same
// word always receives the same opportunities.
type fuzzHyphenator struct{}

func (fuzzHyphenator) BreakOpportunities(word []rune, wordOffset int) []int {
	if len(word) <= 2 {
		return nil
	}
	var out []int
	for i := 1; i < len(word); i++ {
		// Only alphabetic-like interior positions are candidates.
		out = append(out, i)
		if i%3 == 0 {
			out = append(out, i) // duplicate
		}
		if i%4 == 0 {
			out = append(out, i-2) // unsorted relative entry
		}
	}
	// Inject illegal indices that must be ignored.
	out = append(out, 0, len(word), -1, len(word)+5)
	return out
}

// FuzzHyphenation shapes and wraps text with hyphenation enabled, verifying
// that every source rune is still accounted for exactly once and that no
// hyphenated line overflows the requested width.
func FuzzHyphenation(f *testing.F) {
	face := loadOpentypeFont(f, "../font/testdata/Roboto-Regular.ttf")
	var shaper HarfbuzzShaper
	hyphen := shaper.Shape(Input{
		Text:      []rune("-"),
		RunStart:  0,
		RunEnd:    1,
		Direction: di.DirectionLTR,
		Face:      face,
		Size:      fixed.I(16),
		Script:    language.Latin,
	})
	f.Add(benchParagraphLatin, byte(120))
	f.Add("internationalization", byte(40))
	f.Add("a", byte(1))
	f.Fuzz(func(t *testing.T, input string, widthByte byte) {
		textInput := []rune(input)
		if len(textInput) == 0 {
			return
		}
		out := []Output{shaper.Shape(Input{
			Text:      textInput,
			RunStart:  0,
			RunEnd:    len(textInput),
			Direction: di.DirectionLTR,
			Face:      face,
			Size:      fixed.I(16),
			Script:    language.Latin,
		})}
		maxWidth := int(widthByte)
		var l LineWrapper
		lines, _ := l.WrapParagraph(
			WrapConfig{BreakPolicy: Never, Hyphenator: fuzzHyphenator{}, Hyphen: hyphen},
			maxWidth, textInput, NewSliceIterator(out),
		)
		totalRunes := 0
		for lineIdx, line := range lines {
			hasHyphen := false
			var lineAdvance fixed.Int26_6
			for _, run := range line {
				lineAdvance += run.Advance
				if run.Runes.Count == 0 {
					hasHyphen = true
					continue
				}
				if run.Runes.Offset != totalRunes {
					t.Errorf("line %d: expected rune offset %d, got %d", lineIdx, totalRunes, run.Runes.Offset)
				}
				totalRunes += run.Runes.Count
			}
			// A line that actually carries a hyphen must fit within the width.
			if hasHyphen && lineAdvance > fixed.I(maxWidth) {
				t.Errorf("line %d with hyphen overflows: %d > %d", lineIdx, lineAdvance, maxWidth)
			}
		}
		if totalRunes != len(textInput) {
			t.Errorf("mismatched runes! expected %d, got %d", len(textInput), totalRunes)
		}
	})
}

func FuzzBreakOptions(f *testing.F) {
	f.Add(string([]rune{183067, 318808839, 476266048}))
	f.Add(benchParagraphArabic)
	f.Add(benchParagraphLatin)
	f.Fuzz(func(t *testing.T, input string) {
		runes := []rune(input)
		var seg segmenter.Segmenter
		seg.Init(runes)
		breaker := newBreaker(&seg)
		var wordOptions []breakOption
		for b, ok := breaker.nextWordBreak(); ok; b, ok = breaker.nextWordBreak() {
			prevRuneIndex := 0
			if len(wordOptions) > 0 {
				prevRuneIndex = wordOptions[len(wordOptions)-1].breakAtRune + 1
			}
			segmentRunes := runes[prevRuneIndex : b.breakAtRune+1]
			segmentGraphemes := []breakOption{}
			for b, ok := breaker.nextGraphemeBreak(); ok; b, ok = breaker.nextGraphemeBreak() {
				// Adjust break offset to be relative to the start of the segment.
				b.breakAtRune -= prevRuneIndex
				segmentGraphemes = append(segmentGraphemes, b)
			}
			seg := segmenter.Segmenter{}
			seg.Init(segmentRunes)
			correctGraphemes := []int{}
			count := 0
			for iter := seg.GraphemeIterator(); iter.Next(); {
				g := iter.Grapheme()
				breakAt := g.Offset + len(g.Text) - 1
				firstGraphemeInText := prevRuneIndex == 0
				if !firstGraphemeInText {
					continue
				}
				count++
				correctGraphemes = append(correctGraphemes, breakAt)
			}
			if count > 0 && len(segmentGraphemes) != count {
				t.Errorf("runes[%d:%d] expected %d graphemes, got %d", prevRuneIndex, b.breakAtRune+1, count, len(segmentGraphemes))
				t.Errorf("correct graphemes: %v\ngot graphemes: %v", correctGraphemes, segmentGraphemes)
			}
			checkOptions(t, segmentRunes, segmentGraphemes)
			wordOptions = append(wordOptions, b)
		}
		checkOptions(t, runes, wordOptions)
	})
}
