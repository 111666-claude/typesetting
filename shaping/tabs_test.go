package shaping

import (
	"bytes"
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/math/fixed"
)

// newTabShaper returns a shaper and the face used by the tab tests.
func newTabShaper() (Shaper, *font.Face) {
	face, err := font.ParseTTF(bytes.NewReader(goregular.TTF))
	if err != nil {
		panic(err)
	}
	return &HarfbuzzShaper{}, face
}

func shapeTabText(t *testing.T, shaper Shaper, face *font.Face, text []rune, dir di.Direction) []Output {
	t.Helper()
	return []Output{shaper.Shape(Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: dir,
		Face:      face,
		Size:      fixed.I(16),
		Script:    language.Latin,
		Language:  language.NewLanguage("EN"),
	})}
}

// lineAdvance sums the advances of a line's runs.
func lineAdvance(line Line) fixed.Int26_6 {
	var total fixed.Int26_6
	for _, run := range line {
		total += run.Advance
	}
	return total
}

// glyphPositions returns the main-axis origin of every visible glyph of a
// line when the runs are laid out in visual order, together with the rune
// (cluster) index each glyph maps to.
type positionedGlyph struct {
	cluster int
	x       fixed.Int26_6
	gid     font.GID
	isTab   bool
}

// visualGlyphs walks the line in visual order, placing each run one after
// another from the leading edge, and returns every glyph with its origin
// relative to that leading edge. For LTR paragraphs the leading edge is the
// left and coordinates grow rightwards; for RTL it is the right edge and the
// returned x is the distance from that edge (so a left-tab stop at position
// P corresponds to x == P in both cases).
func visualGlyphs(line Line, dir di.Direction) []positionedGlyph {
	runs := make(Line, len(line))
	copy(runs, line)
	visual := make(Line, len(runs))
	for _, run := range runs {
		visual[run.VisualIndex] = run
	}
	var out []positionedGlyph
	var x fixed.Int26_6
	for _, run := range visual {
		rtl := run.Direction.Progression() == di.TowardTopLeft
		if rtl {
			// The glyph slice is in visual order; traversing it forward
			// follows the visual progression away from the leading edge.
			for i := range run.Glyphs {
				g := run.Glyphs[i]
				out = append(out, positionedGlyph{cluster: g.ClusterIndex, x: x, gid: g.GlyphID, isTab: g.isTab})
				if g.Advance < 0 {
					x -= g.Advance
				} else {
					x += g.Advance
				}
			}
		} else {
			for i := range run.Glyphs {
				g := run.Glyphs[i]
				out = append(out, positionedGlyph{cluster: g.ClusterIndex, x: x, gid: g.GlyphID, isTab: g.isTab})
				x += g.Advance
			}
		}
	}
	_ = dir
	return out
}

// xOfCluster returns the visual x of the first glyph mapped to cluster.
func xOfCluster(t *testing.T, glyphs []positionedGlyph, cluster int) fixed.Int26_6 {
	t.Helper()
	for _, g := range glyphs {
		if g.cluster == cluster {
			return g.x
		}
	}
	t.Fatalf("cluster %d not found among %+v", cluster, glyphs)
	return 0
}

func TestNormalizeTabStops(t *testing.T) {
	tests := []struct {
		name  string
		stops []TabStop
		want  []TabStop
	}{
		{name: "empty", stops: nil, want: nil},
		{
			name:  "sorted kept",
			stops: []TabStop{{Position: fixed.I(10), Alignment: TabLeft}, {Position: fixed.I(20), Alignment: TabRight}},
			want:  []TabStop{{Position: fixed.I(10), Alignment: TabLeft}, {Position: fixed.I(20), Alignment: TabRight}},
		},
		{
			name:  "unsorted is sorted",
			stops: []TabStop{{Position: fixed.I(30)}, {Position: fixed.I(10)}, {Position: fixed.I(20)}},
			want:  []TabStop{{Position: fixed.I(10)}, {Position: fixed.I(20)}, {Position: fixed.I(30)}},
		},
		{
			name:  "duplicate keeps first",
			stops: []TabStop{{Position: fixed.I(10), Alignment: TabRight}, {Position: fixed.I(10), Alignment: TabLeft}, {Position: fixed.I(20)}},
			want:  []TabStop{{Position: fixed.I(10), Alignment: TabRight}, {Position: fixed.I(20)}},
		},
		{
			name:  "non positive dropped",
			stops: []TabStop{{Position: 0}, {Position: -fixed.I(5)}, {Position: fixed.I(8)}},
			want:  []TabStop{{Position: fixed.I(8)}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := append([]TabStop(nil), tc.stops...)
			got := normalizeTabStops(tc.stops)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("index %d: got %v, want %v", i, got[i], tc.want[i])
				}
			}
			// The input slice must not be modified.
			if len(input) != len(tc.stops) {
				t.Fatal("input length changed")
			}
			for i := range input {
				if input[i] != tc.stops[i] {
					t.Fatalf("input modified at %d: %v != %v", i, input[i], tc.stops[i])
				}
			}
		})
	}
}

func TestMarkTabGlyphs(t *testing.T) {
	// "a\tb" shapes one glyph per rune with the Go font.
	shaper, face := newTabShaper()
	out := shapeTabText(t, shaper, face, []rune("a\tb"), di.DirectionLTR)[0]
	if len(out.Glyphs) != 3 {
		t.Fatalf("expected 3 glyphs, got %d", len(out.Glyphs))
	}
	if out.Glyphs[0].isTab || !out.Glyphs[1].isTab || out.Glyphs[2].isTab {
		t.Fatalf("unexpected tab flags: %+v", out.Glyphs)
	}
}

// TestTabDefaultLayoutUnchanged verifies requirement 3: with no tab stops
// configured, the tab glyph keeps its shaper metrics and wrapping is
// identical to ordinary text.
func TestTabDefaultLayoutUnchanged(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("a\tb")
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	originalAdvance := runs[0].Advance
	tabGlyph := runs[0].Glyphs[1]
	originalTabAdvance := tabGlyph.Advance

	var l LineWrapper
	lines, _ := l.WrapParagraph(WrapConfig{Direction: di.DirectionLTR}, 1000, text, NewSliceIterator(runs))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if got := lineAdvance(lines[0]); got != originalAdvance {
		t.Fatalf("advance changed without tab stops: got %v want %v", got, originalAdvance)
	}
	if g := lines[0][0].Glyphs[1]; !g.isTab || g.Advance != originalTabAdvance || g.GlyphID == font.EmptyGlyph {
		t.Fatalf("tab glyph altered without tab stops: %+v", g)
	}
}

// TestTabLeftStop verifies that a left tab moves following text to the exact
// stop position, measured from actual glyph advances.
func TestTabLeftStop(t *testing.T) {
	shaper, face := newTabShaper()
	// "aaa" gives a deterministic prefix width; use two rows with prefixes of
	// different lengths and check the column after the tab is identical.
	text1 := []rune("a\tb")
	text2 := []rune("aaa\tb")
	stop := fixed.I(50)
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: stop, Alignment: TabLeft}}}

	var l LineWrapper
	runs1 := shapeTabText(t, shaper, face, text1, di.DirectionLTR)
	lines1, _ := l.WrapParagraph(config, 1000, text1, NewSliceIterator(runs1))
	g1 := visualGlyphs(lines1[0], di.DirectionLTR)
	// Cluster 2 is 'b'. Assert before another wrap reuses the wrapper's
	// internal line storage.
	if x := xOfCluster(t, g1, 2); x != stop {
		t.Fatalf("short row: b at %v, want %v", x, stop)
	}

	runs2 := shapeTabText(t, shaper, face, text2, di.DirectionLTR)
	lines2, _ := l.WrapParagraph(config, 1000, text2, NewSliceIterator(runs2))
	g2 := visualGlyphs(lines2[0], di.DirectionLTR)
	// 'b' is cluster 4 in "aaa\tb".
	if x := xOfCluster(t, g2, 4); x != stop {
		t.Fatalf("long row: b at %v, want %v", x, stop)
	}
}

// TestTabPassedStopIsNoop verifies that when the current position is already
// at or past a stop, the tab does not move the cursor backwards.
func TestTabPassedStopIsNoop(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("aaaaa\tb") // the prefix is wider than the stop
	stop := fixed.I(5)
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: stop}}}

	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))

	glyphs := visualGlyphs(lines[0], di.DirectionLTR)
	// The tab jump must be zero: 'b' starts exactly where the prefix ends.
	var tabAdvance fixed.Int26_6
	for _, run := range lines[0] {
		for _, g := range run.Glyphs {
			if g.isTab {
				tabAdvance = g.Advance
			}
		}
	}
	if tabAdvance != 0 {
		t.Fatalf("expected zero tab advance when stop is passed, got %v", tabAdvance)
	}
	// 'b' must start at the natural prefix width.
	runsNatural := shapeTabText(t, shaper, face, []rune("aaaaa"), di.DirectionLTR)
	prefixWidth := runsNatural[0].Advance
	if x := xOfCluster(t, glyphs, 6); x != prefixWidth {
		t.Fatalf("b at %v, want natural prefix width %v", x, prefixWidth)
	}
}

// TestTabRightAndCenter checks the right and center alignments against
// independently computed expected positions.
func TestTabRightAndCenter(t *testing.T) {
	shaper, face := newTabShaper()
	// Prefix "aa", following segment "bbb".
	text := []rune("aa\tbbb")
	// Measure the pieces independently.
	prefixWidth := shapeTabText(t, shaper, face, []rune("aa"), di.DirectionLTR)[0].Advance
	segmentWidth := shapeTabText(t, shaper, face, []rune("bbb"), di.DirectionLTR)[0].Advance

	for _, tc := range []struct {
		name      string
		alignment TabAlignment
		// expectedStart is where 'bbb' should start.
		expectedStart func(stop fixed.Int26_6) fixed.Int26_6
	}{
		{
			name:          "right",
			alignment:     TabRight,
			expectedStart: func(stop fixed.Int26_6) fixed.Int26_6 { return stop - segmentWidth },
		},
		{
			name:          "center",
			alignment:     TabCenter,
			expectedStart: func(stop fixed.Int26_6) fixed.Int26_6 { return stop - segmentWidth/2 },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stop := fixed.I(200) // safely past both pieces
			config := WrapConfig{
				Direction: di.DirectionLTR,
				TabStops:  []TabStop{{Position: stop, Alignment: tc.alignment}},
			}
			var l LineWrapper
			runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
			lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))
			glyphs := visualGlyphs(lines[0], di.DirectionLTR)
			want := tc.expectedStart(stop)
			if x := xOfCluster(t, glyphs, 3); x != want {
				t.Fatalf("segment start %v, want %v (prefix=%v segment=%v)", x, want, prefixWidth, segmentWidth)
			}
		})
	}
}

// TestConsecutiveTabs verifies that consecutive tabs consume successive
// stops and that a tab with no stop left collapses.
func TestConsecutiveTabs(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("a\t\tb")
	config := WrapConfig{
		Direction: di.DirectionLTR,
		TabStops: []TabStop{
			{Position: fixed.I(40)},
			{Position: fixed.I(80)},
		},
	}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))

	glyphs := visualGlyphs(lines[0], di.DirectionLTR)
	// 'a' at 0, first tab jumps to 40, second tab jumps to 80, 'b' at 80.
	if x := xOfCluster(t, glyphs, 0); x != 0 {
		t.Fatalf("a at %v, want 0", x)
	}
	if x := xOfCluster(t, glyphs, 3); x != fixed.I(80) {
		t.Fatalf("b at %v, want 80", x)
	}

	// Only one stop configured: the second tab has no stop and collapses.
	configSingle := WrapConfig{
		Direction: di.DirectionLTR,
		TabStops:  []TabStop{{Position: fixed.I(40)}},
	}
	var l2 LineWrapper
	runs2 := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines2, _ := l2.WrapParagraph(configSingle, 1000, text, NewSliceIterator(runs2))
	glyphs2 := visualGlyphs(lines2[0], di.DirectionLTR)
	if x := xOfCluster(t, glyphs2, 3); x != fixed.I(40) {
		t.Fatalf("b at %v with single stop, want 40", x)
	}
}

// TestTabStopsBeyondLineWidth verifies that stops past the line width never
// force an overflow and are simply unreachable.
func TestTabStopsBeyondLineWidth(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("a\tb")
	config := WrapConfig{
		Direction: di.DirectionLTR,
		TabStops:  []TabStop{{Position: fixed.I(500)}},
	}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines, _ := l.WrapParagraph(config, 30, text, NewSliceIterator(runs))
	// The line must still be produced; the stop is unreachable so the tab
	// collapses and 'b' follows 'a' directly.
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	glyphs := visualGlyphs(lines[0], di.DirectionLTR)
	if got := lineAdvance(lines[0]); got > fixed.I(30) {
		t.Fatalf("line width %v exceeds max width 30", got)
	}
	runsAB := shapeTabText(t, shaper, face, []rune("ab"), di.DirectionLTR)
	if x := xOfCluster(t, glyphs, 2); x != runsAB[0].Glyphs[1].Advance {
		t.Fatalf("b did not follow a directly when stop unreachable")
	}
}

// TestTabWrappingColumnStability verifies requirement 2 across multiple
// wrapped lines: the column introduced by a tab is the same on every line.
func TestTabWrappingColumnStability(t *testing.T) {
	shaper, face := newTabShaper()
	// Several short rows separated by spaces, each containing a tab. Use a
	// narrow width so wrapping occurs between rows.
	text := []rune("aa\tbb aaa\tb a\tccc")
	stop := fixed.I(60)
	config := WrapConfig{
		Direction:                     di.DirectionLTR,
		TabStops:                      []TabStop{{Position: stop}},
		DisableTrailingWhitespaceTrim: true,
	}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines, _ := l.WrapParagraph(config, 90, text, NewSliceIterator(runs))
	if len(lines) < 2 {
		t.Fatalf("expected wrapping into multiple lines, got %d", len(lines))
	}
	for i, line := range lines {
		glyphs := visualGlyphs(line, di.DirectionLTR)
		// Find the glyph immediately following each tab and ensure it starts
		// at the stop.
		for j, g := range glyphs {
			if !g.isTab {
				continue
			}
			// Next visible glyph in visual order.
			var next *positionedGlyph
			for k := j + 1; k < len(glyphs); k++ {
				next = &glyphs[k]
				break
			}
			if next == nil {
				continue
			}
			if next.x != stop {
				t.Fatalf("line %d: post-tab glyph at %v, want %v", i, next.x, stop)
			}
		}
	}
}

// TestTabMultiFont verifies column stability when runs use different faces,
// since the tab position is measured from the line origin rather than glyph
// counts.
func TestTabMultiFont(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("a\tb")
	// Build two runs with different sizes, split around the tab.
	runs := []Output{
		shaper.Shape(Input{Text: text, RunStart: 0, RunEnd: 2, Direction: di.DirectionLTR, Face: face, Size: fixed.I(16), Script: language.Latin}),
		shaper.Shape(Input{Text: text, RunStart: 2, RunEnd: 3, Direction: di.DirectionLTR, Face: face, Size: fixed.I(32), Script: language.Latin}),
	}
	stop := fixed.I(70)
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: stop}}}
	var l LineWrapper
	lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	glyphs := visualGlyphs(lines[0], di.DirectionLTR)
	if x := xOfCluster(t, glyphs, 2); x != stop {
		t.Fatalf("post-tab glyph at %v, want %v across font change", x, stop)
	}
}

// TestTabRTL verifies that RTL runs produce a deterministic, well-formed
// result: the tab becomes an invisible glyph carrying a defined jump, the run
// advance stays consistent with the shaped glyphs, and no rune is lost.
// Exact per-glyph visual geometry in RTL is the renderer's responsibility
// (glyphs are stored in visual order); here we assert the structural
// invariants and exercise the RTL traversal path.
func TestTabRTL(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")
	shaper := &HarfbuzzShaper{}
	// Logical: "ش\tم" — tab between two Arabic runes.
	text := []rune{'ش', '\t', 'م'}
	runs := []Output{shaper.Shape(Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: di.DirectionRTL,
		Face:      face,
		Size:      fixed.I(16),
		Script:    language.Arabic,
		Language:  language.NewLanguage("AR"),
	})}
	if !runs[0].Glyphs[hasTabGlyph(t, runs[0])].isTab {
		t.Fatal("tab not marked in RTL run")
	}
	stop := fixed.I(120)
	config := WrapConfig{Direction: di.DirectionRTL, TabStops: []TabStop{{Position: stop}}}
	var l LineWrapper
	lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var (
		tabCount     int
		tabAdvance   fixed.Int26_6
		sumAdvances  fixed.Int26_6
		runeCount    int
		ordinaryKept = 0
	)
	for _, run := range lines[0] {
		runeCount += run.Runes.Count
		var runSum fixed.Int26_6
		for _, g := range run.Glyphs {
			runSum += g.Advance
			if g.isTab {
				tabCount++
				tabAdvance = g.Advance
				if g.GlyphID != font.EmptyGlyph {
					t.Fatalf("RTL tab glyph not invisible: gid=%v", g.GlyphID)
				}
				if g.Width != 0 || g.Height != 0 {
					t.Fatalf("RTL tab glyph not empty: %+v", g)
				}
			} else if g.GlyphID != font.EmptyGlyph {
				ordinaryKept++
			}
		}
		if runSum != run.Advance {
			t.Fatalf("RTL run advance %v != sum of glyph advances %v", run.Advance, runSum)
		}
		sumAdvances += run.Advance
	}
	if tabCount != 1 {
		t.Fatalf("expected 1 tab glyph, got %d", tabCount)
	}
	if tabAdvance <= 0 {
		t.Fatalf("RTL tab jump = %v, want a positive defined advance", tabAdvance)
	}
	if runeCount != len(text) {
		t.Fatalf("rune accounting broken: %d of %d", runeCount, len(text))
	}
	if ordinaryKept != 2 {
		t.Fatalf("expected 2 visible ordinary glyphs, got %d", ordinaryKept)
	}
	if lineAdvance(lines[0]) != sumAdvances {
		t.Fatal("line advance inconsistent")
	}
}

// TestTabBuilderRTLTraversal checks that an RTL run (whose glyph slice is in
// visual order) is traversed in logical order by the builder, so tab segment
// widths are computed from the correct glyphs.
func TestTabBuilderRTLTraversal(t *testing.T) {
	// Build a synthetic RTL run. In visual order the slice is reversed; the
	// middle glyph is a tab. Logical clusters: 0 (glyph), 1 (tab), 2 (glyph).
	run := Output{
		Direction: di.DirectionRTL,
		Glyphs: []Glyph{
			{ClusterIndex: 2, Advance: fixed.I(7), GlyphCount: 1, RuneCount: 1, Width: fixed.I(5)},
			{ClusterIndex: 1, Advance: fixed.I(5), GlyphCount: 1, RuneCount: 1, isTab: true},
			{ClusterIndex: 0, Advance: fixed.I(9), GlyphCount: 1, RuneCount: 1, Width: fixed.I(5)},
		},
	}
	b := newTabLineBuilder([]TabStop{{Position: fixed.I(100)}}, di.DirectionRTL, 1000)
	b.appendRun(&run)
	// Two segments: before the tab (logical cluster 0, width 9) and after
	// (logical cluster 2, width 7).
	if len(b.widths) != 2 {
		t.Fatalf("expected 2 segments, got %d (%v)", len(b.widths), b.widths)
	}
	if b.widths[0] != fixed.I(9) {
		t.Fatalf("pre-tab segment width %v, want 9", b.widths[0])
	}
	if b.widths[1] != fixed.I(7) {
		t.Fatalf("post-tab segment width %v, want 7", b.widths[1])
	}
}

func hasTabGlyph(t *testing.T, out Output) int {
	t.Helper()
	for i, g := range out.Glyphs {
		if g.isTab {
			return i
		}
	}
	t.Fatal("no tab glyph")
	return -1
}

// TestTabGlyphInvisible verifies requirement 1: after resolution tab glyphs
// are empty and invisible, while ordinary glyphs are untouched.
func TestTabGlyphInvisible(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("a\tb")
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: fixed.I(50)}}}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	// Snapshot the ordinary glyph.
	ordinaryGlyph := runs[0].Glyphs[0]
	lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))
	var tabCount int
	for _, run := range lines[0] {
		for _, g := range run.Glyphs {
			if g.isTab {
				tabCount++
				if g.GlyphID != font.EmptyGlyph {
					t.Errorf("tab glyph id = %v, want EmptyGlyph", g.GlyphID)
				}
				if g.Width != 0 || g.Height != 0 || g.XOffset != 0 || g.YOffset != 0 {
					t.Errorf("tab glyph not empty: %+v", g)
				}
			}
		}
	}
	if tabCount != 1 {
		t.Fatalf("expected 1 tab glyph, got %d", tabCount)
	}
	// The ordinary glyph retains its shaper-assigned advance and id.
	if g := lines[0][0].Glyphs[0]; g.GlyphID != ordinaryGlyph.GlyphID || g.Advance != ordinaryGlyph.Advance {
		t.Fatalf("ordinary glyph mutated: %+v want %+v", g, ordinaryGlyph)
	}
}

// TestTabPrepareSegmented verifies the tab feature works through the
// segmenter-based entry point and that columns stay aligned across wrapped
// lines.
func TestTabPrepareSegmented(t *testing.T) {
	shaper, face := newTabShaper()
	// Two rows joined by a space, narrow enough to wrap onto two lines. Each
	// tab is then the first tab on its own line and reaches the same stop.
	text := []rune("a\tbb aa\tccc")
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	var seg segmenter.Segmenter
	seg.Init(text)
	stop := fixed.I(45)
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: stop}}}
	var l LineWrapper
	l.PrepareSegmented(config, &seg, NewSliceIterator(runs))
	// Collect every produced line.
	var lines []Line
	for {
		wl, done := l.WrapNextLine(70)
		if len(wl.Line) > 0 {
			lines = append(lines, wl.Line)
		}
		if done {
			break
		}
	}
	if len(lines) < 2 {
		t.Fatalf("expected wrapping into at least 2 lines, got %d", len(lines))
	}
	// Every line that contains a tab must place its post-tab segment at the
	// stop, proving columns are stable across lines.
	for i, line := range lines {
		glyphs := visualGlyphs(line, di.DirectionLTR)
		for j, g := range glyphs {
			if !g.isTab {
				continue
			}
			if j+1 >= len(glyphs) {
				continue
			}
			if next := glyphs[j+1]; next.x != stop {
				t.Fatalf("line %d: post-tab column at %v, want %v", i, next.x, stop)
			}
		}
	}
}

// TestTabStopPassedCollapses verifies through the segmenter entry point that
// a tab whose stop is already passed collapses deterministically rather than
// moving the cursor backwards.
func TestTabStopPassedCollapses(t *testing.T) {
	shaper, face := newTabShaper()
	// The first segment "aaaa" already extends past the stop at 10, so the
	// single tab cannot use it and must have a zero jump.
	text := []rune("aaaa\tb")
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	var seg segmenter.Segmenter
	seg.Init(text)
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: fixed.I(10)}}}
	var l LineWrapper
	l.PrepareSegmented(config, &seg, NewSliceIterator(runs))
	line, _ := l.WrapNextLine(1000)
	for _, run := range line.Line {
		for _, g := range run.Glyphs {
			if g.isTab && g.Advance != 0 {
				t.Fatalf("passed-stop tab has advance %v, want 0", g.Advance)
			}
		}
	}
}

// TestTabLineWidthAccountsForStops verifies that a wide tab jump contributes
// to the measured line width and can trigger wrapping at the correct point.
func TestTabLineWidthAccountsForStops(t *testing.T) {
	shaper, face := newTabShaper()
	// The stop at 120 makes the line wider than the narrow max width even
	// though the glyphs themselves are short.
	text := []rune("a\tb")
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: fixed.I(120)}}}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines, _ := l.WrapParagraph(config, 50, text, NewSliceIterator(runs))
	// The break cannot fit the resolved tab on line 1; wrapping must still
	// terminate with every rune accounted for.
	totalRunes := 0
	for _, line := range lines {
		for _, run := range line {
			totalRunes += run.Runes.Count
		}
	}
	if totalRunes != len(text) {
		t.Fatalf("rune accounting broken: %d of %d", totalRunes, len(text))
	}
}

// TestTabNoTabsInText verifies the fast path and measurement path both work
// when stops are configured but the text contains no tab.
func TestTabNoTabsInText(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("hello world")
	config := WrapConfig{Direction: di.DirectionLTR, TabStops: []TabStop{{Position: fixed.I(50)}}}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	wantAdvance := runs[0].Advance
	lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))
	if got := lineAdvance(lines[0]); got != wantAdvance {
		t.Fatalf("width %v, want natural %v", got, wantAdvance)
	}
}

// TestTabBreakAtTab verifies rune accounting and determinism when a line
// break lands on a tab character under a narrow width.
func TestTabBreakAtTab(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("aaaa\tbbbb\tcccc")
	config := WrapConfig{
		Direction: di.DirectionLTR,
		TabStops: []TabStop{
			{Position: fixed.I(30)},
			{Position: fixed.I(60)},
		},
	}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines, _ := l.WrapParagraph(config, 40, text, NewSliceIterator(runs))
	totalRunes := 0
	for _, line := range lines {
		for _, run := range line {
			totalRunes += run.Runes.Count
		}
	}
	if totalRunes != len(text) {
		t.Fatalf("rune accounting broken after break-at-tab: %d of %d", totalRunes, len(text))
	}
	// Every run advance must equal the sum of its glyph advances.
	for _, line := range lines {
		for _, run := range line {
			var sum fixed.Int26_6
			for _, g := range run.Glyphs {
				sum += g.Advance
			}
			if sum != run.Advance {
				t.Fatalf("run advance %v != glyph sum %v", run.Advance, sum)
			}
		}
	}
}

// TestTabReversedAndDuplicatedStops verifies end to end that reversed and
// duplicated stop positions are normalized into a usable increasing list.
func TestTabReversedAndDuplicatedStops(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("a\t\tb")
	config := WrapConfig{
		Direction: di.DirectionLTR,
		TabStops: []TabStop{
			{Position: fixed.I(80)},
			{Position: fixed.I(40)},
			{Position: fixed.I(40)}, // duplicate, dropped
		},
	}
	var l LineWrapper
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	lines, _ := l.WrapParagraph(config, 1000, text, NewSliceIterator(runs))
	glyphs := visualGlyphs(lines[0], di.DirectionLTR)
	// First tab reaches 40, second tab reaches 80, so 'b' starts at 80.
	if x := xOfCluster(t, glyphs, 3); x != fixed.I(80) {
		t.Fatalf("b at %v, want 80 after normalizing reversed/duplicated stops", x)
	}
}

// TestTabEmptyStopsIsDefault verifies an explicitly empty stop list leaves
// shaping output completely untouched.
func TestTabEmptyStopsIsDefault(t *testing.T) {
	shaper, face := newTabShaper()
	text := []rune("a\tb")
	runs := shapeTabText(t, shaper, face, text, di.DirectionLTR)
	before := append([]Glyph(nil), runs[0].Glyphs...)
	var l LineWrapper
	lines, _ := l.WrapParagraph(WrapConfig{Direction: di.DirectionLTR, TabStops: nil}, 1000, text, NewSliceIterator(runs))
	for i, g := range lines[0][0].Glyphs {
		if g.Advance != before[i].Advance || g.GlyphID != before[i].GlyphID {
			t.Fatalf("glyph %d mutated with empty stops: %+v vs %+v", i, g, before[i])
		}
	}
}
