// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/go-text/typesetting/di"
	"golang.org/x/image/math/fixed"
)

var (
	// ErrInvalidGridStep is returned by [PlaceLines] when the grid step is
	// not strictly positive.
	ErrInvalidGridStep = errors.New("shaping: baseline grid step must be positive")
	// ErrGridAxisMismatch is returned by [PlaceLines] when a run does not
	// use the same layout axis as the grid direction.
	ErrGridAxisMismatch = errors.New("shaping: line runs do not match the grid axis")
	// ErrGridOverflow is returned by [PlaceLines] when a baseline, a run
	// position or a number of skipped grid lines cannot be represented.
	ErrGridOverflow = errors.New("shaping: placed coordinate overflows fixed.Int26_6")
)

// BaselineGrid describes an evenly spaced sequence of baselines used to place
// the lines of a paragraph, for instance to keep body text and footnotes on a
// common baseline grid.
//
// The grid constrains the cross axis of the paragraph only: y for horizontal
// text, x for vertical text. It does not move the runs along their line, and
// [PlaceLines] never modifies the shaped and wrapped lines it is given, so the
// shaping output is preserved.
type BaselineGrid struct {
	// Step is the distance between two consecutive grid lines. It must be
	// strictly positive; [PlaceLines] returns [ErrInvalidGridStep]
	// otherwise.
	Step fixed.Int26_6
	// FirstBaseline is the cross-axis coordinate of the baseline of the
	// first line of the paragraph: y for horizontal text, x for vertical
	// text.
	FirstBaseline fixed.Int26_6
	// Direction is the paragraph direction. Its axis selects the cross axis
	// of the grid, and every placed run must use that same axis (see
	// [ErrGridAxisMismatch]). The zero value lays out horizontal text.
	//
	// Following lines are stacked toward the descent side of the axis: below
	// the first line for horizontal text, and to the left of the first
	// column for vertical text, as described by [Bounds]. Baselines are
	// therefore FirstBaseline minus a multiple of Step.
	Direction di.Direction
}

// LinePlacement is the position of one line on a [BaselineGrid].
type LinePlacement struct {
	// Baseline is the cross-axis coordinate of the baseline of the line:
	// y for horizontal text, x for vertical text. Every run of the line is
	// rendered relative to this line, so that runs using different fonts or
	// sizes still share the same baseline.
	Baseline fixed.Int26_6
	// Runs holds the position of each run of the line, indexed like the
	// runs of the input line.
	//
	// For horizontal text, X is the distance from the start of the line to
	// the dot of the run and Y is Baseline. For vertical text, Y is the
	// distance from the start of the line to the dot of the run and X is
	// Baseline. The start of the line is its leftmost point for horizontal
	// text and its topmost point for vertical text, which is the natural
	// origin for renderers drawing runs in visual order.
	//
	// Coordinates follow the shaped output convention used by [Output] and
	// [Glyph]: y grows up and vertical advances are negative, so the Y
	// coordinate of top-to-bottom runs decreases along the line. The
	// distance of a run is the sum of the [Output.Advance] of the runs
	// before it in visual order, so a line whose runs are already ordered
	// visually (for a left-to-right paragraph) starts at zero.
	//
	// All the runs of a line share the same baseline. Cross-axis offsets
	// internal to a run, in particular the ones applied by
	// [Line.AdjustBaselines] for mixed vertical orientations, are already
	// encoded in the glyph offsets; they are not repeated here.
	Runs []fixed.Point26_6
	// Skipped is the number of grid lines left empty before this line,
	// because placing it on the next grid line would have overlapped the ink
	// of a previous line. It is zero for the first line and for lines placed
	// on the grid line immediately following the previous one.
	Skipped int
}

// PlaceLines places already shaped and wrapped lines on the grid, returning
// the baseline of each line and the position of each of its runs.
//
// The first line is placed with its baseline at [BaselineGrid.FirstBaseline].
// Each following line is placed on the first grid line, toward the descent
// side, whose ink does not overlap the ink of the previous lines. The extent
// of a line is measured from the [Output.GlyphBounds] of its runs, so that the
// actual ascent and descent of the glyphs, rather than the nominal metrics of
// the font, decide whether lines fit; runs without glyph extents (for instance
// shaped with [HarfbuzzShaper.ShapeNoExtents]) fall back to their
// [Output.LineBounds], and the line gap is ignored. A line whose ink does not
// fit in Step therefore skips as many grid lines as needed, and the following
// lines are shifted accordingly while remaining aligned on the grid.
//
// Baselines are always FirstBaseline minus a multiple of Step, so that skipped
// lines do not break the alignment of the paragraph. Lines never overlap, and
// a line is never split or moved before its predecessor.
//
// A line with no run, or whose runs hold no glyph, is empty. An empty line
// consumes exactly one grid line: its baseline is one Step away from the
// previous one. It has no ink, so it neither causes nor prevents skipping;
// blank lines at the start of the paragraph are thus placed on successive grid
// lines from FirstBaseline.
//
// Runs are ordered along the line by their [Output.VisualIndex], using the
// stable order of the line for equal indices, which is the visual order
// produced by the [LineWrapper]. Their distance from the start of the line is
// the sum of the advances of the preceding runs, so that runs of different
// fonts, sizes and directions can be rendered along a common line.
//
// The input lines are not modified. When [Line.AdjustBaselines] is needed, for
// instance for vertical text mixing upright and sideways runs, call it before
// [PlaceLines]: the resulting glyph offsets are taken into account when
// measuring the extent of the lines and are preserved.
//
// PlaceLines returns an error wrapping [ErrInvalidGridStep] if grid.Step is
// not positive, [ErrGridAxisMismatch] if a run uses another axis than
// grid.Direction, and [ErrGridOverflow] if a computed baseline or run position
// does not fit in a [fixed.Int26_6]. No placement is returned in these cases.
func PlaceLines(lines []Line, grid BaselineGrid) ([]LinePlacement, error) {
	if grid.Step <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidGridStep, grid.Step)
	}
	step := int64(grid.Step)
	vertical := grid.Direction.IsVertical()

	placements := make([]LinePlacement, len(lines))
	// lowestBottom is the deepest coordinate (toward the descent side)
	// reached by the ink of the previous lines: the following lines must
	// start at or above it.
	lowestBottom := int64(math.MaxInt64)
	// base is the cross-axis coordinate of the baseline of the previous
	// line, empty or not.
	var base int64

	for lineIndex, line := range lines {
		ascent, descent, hasInk := lineExtents(line)

		for _, run := range line {
			if run.Direction.Axis() != grid.Direction.Axis() {
				return nil, fmt.Errorf("%w: line %d has a %v run, grid is %v",
					ErrGridAxisMismatch, lineIndex, run.Direction.Axis(), grid.Direction.Axis())
			}
		}

		if lineIndex == 0 {
			base = int64(grid.FirstBaseline)
		} else {
			// Empty lines always consume exactly one grid line.
			k := int64(1)
			if hasInk && lowestBottom != math.MaxInt64 {
				// The top of the line, base - k*step + ascent, must be at or
				// below lowestBottom; find the smallest k >= 1 satisfying it.
				if needed := base + int64(ascent) - lowestBottom; needed > step {
					k = (needed + step - 1) / step
				}
			}
			skipped := k - 1
			if skipped > int64(math.MaxInt) {
				return nil, fmt.Errorf("%w: line %d skips %d grid lines",
					ErrGridOverflow, lineIndex, skipped)
			}
			placements[lineIndex].Skipped = int(skipped)
			base -= k * step
		}

		if !fitsInt26_6(base) {
			return nil, fmt.Errorf("%w: baseline of line %d is %d",
				ErrGridOverflow, lineIndex, base)
		}
		runs, err := lineRunPositions(line, vertical, fixed.Int26_6(base))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineIndex, err)
		}
		placements[lineIndex].Baseline = fixed.Int26_6(base)
		placements[lineIndex].Runs = runs

		if hasInk {
			if bottom := base + int64(descent); bottom < lowestBottom {
				lowestBottom = bottom
			}
		}
	}
	return placements, nil
}

// lineExtents returns the cross-axis ascent (positive) and descent (negative)
// of the ink of line, relative to its baseline, together with whether the line
// holds any ink at all. Runs without any glyph extents fall back to their font
// metrics, so that outputs produced by a shaper skipping extents are still
// usable.
func lineExtents(line Line) (ascent, descent fixed.Int26_6, hasInk bool) {
	for _, run := range line {
		if len(run.Glyphs) == 0 {
			// An empty run carries no ink, even if the shaper filled its
			// line bounds.
			continue
		}
		bounds := run.GlyphBounds
		if bounds.Ascent == 0 && bounds.Descent == 0 {
			bounds = run.LineBounds
		}
		if bounds.Ascent > ascent {
			ascent = bounds.Ascent
		}
		if bounds.Descent < descent {
			descent = bounds.Descent
		}
		hasInk = true
	}
	return ascent, descent, hasInk
}

// lineRunPositions returns the position of each run of line, indexed like
// line, accumulating the advances in visual order along the main axis.
func lineRunPositions(line Line, vertical bool, baseline fixed.Int26_6) ([]fixed.Point26_6, error) {
	if len(line) == 0 {
		return nil, nil
	}
	order := make([]int, len(line))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return line[order[i]].VisualIndex < line[order[j]].VisualIndex
	})

	positions := make([]fixed.Point26_6, len(line))
	var mainAxis int64
	for _, runIndex := range order {
		if !fitsInt26_6(mainAxis) {
			return nil, fmt.Errorf("%w: position of run %d is %d",
				ErrGridOverflow, runIndex, mainAxis)
		}
		if vertical {
			positions[runIndex] = fixed.Point26_6{X: baseline, Y: fixed.Int26_6(mainAxis)}
		} else {
			positions[runIndex] = fixed.Point26_6{X: fixed.Int26_6(mainAxis), Y: baseline}
		}
		mainAxis += int64(line[runIndex].Advance)
	}
	return positions, nil
}

func fitsInt26_6(value int64) bool {
	return value >= math.MinInt32 && value <= math.MaxInt32
}
