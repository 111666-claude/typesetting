// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"errors"
	"sort"

	"github.com/go-text/typesetting/di"
	ft "github.com/go-text/typesetting/font"
	ucd "github.com/go-text/typesetting/internal/unicodedata"
	"github.com/go-text/typesetting/language"
	"golang.org/x/image/math/fixed"
)

// This file implements horizontal ruby (also called furigana or interlinear
// annotation) layout: small annotation runs centered above ranges of base
// text. A base range and its annotation form a ruby group that moves as an
// atomic unit through line wrapping.

// Errors returned by [RubyShaper.Layout].
var (
	// ErrRubyDirection is returned when the base text contains strong
	// right-to-left runes. Vertical layout and bidirectional base text are not
	// supported; callers encountering RTL runes should resolve bidi and lay the
	// RTL runs out before using the ruby layout.
	ErrRubyDirection = errors.New("shaping: ruby layout requires horizontal, left-to-right text")

	// ErrRubyRange is returned when an annotation range is empty, inverted, or
	// reaches past the end of the base text.
	ErrRubyRange = errors.New("shaping: ruby annotation range is out of bounds")

	// ErrRubyOverlap is returned when two annotation ranges overlap. Ranges may
	// touch (one ending exactly where the next starts) but may not share a rune.
	ErrRubyOverlap = errors.New("shaping: ruby annotation ranges overlap")

	// ErrRubyCluster is returned when an annotation boundary falls inside a
	// shaping cluster (for instance inside a ligature or between a base rune and
	// its combining mark). A ruby group may only cover whole clusters.
	ErrRubyCluster = errors.New("shaping: ruby annotation boundary splits a shaping cluster")
)

// RubyOverwidthPolicy decides what happens when an annotation is wider than
// the base runes it annotates.
type RubyOverwidthPolicy uint8

const (
	// RubyExtendGroup widens the ruby group so that the annotation fits: the
	// extra width is split in two halves and added on both sides of the base
	// runes. This is the zero value and follows the CSS Ruby default behavior.
	RubyExtendGroup RubyOverwidthPolicy = iota

	// RubyShrinkAnnotation keeps the group exactly as wide as the base runes and
	// scales the annotation down uniformly until it fits. The annotation is
	// never made larger than its natural size by this policy.
	RubyShrinkAnnotation
)

// RubyAnnotation associates a piece of annotation text with a range of base
// runes. The rune indices are absolute indices into the Text slice passed to
// [RubyShaper.Layout]; the range is half-open, [Start, End).
type RubyAnnotation struct {
	// Start is the index of the first annotated base rune.
	Start int
	// End is the index one past the last annotated base rune.
	End int
	// Text is the annotation content. An empty slice is allowed: the group is
	// still kept atomic during wrapping, but no annotation glyphs are produced.
	Text []rune
}

// RubyIssue reports a non-fatal condition detected while building the layout.
type RubyIssue uint8

const (
	// RubyMissingBaseGlyph means at least one base rune of the group has no
	// glyph in the base face and was shaped to the notdef glyph.
	RubyMissingBaseGlyph RubyIssue = 1 << iota
	// RubyMissingAnnotationGlyph means at least one annotation rune has no glyph
	// in the annotation face and was shaped to the notdef glyph.
	RubyMissingAnnotationGlyph
)

// RubyGroup is a base rune range together with its centered annotation.
type RubyGroup struct {
	// Range is the covered base rune range, as an offset into the layout text.
	Range Range
	// Base is the shaped output of the base runes, owned by the group.
	Base Output
	// Annotation is the shaped output of the annotation runes. It holds no
	// glyphs when the annotation text was empty.
	Annotation Output

	// BaseWidth is the natural advance of Base.
	BaseWidth fixed.Int26_6
	// AnnotationWidth is the natural advance of Annotation, zero for an empty
	// annotation.
	AnnotationWidth fixed.Int26_6
	// Width is the advance occupied by the whole group on the line. With
	// [RubyExtendGroup] it may exceed BaseWidth; with [RubyShrinkAnnotation]
	// it always equals BaseWidth.
	Width fixed.Int26_6

	// AnnotationScale is the uniform scale applied to the annotation glyphs,
	// relative to their shaped size (fixed.I(1) means 100%). It differs from
	// fixed.I(1) only when [RubyShrinkAnnotation] shrank an overwide annotation.
	AnnotationScale fixed.Int26_6
	// AnnotationXOffset is the pen position of the annotation relative to the
	// group origin, so that the annotation is centered over the base.
	AnnotationXOffset fixed.Int26_6
	// BaseXOffset is the pen position of the base relative to the group origin.
	// With [RubyExtendGroup] it holds the left half of the extra width.
	BaseXOffset fixed.Int26_6

	// Issues accumulates the non-fatal conditions detected for this group.
	Issues RubyIssue
}

// RubySegment is one logical piece of a ruby layout: plain shaped text with no
// annotation, or an atomic ruby group.
type RubySegment struct {
	// Range is the rune range covered by the segment.
	Range Range
	// Plain is the shaped output for a plain segment, and nil for a group.
	Plain *Output
	// Group is the ruby group for a group segment, and nil for plain text.
	Group *RubyGroup
}

// Advance returns the horizontal advance occupied by the segment.
func (s RubySegment) Advance() fixed.Int26_6 {
	if s.Plain != nil {
		return s.Plain.Advance
	}
	return s.Group.Width
}

// RubyConfig configures a ruby layout.
type RubyConfig struct {
	// Face shapes the base text. It must not be nil.
	Face *ft.Face
	// Size is the font size of the base text.
	Size fixed.Int26_6
	// AnnotationFace shapes the annotation text. If nil, Face is used.
	AnnotationFace *ft.Face
	// AnnotationSize is the font size of the annotation text. It is typically
	// smaller than Size. If zero, Size is used.
	AnnotationSize fixed.Int26_6
	// Overwidth decides how overwide annotations are handled.
	Overwidth RubyOverwidthPolicy
	// Script and Language are passed to the shaper for both base and annotation.
	Script   language.Script
	Language language.Language
	// FontFeatures are applied to both the base and the annotation shaping.
	FontFeatures []FontFeature
}

// RubyLayout is the shaped result of [RubyShaper.Layout]: the paragraph cut
// into plain segments and atomic ruby groups, in logical (reading) order.
type RubyLayout struct {
	// Text is the base text the layout was built from.
	Text []rune
	// Segments are in logical order and together cover the whole text.
	Segments []RubySegment
	// Groups contains the ruby groups in logical order, the same objects as
	// the corresponding group segments.
	Groups []*RubyGroup
	// Direction is always horizontal left-to-right.
	Direction di.Direction
	// LineBounds holds the cross-axis bounds of an unwrapped line of this
	// layout, including the annotation band above the base. Its Gap field is
	// always zero.
	LineBounds Bounds
	// Advance is the total horizontal advance of the unwrapped layout.
	Advance fixed.Int26_6
}

// RubyShaper builds horizontal ruby layouts. The zero value is ready to use;
// reusing a RubyShaper across layouts reuses internal shaping buffers.
type RubyShaper struct {
	shaper HarfbuzzShaper
}

// isStrongRtl reports whether r is a strong right-to-left rune.
func isStrongRtl(r rune) bool {
	c, _ := ucd.LookupBidiClass(r)
	return c&(ucd.BD_R|ucd.BD_AL) != 0
}

// hasMissingGlyph reports whether out contains a notdef glyph. Glyph 0 is
// harfbuzz' marker for a rune unsupported by the face; spaces replaced by
// [font.EmptyGlyph] by the shaper are not reported.
func hasMissingGlyph(out Output) bool {
	for _, g := range out.Glyphs {
		if g.GlyphID == ft.GID(0) {
			return true
		}
	}
	return false
}

// validateAnnotations normalizes and validates the annotation ranges, returning
// them sorted by Start without modifying the caller's slice.
func validateAnnotations(text []rune, annotations []RubyAnnotation) ([]RubyAnnotation, error) {
	out := append([]RubyAnnotation(nil), annotations...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	prevEnd := 0
	for i := range out {
		a := &out[i]
		if a.Start < 0 || a.End <= a.Start || a.End > len(text) {
			return nil, ErrRubyRange
		}
		if a.Start < prevEnd {
			return nil, ErrRubyOverlap
		}
		prevEnd = a.End
	}
	return out, nil
}

// Layout shapes the text and the annotations and builds the ruby groups and
// the plain segments between them.
//
// Only horizontal left-to-right layout is supported. Empty annotation text,
// missing glyphs and overwide annotations are handled as part of the returned
// layout (see [RubyIssue] and [RubyOverwidthPolicy]); invalid ranges,
// overlapping ranges, cluster-splitting boundaries and strong RTL runes return
// one of the documented errors.
func (rs *RubyShaper) Layout(text []rune, annotations []RubyAnnotation, config RubyConfig) (*RubyLayout, error) {
	if config.Face == nil {
		return nil, errors.New("shaping: ruby layout requires a non-nil base face")
	}
	if config.AnnotationFace == nil {
		config.AnnotationFace = config.Face
	}
	if config.AnnotationSize == 0 {
		config.AnnotationSize = config.Size
	}

	for _, r := range text {
		if isStrongRtl(r) {
			return nil, ErrRubyDirection
		}
	}

	sorted, err := validateAnnotations(text, annotations)
	if err != nil {
		return nil, err
	}

	layout := &RubyLayout{
		Text:      text,
		Direction: di.DirectionLTR,
	}

	// Shape the whole paragraph once, both as context for sub-range shaping and
	// to locate the shaping cluster boundaries that groups must respect.
	fullBase := rs.shaper.Shape(Input{
		Text:         text,
		RunStart:     0,
		RunEnd:       len(text),
		Direction:    di.DirectionLTR,
		Face:         config.Face,
		Size:         config.Size,
		Script:       config.Script,
		Language:     config.Language,
		FontFeatures: config.FontFeatures,
	})

	cursor := 0
	for _, ann := range sorted {
		// Plain segment before the group.
		if ann.Start > cursor {
			plain := rs.shapeSubRange(config.Face, config.Size, config, text, cursor, ann.Start)
			layout.Segments = append(layout.Segments, RubySegment{
				Range: Range{Offset: cursor, Count: ann.Start - cursor},
				Plain: &plain,
			})
		}

		group, err := rs.buildGroup(text, ann, config, &fullBase)
		if err != nil {
			return nil, err
		}
		layout.Groups = append(layout.Groups, group)
		layout.Segments = append(layout.Segments, RubySegment{
			Range: group.Range,
			Group: group,
		})
		cursor = ann.End
	}
	// Trailing plain segment.
	if cursor < len(text) {
		plain := rs.shapeSubRange(config.Face, config.Size, config, text, cursor, len(text))
		layout.Segments = append(layout.Segments, RubySegment{
			Range: Range{Offset: cursor, Count: len(text) - cursor},
			Plain: &plain,
		})
	}

	layout.computeMetrics()
	return layout, nil
}

// shapeSubRange shapes text[start:end] as a standalone run, keeping the full
// text available as context for the shaper.
func (rs *RubyShaper) shapeSubRange(face *ft.Face, size fixed.Int26_6, config RubyConfig, text []rune, start, end int) Output {
	return rs.shaper.Shape(Input{
		Text:         text,
		RunStart:     start,
		RunEnd:       end,
		Direction:    di.DirectionLTR,
		Face:         face,
		Size:         size,
		Script:       config.Script,
		Language:     config.Language,
		FontFeatures: config.FontFeatures,
	})
}

// buildGroup shapes the base and annotation of one range and resolves the
// group geometry.
func (rs *RubyShaper) buildGroup(text []rune, ann RubyAnnotation, config RubyConfig, fullBase *Output) (*RubyGroup, error) {
	if !boundaryOnCluster(fullBase, ann.Start) || !boundaryOnCluster(fullBase, ann.End) {
		return nil, ErrRubyCluster
	}

	baseOut := rs.shapeSubRange(config.Face, config.Size, config, text, ann.Start, ann.End)

	group := &RubyGroup{
		Range:     Range{Offset: ann.Start, Count: ann.End - ann.Start},
		Base:      baseOut,
		BaseWidth: baseOut.Advance,
	}
	if hasMissingGlyph(baseOut) {
		group.Issues |= RubyMissingBaseGlyph
	}

	if len(ann.Text) > 0 {
		annOut := rs.shaper.Shape(Input{
			Text:         ann.Text,
			RunStart:     0,
			RunEnd:       len(ann.Text),
			Direction:    di.DirectionLTR,
			Face:         config.AnnotationFace,
			Size:         config.AnnotationSize,
			Script:       config.Script,
			Language:     config.Language,
			FontFeatures: config.FontFeatures,
		})
		group.Annotation = annOut
		group.AnnotationWidth = annOut.Advance
		if hasMissingGlyph(annOut) {
			group.Issues |= RubyMissingAnnotationGlyph
		}
	}

	group.resolveGeometry(config.Overwidth)
	return group, nil
}

// boundaryOnCluster reports whether a rune index is the start of a shaping
// cluster of the fully shaped base text.
func boundaryOnCluster(fullBase *Output, runeIdx int) bool {
	if runeIdx == fullBase.Runes.Offset+fullBase.Runes.Count {
		return true // the end of the text is always a valid boundary
	}
	for i := range fullBase.Glyphs {
		if fullBase.Glyphs[i].ClusterIndex == runeIdx {
			return true
		}
	}
	return false
}

// resolveGeometry computes the group width and the relative base and
// annotation pen positions according to the overwidth policy.
func (g *RubyGroup) resolveGeometry(policy RubyOverwidthPolicy) {
	g.AnnotationScale = fixed.I(1)
	switch {
	case g.AnnotationWidth == 0:
		// No annotation: the group is just its base.
		g.Width = g.BaseWidth
	case g.AnnotationWidth <= g.BaseWidth:
		// The annotation fits: center it, keep the base width.
		g.Width = g.BaseWidth
		g.AnnotationXOffset = (g.BaseWidth - g.AnnotationWidth) / 2
	case policy == RubyShrinkAnnotation:
		// Scale the annotation down to the base width.
		g.Width = g.BaseWidth
		g.AnnotationScale = divFixed(g.BaseWidth, g.AnnotationWidth)
		g.AnnotationXOffset = 0
	default: // RubyExtendGroup
		extra := g.AnnotationWidth - g.BaseWidth
		g.Width = g.AnnotationWidth
		g.BaseXOffset = extra / 2
		g.AnnotationXOffset = 0
	}
}

// divFixed returns the ratio a/b expressed in fixed.Int26_6 units, suitable as
// a glyph scale factor where fixed.I(1) means 100%.
func divFixed(a, b fixed.Int26_6) fixed.Int26_6 {
	return fixed.Int26_6((int64(a) << 6) / int64(b))
}

// annotationLineGap is the vertical gap kept between the top of the base glyph
// band and the bottom of the annotation glyph band. It is set to a quarter of
// the annotation font size, a common ruby spacing convention.
func annotationLineGap(annotationSize fixed.Int26_6) fixed.Int26_6 {
	return annotationSize / 4
}

// annotationBaseline returns the Y position of the annotation baseline
// relative to the base baseline (positive upward). The bottom of the
// annotation glyph box sits one gap above the top of the base glyph box.
func (g *RubyGroup) annotationBaseline() fixed.Int26_6 {
	if g.AnnotationWidth == 0 {
		return 0
	}
	gap := annotationLineGap(g.Annotation.Size)
	return g.Base.GlyphBounds.Ascent + gap - g.Annotation.GlyphBounds.Descent
}

// annotationTop returns the top of the annotation glyph box relative to the
// base baseline, or zero when the group has no annotation.
func (g *RubyGroup) annotationTop() fixed.Int26_6 {
	if g.AnnotationWidth == 0 {
		return 0
	}
	return g.annotationBaseline() + g.Annotation.GlyphBounds.Ascent
}

// computeMetrics fills the layout advance and the combined line bounds, where
// the annotation band adds ascent above the base line.
func (l *RubyLayout) computeMetrics() {
	var (
		advance    fixed.Int26_6
		baseAscent fixed.Int26_6
		descent    fixed.Int26_6
		annTop     fixed.Int26_6
	)
	for _, seg := range l.Segments {
		advance += seg.Advance()
		if seg.Plain != nil {
			if a := seg.Plain.GlyphBounds.Ascent; a > baseAscent {
				baseAscent = a
			}
			if d := seg.Plain.GlyphBounds.Descent; d < descent {
				descent = d
			}
			continue
		}
		g := seg.Group
		if a := g.Base.GlyphBounds.Ascent; a > baseAscent {
			baseAscent = a
		}
		if d := g.Base.GlyphBounds.Descent; d < descent {
			descent = d
		}
		if top := g.annotationTop(); top > annTop {
			annTop = top
		}
	}
	l.Advance = advance
	ascent := baseAscent
	if annTop > ascent {
		ascent = annTop
	}
	l.LineBounds = Bounds{Ascent: ascent, Descent: descent}
}

// PositionedGlyph is a shaped glyph together with the dot at which it must be
// drawn, relative to a line origin whose base baseline is Y = 0.
type PositionedGlyph struct {
	// Glyph is the shaped glyph.
	Glyph Glyph
	// Face and Size identify the font the glyph was shaped in.
	Face *ft.Face
	Size fixed.Int26_6
	// X is the dot position on the main axis, before the glyph XOffset.
	X fixed.Int26_6
	// Y is the dot position on the cross axis relative to the base baseline
	// (positive upward), before the glyph YOffset.
	Y fixed.Int26_6
	// Scale is the uniform scale to apply to the glyph, fixed.I(1) for 100%.
	// It differs from fixed.I(1) only for annotations shrunk via
	// [RubyShrinkAnnotation].
	Scale fixed.Int26_6
	// IsAnnotation marks glyphs belonging to an annotation run.
	IsAnnotation bool
}

// PositionedGlyphs returns the absolute dot positions of the base and
// annotation glyphs of the group. originX is the group origin on the line.
// Base glyphs come first, followed by the annotation glyphs; within each part
// the glyphs are in shaping order.
func (g *RubyGroup) PositionedGlyphs(originX fixed.Int26_6) []PositionedGlyph {
	out := make([]PositionedGlyph, 0, len(g.Base.Glyphs)+len(g.Annotation.Glyphs))

	pen := originX + g.BaseXOffset
	for _, glyph := range g.Base.Glyphs {
		out = append(out, PositionedGlyph{
			Glyph: glyph,
			Face:  g.Base.Face,
			Size:  g.Base.Size,
			X:     pen,
			Y:     0,
			Scale: fixed.I(1),
		})
		pen += glyph.Advance
	}

	if g.AnnotationWidth == 0 {
		return out
	}
	scale := g.AnnotationScale
	pen = originX + g.AnnotationXOffset
	baseline := g.annotationBaseline()
	for _, glyph := range g.Annotation.Glyphs {
		out = append(out, PositionedGlyph{
			Glyph:        glyph,
			Face:         g.Annotation.Face,
			Size:         g.Annotation.Size,
			X:            pen,
			Y:            baseline,
			Scale:        scale,
			IsAnnotation: true,
		})
		pen = mulFixed(pen, glyph.Advance, scale)
	}
	return out
}

// mulFixed returns pen + advance*scale, where scale is in fixed.Int26_6 units.
func mulFixed(pen, advance, scale fixed.Int26_6) fixed.Int26_6 {
	return pen + fixed.Int26_6(int64(advance)*int64(scale)>>6)
}

// PositionedGlyphs returns the glyph positions of the whole unwrapped layout
// as if it were laid on a single line.
func (l *RubyLayout) PositionedGlyphs() []PositionedGlyph {
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
