// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"github.com/go-text/typesetting/segmenter"
	"golang.org/x/image/math/fixed"
)

// This file wraps a [RubyLayout] onto lines. Plain segments are cut at UAX#14
// and UAX#29 break opportunities like ordinary shaped text, while ruby groups
// are atomic: a break can never land inside a group, so the base and its
// annotation always move to the same line together.

// RubyWrapConfig provides settings for ruby line wrapping.
type RubyWrapConfig struct {
	// BreakPolicy determines when breaks within "words" (UAX#14 segments) are
	// considered on plain text. It has no effect inside ruby groups, which are
	// never split.
	BreakPolicy LineBreakPolicy
}

// RubyLine is one wrapped line of a [RubyLayout]: a contiguous run of segments
// in logical order, together with the cross-axis bounds of the line.
type RubyLine struct {
	// Segments are the segments placed on the line, in logical order. Plain
	// segments may have been cut to the line; group segments are always whole.
	Segments []RubySegment
	// NextLine is the rune index at which the next line starts. It equals the
	// length of the layout text when the whole text has been consumed.
	NextLine int
	// Bounds are the line bounds, including the annotation band above the base.
	// The Gap field is always zero.
	Bounds Bounds
	// Advance is the total horizontal advance of the line.
	Advance fixed.Int26_6
	// Overflow is true when a ruby group (or a grapheme, with
	// [LineBreakPolicy] Never) wider than the line width was placed on the line
	// anyway, since ruby groups cannot be split.
	Overflow bool
}

// PositionedGlyphs returns the absolute dot positions of every base and
// annotation glyph on the line, relative to the line origin whose base
// baseline is Y = 0.
func (l RubyLine) PositionedGlyphs() []PositionedGlyph {
	var out []PositionedGlyph
	var pen fixed.Int26_6
	for _, seg := range l.Segments {
		if seg.Plain != nil {
			for _, glyph := range seg.Plain.Glyphs {
				out = append(out, PositionedGlyph{
					Glyph: glyph,
					Face:  seg.Plain.Face,
					Size:  seg.Plain.Size,
					X:     pen,
					Y:     0,
					Scale: fixed.I(1),
				})
				pen += glyph.Advance
			}
		} else {
			out = append(out, seg.Group.PositionedGlyphs(pen)...)
			pen += seg.Group.Width
		}
	}
	return out
}

// RubyWrapper holds reusable state for wrapping [RubyLayout]s. Reusing one
// across paragraphs reduces allocations. The zero value is ready to use.
type RubyWrapper struct {
	config RubyWrapConfig
	layout *RubyLayout

	seg     segmenter.Segmenter
	breaker *breaker

	// segmentIdx is the index of the segment containing the first rune not yet
	// placed on a committed line.
	segmentIdx int
	// lineStartRune is the rune index of the first rune of the current line.
	lineStartRune int
	more          bool

	// mappings caches rune->glyph cluster mappings for plain segments.
	mappings [][]glyphIndex
}

// Prepare initializes the wrapper for the given layout. It must be called
// before [RubyWrapper.WrapNextLine]. Prepare invalidates lines returned by a
// previous use of the wrapper.
func (w *RubyWrapper) Prepare(config RubyWrapConfig, layout *RubyLayout) {
	w.config = config
	w.layout = layout
	w.seg.Init(layout.Text)
	w.breaker = newBreaker(&w.seg)
	w.segmentIdx = 0
	w.lineStartRune = 0
	w.more = true
	if cap(w.mappings) < len(layout.Segments) {
		w.mappings = make([][]glyphIndex, len(layout.Segments))
	}
	w.mappings = w.mappings[:len(layout.Segments)]
	for i := range w.mappings {
		w.mappings[i] = nil
	}
}

// segmentAt returns the index of the segment whose range contains runeIdx.
func (w *RubyWrapper) segmentAt(runeIdx int) int {
	segs := w.layout.Segments
	// segmentIdx only moves forward while wrapping.
	idx := w.segmentIdx
	for idx < len(segs) {
		s := segs[idx]
		end := s.Range.Offset + s.Range.Count
		if runeIdx < end {
			return idx
		}
		idx++
	}
	return len(segs) - 1
}

// segmentMapping lazily builds the rune->glyph cluster mapping of a plain
// segment.
func (w *RubyWrapper) segmentMapping(segIdx int) []glyphIndex {
	if m := w.mappings[segIdx]; m != nil {
		return m
	}
	seg := w.layout.Segments[segIdx]
	m := mapRunesToClusterIndices3(seg.Plain.Direction, seg.Plain.Runes, seg.Plain.Glyphs, nil)
	w.mappings[segIdx] = m
	return m
}

// rubyCandidate is a candidate line content: the segments and segment pieces
// covering [lineStartRune, endRune].
type rubyCandidate struct {
	segments []RubySegment
	endRune  int
	advance  fixed.Int26_6
	overflow bool
}

// buildCandidate walks the segments from the current position and produces the
// pieces covering every rune up to and including breakAfter. A break landing
// inside a ruby group is rounded up to the end of the group, since groups are
// atomic.
func (w *RubyWrapper) buildCandidate(breakAfter int) rubyCandidate {
	var cand rubyCandidate
	cand.endRune = breakAfter

	idx := w.segmentIdx
	for idx < len(w.layout.Segments) {
		seg := w.layout.Segments[idx]
		start := seg.Range.Offset
		end := start + seg.Range.Count
		if start > breakAfter {
			break
		}

		if seg.Group != nil {
			// Groups are atomic: either the whole group fits within the break or
			// the break is rounded up to include it entirely.
			if end-1 <= breakAfter {
				cand.segments = append(cand.segments, seg)
				cand.advance += seg.Group.Width
				idx++
				continue
			}
			// The break is inside this group; include the whole group and stop.
			cand.segments = append(cand.segments, seg)
			cand.advance += seg.Group.Width
			cand.endRune = end - 1
			idx++
			break
		}

		// Plain segment: cut at min(end-1, breakAfter).
		cutEnd := end - 1
		if cutEnd > breakAfter {
			cutEnd = breakAfter
		}
		cutStart := start
		if cutStart < w.lineStartRune {
			cutStart = w.lineStartRune
		}
		piece := w.cutPlain(seg, idx, cutStart, cutEnd)
		cand.segments = append(cand.segments, RubySegment{
			Range: Range{Offset: cutStart, Count: cutEnd - cutStart + 1},
			Plain: &piece,
		})
		cand.advance += piece.Advance
		if cutEnd < end-1 {
			// The remaining part of this segment starts the next candidate.
			break
		}
		idx++
	}
	return cand
}

// cutPlain returns the sub-run of a plain segment covering the absolute rune
// range [startRune, endRune].
func (w *RubyWrapper) cutPlain(seg RubySegment, segIdx, startRune, endRune int) Output {
	mapping := w.segmentMapping(segIdx)
	trimStart := w.lineStartRune == startRune
	return cutRun(*seg.Plain, mapping, startRune, endRune, trimStart)
}

// rubyOptionResult classifies a break candidate.
type rubyOptionResult uint8

const (
	rubyFits rubyOptionResult = iota
	rubyNewLine
	rubyCannotFit
)

// processOption evaluates whether the text up to the break option fits the
// line and returns the candidate. hasBest reports whether a viable line has
// already been committed, which distinguishes "does not fit" from "no
// candidate yet".
func (w *RubyWrapper) processOption(option breakOption, maxWidth fixed.Int26_6, hasBest bool) (rubyOptionResult, rubyCandidate) {
	if option.breakAtRune < w.lineStartRune {
		return rubyFits, rubyCandidate{}
	}
	cand := w.buildCandidate(option.breakAtRune)
	if cand.advance > maxWidth {
		if hasBest {
			return rubyNewLine, cand
		}
		return rubyCannotFit, cand
	}
	return rubyFits, cand
}

// WrapParagraph wraps the whole layout to the constant maxWidth and returns
// the lines.
func (w *RubyWrapper) WrapParagraph(config RubyWrapConfig, maxWidth fixed.Int26_6, layout *RubyLayout) []RubyLine {
	w.Prepare(config, layout)
	var lines []RubyLine
	for w.more {
		line := w.WrapNextLine(maxWidth)
		lines = append(lines, line)
	}
	return lines
}

// WrapNextLine wraps one line to maxWidth and returns it. After the final line
// subsequent calls return an empty line with NextLine set to the text length.
func (w *RubyWrapper) WrapNextLine(maxWidth fixed.Int26_6) RubyLine {
	if !w.more {
		return RubyLine{NextLine: w.lineStartRune}
	}

	totalRunes := len(w.layout.Text)
	if totalRunes == 0 {
		w.more = false
		return RubyLine{NextLine: 0}
	}

	var (
		best         rubyCandidate
		hasBest      bool
		done         bool
		overflowLine bool
	)

	// commit stores the current best candidate.
	commit := func(cand rubyCandidate) {
		best = cand
		hasBest = true
	}

	// settleFinal runs when no break option remains: the rest of the text is
	// placed on the current line.
	settleFinal := func() RubyLine {
		if !hasBest {
			best = w.buildCandidate(totalRunes - 1)
			hasBest = true
			if best.advance > maxWidth {
				overflowLine = true
			}
		}
		w.more = false
		return w.finalizeLine(best, overflowLine)
	}

	// Word break loop.
wordLoop:
	for {
		option, ok := w.breaker.nextWordBreak()
		if !ok {
			done = true
			break wordLoop
		}
		if option.breakAtRune < w.lineStartRune {
			continue
		}
		result, cand := w.processOption(option, maxWidth, hasBest)
		switch result {
		case rubyFits:
			commit(cand)
			if option.required {
				done = false
				return w.finish(best, overflowLine, done)
			}
		case rubyNewLine:
			// Reuse this word break on the next line.
			w.breaker.markWordOptionUnused()
			if w.config.BreakPolicy == Never || w.config.BreakPolicy == WhenNecessary {
				done = false
				return w.finish(best, overflowLine, done)
			}
			break wordLoop
		case rubyCannotFit:
			if w.config.BreakPolicy == Never {
				// The overwide content cannot be split: place it on the line
				// anyway and report the overflow.
				overflowLine = true
				commit(cand)
				done = cand.endRune >= totalRunes-1
				return w.finish(best, overflowLine, done)
			}
			break wordLoop
		}
	}
	if done {
		return settleFinal()
	}

	// Grapheme break loop for overwide content on plain segments.
graphemeLoop:
	for {
		option, ok := w.breaker.nextGraphemeBreak()
		if !ok {
			break graphemeLoop
		}
		if option.breakAtRune < w.lineStartRune {
			continue
		}
		result, cand := w.processOption(option, maxWidth, hasBest)
		switch result {
		case rubyFits:
			commit(cand)
			w.breaker.markWordOptionUnused()
		case rubyNewLine:
			w.breaker.markWordOptionUnused()
			w.breaker.markGraphemeOptionUnused()
			done = false
			return w.finish(best, overflowLine, done)
		case rubyCannotFit:
			if hasBest {
				// Keep the best line and retry this grapheme on the next line.
				w.breaker.markWordOptionUnused()
				w.breaker.markGraphemeOptionUnused()
				done = false
				return w.finish(best, overflowLine, done)
			}
			// Nothing fits: place one grapheme (or the enclosing atomic group)
			// on the line and accept the overflow.
			overflowLine = true
			commit(cand)
			done = cand.endRune >= totalRunes-1
			return w.finish(best, overflowLine, done)
		}
	}

	return settleFinal()
}

// finish finalizes a line and clears the wrapper's more flag when the text has
// been fully consumed.
func (w *RubyWrapper) finish(cand rubyCandidate, overflow, done bool) RubyLine {
	line := w.finalizeLine(cand, overflow)
	if done || line.NextLine >= len(w.layout.Text) {
		w.more = false
	}
	return line
}

// finalizeLine turns a committed candidate into a [RubyLine] and advances the
// wrapper state to the start of the next line.
func (w *RubyWrapper) finalizeLine(cand rubyCandidate, overflow bool) RubyLine {
	nextLine := cand.endRune + 1

	// Advance the segment cursor to the segment containing the next line.
	w.segmentIdx = w.segmentAt(nextLine)
	w.lineStartRune = nextLine

	var (
		ascent  fixed.Int26_6
		descent fixed.Int26_6
		annTop  fixed.Int26_6
	)
	for _, seg := range cand.segments {
		if seg.Plain != nil {
			if a := seg.Plain.GlyphBounds.Ascent; a > ascent {
				ascent = a
			}
			if d := seg.Plain.GlyphBounds.Descent; d < descent {
				descent = d
			}
			continue
		}
		g := seg.Group
		if a := g.Base.GlyphBounds.Ascent; a > ascent {
			ascent = a
		}
		if d := g.Base.GlyphBounds.Descent; d < descent {
			descent = d
		}
		if top := g.annotationTop(); top > annTop {
			annTop = top
		}
	}
	if annTop > ascent {
		ascent = annTop
	}

	return RubyLine{
		Segments: cand.segments,
		NextLine: nextLine,
		Bounds:   Bounds{Ascent: ascent, Descent: descent},
		Advance:  cand.advance,
		Overflow: overflow,
	}
}
