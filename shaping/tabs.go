// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"sort"

	"github.com/go-text/typesetting/di"
	ft "github.com/go-text/typesetting/font"
	"golang.org/x/image/math/fixed"
)

// markTabGlyphs flags the glyph clusters covering a tab rune (U+0009).
// A cluster may cover several runes (ligatures); it is flagged as soon as
// one of them is a tab so that the wrapper never tries to draw it once tab
// stops are enabled.
func markTabGlyphs(text []rune, runStart, runEnd int, glyphs []Glyph) {
	if runStart < 0 {
		runStart = 0
	}
	if runEnd > len(text) {
		runEnd = len(text)
	}
	for i := range glyphs {
		g := &glyphs[i]
		start := g.ClusterIndex
		if start < runStart {
			start = runStart
		}
		end := start + g.RuneCount
		if end > runEnd {
			end = runEnd
		}
		for r := start; r < end; r++ {
			if text[r] == '\t' {
				g.isTab = true
				break
			}
		}
	}
}

// TabAlignment selects how the text following a tab is positioned at a
// [TabStop].
type TabAlignment uint8

const (
	// TabLeft aligns the start of the text following the tab with the
	// tab stop position. This is the usual tab stop in word processors.
	TabLeft TabAlignment = iota
	// TabRight aligns the end of the text following the tab with the
	// tab stop position.
	TabRight
	// TabCenter centers the text following the tab on the tab stop
	// position.
	TabCenter
)

// TabStop is a position on the main axis of a line at which the text
// following a tab character ('\t', U+0009) can be anchored.
//
// Positions are distances from the leading edge of the line, measured in
// the same coordinates as the shaped glyph advances. They must be positive.
// A line wrapper ignores any tab stop whose position lies beyond the line
// width.
type TabStop struct {
	// Position is the distance of the stop from the leading edge of
	// the line. For LTR text it is the x coordinate from the left edge,
	// for RTL text it is measured from the right edge, and for vertical
	// text from the top (or bottom) edge.
	Position fixed.Int26_6
	// Alignment selects how the text following the tab is laid out at
	// the stop.
	Alignment TabAlignment
}

// normalizeTabStops returns a copy of stops sorted by increasing position,
// with non-positive positions dropped and, when several stops share a
// position, only the first one kept. The returned slice is empty if no stop
// remains. It never aliases stops.
func normalizeTabStops(stops []TabStop) []TabStop {
	if len(stops) == 0 {
		return nil
	}
	out := make([]TabStop, 0, len(stops))
	out = append(out, stops...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Position < out[j].Position
	})
	filtered := out[:0]
	for _, s := range out {
		if s.Position <= 0 {
			continue
		}
		if len(filtered) > 0 && s.Position == filtered[len(filtered)-1].Position {
			// Keep the first stop at a given position.
			continue
		}
		filtered = append(filtered, s)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// glyphAdvance returns the main-axis advance of g, always as a positive
// distance.
func glyphAdvance(g Glyph, vertical bool) fixed.Int26_6 {
	if vertical {
		if g.Advance < 0 {
			return -g.Advance
		}
		return g.Advance
	}
	return g.Advance
}

// runHasTabs reports whether run contains a glyph cluster mapped to a tab
// rune. Only runs marked by the shaper with the isTab flag can contain tabs.
func runHasTabs(run Output) bool {
	for i := range run.Glyphs {
		if run.Glyphs[i].isTab {
			return true
		}
	}
	return false
}

// tabLineItem is one glyph of a line candidate in logical order, annotated
// with the run it belongs to.
type tabLineItem struct {
	run   *Output
	glyph *Glyph
	// tabStart is true for the first glyph in logical order of a tab
	// cluster: that glyph carries the jump advance, while the remaining
	// glyphs of the same cluster are hidden with a zero advance.
	tabStart bool
}

// tabLineBuilder accumulates the glyphs of a line candidate in logical order
// and measures the line as if tab characters had no intrinsic advance,
// resolving them against a fixed list of tab stops.
//
// Tabs split the line into tab segments: the pieces of text between tabs
// (and the line edges). Segment 0 holds the text before the first tab,
// segment 1 the text between the first and second tab, and so on. A tab
// positions the start of the segment following it. A tab cluster, which may
// contain several glyphs after shaping, still counts as a single tab.
//
// The zero value is not usable; use newTabLineBuilder.
type tabLineBuilder struct {
	stops    []TabStop
	vertical bool
	// maxWidth bounds reachable tab stops. A stop whose position is strictly
	// greater than maxWidth is treated as absent, so a tab that would only
	// match it collapses instead of overflowing the line. A zero or negative
	// maxWidth disables the bound.
	maxWidth fixed.Int26_6

	// items is the logical sequence of glyphs currently on the line.
	items []tabLineItem
	// widths is the sum of the non-tab glyph advances of each tab
	// segment, indexed like the segments. Segment i grows as glyphs are
	// appended until the next tab is reached, at which point a new
	// zero-width segment is opened.
	widths []fixed.Int26_6
}

func newTabLineBuilder(stops []TabStop, paragraphDir di.Direction, maxWidth fixed.Int26_6) tabLineBuilder {
	return tabLineBuilder{
		stops:    stops,
		vertical: paragraphDir.IsVertical(),
		maxWidth: maxWidth,
		items:    make([]tabLineItem, 0, 32),
		widths:   make([]fixed.Int26_6, 1, 8),
	}
}

// stopReachable reports whether the stop at position p can be used given the
// configured line width bound.
func (b *tabLineBuilder) stopReachable(p fixed.Int26_6) bool {
	return b.maxWidth <= 0 || p <= b.maxWidth
}

// appendRun appends the glyphs of run in logical order. For RTL runs the
// shaped glyph slice is stored in visual order, so it is traversed backwards.
func (b *tabLineBuilder) appendRun(run *Output) {
	rtl := run.Direction.Progression() == di.TowardTopLeft
	if rtl {
		for i := len(run.Glyphs) - 1; i >= 0; i-- {
			g := &run.Glyphs[i]
			// In reverse traversal the first glyph seen for a cluster is
			// its logical start: it is either the first traversed glyph
			// or its right neighbor belongs to another cluster.
			tabStart := g.isTab && (i == len(run.Glyphs)-1 || run.Glyphs[i+1].ClusterIndex != g.ClusterIndex)
			b.appendGlyph(run, g, tabStart)
		}
	} else {
		for i := range run.Glyphs {
			g := &run.Glyphs[i]
			// In forward traversal a cluster start is detected by looking
			// at the previous glyph.
			tabStart := g.isTab && (i == 0 || run.Glyphs[i-1].ClusterIndex != g.ClusterIndex)
			b.appendGlyph(run, g, tabStart)
		}
	}
}

func (b *tabLineBuilder) appendGlyph(run *Output, g *Glyph, tabStart bool) {
	b.items = append(b.items, tabLineItem{run: run, glyph: g, tabStart: tabStart})
	if g.isTab {
		if tabStart {
			// The tab itself carries no width; it opens the next segment.
			b.widths = append(b.widths, 0)
		}
		return
	}
	b.widths[len(b.widths)-1] += glyphAdvance(*g, b.vertical)
}

// width resolves every tab against the stops and returns the total main-axis
// extent of the line. When trimTrailing is true, trailing whitespace is
// excluded, mirroring the default wrapper behavior.
//
// Resolution walks the segments in order. The current position cursor holds
// the distance from the leading edge reached after the previous segment.
// Each tab consumes the first stop strictly after the cursor; if no such
// stop exists (because the cursor is at or past the last stop), the tab has
// no effect and the following segment starts immediately. Left, right and
// center alignments only change where the following segment starts; the
// cursor never moves backwards.
func (b *tabLineBuilder) width(paragraphDir di.Direction, trimTrailing bool) fixed.Int26_6 {
	var cursor fixed.Int26_6
	stopIdx := 0
	// Segment 0 starts at the line origin and is not preceded by a tab.
	for seg := 0; seg < len(b.widths); seg++ {
		if seg > 0 {
			// Find the first reachable stop strictly past the current position.
			for stopIdx < len(b.stops) && (b.stops[stopIdx].Position <= cursor || !b.stopReachable(b.stops[stopIdx].Position)) {
				stopIdx++
			}
			if stopIdx < len(b.stops) {
				stop := b.stops[stopIdx]
				stopIdx++
				switch stop.Alignment {
				case TabLeft:
					cursor = stop.Position
				case TabRight:
					// The segment end lands on the stop.
					if p := stop.Position - b.widths[seg]; p > cursor {
						cursor = p
					}
				case TabCenter:
					// The segment is centered on the stop.
					if p := stop.Position - b.widths[seg]/2; p > cursor {
						cursor = p
					}
				}
			}
		}
		cursor += b.widths[seg]
	}
	if trimTrailing {
		return b.trimTrailingSpace(cursor, paragraphDir)
	}
	return cursor
}

// trimTrailingSpace adjusts the measured width the same way
// [Output.advanceSpaceAware] does for the run ending a line candidate:
// a trailing whitespace glyph contributes nothing, and otherwise the final
// glyph's end letter spacing is ignored. A line ending with a tab is not
// trimmed, since the tab itself has no intrinsic width.
func (b *tabLineBuilder) trimTrailingSpace(width fixed.Int26_6, paragraphDir di.Direction) fixed.Int26_6 {
	if len(b.items) == 0 {
		return width
	}
	item := b.items[len(b.items)-1]
	if item.glyph.isTab {
		return width
	}
	run := item.run
	if paragraphDir != run.Direction {
		return width
	}
	g := item.glyph
	if b.vertical {
		if g.Height == 0 {
			return width - glyphAdvance(*g, true)
		}
	} else if g.Width == 0 {
		return width - glyphAdvance(*g, false)
	}
	return width - g.endLetterSpacing
}

// apply mutates the glyphs of the line so that tabs produce no visible
// output: tab cluster glyphs become empty glyphs, and the first glyph of
// each tab cluster carries an advance equal to the distance to the resolved
// position of the segment it introduces. Non-tab glyphs keep their
// shaper-assigned metrics. The Advance field of every modified run is
// recomputed.
//
// A zero-distance tab (one with no remaining stop, or whose stop was already
// passed) gets a zero advance and is therefore collapsed entirely.
func (b *tabLineBuilder) apply() {
	// Jump widths are written onto the tab glyphs. A tab glyph may be
	// reached more than once if the same run is shared between line
	// candidates, so first clear previous resolutions.
	for _, item := range b.items {
		if item.glyph.isTab {
			b.setTabAdvance(item.glyph, 0)
		}
	}

	var (
		cursor  fixed.Int26_6
		stopIdx int
		seg     int
	)
	for _, item := range b.items {
		g := item.glyph
		if !g.isTab {
			cursor += glyphAdvance(*g, b.vertical)
			continue
		}
		if !item.tabStart {
			// Additional glyphs of the same tab cluster stay hidden.
			continue
		}
		// This tab opens segment seg+1, whose width is known now.
		next := seg + 1
		var jump fixed.Int26_6
		for stopIdx < len(b.stops) && (b.stops[stopIdx].Position <= cursor || !b.stopReachable(b.stops[stopIdx].Position)) {
			stopIdx++
		}
		if stopIdx < len(b.stops) {
			stop := b.stops[stopIdx]
			stopIdx++
			var target fixed.Int26_6
			switch stop.Alignment {
			case TabLeft:
				target = stop.Position
			case TabRight:
				target = stop.Position - b.widths[next]
			case TabCenter:
				target = stop.Position - b.widths[next]/2
			}
			if target > cursor {
				jump = target - cursor
			}
		}
		b.setTabAdvance(g, jump)
		cursor += jump
		seg = next
	}

	// Recompute run advances once per modified run.
	modified := map[*Output]bool{}
	for _, item := range b.items {
		if item.glyph.isTab {
			modified[item.run] = true
		}
	}
	for run := range modified {
		run.RecomputeAdvance()
	}
}

// setTabAdvance turns g into an invisible glyph carrying the given jump
// along the main axis.
func (b *tabLineBuilder) setTabAdvance(g *Glyph, jump fixed.Int26_6) {
	g.GlyphID = ft.EmptyGlyph // invisible: callers skip tab glyphs when drawing
	g.Width, g.Height = 0, 0
	g.XBearing, g.YBearing = 0, 0
	g.XOffset, g.YOffset = 0, 0
	if b.vertical {
		g.Advance = -jump
		g.YAdvance = -jump
		g.XAdvance = 0
	} else {
		g.Advance = jump
		g.XAdvance = jump
		g.YAdvance = 0
	}
}
