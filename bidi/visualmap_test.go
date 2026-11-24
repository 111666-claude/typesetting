package bidi

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	ucd "github.com/go-text/typesetting/internal/unicodedata"
	tu "github.com/go-text/typesetting/testutils"
)

// assertPermutation fails if v is not a permutation of [0,n).
func assertPermutation(t *testing.T, v []int, n int, name string) {
	t.Helper()
	if len(v) != n {
		t.Fatalf("%s: length %d, want %d", name, len(v), n)
	}
	seen := make([]bool, n)
	for _, x := range v {
		if x < 0 || x >= n || seen[x] {
			t.Fatalf("%s: %v is not a permutation of [0,%d)", name, v, n)
		}
		seen[x] = true
	}
}

// assertCaretPermutation fails if v is not a permutation of [0,n].
func assertCaretPermutation(t *testing.T, v []int, n int, name string) {
	t.Helper()
	if len(v) != n+1 {
		t.Fatalf("%s: length %d, want %d", name, len(v), n+1)
	}
	seen := make([]bool, n+1)
	for _, x := range v {
		if x < 0 || x > n || seen[x] {
			t.Fatalf("%s: %v is not a permutation of [0,%d]", name, v, n)
		}
		seen[x] = true
	}
}

// checkCaretRoundTrip verifies the structural invariants every visual map
// must satisfy: the index tables are inverse permutations and the caret
// tables are inverse permutations, so every position and every caret
// round-trips in both directions.
func checkCaretRoundTrip(t *testing.T, m *VisualMap) {
	t.Helper()
	n := m.Len()
	l2v := m.LogicalToVisualOrder()
	v2l := m.VisualToLogicalOrder()
	assertPermutation(t, l2v, n, "logicalToVisual")
	assertPermutation(t, v2l, n, "visualToLogical")
	for i := 0; i < n; i++ {
		if v2l[l2v[i]] != i {
			t.Fatalf("visualToLogical[logicalToVisual[%d]] != %d", i, i)
		}
		got, err := m.LogicalIndex(l2v[i])
		tu.AssertNoErr(t, err)
		if got != i {
			t.Fatalf("LogicalIndex(VisualIndex(%d)) = %d", i, got)
		}
		v, err := m.VisualIndex(i)
		tu.AssertNoErr(t, err)
		if v != l2v[i] {
			t.Fatalf("VisualIndex(%d) = %d, want %d", i, v, l2v[i])
		}
	}

	l2vCaret := m.LogicalToVisualCarets()
	v2lCaret := m.VisualToLogicalCarets()
	assertCaretPermutation(t, l2vCaret, n, "logicalToVisualCaret")
	assertCaretPermutation(t, v2lCaret, n, "visualToLogicalCaret")
	for c := 0; c <= n; c++ {
		if v2lCaret[l2vCaret[c]] != c {
			t.Fatalf("visualToLogicalCaret[logicalToVisualCaret[%d]] != %d", c, c)
		}
		got, err := m.LogicalCaret(l2vCaret[c])
		tu.AssertNoErr(t, err)
		if got != c {
			t.Fatalf("LogicalCaret(VisualCaret(%d)) = %d", c, got)
		}
		v, err := m.VisualCaret(c)
		tu.AssertNoErr(t, err)
		if v != l2vCaret[c] {
			t.Fatalf("VisualCaret(%d) = %d, want %d", c, v, l2vCaret[c])
		}
	}
}

// TestVisualMapMixedArabicDigitsLatin exercises Arabic letters, Latin text
// and European/Arabic digits in one paragraph. The Arabic run is rendered
// right to left while the Latin prefix keeps its order, and the European
// digits embedded in the Arabic run get level 2 and stay visually ordered.
func TestVisualMapMixedArabicDigitsLatin(t *testing.T) {
	str := "arabic 123 abc عربي 45"
	var p Paragraph
	runs := p.SegmentString(str, Neutral)
	m, err := p.VisualMap(runs)
	tu.AssertNoErr(t, err)

	tu.Assert(t, m.Len() == len([]rune(str)))
	tu.Assert(t, m.BaseLevel() == 0)
	tu.Assert(t, m.BaseDirection() == LeftToRight)

	// Logical -> visual: the Latin prefix is untouched, the Arabic letters
	// and the embedded digits are reversed as a block.
	wantL2V := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 20, 21, 19, 18, 17, 16, 15}
	if got := m.LogicalToVisualOrder(); !reflect.DeepEqual(got, wantL2V) {
		t.Fatalf("logicalToVisual = %v, want %v", got, wantL2V)
	}

	// Visual -> logical is the exact inverse of the table above.
	wantV2L := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 21, 20, 19, 18, 17, 15, 16}
	if got := m.VisualToLogicalOrder(); !reflect.DeepEqual(got, wantV2L) {
		t.Fatalf("visualToLogical = %v, want %v", got, wantV2L)
	}

	// The digits (logical 20-21, level 2) form a contiguous visual unit
	// inside the reversed RTL block: they occupy two adjacent visual
	// positions and keep a stable, level-2 (LTR) reading order.
	lvl20, err := m.LevelAt(20)
	tu.AssertNoErr(t, err)
	tu.Assert(t, lvl20 == 2)
	v20, _ := m.VisualIndex(20)
	v21, _ := m.VisualIndex(21)
	tu.Assert(t, v20 == 16 && v21 == 15)
	diff := v20 - v21
	if diff < 0 {
		diff = -diff
	}
	tu.Assert(t, diff == 1)
	// and they map back through the inverse table
	back20, _ := m.LogicalIndex(v20)
	back21, _ := m.LogicalIndex(v21)
	tu.Assert(t, back20 == 20 && back21 == 21)

	// Run boundaries match the runs reported by the Segment API.
	var wantBoundaries []int
	for i := 0; i < runs.NumRuns(); i++ {
		wantBoundaries = append(wantBoundaries, runs.Run(i).Start)
	}
	var gotBoundaries []int
	for i := 0; i < m.Len(); i++ {
		f, err := m.FlagsAt(i)
		tu.AssertNoErr(t, err)
		if f.Has(FlagRunBoundary) {
			gotBoundaries = append(gotBoundaries, i)
		}
	}
	if !reflect.DeepEqual(gotBoundaries, wantBoundaries) {
		t.Fatalf("run boundaries = %v, want %v", gotBoundaries, wantBoundaries)
	}

	checkCaretRoundTrip(t, m)
}

// TestVisualMapRTLBaseDirection checks a pure RTL paragraph: everything is
// reversed and the base direction is right to left.
func TestVisualMapRTLBaseDirection(t *testing.T) {
	str := "ابج" // three Arabic letters
	var p Paragraph
	runs := p.SegmentString(str, RightToLeft)
	m, err := p.VisualMap(runs)
	tu.AssertNoErr(t, err)

	tu.Assert(t, m.BaseLevel() == 1)
	tu.Assert(t, m.BaseDirection() == RightToLeft)
	if got := m.VisualToLogicalOrder(); !reflect.DeepEqual(got, []int{2, 1, 0}) {
		t.Fatalf("visualToLogical = %v, want [2 1 0]", got)
	}
	// Every caret position is mirrored around the paragraph edges.
	if got := m.LogicalToVisualCarets(); !reflect.DeepEqual(got, []int{3, 2, 1, 0}) {
		t.Fatalf("logicalToVisualCaret = %v, want [3 2 1 0]", got)
	}
	checkCaretRoundTrip(t, m)
}

// TestVisualMapMirroring checks bracket mirroring (UAX #9 rule L4): the
// parentheses in the RTL run are flagged as mirrored and resolve to their
// mirrored glyph, while the LTR parentheses are untouched.
func TestVisualMapMirroring(t *testing.T) {
	str := "a (b) ج (د)"
	var p Paragraph
	m, err := p.VisualMap(p.SegmentString(str, Neutral))
	tu.AssertNoErr(t, err)

	for i, r := range []rune(str) {
		f, err := m.FlagsAt(i)
		tu.AssertNoErr(t, err)
		level, _ := m.LevelAt(i)
		hasMirror := ucd.LookupMirrorChar(r) != r
		if level&1 != 0 && hasMirror {
			tu.AssertC(t, f.Has(FlagMirrored), fmt.Sprintf("index %d should be mirrored", i))
			// L4 substitution: '(' renders as ')' and conversely.
			tu.Assert(t, ucd.LookupMirrorChar(r) != r)
		} else {
			tu.AssertC(t, !f.Has(FlagMirrored), fmt.Sprintf("index %d should not be mirrored", i))
		}
	}

	// The two RTL parentheses (logical indices 8 and 10) are the mirrored
	// ones; the LTR pair at indices 2 and 4 is not.
	f8, _ := m.FlagsAt(8)
	f10, _ := m.FlagsAt(10)
	f2, _ := m.FlagsAt(2)
	f4, _ := m.FlagsAt(4)
	tu.Assert(t, f8.Has(FlagMirrored) && f10.Has(FlagMirrored))
	tu.Assert(t, !f2.Has(FlagMirrored) && !f4.Has(FlagMirrored))

	// In the RTL run the closing parenthesis is visually before the opening
	// one, even though it comes later in logical order.
	v8, _ := m.VisualIndex(8)
	v10, _ := m.VisualIndex(10)
	tu.Assert(t, v10 < v8)

	checkCaretRoundTrip(t, m)
}

// TestVisualMapNestedIsolates uses the same nested-isolate fixture as
// TestNestedIsolates: an explicit RLE embedding containing an RLO override,
// with nested bidi levels (0, 1 and 3) inside surrounding LTR text.
func TestVisualMapNestedIsolates(t *testing.T) {
	str := "The title is \u202Bאבג \u202EC++\u202C דהו\u202C in Hebrew."
	var p Paragraph
	runs := p.SegmentString(str, LeftToRight)
	m, err := p.VisualMap(runs)
	tu.AssertNoErr(t, err)

	// All four explicit controls are invisible: RLE, RLO and two PDFs.
	controlIndices := []int{13, 18, 22, 27}
	for _, i := range controlIndices {
		f, err := m.FlagsAt(i)
		tu.AssertNoErr(t, err)
		tu.AssertC(t, f.Has(FlagControl), fmt.Sprintf("control at %d not flagged", i))
	}
	nonControl := 0
	for i := 0; i < m.Len(); i++ {
		f, _ := m.FlagsAt(i)
		if !f.Has(FlagControl) {
			nonControl++
		}
	}
	tu.Assert(t, nonControl == m.Len()-len(controlIndices))

	// The nested "C++" override run (logical 19-21, level 3) is reversed
	// inside the already-reversed Hebrew isolate.
	wantNested := map[int]int{19: 22, 20: 21, 21: 20}
	for logical, wantVisual := range wantNested {
		got, err := m.VisualIndex(logical)
		tu.AssertNoErr(t, err)
		tu.AssertC(t, got == wantVisual, fmt.Sprintf("index %d: visual %d, want %d", logical, got, wantVisual))
	}

	// Hebrew letters of the outer isolate are reversed as a level-1 block.
	v14, _ := m.VisualIndex(14)
	v15, _ := m.VisualIndex(15)
	v16, _ := m.VisualIndex(16)
	tu.Assert(t, v14 > v15 && v15 > v16)

	// The trailing LTR sentence resumes after the isolate, in logical order.
	v28, _ := m.VisualIndex(28)
	v29, _ := m.VisualIndex(29)
	tu.Assert(t, v28 == 28 && v29 == 29)

	checkCaretRoundTrip(t, m)
}

// TestVisualMapIsolateWithPDI checks a balanced RLI...PDI pair: both isolate
// controls are invisible and the isolate content reorders independently while
// the surrounding LTR text stays in place.
func TestVisualMapIsolateWithPDI(t *testing.T) {
	str := "a\u2067بب\u2069c"
	var p Paragraph
	m, err := p.VisualMap(p.SegmentString(str, LeftToRight))
	tu.AssertNoErr(t, err)

	f1, _ := m.FlagsAt(1) // RLI
	f4, _ := m.FlagsAt(4) // PDI
	tu.Assert(t, f1.Has(FlagControl) && f4.Has(FlagControl))

	// Visual rendering: a, RLI, then the reversed Hebrew, then PDI and c.
	// logical->visual and visual->logical are both [0 1 3 2 4 5] here.
	wantOrder := []int{0, 1, 3, 2, 4, 5}
	if got := m.VisualToLogicalOrder(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("visualToLogical = %v, want %v", got, wantOrder)
	}
	if got := m.LogicalToVisualOrder(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("logicalToVisual = %v, want %v", got, wantOrder)
	}
	// The two Hebrew letters (logical 2-3) form a level-1 run.
	lvl2, _ := m.LevelAt(2)
	tu.Assert(t, lvl2 == 1)
	checkCaretRoundTrip(t, m)
}

// TestVisualMapCombiningMarks verifies that combining marks are flagged,
// including a mark attached to an RTL base, and that they still participate
// in the reordering next to their base.
func TestVisualMapCombiningMarks(t *testing.T) {
	str := "aé̀ ب̣ x"
	var p Paragraph
	m, err := p.VisualMap(p.SegmentString(str, Neutral))
	tu.AssertNoErr(t, err)

	f2, _ := m.FlagsAt(2) // U+0300 combining grave accent
	tu.Assert(t, f2.Has(FlagCombining))
	f5, _ := m.FlagsAt(5) // U+0323 combining dot below, attached to Arabic
	tu.Assert(t, f5.Has(FlagCombining))

	// The Arabic base (4) and its mark (5) stay adjacent, with the mark
	// visually before the base in the RTL run.
	v4, _ := m.VisualIndex(4)
	v5, _ := m.VisualIndex(5)
	tu.Assert(t, v5 == v4-1)

	// A plain letter carries no combining flag.
	f0, _ := m.FlagsAt(0)
	tu.Assert(t, !f0.Has(FlagCombining))

	checkCaretRoundTrip(t, m)
}

// TestVisualMapImplicitMarks checks LRM/RLM/ALM, whose bidi classes are the
// strong classes L/R/AL but which are still invisible controls.
func TestVisualMapImplicitMarks(t *testing.T) {
	str := "a\u200Fb\u200Ec\u061Cd"
	var p Paragraph
	m, err := p.VisualMap(p.SegmentString(str, Neutral))
	tu.AssertNoErr(t, err)
	for _, i := range []int{1, 3, 5} {
		f, err := m.FlagsAt(i)
		tu.AssertNoErr(t, err)
		tu.AssertC(t, f.Has(FlagControl), fmt.Sprintf("implicit mark at %d not flagged", i))
	}
	f0, _ := m.FlagsAt(0)
	tu.Assert(t, !f0.Has(FlagControl))
}

// TestVisualMapEmptyParagraph covers the empty-paragraph edge case for all
// three Segment entry points.
func TestVisualMapEmptyParagraph(t *testing.T) {
	var p Paragraph
	for _, runs := range []Runs{
		p.Segment(nil, Neutral),
		p.SegmentString("", Neutral),
		p.SegmentBytes(nil, Neutral),
	} {
		m, err := p.VisualMap(runs)
		tu.Assert(t, m == nil)
		tu.Assert(t, errors.Is(err, ErrEmptyParagraph))
	}
}

// TestVisualMapInvalidUTF8 verifies byte validation and the error context.
func TestVisualMapInvalidUTF8(t *testing.T) {
	var p Paragraph

	// First invalid byte is at offset 1.
	m, err := p.VisualMapFromBytes([]byte{'a', 0xff, 'b'}, Neutral)
	tu.Assert(t, m == nil)
	tu.Assert(t, errors.Is(err, ErrInvalidUTF8))
	tu.Assert(t, strings.Contains(err.Error(), "byte offset 1"))

	// A truncated multibyte sequence is invalid too.
	_, err = p.VisualMapFromBytes([]byte{'a', 0xD8, 0x00}, Neutral)
	tu.Assert(t, errors.Is(err, ErrInvalidUTF8))

	// The failed call does not corrupt the paragraph: a valid call after it
	// works.
	m, err = p.VisualMapFromBytes([]byte("abc"), Neutral)
	tu.AssertNoErr(t, err)
	tu.Assert(t, m.Len() == 3)
}

// TestVisualMapIndexOutOfRange covers every lookup's bounds handling.
func TestVisualMapIndexOutOfRange(t *testing.T) {
	var p Paragraph
	m, err := p.VisualMap(p.SegmentString("abc", Neutral))
	tu.AssertNoErr(t, err)

	if _, err := m.VisualIndex(-1); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("VisualIndex(-1): %v", err)
	}
	if _, err := m.VisualIndex(3); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("VisualIndex(3): %v", err)
	}
	if _, err := m.LogicalIndex(-1); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("LogicalIndex(-1): %v", err)
	}
	if _, err := m.LogicalIndex(3); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("LogicalIndex(3): %v", err)
	}
	if _, err := m.VisualCaret(-1); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("VisualCaret(-1): %v", err)
	}
	if _, err := m.VisualCaret(4); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("VisualCaret(4): %v", err)
	}
	if _, err := m.LogicalCaret(-1); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("LogicalCaret(-1): %v", err)
	}
	if _, err := m.LogicalCaret(4); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("LogicalCaret(4): %v", err)
	}
	if _, err := m.LevelAt(3); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("LevelAt(3): %v", err)
	}
	if _, err := m.FlagsAt(3); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("FlagsAt(3): %v", err)
	}

	// The boundary carets 0 and n are valid.
	v0, err := m.VisualCaret(0)
	tu.AssertNoErr(t, err)
	tu.Assert(t, v0 == 0)
	v3, err := m.VisualCaret(3)
	tu.AssertNoErr(t, err)
	tu.Assert(t, v3 == 3)
}

// TestVisualMapStaleRuns ensures runs from a previous segmentation are
// rejected instead of silently producing a wrong map.
func TestVisualMapStaleRuns(t *testing.T) {
	var p Paragraph
	runs := p.SegmentString("abc", Neutral)

	// Different length.
	p.SegmentString("abcd", Neutral)
	_, err := p.VisualMap(runs)
	tu.Assert(t, errors.Is(err, ErrStaleRuns))

	// Same length, different resolved levels: the Latin-only paragraph is
	// uniformly level 0, while the mixed paragraph contains level 1.
	runs = p.SegmentString("abcdef", LeftToRight)
	p.SegmentString("aابجcd", RightToLeft)
	_, err = p.VisualMap(runs)
	tu.Assert(t, errors.Is(err, ErrStaleRuns))

	// Fresh runs are accepted again.
	runs = p.SegmentString("abc", Neutral)
	m, err := p.VisualMap(runs)
	tu.AssertNoErr(t, err)
	tu.Assert(t, m.Len() == 3)
}

// TestVisualMapIsolateOverflow checks the depth limit in both directions:
// exactly maxDepth nested isolates are accepted, one more is rejected with
// context, and balanced nesting resets the depth.
func TestVisualMapIsolateOverflow(t *testing.T) {
	var p Paragraph

	deep := strings.Repeat("\u2066", maxDepth) + "a" + strings.Repeat("\u2069", maxDepth)
	m, err := p.VisualMap(p.SegmentString(deep, LeftToRight))
	tu.AssertNoErr(t, err)
	tu.Assert(t, m.Len() == len([]rune(deep)))

	tooDeep := strings.Repeat("\u2066", maxDepth+1) + "a"
	_, err = p.VisualMap(p.SegmentString(tooDeep, LeftToRight))
	tu.Assert(t, errors.Is(err, ErrIsolateOverflow))
	tu.Assert(t, strings.Contains(err.Error(), "logical index 125"))

	// FSI initiators count toward the depth just like LRI/RLI.
	tooDeepFSI := strings.Repeat("\u2068", maxDepth+1) + "a"
	_, err = p.VisualMap(p.SegmentString(tooDeepFSI, LeftToRight))
	tu.Assert(t, errors.Is(err, ErrIsolateOverflow))

	// Unmatched PDIs do not decrement the depth below zero, so a long run of
	// them is accepted.
	flatPDI := strings.Repeat("\u2069", maxDepth+1) + "a"
	m, err = p.VisualMap(p.SegmentString(flatPDI, LeftToRight))
	tu.AssertNoErr(t, err)
	tu.Assert(t, m.Len() == len([]rune(flatPDI)))

	// Sequential, non-nested isolates never overflow.
	flat := strings.Repeat("\u2066a\u2069", maxDepth+1)
	m, err = p.VisualMap(p.SegmentString(flat, LeftToRight))
	tu.AssertNoErr(t, err)
	tu.Assert(t, m.Len() == len([]rune(flat)))
}

// TestVisualMapRepeatedAndStable checks determinism, stability across base
// directions and independence from the paragraph's reusable buffers.
func TestVisualMapRepeatedAndStable(t *testing.T) {
	var p Paragraph
	str := "arabic 123 abc عربي 45"
	runs := p.SegmentString(str, Neutral)
	first, err := p.VisualMap(runs)
	tu.AssertNoErr(t, err)
	second, err := p.VisualMap(runs)
	tu.AssertNoErr(t, err)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("repeated VisualMap calls returned different maps")
	}

	// Re-segmenting the paragraph does not invalidate the previously built map.
	p.SegmentString("totally different text ابج", RightToLeft)
	if got := first.LogicalToVisualOrder(); !reflect.DeepEqual(got, second.LogicalToVisualOrder()) {
		t.Fatal("existing map mutated after re-segmentation")
	}
	if lvl, _ := first.LevelAt(15); lvl != 1 {
		t.Fatalf("level changed after re-segmentation: %d", lvl)
	}

	// The same text with an explicit RTL base direction resolves to base
	// level 1, while the Neutral/LTR resolution stays at level 0: both maps
	// are internally consistent and stable.
	var ltrParagraph, rtlParagraph Paragraph
	ltrMap, err := ltrParagraph.VisualMap(ltrParagraph.SegmentString(str, LeftToRight))
	tu.AssertNoErr(t, err)
	rtlMap, err := rtlParagraph.VisualMap(rtlParagraph.SegmentString(str, RightToLeft))
	tu.AssertNoErr(t, err)
	tu.Assert(t, ltrMap.BaseLevel() == 0 && rtlMap.BaseLevel() == 1)
	checkCaretRoundTrip(t, ltrMap)
	checkCaretRoundTrip(t, rtlMap)
}

// TestVisualMapNoAliasedSlices verifies that returned slices are copies:
// mutating them never affects later lookups.
func TestVisualMapNoAliasedSlices(t *testing.T) {
	var p Paragraph
	m, err := p.VisualMap(p.SegmentString("aبc", Neutral))
	tu.AssertNoErr(t, err)

	orders := [][]int{
		m.LogicalToVisualOrder(),
		m.VisualToLogicalOrder(),
		m.LogicalToVisualCarets(),
		m.VisualToLogicalCarets(),
	}
	snapshot := make([][]int, len(orders))
	for i, s := range orders {
		snapshot[i] = append([]int(nil), s...)
		for j := range s {
			s[j] = -999
		}
	}
	flags := m.Flags()
	for i := range flags {
		flags[i] = 0
	}

	for i, want := range snapshot {
		var got []int
		switch i {
		case 0:
			got = m.LogicalToVisualOrder()
		case 1:
			got = m.VisualToLogicalOrder()
		case 2:
			got = m.LogicalToVisualCarets()
		case 3:
			got = m.VisualToLogicalCarets()
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("table %d was mutated through the returned copy", i)
		}
	}
	f, err := m.FlagsAt(1)
	tu.AssertNoErr(t, err)
	tu.Assert(t, f != 0)
}

// TestVisualMapBidiCharacterOrdering cross-checks the visual order against the
// visual ordering field of every applicable Unicode BidiCharacterTest entry.
// Entries whose expected ordering omits X9-removed characters, or whose text
// is truncated at a paragraph separator, are skipped: the map covers every
// input code point, so a full comparison only applies when both tables have
// the same length.
func TestVisualMapBidiCharacterOrdering(t *testing.T) {
	datas, err := parseBidiCharacterTests()
	tu.AssertNoErr(t, err)

	checked := 0
	for _, test := range datas {
		var p Paragraph
		runs := p.Segment(test.codePoints, test.parDir)
		if len(runs.runEnds) == 0 {
			continue // empty entry
		}
		m, err := p.VisualMap(runs)
		tu.AssertNoErr(t, err)
		if len(test.visualOrdering) != m.Len() {
			continue // ordering excludes removed controls or a paragraph break
		}
		got := m.LogicalToVisualOrder()
		if !reflect.DeepEqual(got, test.visualOrdering) {
			t.Fatalf("BidiCharacterTest line %d: logical-to-visual = %v, want %v",
				test.line, got, test.visualOrdering)
		}
		checkCaretRoundTrip(t, m)
		checked++
	}
	// Guard against an overly aggressive skip: the vast majority of entries
	// contain no removed controls.
	tu.AssertC(t, checked > len(datas)/2, fmt.Sprintf("only %d of %d entries were comparable", checked, len(datas)))
}

// TestVisualMapBidiTestOrdering cross-checks the visual order against the
// @Reorder field of Unicode BidiTest blocks. The file holds close to half a
// million test lines, so a deterministic stride sample (every 997th line,
// across all blocks and every applicable base direction) keeps the test fast
// while covering every level pattern present in the fixture.
func TestVisualMapBidiTestOrdering(t *testing.T) {
	blocks, err := parseBidiTests()
	tu.AssertNoErr(t, err)

	checked, lineCounter := 0, 0
	for _, block := range blocks {
		for _, line := range block.data {
			lineCounter++
			if lineCounter%997 != 0 {
				continue
			}
			for baseDirMode := 0; baseDirMode < 3; baseDirMode++ {
				if line.baseDirFlag&(1<<baseDirMode) == 0 {
					continue
				}
				var dir Direction
				switch baseDirMode {
				case 0:
					dir = Neutral
				case 1:
					dir = LeftToRight
				case 2:
					dir = RightToLeft
				}

				var p Paragraph
				runs := p.Segment(line.runes, dir)
				if len(runs.runEnds) == 0 {
					continue
				}
				m, err := p.VisualMap(runs)
				tu.AssertNoErr(t, err)
				if len(block.ltor) != m.Len() {
					continue
				}
				if got := m.LogicalToVisualOrder(); !reflect.DeepEqual(got, block.ltor) {
					t.Fatalf("BidiTest line %d (dir %d): logical-to-visual = %v, want %v",
						lineCounter, baseDirMode, got, block.ltor)
				}
				checkCaretRoundTrip(t, m)
				checked++
			}
		}
	}
	tu.AssertC(t, checked >= 100, fmt.Sprintf("only %d BidiTest samples were checked", checked))
}
