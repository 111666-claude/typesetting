// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"math"

	"github.com/go-text/typesetting/di"
	"golang.org/x/image/math/fixed"
)

// HangingPunctuation configures optional hanging punctuation (also known as
// optical margin alignment) for the [LineWrapper].
//
// When enabled, punctuation listed in [HangingPunctuation.Start]
// (resp. [HangingPunctuation.End]) located at the leading (resp. trailing)
// edge of a line is allowed to protrude beyond the text frame by up to
// [HangingPunctuation.StartRatio] (resp. [HangingPunctuation.EndRatio]) of
// its advance. The line wrapper takes this allowance into account when
// choosing break points, and reports the overhang of each line in
// [WrappedLine.StartOverhang] and [WrappedLine.EndOverhang].
//
// The allowance is measured on the shaped glyphs, not on character counts or
// nominal widths, so mixed fonts, font fallback and right-to-left lines are
// handled correctly. Line breaking opportunities are unchanged: the Unicode
// line breaking rules (UAX#14) are still enforced, hanging only relaxes the
// width constraint at the line edges.
//
// The zero value disables hanging punctuation entirely, in which case the
// output of the wrapper is strictly identical to a wrapper without this
// feature.
type HangingPunctuation struct {
	// Start lists the runes allowed to hang beyond the leading edge of a
	// line (in reading order), for example opening quotes or brackets.
	Start []rune
	// End lists the runes allowed to hang beyond the trailing edge of a
	// line (in reading order), for example commas, periods or closing
	// brackets.
	End []rune
	// StartRatio is the fraction of each leading punctuation glyph's
	// advance allowed to protrude beyond the leading edge of the frame.
	// Values outside [0, 1] (including NaN) are clamped.
	StartRatio float64
	// EndRatio is the fraction of each trailing punctuation glyph's
	// advance allowed to protrude beyond the trailing edge of the frame.
	// Values outside [0, 1] (including NaN) are clamped.
	EndRatio float64
}

// hangingPunctuation is the compiled, lookup-efficient form of
// [HangingPunctuation]. Ratios are stored as fixed point fractions with
// 64 meaning 100%.
type hangingPunctuation struct {
	start, end           map[rune]struct{}
	startRatio, endRatio fixed.Int26_6
	// enabled is false when no punctuation can ever hang, in which case
	// the wrapper skips all hanging related work.
	enabled bool
}

func newHangingPunctuation(config HangingPunctuation) (h hangingPunctuation) {
	h.startRatio = clampHangRatio(config.StartRatio)
	h.endRatio = clampHangRatio(config.EndRatio)
	if len(config.Start) > 0 && h.startRatio > 0 {
		h.start = make(map[rune]struct{}, len(config.Start))
		for _, r := range config.Start {
			h.start[r] = struct{}{}
		}
	}
	if len(config.End) > 0 && h.endRatio > 0 {
		h.end = make(map[rune]struct{}, len(config.End))
		for _, r := range config.End {
			h.end[r] = struct{}{}
		}
	}
	h.enabled = h.start != nil || h.end != nil
	return h
}

// clampHangRatio converts a user provided ratio to a fixed point fraction
// (64 = 100%), clamping invalid values into [0, 1].
func clampHangRatio(ratio float64) fixed.Int26_6 {
	if math.IsNaN(ratio) || ratio <= 0 {
		return 0
	}
	if ratio >= 1 {
		return 1 << 6
	}
	return fixed.Int26_6(ratio * (1 << 6))
}

// set returns the punctuation set and ratio for the given edge.
func (h *hangingPunctuation) set(leading bool) (map[rune]struct{}, fixed.Int26_6) {
	if leading {
		return h.start, h.startRatio
	}
	return h.end, h.endRatio
}

// runEdgeHang measures the hang allowance at one edge of a single run,
// using the shaped glyphs. leading selects the edge where the run starts in
// reading order, otherwise the trailing edge is measured.
//
// Consecutive hanging punctuation glyphs accumulate their allowances.
// Glyphs without ink (whitespace) or without advance (combining marks, zero
// width characters) at the edge are skipped: they are either trimmed by the
// wrapper or carried by their base glyph. Glyphs whose cluster cannot be
// resolved to a rune of the paragraph stop the walk, as does the first
// non-punctuation glyph.
//
// It returns 0 when the run direction does not match the paragraph
// direction: cross-direction runs (in bidi text) are never adjacent to the
// frame edge in reading order. exhausted reports whether every glyph of the
// run was skipped or hanging punctuation, meaning the visual edge of the
// line lies in the adjacent run.
func (h *hangingPunctuation) runEdgeHang(run Output, set map[rune]struct{}, ratio fixed.Int26_6, paragraphDir di.Direction, text []rune, leading bool) (hang fixed.Int26_6, exhausted bool) {
	if run.Direction != paragraphDir {
		return 0, false
	}
	glyphs := run.Glyphs
	// Within a run, glyphs are stored in visual order: for LTR text the
	// trailing edge (in reading order) is the last glyph of the slice, for
	// RTL text it is the first one.
	rtl := run.Direction.Progression() == di.TowardTopLeft
	index, step := 0, 1
	if leading == rtl {
		index, step = len(glyphs)-1, -1
	}
	vertical := run.Direction.IsVertical()
	for index >= 0 && index < len(glyphs) {
		glyph := glyphs[index]
		index += step
		width := glyph.Width
		if vertical {
			width = glyph.Height
		}
		if glyph.Advance == 0 || width == 0 {
			// Zero advance glyphs (combining marks, zero width characters)
			// are carried by their base glyph, and zero ink glyphs
			// (whitespace) are trimmed by the wrapper: neither of them
			// forms the visual edge of the line.
			continue
		}
		cluster := glyph.ClusterIndex
		if cluster < 0 || cluster >= len(text) {
			return hang, false
		}
		if _, ok := set[text[cluster]]; !ok {
			return hang, false
		}
		advance := glyph.Advance
		if advance < 0 { // vertical text advances negatively
			advance = -advance
		}
		hang += fixed.Int26_6((int64(ratio) * int64(advance)) >> 6)
	}
	return hang, true
}

// edgeHangOf measures the total hang allowance at one edge of a sequence of
// runs (in logical order), crossing run boundaries over runs made entirely
// of skipped or hanging glyphs.
func (h *hangingPunctuation) edgeHangOf(runs []Output, paragraphDir di.Direction, text []rune, leading bool) fixed.Int26_6 {
	set, ratio := h.set(leading)
	if len(set) == 0 || ratio == 0 {
		return 0
	}
	var hang fixed.Int26_6
	start, end, step := 0, len(runs), 1
	if !leading {
		start, end, step = len(runs)-1, -1, -1
	}
	for i := start; i != end; i += step {
		runHang, exhausted := h.runEdgeHang(runs[i], set, ratio, paragraphDir, text, leading)
		hang += runHang
		if !exhausted {
			break
		}
	}
	return hang
}

// edgeHang measures the hang allowance at one edge of a single run. See
// [hangingPunctuation.runEdgeHang] for the exact semantics.
func (h *hangingPunctuation) edgeHang(run Output, paragraphDir di.Direction, text []rune, leading bool) fixed.Int26_6 {
	set, ratio := h.set(leading)
	if len(set) == 0 || ratio == 0 {
		return 0
	}
	hang, _ := h.runEdgeHang(run, set, ratio, paragraphDir, text, leading)
	return hang
}

// lineOverhangs computes the hanging punctuation overhangs of a finalized
// line. maxWidth is the width the line was wrapped to, used to report the
// actual visual overflow at the trailing edge.
func (l *LineWrapper) lineOverhangs(line Line, maxWidth fixed.Int26_6) (start, end fixed.Int26_6) {
	// Locate the leading and trailing runs in reading order, using the
	// visual indices computed by computeBidiOrdering.
	leadVisual, trailVisual := 0, len(line)-1
	if l.config.Direction.Progression() == di.TowardTopLeft {
		leadVisual, trailVisual = trailVisual, leadVisual
	}
	leadLogical, trailLogical := -1, -1
	var advance fixed.Int26_6
	for i := range line {
		advance += line[i].Advance
		if line[i].VisualIndex == int32(leadVisual) {
			leadLogical = i
		}
		if line[i].VisualIndex == int32(trailVisual) {
			trailLogical = i
		}
	}
	if leadLogical == -1 || trailLogical == -1 {
		return 0, 0
	}
	// The hang may extend over adjacent runs (in logical order) sharing the
	// paragraph direction, for instance when trailing whitespace was shaped
	// in a different run than the punctuation preceding it.
	start = l.hanging.edgeHangOf(line[leadLogical:], l.config.Direction, l.text, true)
	end = l.hanging.edgeHangOf(line[:trailLogical+1], l.config.Direction, l.text, false)
	// Report the overhang actually overflowing the frame: a line which does
	// not fill the frame does not visually overflow at its trailing edge.
	if overflow := advance - maxWidth; overflow < end {
		end = max(overflow, 0)
	}
	return start, end
}
