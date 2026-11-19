// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	td "github.com/go-text/typesetting-utils/opentype"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/math/fixed"
)

// gridTestRun returns a horizontal run with the given vertical metrics and no
// glyphs, for tests focusing on the grid arithmetic.
func gridTestRun(ascent, descent, advance int) Output {
	return Output{
		Direction: di.DirectionLTR,
		Advance:   fixed.I(advance),
		GlyphBounds: Bounds{
			Ascent:  fixed.I(ascent),
			Descent: fixed.I(descent),
		},
		Glyphs: []Glyph{{Width: fixed.I(1), Advance: fixed.I(advance)}},
	}
}

func point(x, y int) fixed.Point26_6 {
	return fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)}
}

func TestPlaceLinesArithmetic(t *testing.T) {
	type testcase struct {
		name  string
		lines []Line
		grid  BaselineGrid
		want  []LinePlacement
	}
	for _, tc := range []testcase{
		{
			name:  "no lines",
			lines: nil,
			grid:  BaselineGrid{Step: fixed.I(10), FirstBaseline: fixed.I(50)},
			want:  nil,
		},
		{
			name: "consecutive lines share the grid",
			lines: []Line{
				{gridTestRun(8, -2, 10)},
				{gridTestRun(6, -3, 12)},
			},
			grid: BaselineGrid{Step: fixed.I(12), FirstBaseline: fixed.I(48)},
			want: []LinePlacement{
				{Baseline: fixed.I(48), Runs: []fixed.Point26_6{point(0, 48)}},
				{Baseline: fixed.I(36), Runs: []fixed.Point26_6{point(0, 36)}},
			},
		},
		{
			name: "tall line skips grid lines",
			lines: []Line{
				{gridTestRun(8, -2, 10)},
				// ascent+descent is 24, larger than the 12 step.
				{gridTestRun(20, -4, 10)},
				{gridTestRun(8, -2, 10)},
			},
			grid: BaselineGrid{Step: fixed.I(12), FirstBaseline: fixed.I(48)},
			want: []LinePlacement{
				{Baseline: fixed.I(48), Runs: []fixed.Point26_6{point(0, 48)}},
				// On the next grid line, the top would be 44 > 46 (previous
				// descent), so one grid line is skipped.
				{Baseline: fixed.I(24), Runs: []fixed.Point26_6{point(0, 24)}, Skipped: 1},
				{Baseline: fixed.I(12), Runs: []fixed.Point26_6{point(0, 12)}},
			},
		},
		{
			name: "glyph extents decide, not line bounds",
			lines: []Line{
				{
					{
						Direction:   di.DirectionLTR,
						Advance:     fixed.I(10),
						LineBounds:  Bounds{Ascent: fixed.I(30), Descent: fixed.I(-30)},
						GlyphBounds: Bounds{Ascent: fixed.I(5), Descent: fixed.I(-1)},
						Glyphs:      []Glyph{simpleGlyph(0)},
					},
				},
				{gridTestRun(5, -1, 10)},
			},
			grid: BaselineGrid{Step: fixed.I(10), FirstBaseline: fixed.I(100)},
			want: []LinePlacement{
				{Baseline: fixed.I(100), Runs: []fixed.Point26_6{point(0, 100)}},
				// The nominal 30/30 metrics would have skipped a line.
				{Baseline: fixed.I(90), Runs: []fixed.Point26_6{point(0, 90)}},
			},
		},
		{
			name: "empty lines consume one step and have no ink",
			lines: []Line{
				{},
				{gridTestRun(8, -2, 10)},
				{},
				{},
				// The tall line must still clear the ink of the first inked
				// line, even across the empty ones.
				{gridTestRun(20, -4, 10)},
				{},
				{gridTestRun(8, -2, 10)},
			},
			grid: BaselineGrid{Step: fixed.I(12), FirstBaseline: fixed.I(48)},
			want: []LinePlacement{
				{Baseline: fixed.I(48)},
				{Baseline: fixed.I(36), Runs: []fixed.Point26_6{point(0, 36)}},
				{Baseline: fixed.I(24)},
				{Baseline: fixed.I(12)},
				{Baseline: fixed.I(0), Runs: []fixed.Point26_6{point(0, 0)}},
				{Baseline: fixed.I(-12)},
				{Baseline: fixed.I(-24), Runs: []fixed.Point26_6{point(0, -24)}},
			},
		},
		{
			name: "leading empty lines",
			lines: []Line{
				{},
				{},
				{gridTestRun(8, -2, 10)},
			},
			grid: BaselineGrid{Step: fixed.I(12), FirstBaseline: fixed.I(48)},
			want: []LinePlacement{
				{Baseline: fixed.I(48)},
				{Baseline: fixed.I(36)},
				{Baseline: fixed.I(24), Runs: []fixed.Point26_6{point(0, 24)}},
			},
		},
		{
			name: "visual runs within one line",
			lines: []Line{
				{
					{Direction: di.DirectionLTR, Advance: fixed.I(10), VisualIndex: 1, GlyphBounds: Bounds{Ascent: fixed.I(8)}, Glyphs: []Glyph{simpleGlyph(0)}},
					{Direction: di.DirectionLTR, Advance: fixed.I(25), VisualIndex: 0, GlyphBounds: Bounds{Ascent: fixed.I(8)}, Glyphs: []Glyph{simpleGlyph(1)}},
					{Direction: di.DirectionLTR, Advance: fixed.I(5), VisualIndex: 2, GlyphBounds: Bounds{Ascent: fixed.I(8)}, Glyphs: []Glyph{simpleGlyph(2)}},
				},
			},
			grid: BaselineGrid{Step: fixed.I(10), FirstBaseline: fixed.I(100)},
			want: []LinePlacement{
				{Baseline: fixed.I(100), Runs: []fixed.Point26_6{point(25, 100), point(0, 100), point(35, 100)}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlaceLines(tc.lines, tc.grid)
			tu.AssertNoErr(t, err)
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d placements, got %d", len(tc.want), len(got))
			}
			for i := range got {
				if got[i].Baseline != tc.want[i].Baseline {
					t.Errorf("line %d: baseline %v, expected %v", i, got[i].Baseline, tc.want[i].Baseline)
				}
				if got[i].Skipped != tc.want[i].Skipped {
					t.Errorf("line %d: skipped %d, expected %d", i, got[i].Skipped, tc.want[i].Skipped)
				}
				if len(got[i].Runs) != len(tc.want[i].Runs) {
					t.Fatalf("line %d: %d runs placed, expected %d", i, len(got[i].Runs), len(tc.want[i].Runs))
				}
				for j := range got[i].Runs {
					if got[i].Runs[j] != tc.want[i].Runs[j] {
						t.Errorf("line %d run %d: %v, expected %v", i, j, got[i].Runs[j], tc.want[i].Runs[j])
					}
				}
			}
		})
	}
}

func TestPlaceLinesVerticalRunPositions(t *testing.T) {
	// Vertical grids stack toward the left: check the run coordinates are on
	// the grid column, along the (negative) vertical advance.
	lines := []Line{
		{{Direction: di.DirectionTTB, Advance: fixed.I(-40), GlyphBounds: Bounds{Ascent: fixed.I(8), Descent: fixed.I(-2)}, Glyphs: []Glyph{simpleGlyph(0)}}},
		{{Direction: di.DirectionTTB, Advance: fixed.I(-40), GlyphBounds: Bounds{Ascent: fixed.I(20), Descent: fixed.I(-4)}, Glyphs: []Glyph{simpleGlyph(1)}}},
	}
	got, err := PlaceLines(lines, BaselineGrid{
		Step: fixed.I(12), FirstBaseline: fixed.I(8), Direction: di.DirectionTTB,
	})
	tu.AssertNoErr(t, err)
	if len(got) != 2 {
		t.Fatalf("expected 2 placements, got %d", len(got))
	}
	tu.Assert(t, got[0].Baseline == fixed.I(8))
	tu.Assert(t, got[0].Runs[0] == fixed.Point26_6{X: fixed.I(8), Y: 0})
	// On the next grid line, the right edge would be at 8-12+20 = 16, past
	// the previous left edge (6), so one grid line is skipped.
	tu.Assert(t, got[1].Skipped == 1)
	tu.Assert(t, got[1].Baseline == fixed.I(-16))
	tu.Assert(t, got[1].Runs[0] == fixed.Point26_6{X: fixed.I(-16), Y: 0})
}

func TestPlaceLinesErrors(t *testing.T) {
	line := Line{gridTestRun(8, -2, 10)}
	verticalRun := Line{{
		Direction: di.DirectionTTB,
		Advance:   fixed.I(-10),
		Glyphs:    []Glyph{simpleGlyph(0)},
	}}

	for _, tc := range []struct {
		name  string
		lines []Line
		grid  BaselineGrid
		want  error
	}{
		{
			name:  "zero step",
			lines: []Line{line},
			grid:  BaselineGrid{Step: 0, FirstBaseline: fixed.I(10)},
			want:  ErrInvalidGridStep,
		},
		{
			name:  "negative step",
			lines: []Line{line},
			grid:  BaselineGrid{Step: -fixed.I(10), FirstBaseline: fixed.I(10)},
			want:  ErrInvalidGridStep,
		},
		{
			name:  "axis mismatch",
			lines: []Line{line, verticalRun},
			grid:  BaselineGrid{Step: fixed.I(10), FirstBaseline: fixed.I(10)},
			want:  ErrGridAxisMismatch,
		},
		{
			name:  "baseline overflow",
			lines: []Line{line, line},
			grid:  BaselineGrid{Step: fixed.I(10), FirstBaseline: -fixed.I(1 << 25)},
			want:  ErrGridOverflow,
		},
		{
			name: "run position overflow",
			lines: []Line{{
				{Direction: di.DirectionLTR, Advance: 1 << 30, Glyphs: []Glyph{simpleGlyph(0)}},
				{Direction: di.DirectionLTR, Advance: 1 << 30, Glyphs: []Glyph{simpleGlyph(1)}},
				{Direction: di.DirectionLTR, Advance: 1 << 30, Glyphs: []Glyph{simpleGlyph(2)}},
			}},
			grid: BaselineGrid{Step: fixed.I(10), FirstBaseline: 0},
			want: ErrGridOverflow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlaceLines(tc.lines, tc.grid)
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected error %v, got %v", tc.want, err)
			}
			if got != nil {
				t.Fatalf("expected no placement on error, got %v", got)
			}
		})
	}
}

// gridTestFace loads a font face from the bundled test fonts.
func gridTestFace(t *testing.T, path string) *font.Face {
	t.Helper()
	b, err := td.Files.ReadFile(path)
	tu.AssertNoErr(t, err)
	face, err := font.ParseTTF(bytes.NewReader(b))
	tu.AssertNoErr(t, err)
	return face
}

// gridSpan is a span of text shaped with its own face and size.
type gridSpan struct {
	end  int
	face *font.Face
	size int
}

// shapeGridRuns shapes text split in explicit spans, each with its own face
// and size, so that mixed-font and mixed-size lines can be built.
func shapeGridRuns(text []rune, spans []gridSpan, dir di.Direction) []Output {
	shaper := HarfbuzzShaper{}
	outs := make([]Output, 0, len(spans))
	start := 0
	for _, span := range spans {
		outs = append(outs, shaper.Shape(Input{
			Text:      text,
			RunStart:  start,
			RunEnd:    span.end,
			Direction: dir,
			Face:      span.face,
			Size:      fixed.I(span.size),
			Script:    language.Latin,
			Language:  language.NewLanguage("en"),
		}))
		start = span.end
	}
	return outs
}

func wrapGridText(text []rune, dir di.Direction, maxWidth fixed.Int26_6, runs []Output) []Line {
	var wrapper LineWrapper
	lines, _ := wrapper.WrapParagraphF(WrapConfig{Direction: dir}, maxWidth, text, NewSliceIterator(runs))
	return lines
}

func TestPlaceLinesMixedFonts(t *testing.T) {
	regular, err := font.ParseTTF(bytes.NewReader(goregular.TTF))
	tu.AssertNoErr(t, err)
	mono, err := font.ParseTTF(bytes.NewReader(gomono.TTF))
	tu.AssertNoErr(t, err)

	// A hard line break separates the two lines; the first one mixes two
	// fonts at very different sizes, the second one is all at the small size.
	text := []rune("Hg Hi\nmono")
	runs := shapeGridRuns(text, []gridSpan{
		{end: 3, face: regular, size: 20},
		{end: 6, face: mono, size: 8},
		{end: len(text), face: mono, size: 8},
	}, di.DirectionLTR)
	lines := wrapGridText(text, di.DirectionLTR, fixed.I(1000), runs)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	grid := BaselineGrid{Step: fixed.I(24), FirstBaseline: fixed.I(120), Direction: di.DirectionLTR}
	placements, err := PlaceLines(lines, grid)
	tu.AssertNoErr(t, err)
	if len(placements) != 2 {
		t.Fatalf("expected 2 placements, got %d", len(placements))
	}

	// Both runs of the first line share the grid baseline, and the second run
	// starts at the advance of the first.
	first := placements[0]
	if len(first.Runs) != 2 {
		t.Fatalf("expected 2 runs on the first line, got %d", len(first.Runs))
	}
	tu.Assert(t, first.Baseline == grid.FirstBaseline)
	tu.Assert(t, first.Runs[0] == fixed.Point26_6{X: 0, Y: grid.FirstBaseline})
	tu.Assert(t, first.Runs[1] == fixed.Point26_6{X: lines[0][0].Advance, Y: grid.FirstBaseline})

	// The taller first line still fits in the step (the 20pt ascent is about
	// 15px, well below 24), so the second line is on the next grid line.
	second := placements[1]
	tu.Assert(t, second.Skipped == 0)
	tu.Assert(t, second.Baseline == grid.FirstBaseline-grid.Step)

	// The layout fits: no line overlaps its predecessor.
	checkNoOverlap(t, lines, placements)
}

func TestPlaceLinesSkipsTallLines(t *testing.T) {
	regular, err := font.ParseTTF(bytes.NewReader(goregular.TTF))
	tu.AssertNoErr(t, err)
	mono, err := font.ParseTTF(bytes.NewReader(gomono.TTF))
	tu.AssertNoErr(t, err)

	// Small, then very large, then small again, with hard breaks so that a
	// single line is much taller than the grid step.
	text := []rune("hi\nBIG\nhi")
	runs := shapeGridRuns(text, []gridSpan{
		{end: 3, face: mono, size: 8},
		{end: 7, face: regular, size: 60},
		{end: len(text), face: mono, size: 8},
	}, di.DirectionLTR)
	lines := wrapGridText(text, di.DirectionLTR, fixed.I(1000), runs)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	grid := BaselineGrid{Step: fixed.I(10), FirstBaseline: fixed.I(100), Direction: di.DirectionLTR}
	placements, err := PlaceLines(lines, grid)
	tu.AssertNoErr(t, err)

	tu.Assert(t, placements[0].Baseline == fixed.I(100))
	tu.Assert(t, placements[0].Skipped == 0)

	// The 60pt line is about 45px tall, several grid steps.
	tall := placements[1]
	tu.Assert(t, tall.Skipped > 0)
	tu.Assert(t, tall.Baseline == fixed.I(100)-fixed.Int26_6(tall.Skipped+1)*grid.Step)

	// Every baseline remains on the infinite grid.
	for i, placement := range placements {
		offset := grid.FirstBaseline - placement.Baseline
		if offset%grid.Step != 0 {
			t.Errorf("line %d baseline %v is off the grid", i, placement.Baseline)
		}
	}
	checkNoOverlap(t, lines, placements)
}

// checkNoOverlap verifies that the ink of consecutive placed lines does not
// overlap, using the same extents as [PlaceLines].
func checkNoOverlap(t *testing.T, lines []Line, placements []LinePlacement) {
	t.Helper()
	for i := 1; i < len(placements); i++ {
		_, prevDescent, prevInk := lineExtents(lines[i-1])
		ascent, _, hasInk := lineExtents(lines[i])
		if !prevInk || !hasInk {
			continue
		}
		prevBottom := placements[i-1].Baseline - prevDescent
		top := placements[i].Baseline + ascent
		if top > prevBottom {
			t.Errorf("line %d (top %v) overlaps line %d (bottom %v)", i, top, i-1, prevBottom)
		}
	}
}

func TestPlaceLinesKeepsShapingOutput(t *testing.T) {
	face, err := font.ParseTTF(bytes.NewReader(goregular.TTF))
	tu.AssertNoErr(t, err)
	text := []rune("one two\nthree four")
	runs := shapeGridRuns(text, []gridSpan{{end: len(text), face: face, size: 16}}, di.DirectionLTR)
	lines := wrapGridText(text, di.DirectionLTR, fixed.I(50), runs)
	if len(lines) < 2 {
		t.Fatalf("expected wrapped lines, got %d", len(lines))
	}

	before := make([]Line, len(lines))
	for i := range lines {
		before[i] = append(Line(nil), lines[i]...)
		for j := range lines[i] {
			before[i][j].Glyphs = append([]Glyph(nil), lines[i][j].Glyphs...)
		}
	}

	_, err = PlaceLines(lines, BaselineGrid{Step: fixed.I(20), FirstBaseline: fixed.I(100), Direction: di.DirectionLTR})
	tu.AssertNoErr(t, err)

	if len(before) != len(lines) {
		t.Fatalf("line count changed")
	}
	for i := range before {
		if len(before[i]) != len(lines[i]) {
			t.Fatalf("run count of line %d changed", i)
		}
		for j := range before[i] {
			a, b := before[i][j], lines[i][j]
			if a.Advance != b.Advance || a.Size != b.Size ||
				a.GlyphBounds != b.GlyphBounds || a.LineBounds != b.LineBounds ||
				a.VisualIndex != b.VisualIndex || a.Runes != b.Runes {
				t.Errorf("line %d run %d changed by PlaceLines", i, j)
			}
			if len(a.Glyphs) != len(b.Glyphs) {
				t.Errorf("line %d run %d glyph count changed by PlaceLines", i, j)
				continue
			}
			for k := range a.Glyphs {
				if a.Glyphs[k] != b.Glyphs[k] {
					t.Errorf("line %d run %d glyph %d changed by PlaceLines", i, j, k)
				}
			}
		}
	}
}

func TestPlaceLinesVerticalMixedSizes(t *testing.T) {
	face := gridTestFace(t, "common/mplus-1p-regular.ttf")
	text := []rune("あa\nB")
	runs := shapeGridRuns(text, []gridSpan{
		{end: 2, face: face, size: 20},
		{end: 4, face: face, size: 40},
	}, di.DirectionTTB)
	lines := wrapGridText(text, di.DirectionTTB, fixed.I(1000), runs)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	grid := BaselineGrid{Step: fixed.I(12), FirstBaseline: fixed.I(80), Direction: di.DirectionTTB}
	placements, err := PlaceLines(lines, grid)
	tu.AssertNoErr(t, err)

	// Runs of the first line are side by side along the (negative) vertical
	// advance, on the same grid column.
	tu.Assert(t, placements[0].Baseline == fixed.I(80))
	tu.Assert(t, placements[0].Runs[0] == fixed.Point26_6{X: fixed.I(80), Y: 0})
	tu.Assert(t, placements[0].Runs[1] == fixed.Point26_6{X: fixed.I(80), Y: lines[0][0].Advance})
	tu.Assert(t, placements[0].Runs[1].Y < 0)

	// The 40pt line is much wider than the 12px step: it skips grid lines.
	tu.Assert(t, placements[1].Skipped > 0)
	tu.Assert(t, placements[1].Baseline == fixed.I(80)-fixed.Int26_6(placements[1].Skipped+1)*grid.Step)
	for i, placement := range placements {
		offset := grid.FirstBaseline - placement.Baseline
		if offset%grid.Step != 0 {
			t.Errorf("line %d baseline %v is off the grid", i, placement.Baseline)
		}
	}

	// The vertical extent is measured on the x axis.
	for i := 1; i < len(lines); i++ {
		prevDescent, _, prevInk := lineExtents(lines[i-1])
		_, ascent, hasInk := lineExtents(lines[i])
		if !prevInk || !hasInk {
			continue
		}
		// Toward the descent side means toward smaller x for vertical text.
		prevLeft := placements[i-1].Baseline + prevDescent
		right := placements[i].Baseline - ascent
		if right > prevLeft {
			t.Errorf("line %d (right %v) overlaps line %d (left %v)", i, right, i-1, prevLeft)
		}
	}
}

func TestPlaceLinesAdjustBaselinesPreserved(t *testing.T) {
	face := gridTestFace(t, "common/mplus-1p-regular.ttf")
	// Upright ideographs and a sideways Latin letter, so that
	// [Line.AdjustBaselines] actually shifts the sideways run.
	text := []rune("あAあ")
	var seg Segmenter
	inputs := seg.Split(Input{
		Text:      text,
		RunEnd:    len(text),
		Direction: di.DirectionTTB,
		Face:      face,
		Size:      fixed.I(20),
		Language:  language.NewLanguage("ja"),
	}, fixedFontmap{face})
	shaper := HarfbuzzShaper{}
	line := make(Line, len(inputs))
	for i, in := range inputs {
		line[i] = shaper.Shape(in)
	}
	if len(line) != 3 {
		t.Fatalf("expected upright/sideways/upright runs, got %d", len(line))
	}
	before := line[1].GlyphBounds
	line.AdjustBaselines()
	if line[1].GlyphBounds == before {
		t.Fatalf("AdjustBaselines did not shift the sideways run")
	}
	adjusted := make([][]Glyph, len(line))
	for i := range line {
		adjusted[i] = append([]Glyph(nil), line[i].Glyphs...)
	}

	placements, err := PlaceLines([]Line{line}, BaselineGrid{
		Step: fixed.I(20), FirstBaseline: fixed.I(30), Direction: di.DirectionTTB,
	})
	tu.AssertNoErr(t, err)
	tu.Assert(t, placements[0].Baseline == fixed.I(30))

	for i := range line {
		for j := range adjusted[i] {
			if line[i].Glyphs[j] != adjusted[i][j] {
				t.Fatalf("PlaceLines overwrote the baseline adjustment of run %d glyph %d", i, j)
			}
		}
	}
}

func TestPlaceLinesRTLVisualOrder(t *testing.T) {
	// Latin and Hebrew runs in an RTL paragraph: the runs of a line are
	// stored logically, and only [Output.VisualIndex] gives their visual
	// order, from right to left.
	face := gridTestFace(t, "common/DejaVuSans.ttf")
	text := []rune("abc שלום xyz")
	var seg Segmenter
	inputs := seg.Split(Input{
		Text:      text,
		RunEnd:    len(text),
		Direction: di.DirectionRTL,
		Face:      face,
		Size:      fixed.I(16),
		Script:    language.Latin,
		Language:  language.NewLanguage("he"),
	}, fixedFontmap{face})

	shaper := HarfbuzzShaper{}
	outs := make([]Output, len(inputs))
	for i, in := range inputs {
		outs[i] = shaper.Shape(in)
	}
	if len(outs) < 3 {
		t.Fatalf("expected Latin/Hebrew/Latin runs, got %d", len(outs))
	}

	lines := wrapGridText(text, di.DirectionRTL, fixed.I(10000), outs)
	if len(lines) != 1 {
		t.Fatalf("expected a single line, got %d", len(lines))
	}
	line := lines[0]

	placements, err := PlaceLines(lines, BaselineGrid{
		Step: fixed.I(20), FirstBaseline: fixed.I(200), Direction: di.DirectionRTL,
	})
	tu.AssertNoErr(t, err)

	// The leftmost run is the one with the lowest visual index.
	leftmost := 0
	for i, run := range line {
		if run.VisualIndex < line[leftmost].VisualIndex {
			leftmost = i
		}
	}
	tu.Assert(t, placements[0].Runs[leftmost].X == 0)

	// Runs are contiguous and ordered by increasing visual index.
	byVisual := make([]int, len(line))
	for i := range byVisual {
		byVisual[i] = i
	}
	sort.Slice(byVisual, func(i, j int) bool {
		return line[byVisual[i]].VisualIndex < line[byVisual[j]].VisualIndex
	})
	var x fixed.Int26_6
	for _, runIndex := range byVisual {
		run := line[runIndex]
		position := placements[0].Runs[runIndex]
		if position.X != x {
			t.Errorf("run %d starts at %v, expected %v", runIndex, position.X, x)
		}
		if position.Y != fixed.I(200) {
			t.Errorf("run %d is not on the baseline: %v", runIndex, position.Y)
		}
		x += run.Advance
		if run.Advance <= 0 {
			t.Errorf("horizontal run %d has non positive advance %v", runIndex, run.Advance)
		}
	}
}
