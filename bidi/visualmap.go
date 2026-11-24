package bidi

import (
	"errors"
	"fmt"
	"unicode/utf8"

	ucd "github.com/go-text/typesetting/internal/unicodedata"
)

// VisualFlag annotates a logical character index in a [VisualMap].
type VisualFlag uint8

const (
	// FlagControl marks an invisible directional control character:
	// explicit embedding/override/isolate codes (LRE, RLE, LRO, RLO, PDF,
	// LRI, RLI, FSI, PDI), boundary neutrals (BN), segment (S) and paragraph
	// (B) separators. Such characters occupy no rendered glyph position.
	FlagControl VisualFlag = 1 << iota

	// FlagCombining marks a combining mark (general category Mn, Mc or Me),
	// which attaches to its preceding base character rather than occupying
	// an independent caret position.
	FlagCombining

	// FlagRunBoundary marks the first logical index of every bidi level run,
	// including index 0. The marked indices are exactly the run starts
	// reported by [Runs.Run].
	FlagRunBoundary

	// FlagMirrored marks a character that has a Bidi_Mirrored glyph
	// equivalent and is resolved to an odd (RTL) level, where the rendering
	// engine is expected to substitute the mirrored glyph (UAX #9, rule L4).
	FlagMirrored
)

// Has reports whether all the flags in f are set.
func (f VisualFlag) Has(flag VisualFlag) bool { return f&flag == flag }

// String describes the set flags, mainly for debugging and tests.
func (f VisualFlag) String() string {
	var out []byte
	if f.Has(FlagControl) {
		out = append(out, "control,"...)
	}
	if f.Has(FlagCombining) {
		out = append(out, "combining,"...)
	}
	if f.Has(FlagRunBoundary) {
		out = append(out, "run-boundary,"...)
	}
	if f.Has(FlagMirrored) {
		out = append(out, "mirrored,"...)
	}
	if len(out) == 0 {
		return "none"
	}
	return string(out[:len(out)-1])
}

// Errors returned by [VisualMap] construction and lookups. Errors are wrapped
// with contextual information (index, byte offset, depth), so callers should
// use [errors.Is] rather than comparing values directly.
var (
	// ErrEmptyParagraph is returned when the paragraph carries no text.
	ErrEmptyParagraph = errors.New("bidi: cannot build a visual map for an empty paragraph")

	// ErrInvalidUTF8 is returned by [Paragraph.VisualMapFromBytes] when the
	// input bytes are not valid UTF-8. The error message gives the byte
	// offset of the first invalid sequence.
	ErrInvalidUTF8 = errors.New("bidi: invalid UTF-8 input")

	// ErrIndexOutOfRange is returned when a logical or visual index, or a
	// caret offset, falls outside the map.
	ErrIndexOutOfRange = errors.New("bidi: index out of range")

	// ErrStaleRuns is returned when the [Runs] passed to the constructor do
	// not match the paragraph's current segmentation state, for instance
	// because [Paragraph.Segment] was called again in between.
	ErrStaleRuns = errors.New("bidi: runs do not match the current paragraph state")

	// ErrIsolateOverflow is returned when the explicit isolate nesting is
	// deeper than the maximum bidi depth (125). The error message gives the
	// logical index at which the overflow was detected.
	ErrIsolateOverflow = errors.New("bidi: explicit isolate nesting exceeds the maximum depth")
)

// VisualMap stores the bidirectional mapping between logical character
// indices (positions in the paragraph text) and visual indices (rendered
// positions, from left to right), together with caret offsets and per-index
// annotations.
//
// A VisualMap owns all of its backing arrays: the slice-returning accessors
// hand out copies, so retaining or mutating them never corrupts the map and
// a map stays valid after the originating [Paragraph] is reused. Repeated
// constructions over the same input produce byte-identical maps: the result
// only depends on the resolved base direction and the Unicode bidi levels.
type VisualMap struct {
	// logicalToVisual[i] is the visual position of logical index i.
	logicalToVisual []int
	// visualToLogical[v] is the logical index rendered at visual position v.
	visualToLogical []int

	// logicalToVisualCaret[i] is the visual caret offset of logical caret i.
	logicalToVisualCaret []int
	// visualToLogicalCaret[v] is the logical caret offset at visual caret v.
	visualToLogicalCaret []int

	flags  []VisualFlag
	levels []Level

	baseLevel     Level
	baseDirection Direction
}

// Len returns the number of mapped code points.
func (m *VisualMap) Len() int { return len(m.logicalToVisual) }

// BaseLevel returns the resolved paragraph embedding level (0 for LTR,
// 1 for RTL).
func (m *VisualMap) BaseLevel() Level { return m.baseLevel }

// BaseDirection returns the resolved paragraph base direction, derived from
// the base level.
func (m *VisualMap) BaseDirection() Direction { return m.baseDirection }

// VisualIndex returns the visual position of the logical index i.
// It returns [ErrIndexOutOfRange] if i is not in [0, Len()[.
func (m *VisualMap) VisualIndex(i int) (int, error) {
	if i < 0 || i >= m.Len() {
		return -1, fmt.Errorf("%w: logical index %d, length %d", ErrIndexOutOfRange, i, m.Len())
	}
	return m.logicalToVisual[i], nil
}

// LogicalIndex returns the logical index rendered at visual position v.
// It returns [ErrIndexOutOfRange] if v is not in [0, Len()[.
func (m *VisualMap) LogicalIndex(v int) (int, error) {
	if v < 0 || v >= m.Len() {
		return -1, fmt.Errorf("%w: visual index %d, length %d", ErrIndexOutOfRange, v, m.Len())
	}
	return m.visualToLogical[v], nil
}

// VisualCaret returns the visual caret offset of the logical caret offset
// logicalCaret, in [0, Len()].
// It returns [ErrIndexOutOfRange] if the caret is outside that range.
func (m *VisualMap) VisualCaret(logicalCaret int) (int, error) {
	if logicalCaret < 0 || logicalCaret > m.Len() {
		return -1, fmt.Errorf("%w: logical caret %d, length %d", ErrIndexOutOfRange, logicalCaret, m.Len())
	}
	return m.logicalToVisualCaret[logicalCaret], nil
}

// LogicalCaret returns the logical caret offset at the visual caret offset
// visualCaret, in [0, Len()].
// It returns [ErrIndexOutOfRange] if the caret is outside that range.
func (m *VisualMap) LogicalCaret(visualCaret int) (int, error) {
	if visualCaret < 0 || visualCaret > m.Len() {
		return -1, fmt.Errorf("%w: visual caret %d, length %d", ErrIndexOutOfRange, visualCaret, m.Len())
	}
	return m.visualToLogicalCaret[visualCaret], nil
}

// LevelAt returns the resolved bidi level of the logical index i.
// It returns [ErrIndexOutOfRange] if i is not in [0, Len()[.
func (m *VisualMap) LevelAt(i int) (Level, error) {
	if i < 0 || i >= m.Len() {
		return 0, fmt.Errorf("%w: level index %d, length %d", ErrIndexOutOfRange, i, m.Len())
	}
	return m.levels[i], nil
}

// FlagsAt returns the annotation flags of the logical index i.
// It returns [ErrIndexOutOfRange] if i is not in [0, Len()[.
func (m *VisualMap) FlagsAt(i int) (VisualFlag, error) {
	if i < 0 || i >= m.Len() {
		return 0, fmt.Errorf("%w: flag index %d, length %d", ErrIndexOutOfRange, i, m.Len())
	}
	return m.flags[i], nil
}

// LogicalToVisualOrder returns a copy of the logical-to-visual index table.
func (m *VisualMap) LogicalToVisualOrder() []int {
	out := make([]int, len(m.logicalToVisual))
	copy(out, m.logicalToVisual)
	return out
}

// VisualToLogicalOrder returns a copy of the visual-to-logical index table.
func (m *VisualMap) VisualToLogicalOrder() []int {
	out := make([]int, len(m.visualToLogical))
	copy(out, m.visualToLogical)
	return out
}

// LogicalToVisualCarets returns a copy of the logical-to-visual caret table.
func (m *VisualMap) LogicalToVisualCarets() []int {
	out := make([]int, len(m.logicalToVisualCaret))
	copy(out, m.logicalToVisualCaret)
	return out
}

// VisualToLogicalCarets returns a copy of the visual-to-logical caret table.
func (m *VisualMap) VisualToLogicalCarets() []int {
	out := make([]int, len(m.visualToLogicalCaret))
	copy(out, m.visualToLogicalCaret)
	return out
}

// Flags returns a copy of the per-index annotation table.
func (m *VisualMap) Flags() []VisualFlag {
	out := make([]VisualFlag, len(m.flags))
	copy(out, m.flags)
	return out
}

// VisualMap builds the visual map of the paragraph most recently processed by
// [Paragraph.Segment], [Paragraph.SegmentString] or [Paragraph.SegmentBytes].
//
// The runs argument must be the [Runs] returned by that segmentation: passing
// runs from an earlier segmentation returns [ErrStaleRuns]. Building a map
// for an empty paragraph returns [ErrEmptyParagraph], and isolate nesting
// deeper than 125 returns [ErrIsolateOverflow].
//
// The returned map is independent of the paragraph: it copies every slice it
// needs, and later segmentations do not invalidate it.
func (p *Paragraph) VisualMap(runs Runs) (*VisualMap, error) {
	n := len(p.text)
	if n == 0 {
		return nil, ErrEmptyParagraph
	}
	if err := validateRuns(runs, p.resultLevels, n); err != nil {
		return nil, err
	}
	if err := checkIsolateDepth(p.initialTypes); err != nil {
		return nil, err
	}

	levels := make([]Level, n)
	copy(levels, p.resultLevels)

	logicalToVisual := reorderLogicalToVisual(levels)
	visualToLogical := make([]int, n)
	for logical, visual := range logicalToVisual {
		visualToLogical[visual] = logical
	}

	l2vCaret, v2lCaret := buildCaretMaps(levels, visualToLogical)

	baseLevel := p.embeddingLevel
	baseDirection := LeftToRight
	if baseLevel&1 != 0 {
		baseDirection = RightToLeft
	}

	return &VisualMap{
		logicalToVisual:      logicalToVisual,
		visualToLogical:      visualToLogical,
		logicalToVisualCaret: l2vCaret,
		visualToLogicalCaret: v2lCaret,
		flags:                buildFlags(p.text, p.initialTypes, levels, runs.runEnds),
		levels:               levels,
		baseLevel:            baseLevel,
		baseDirection:        baseDirection,
	}, nil
}

// VisualMapFromBytes validates text as UTF-8, segments it and builds the
// visual map. On invalid UTF-8 it returns [ErrInvalidUTF8] wrapped with the
// byte offset of the first invalid sequence, without modifying the paragraph.
func (p *Paragraph) VisualMapFromBytes(text []byte, defaultDirection Direction) (*VisualMap, error) {
	if offset := invalidUTF8Offset(text); offset >= 0 {
		return nil, fmt.Errorf("%w at byte offset %d", ErrInvalidUTF8, offset)
	}
	runs := p.SegmentBytes(text, defaultDirection)
	return p.VisualMap(runs)
}

// validateRuns checks that runs describes the current paragraph: every run
// end is strictly increasing and covers [0,n], and the carried levels are
// exactly the paragraph levels.
func validateRuns(runs Runs, paragraphLevels []Level, n int) error {
	if len(runs.levels) != n || len(runs.runEnds) == 0 || runs.runEnds[len(runs.runEnds)-1] != n {
		return fmt.Errorf("%w: run table does not cover %d characters", ErrStaleRuns, n)
	}
	prev := 0
	for _, end := range runs.runEnds {
		if end <= prev || end > n {
			return fmt.Errorf("%w: invalid run end %d", ErrStaleRuns, end)
		}
		prev = end
	}
	for i, level := range runs.levels {
		if level < 0 || level != paragraphLevels[i] {
			return fmt.Errorf("%w: level mismatch at index %d", ErrStaleRuns, i)
		}
	}
	return nil
}

// checkIsolateDepth reports ErrIsolateOverflow if isolate initiators
// (LRI, RLI, FSI) nest deeper than maxDepth.
func checkIsolateDepth(types []ucd.BidiClass) error {
	depth := 0
	for i, t := range types {
		if t&(ucd.BD_LRI|ucd.BD_RLI|ucd.BD_FSI) != 0 {
			depth++
			if depth > maxDepth {
				return fmt.Errorf("%w at logical index %d", ErrIsolateOverflow, i)
			}
		} else if t == ucd.BD_PDI && depth > 0 {
			depth--
		}
	}
	return nil
}

// invalidUTF8Offset returns the byte offset of the first invalid UTF-8
// sequence, or -1 if text is valid UTF-8.
func invalidUTF8Offset(text []byte) int {
	for offset := 0; len(text) > 0; {
		r, size := utf8.DecodeRune(text)
		if r == utf8.RuneError && size == 1 {
			return offset
		}
		text = text[size:]
		offset += size
	}
	return -1
}

// reorderLogicalToVisual applies rule L2 of UAX #9: from the highest resolved
// level down to the lowest odd level, every contiguous logical sequence at
// that level or higher is reversed. The returned table maps every logical
// index to its visual position.
func reorderLogicalToVisual(levels []Level) []int {
	n := len(levels)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}

	highest := Level(0)
	lowestOdd := Level(maxDepth + 1)
	for _, level := range levels {
		if level > highest {
			highest = level
		}
		if level&1 != 0 && level < lowestOdd {
			lowestOdd = level
		}
	}
	// no odd level: the paragraph is fully LTR
	if lowestOdd > maxDepth {
		return order
	}

	for level := highest; level >= lowestOdd; level-- {
		start := -1
		for i := 0; i <= n; i++ {
			if i < n && levels[i] >= level {
				if start == -1 {
					start = i
				}
			} else if start != -1 {
				reverseInts(order[start:i])
				start = -1
			}
		}
	}
	return order
}

func reverseInts(s []int) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// buildCaretMaps derives bidirectional caret tables.
//
// Logical carets sit between adjacent logical indices (with carets 0 and n at
// the paragraph edges); visual carets sit between adjacent visual slots.
//
// The authoritative table is visual-to-logical. Each visual gap gets the
// geometric logical caret from the glyphs on its two sides: the paragraph
// edges follow the first/last glyph's level, a gap between logical neighbors
// takes the later index, and a run boundary takes the edge of the LTR run.
// At embedded run boundaries two visual gaps can resolve to the same primary
// logical caret, leaving an equal number of gaps without one; those are
// completed with the unused logical carets in increasing order. Both returned
// tables are therefore permutations of [0,n] and exact inverses of each
// other: every caret round-trips in both directions.
func buildCaretMaps(levels []Level, visualToLogical []int) (l2vCaret, v2lCaret []int) {
	n := len(levels)

	// Assign each visual gap its geometric logical caret.
	v2lCaret = make([]int, n+1)
	used := make([]bool, n+1)
	var holes []int
	assign := func(visualCaret, logicalCaret int) {
		if used[logicalCaret] {
			holes = append(holes, visualCaret)
			return
		}
		v2lCaret[visualCaret] = logicalCaret
		used[logicalCaret] = true
	}

	for visualCaret := 0; visualCaret <= n; visualCaret++ {
		switch {
		case visualCaret == 0:
			logical := visualToLogical[0]
			if levels[logical]&1 == 0 {
				assign(visualCaret, logical)
			} else {
				assign(visualCaret, logical+1)
			}
		case visualCaret == n:
			logical := visualToLogical[n-1]
			if levels[logical]&1 == 0 {
				assign(visualCaret, logical+1)
			} else {
				assign(visualCaret, logical)
			}
		default:
			left := visualToLogical[visualCaret-1]
			right := visualToLogical[visualCaret]
			switch delta := left - right; {
			case delta == 1 || delta == -1:
				// logical neighbors: the caret follows the later index
				assign(visualCaret, max(left, right))
			case levels[left]&1 == 0:
				// LTR run on the visual left: caret closes that run
				assign(visualCaret, left+1)
			default:
				// RTL run on the visual left: caret opens the next run
				assign(visualCaret, right)
			}
		}
	}

	// Complete the permutation: unused logical carets fill the visual gaps
	// that no primary caret claims, in increasing order on both sides.
	var missing []int
	for logicalCaret := 0; logicalCaret <= n; logicalCaret++ {
		if !used[logicalCaret] {
			missing = append(missing, logicalCaret)
		}
	}
	for i, visualCaret := range holes {
		v2lCaret[visualCaret] = missing[i]
	}

	// Build the logical-to-visual table as the exact inverse so that every
	// caret round-trips in both directions.
	l2vCaret = make([]int, n+1)
	for visualCaret, logicalCaret := range v2lCaret {
		l2vCaret[logicalCaret] = visualCaret
	}
	return l2vCaret, v2lCaret
}

// buildFlags annotates every logical index.
func buildFlags(text []rune, types []ucd.BidiClass, levels []Level, runEnds []int) []VisualFlag {
	n := len(text)
	flags := make([]VisualFlag, n)

	for i, class := range types {
		if isInvisibleControl(class) {
			flags[i] |= FlagControl
		}
		if _, ok := invisibleControlRunes[text[i]]; ok {
			flags[i] |= FlagControl
		}
		if ucd.LookupGeneralCategory(text[i]).IsMark() {
			flags[i] |= FlagCombining
		}
		if levels[i]&1 != 0 && ucd.LookupMirrorChar(text[i]) != text[i] {
			flags[i] |= FlagMirrored
		}
	}

	// runEnds holds exclusive run ends, so run starts are 0 and each end.
	flags[0] |= FlagRunBoundary
	for _, end := range runEnds[:len(runEnds)-1] {
		flags[end] |= FlagRunBoundary
	}
	return flags
}

// isInvisibleControl reports whether the rune is a directional control or a
// separator that renders no glyph: explicit embedding/override/isolate codes,
// boundary neutrals, the implicit marks (LRM, RLM, ALM) and paragraph/segment
// separators.
func isInvisibleControl(class ucd.BidiClass) bool {
	return class&(ucd.BD_LRE|ucd.BD_RLE|ucd.BD_LRO|ucd.BD_RLO|ucd.BD_PDF|
		ucd.BD_LRI|ucd.BD_RLI|ucd.BD_FSI|ucd.BD_PDI|
		ucd.BD_BN|ucd.BD_B|ucd.BD_S) != 0
}

// invisibleControlRunes are the implicit bidi marks: they carry strong
// directional classes (L, R, AL) and therefore cannot be recognized through
// their bidi class alone.
var invisibleControlRunes = map[rune]struct{}{
	'\u200E': {}, // U+200E LEFT-TO-RIGHT MARK
	'\u200F': {}, // U+200F RIGHT-TO-LEFT MARK
	'\u061C': {}, // U+061C ARABIC LETTER MARK
}
