// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"bytes"
	"errors"
	"testing"

	td "github.com/go-text/typesetting-utils/opentype"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

// rubyTestFaces holds the two fixed faces used by the ruby tests: mplus shapes
// the base text, Noto CJK shapes the annotations. Both are embedded in the
// opentype testdata module and are deterministic across machines.
type rubyTestFaces struct {
	base *font.Face // common/mplus-1p-regular.ttf
	ann  *font.Face // common/NotoSansCJKjp-VF.otf
	serif *font.Face // common/FreeSerif.ttf, for cluster-fusion checks
}

func loadRubyFaces(t *testing.T) rubyTestFaces {
	t.Helper()
	load := func(name string) *font.Face {
		b, err := td.Files.ReadFile(name)
		tu.AssertNoErr(t, err)
		f, err := font.ParseTTF(bytes.NewReader(b))
		tu.AssertNoErr(t, err)
		return f
	}
	return rubyTestFaces{
		base:  load("common/mplus-1p-regular.ttf"),
		ann:   load("common/NotoSansCJKjp-VF.otf"),
		serif: load("common/FreeSerif.ttf"),
	}
}

func rubyTestConfig(faces rubyTestFaces) RubyConfig {
	return RubyConfig{
		Face:           faces.base,
		Size:           fixed.I(32),
		AnnotationFace: faces.ann,
		AnnotationSize: fixed.I(16),
		Script:         language.Han,
		Language:       language.NewLanguage("ja"),
	}
}

// findGroup returns the group annotating the given base rune offset.
func findGroup(t *testing.T, layout *RubyLayout, offset int) *RubyGroup {
	t.Helper()
	for _, g := range layout.Groups {
		if g.Range.Offset == offset {
			return g
		}
	}
	t.Fatalf("no ruby group at offset %d", offset)
	return nil
}

func TestRubyLayoutCenteringAndGeometry(t *testing.T) {
	faces := loadRubyFaces(t)
	// "龠" is one full-width kanji (32px advance) in mplus; its annotation
	// "とう" is two full-width kana (32px advance at 16px) in Noto, so the
	// annotation is exactly as wide as the base.
	text := []rune("龠")
	anns := []RubyAnnotation{{Start: 0, End: 1, Text: []rune("とう")}}

	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)
	tu.Assert(t, len(layout.Segments) == 1)
	tu.Assert(t, layout.Segments[0].Group != nil)

	g := layout.Groups[0]
	tu.Assert(t, g.Range == (Range{Offset: 0, Count: 1}))
	tu.Assert(t, g.BaseWidth == fixed.I(32))
	tu.Assert(t, g.AnnotationWidth == fixed.I(32))
	tu.Assert(t, g.Width == fixed.I(32))
	tu.Assert(t, g.AnnotationXOffset == 0)
	tu.Assert(t, g.BaseXOffset == 0)
	tu.Assert(t, g.AnnotationScale == fixed.I(1))
	tu.Assert(t, g.Issues == 0)

	// A narrower annotation (one kana, 16px advance) must be centered over the
	// 32px base, leaving an 8px margin on each side.
	anns[0].Text = []rune("と")
	layout, err = rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)
	g = layout.Groups[0]
	tu.Assert(t, g.AnnotationWidth == fixed.I(16))
	tu.Assert(t, g.Width == fixed.I(32))
	tu.Assert(t, g.AnnotationXOffset == fixed.I(8))

	// PositionedGlyphs: one base glyph at X=0 on the base baseline, one
	// annotation glyph at X=8 above it.
	pos := g.PositionedGlyphs(0)
	tu.Assert(t, len(pos) == 2)
	tu.Assert(t, !pos[0].IsAnnotation && pos[0].X == 0 && pos[0].Y == 0)
	tu.Assert(t, pos[1].IsAnnotation && pos[1].X == fixed.I(8))
	tu.Assert(t, pos[1].Y > 0) // annotation baseline is above the base baseline
	tu.Assert(t, pos[1].Face == faces.ann)
	tu.Assert(t, pos[0].Face == faces.base)

	// Origin translation is honored.
	pos = g.PositionedGlyphs(fixed.I(100))
	tu.Assert(t, pos[0].X == fixed.I(100))
	tu.Assert(t, pos[1].X == fixed.I(108))
}

func TestRubyLayoutOverwidthPolicies(t *testing.T) {
	faces := loadRubyFaces(t)
	// Base "龠" advances 32px; annotation "とうてつ" (4 kana) advances 64px,
	// i.e. it is twice as wide as the base.
	text := []rune("龠")
	anns := []RubyAnnotation{{Start: 0, End: 1, Text: []rune("とうてつ")}}

	t.Run("extend group", func(t *testing.T) {
		cfg := rubyTestConfig(faces)
		cfg.Overwidth = RubyExtendGroup
		var rs RubyShaper
		layout, err := rs.Layout(text, anns, cfg)
		tu.AssertNoErr(t, err)
		g := layout.Groups[0]
		tu.Assert(t, g.AnnotationWidth == fixed.I(64))
		tu.Assert(t, g.BaseWidth == fixed.I(32))
		tu.Assert(t, g.Width == fixed.I(64))
		// The extra 32px are split equally: base shifted right by 16px.
		tu.Assert(t, g.BaseXOffset == fixed.I(16))
		tu.Assert(t, g.AnnotationXOffset == 0)
		tu.Assert(t, g.AnnotationScale == fixed.I(1))

		pos := g.PositionedGlyphs(0)
		tu.Assert(t, len(pos) == 5) // 1 base + 4 annotation glyphs
		tu.Assert(t, pos[0].X == fixed.I(16))
		var annAdvance fixed.Int26_6
		for i := 1; i < 5; i++ {
			tu.Assert(t, pos[i].IsAnnotation)
			annAdvance += pos[i].Glyph.Advance
		}
		tu.Assert(t, annAdvance == fixed.I(64))
	})

	t.Run("shrink annotation", func(t *testing.T) {
		cfg := rubyTestConfig(faces)
		cfg.Overwidth = RubyShrinkAnnotation
		var rs RubyShaper
		layout, err := rs.Layout(text, anns, cfg)
		tu.AssertNoErr(t, err)
		g := layout.Groups[0]
		tu.Assert(t, g.Width == fixed.I(32))
		tu.Assert(t, g.BaseXOffset == 0)
		tu.Assert(t, g.AnnotationXOffset == 0)
		// 32/64 = 0.5.
		tu.Assert(t, g.AnnotationScale == fixed.I(1)/2)

		pos := g.PositionedGlyphs(0)
		tu.Assert(t, len(pos) == 5)
		// The scaled annotation advances must total the base width.
		var scaled fixed.Int26_6
		for i := 1; i < 5; i++ {
			scaled = mulFixed(scaled, pos[i].Glyph.Advance, pos[i].Scale)
		}
		tu.Assert(t, scaled == fixed.I(32))
	})
}

func TestRubyLayoutSegmentsAndLineHeight(t *testing.T) {
	faces := loadRubyFaces(t)
	// Two groups separated and surrounded by plain text: "ab龠cd鬱ef".
	text := []rune("ab龠cd鬱ef")
	anns := []RubyAnnotation{
		{Start: 2, End: 3, Text: []rune("とう")},
		{Start: 5, End: 6, Text: []rune("うつ")},
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)

	// Expected logical segmentation: plain[0:2], group[2:3], plain[3:5],
	// group[5:6], plain[6:8].
	tu.Assert(t, len(layout.Segments) == 5)
	tu.Assert(t, layout.Segments[0].Plain != nil && layout.Segments[0].Range == (Range{0, 2}))
	tu.Assert(t, layout.Segments[1].Group != nil && layout.Segments[1].Range == (Range{2, 1}))
	tu.Assert(t, layout.Segments[2].Plain != nil && layout.Segments[2].Range == (Range{3, 2}))
	tu.Assert(t, layout.Segments[3].Group != nil && layout.Segments[3].Range == (Range{5, 1}))
	tu.Assert(t, layout.Segments[4].Plain != nil && layout.Segments[4].Range == (Range{6, 2}))
	tu.Assert(t, len(layout.Groups) == 2)

	// The segment advances sum to the layout advance and cover every rune.
	var sum fixed.Int26_6
	covered := 0
	for _, s := range layout.Segments {
		sum += s.Advance()
		covered += s.Range.Count
	}
	tu.Assert(t, covered == len(text))
	tu.Assert(t, sum == layout.Advance)

	// Line height must account for the annotation band: the layout ascent is
	// strictly above the plain base ascent.
	var plainAscent fixed.Int26_6
	for _, s := range layout.Segments {
		if s.Plain != nil && s.Plain.GlyphBounds.Ascent > plainAscent {
			plainAscent = s.Plain.GlyphBounds.Ascent
		}
	}
	tu.Assert(t, layout.LineBounds.Ascent > plainAscent)
	tu.Assert(t, layout.LineBounds.Descent <= 0)

	// An all-plain layout has no annotation band and its bounds equal the base
	// glyph bounds.
	plain, err := rs.Layout([]rune("ab"), nil, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)
	tu.Assert(t, len(plain.Segments) == 1 && plain.Segments[0].Plain != nil)
	tu.Assert(t, plain.LineBounds.Ascent == plain.Segments[0].Plain.GlyphBounds.Ascent)
	tu.Assert(t, plain.LineBounds.Descent == plain.Segments[0].Plain.GlyphBounds.Descent)
}

func TestRubyLayoutEmptyAnnotation(t *testing.T) {
	faces := loadRubyFaces(t)
	text := []rune("龠鬱")
	anns := []RubyAnnotation{
		{Start: 0, End: 1, Text: nil}, // empty annotation: group stays atomic
		{Start: 1, End: 2, Text: []rune{}},
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)
	tu.Assert(t, len(layout.Groups) == 2)
	for _, g := range layout.Groups {
		tu.Assert(t, len(g.Annotation.Glyphs) == 0)
		tu.Assert(t, g.AnnotationWidth == 0)
		tu.Assert(t, g.Width == g.BaseWidth)
		pos := g.PositionedGlyphs(0)
		for _, p := range pos {
			tu.Assert(t, !p.IsAnnotation)
		}
	}
	// No annotation band is added: with no annotation glyphs the layout bounds
	// equal the maximum base glyph bounds across the groups.
	var maxBaseAscent fixed.Int26_6
	for _, g := range layout.Groups {
		if g.Base.GlyphBounds.Ascent > maxBaseAscent {
			maxBaseAscent = g.Base.GlyphBounds.Ascent
		}
	}
	tu.Assert(t, layout.LineBounds.Ascent == maxBaseAscent)
}

func TestRubyLayoutMissingGlyphs(t *testing.T) {
	faces := loadRubyFaces(t)
	// "饕" and "餮" are absent from mplus (notdef), present in Noto.
	text := []rune("饕餮")
	anns := []RubyAnnotation{
		{Start: 0, End: 2, Text: []rune("とうてつ")},
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err) // missing glyphs are reported, not fatal
	g := layout.Groups[0]
	tu.Assert(t, g.Issues&RubyMissingBaseGlyph != 0)
	tu.Assert(t, g.Issues&RubyMissingAnnotationGlyph == 0)

	// An annotation rune absent from the annotation face is reported too.
	// U+30000 (CJK extension G) is in neither test face.
	anns[0].Text = []rune{'龠', 0x30000}
	layout, err = rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)
	g = layout.Groups[0]
	tu.Assert(t, g.Issues&RubyMissingAnnotationGlyph != 0)
}

func TestRubyLayoutInvalidInputs(t *testing.T) {
	faces := loadRubyFaces(t)
	cfg := rubyTestConfig(faces)
	text := []rune("龠鬱龠")
	var rs RubyShaper

	for _, tc := range []struct {
		name string
		anns []RubyAnnotation
		want error
	}{
		{"empty range", []RubyAnnotation{{Start: 1, End: 1, Text: []rune("x")}}, ErrRubyRange},
		{"inverted range", []RubyAnnotation{{Start: 2, End: 1, Text: []rune("x")}}, ErrRubyRange},
		{"negative start", []RubyAnnotation{{Start: -1, End: 1, Text: []rune("x")}}, ErrRubyRange},
		{"end past text", []RubyAnnotation{{Start: 0, End: 9, Text: []rune("x")}}, ErrRubyRange},
		{"overlap", []RubyAnnotation{
			{Start: 0, End: 2, Text: []rune("a")},
			{Start: 1, End: 2, Text: []rune("b")},
		}, ErrRubyOverlap},
		{"touching ranges are allowed", []RubyAnnotation{
			{Start: 0, End: 1, Text: []rune("a")},
			{Start: 1, End: 2, Text: []rune("b")},
		}, nil},
		{"unsorted input is accepted", []RubyAnnotation{
			{Start: 1, End: 2, Text: []rune("b")},
			{Start: 0, End: 1, Text: []rune("a")},
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rs.Layout(text, tc.anns, cfg)
			if tc.want == nil {
				tu.AssertNoErr(t, err)
			} else {
				tu.Assert(t, errors.Is(err, tc.want))
			}
		})
	}

	t.Run("nil face", func(t *testing.T) {
		bad := cfg
		bad.Face = nil
		_, err := rs.Layout(text, []RubyAnnotation{{Start: 0, End: 1}}, bad)
		tu.Assert(t, err != nil)
	})

	t.Run("rtl rejected", func(t *testing.T) {
		// U+0628 ARABIC LETTER BEH is a strong RTL rune.
		_, err := rs.Layout([]rune("aب"), []RubyAnnotation{{Start: 0, End: 1}}, cfg)
		tu.Assert(t, errors.Is(err, ErrRubyDirection))
	})
}

func TestRubyLayoutClusterBoundary(t *testing.T) {
	faces := loadRubyFaces(t)
	// "अक्षि" shapes into fused clusters in FreeSerif: rune 1 and 2 share
	// cluster index 1, so a boundary between them splits a shaping cluster.
	text := []rune("अक्षि")
	cfg := RubyConfig{
		Face:           faces.serif,
		Size:           fixed.I(20),
		AnnotationFace: faces.ann,
		AnnotationSize: fixed.I(12),
		Script:         language.Devanagari,
	}
	var rs RubyShaper

	// Boundary at rune 2 (inside the क्ष conjunct) is rejected.
	_, err := rs.Layout(text, []RubyAnnotation{{Start: 1, End: 2, Text: []rune("x")}}, cfg)
	tu.Assert(t, errors.Is(err, ErrRubyCluster))

	// Boundaries on cluster edges (0..1 and 1..3) are accepted.
	_, err = rs.Layout(text, []RubyAnnotation{{Start: 1, End: 3, Text: []rune("x")}}, cfg)
	tu.AssertNoErr(t, err)
	_, err = rs.Layout(text, []RubyAnnotation{{Start: 0, End: 1, Text: []rune("x")}}, cfg)
	tu.AssertNoErr(t, err)
}

func TestRubyWrappingAtomicGroups(t *testing.T) {
	faces := loadRubyFaces(t)
	// Each CJK rune advances 32px. Width 96px fits exactly three full-width
	// units, and the groups must never be split across lines.
	text := []rune("龠ab鬱cd龠") // groups at 0, 3, 6; plain latin between
	anns := []RubyAnnotation{
		{Start: 0, End: 1, Text: []rune("とう")},
		{Start: 3, End: 4, Text: []rune("うつ")},
		{Start: 6, End: 7, Text: []rune("さく")},
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)

	var rw RubyWrapper
	lines := rw.WrapParagraph(RubyWrapConfig{}, fixed.I(110), layout)
	tu.Assert(t, len(lines) >= 2)

	// No line may contain half a group: every group segment on a line keeps
	// its full rune range and both base and annotation glyphs.
	totalRunes := 0
	for li, line := range lines {
		var lineAdvance fixed.Int26_6
		for _, seg := range line.Segments {
			if seg.Group != nil {
				tu.Assert(t, seg.Group.Range.Count == 1)
				tu.Assert(t, len(seg.Group.Base.Glyphs) == 1)
				tu.Assert(t, len(seg.Group.Annotation.Glyphs) == 2)
			}
			lineAdvance += seg.Advance()
			totalRunes += seg.Range.Count
		}
		tu.Assert(t, line.Advance == lineAdvance)
		if li < len(lines)-1 {
			tu.Assert(t, !line.Overflow)
			tu.Assert(t, line.Advance <= fixed.I(110))
		}
		// Every wrapped line containing a group carries the annotation band.
		hasGroup := false
		for _, seg := range line.Segments {
			if seg.Group != nil {
				hasGroup = true
			}
		}
		if hasGroup {
			tu.Assert(t, line.Bounds.Ascent > line.Segments[0].plainOrGroupBaseAscent())
		}
	}
	tu.Assert(t, totalRunes == len(text))

	// NextLine indices are contiguous and end exactly at the text length.
	tu.Assert(t, lines[0].NextLine > 0)
	tu.Assert(t, lines[len(lines)-1].NextLine == len(text))
	for i := 1; i < len(lines); i++ {
		tu.Assert(t, lines[i].NextLine > lines[i-1].NextLine)
	}
}

// plainOrGroupBaseAscent is a test helper exposing the base ascent of a
// segment for line-height assertions.
func (s RubySegment) plainOrGroupBaseAscent() fixed.Int26_6 {
	if s.Plain != nil {
		return s.Plain.GlyphBounds.Ascent
	}
	return s.Group.Base.GlyphBounds.Ascent
}

func TestRubyWrappingGroupMovesAsUnit(t *testing.T) {
	faces := loadRubyFaces(t)
	// A single overwide group: the base is one 32px kanji but the annotation
	// extends the group to 64px. Put it between plain words and wrap tightly.
	text := []rune("ab龠cd")
	anns := []RubyAnnotation{{Start: 2, End: 3, Text: []rune("とうてつ")}}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)
	g := findGroup(t, layout, 2)
	tu.Assert(t, g.Width == fixed.I(64))

	var rw RubyWrapper
	// Width 48px: the 64px group cannot fit after "ab", so the whole group
	// (base AND annotation) moves to its own line rather than being split.
	lines := rw.WrapParagraph(RubyWrapConfig{}, fixed.I(48), layout)
	tu.Assert(t, len(lines) >= 2)

	// Locate the line carrying the group and assert it is whole.
	groupLine := -1
	for i, line := range lines {
		for _, seg := range line.Segments {
			if seg.Group != nil && seg.Group.Range.Offset == 2 {
				groupLine = i
			}
		}
	}
	tu.Assert(t, groupLine > 0) // the group is not on the first line
	groupSeg := lines[groupLine].Segments[0]
	tu.Assert(t, groupSeg.Group != nil)
	tu.Assert(t, groupSeg.Group.Range == (Range{2, 1}))
	tu.Assert(t, len(groupSeg.Group.Base.Glyphs) == 1)
	tu.Assert(t, len(groupSeg.Group.Annotation.Glyphs) == 4)
	// The group overflows the 48px line but was still kept atomic.
	tu.Assert(t, lines[groupLine].Overflow)

	// The first line only contains the plain "ab" prefix.
	tu.Assert(t, lines[0].Segments[0].Plain != nil)
	tu.Assert(t, lines[0].Segments[0].Range == (Range{0, 2}))
	tu.Assert(t, lines[0].Advance <= fixed.I(48))
}

func TestRubyWrappingNarrowLineAndOverflow(t *testing.T) {
	faces := loadRubyFaces(t)
	text := []rune("龠鬱")
	anns := []RubyAnnotation{
		{Start: 0, End: 1, Text: []rune("とうてつ")}, // 64px group
		{Start: 1, End: 2, Text: []rune("うつ")},
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)

	t.Run("group wider than line overflows once", func(t *testing.T) {
		var rw RubyWrapper
		lines := rw.WrapParagraph(RubyWrapConfig{BreakPolicy: WhenNecessary}, fixed.I(40), layout)
		tu.Assert(t, len(lines) == 2)
		tu.Assert(t, lines[0].Overflow)
		tu.Assert(t, lines[0].Advance == fixed.I(64))
		tu.Assert(t, lines[0].Segments[0].Group != nil)
		tu.Assert(t, lines[1].Segments[0].Group != nil)
	})

	t.Run("never policy keeps overwide words intact", func(t *testing.T) {
		var rw RubyWrapper
		// Plain latin word wider than the line is never broken with Never.
		latinText := []rune("abcdef")
		latinCfg := rubyTestConfig(faces)
		latinLayout, err := rs.Layout(latinText, nil, latinCfg)
		tu.AssertNoErr(t, err)
		lines := rw.WrapParagraph(RubyWrapConfig{BreakPolicy: Never}, fixed.I(30), latinLayout)
		tu.Assert(t, len(lines) == 1)
		tu.Assert(t, lines[0].Overflow)
	})

	t.Run("empty text", func(t *testing.T) {
		_, err := rs.Layout(nil, nil, rubyTestConfig(faces))
		tu.AssertNoErr(t, err)
		var rw RubyWrapper
		rw.Prepare(RubyWrapConfig{}, &RubyLayout{Text: nil, Direction: di.DirectionLTR})
		line := rw.WrapNextLine(fixed.I(100))
		tu.Assert(t, line.NextLine == 0)
		tu.Assert(t, len(line.Segments) == 0)
		line2 := rw.WrapNextLine(fixed.I(100))
		tu.Assert(t, line2.NextLine == 0) // stays exhausted
	})
}

func TestRubyPositionedGlyphsMatchShaping(t *testing.T) {
	faces := loadRubyFaces(t)
	// Mixed Chinese/English text with two groups; verify that base glyph
	// positions produced by the ruby layout match a plain shaping of the same
	// text once the group-extension offsets are accounted for.
	text := []rune("ab龠cd鬱ef")
	anns := []RubyAnnotation{
		{Start: 2, End: 3, Text: []rune("と")},
		{Start: 5, End: 6, Text: []rune("う")},
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)

	// Reference: shape the whole paragraph plainly.
	reference := rs.shaper.Shape(Input{
		Text: text, RunStart: 0, RunEnd: len(text),
		Direction: di.DirectionLTR, Face: faces.base, Size: fixed.I(32),
		Script: language.Han, Language: language.NewLanguage("ja"),
	})

	// The layout's base glyphs must be the same glyph IDs in the same order.
	positioned := layout.PositionedGlyphs()
	var basePositions []PositionedGlyph
	for _, p := range positioned {
		if !p.IsAnnotation {
			basePositions = append(basePositions, p)
		}
	}
	tu.Assert(t, len(basePositions) == len(reference.Glyphs))
	for i, p := range basePositions {
		tu.Assert(t, p.Glyph.GlyphID == reference.Glyphs[i].GlyphID)
		tu.Assert(t, p.Glyph.Advance == reference.Glyphs[i].Advance)
	}

	// Annotation glyphs carry the annotation face and size.
	for _, p := range positioned {
		if p.IsAnnotation {
			tu.Assert(t, p.Face == faces.ann)
			tu.Assert(t, p.Size == fixed.I(16))
			tu.Assert(t, p.Y > 0)
		}
	}

	// Wrapping must not alter glyph identity: collect glyph IDs across all
	// wrapped lines and compare with the unwrapped base + annotation sets.
	var rw RubyWrapper
	lines := rw.WrapParagraph(RubyWrapConfig{}, fixed.I(90), layout)
	var wrappedBaseIDs, wrappedAnnIDs []font.GID
	for _, line := range lines {
		for _, p := range line.PositionedGlyphs() {
			if p.IsAnnotation {
				wrappedAnnIDs = append(wrappedAnnIDs, p.Glyph.GlyphID)
			} else {
				wrappedBaseIDs = append(wrappedBaseIDs, p.Glyph.GlyphID)
			}
		}
	}
	var flatBaseIDs, flatAnnIDs []font.GID
	for _, p := range positioned {
		if p.IsAnnotation {
			flatAnnIDs = append(flatAnnIDs, p.Glyph.GlyphID)
		} else {
			flatBaseIDs = append(flatBaseIDs, p.Glyph.GlyphID)
		}
	}
	tu.Assert(t, len(wrappedBaseIDs) == len(flatBaseIDs))
	tu.Assert(t, len(wrappedAnnIDs) == len(flatAnnIDs))
	for i := range flatBaseIDs {
		tu.Assert(t, wrappedBaseIDs[i] == flatBaseIDs[i])
	}
	for i := range flatAnnIDs {
		tu.Assert(t, wrappedAnnIDs[i] == flatAnnIDs[i])
	}
}

func TestRubyDirectionIsLTR(t *testing.T) {
	faces := loadRubyFaces(t)
	var rs RubyShaper
	layout, err := rs.Layout([]rune("ab龠"), []RubyAnnotation{{Start: 2, End: 3, Text: []rune("x")}}, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)
	tu.Assert(t, layout.Direction == di.DirectionLTR)
	for _, g := range layout.Groups {
		tu.Assert(t, g.Base.Direction == di.DirectionLTR)
		tu.Assert(t, g.Annotation.Direction == di.DirectionLTR)
	}
}

func TestRubyLayoutBoundaryPositions(t *testing.T) {
	faces := loadRubyFaces(t)
	var rs RubyShaper

	t.Run("group covers the whole text", func(t *testing.T) {
		layout, err := rs.Layout([]rune("龠鬱"), []RubyAnnotation{
			{Start: 0, End: 2, Text: []rune("とう")},
		}, rubyTestConfig(faces))
		tu.AssertNoErr(t, err)
		tu.Assert(t, len(layout.Segments) == 1)
		tu.Assert(t, layout.Segments[0].Group != nil)
		tu.Assert(t, len(layout.Groups) == 1)
		tu.Assert(t, layout.Groups[0].Range == (Range{0, 2}))
	})

	t.Run("group at the very end", func(t *testing.T) {
		layout, err := rs.Layout([]rune("ab龠"), []RubyAnnotation{
			{Start: 2, End: 3, Text: []rune("と")},
		}, rubyTestConfig(faces))
		tu.AssertNoErr(t, err)
		tu.Assert(t, len(layout.Segments) == 2)
		tu.Assert(t, layout.Segments[0].Plain != nil)
		tu.Assert(t, layout.Segments[1].Group != nil)
		tu.Assert(t, layout.Segments[1].Range == (Range{2, 1}))
	})

	t.Run("group at the very start", func(t *testing.T) {
		layout, err := rs.Layout([]rune("龠ab"), []RubyAnnotation{
			{Start: 0, End: 1, Text: []rune("と")},
		}, rubyTestConfig(faces))
		tu.AssertNoErr(t, err)
		tu.Assert(t, len(layout.Segments) == 2)
		tu.Assert(t, layout.Segments[0].Group != nil)
		tu.Assert(t, layout.Segments[1].Plain != nil)
	})

	t.Run("nil annotation face and size fall back to base", func(t *testing.T) {
		cfg := RubyConfig{Face: faces.base, Size: fixed.I(32)}
		layout, err := rs.Layout([]rune("龠"), []RubyAnnotation{
			{Start: 0, End: 1, Text: []rune("と")},
		}, cfg)
		tu.AssertNoErr(t, err)
		g := layout.Groups[0]
		tu.Assert(t, g.Annotation.Face == faces.base)
		tu.Assert(t, g.Annotation.Size == fixed.I(32))
	})

	t.Run("no annotations is plain layout", func(t *testing.T) {
		layout, err := rs.Layout([]rune("ab龠"), nil, rubyTestConfig(faces))
		tu.AssertNoErr(t, err)
		tu.Assert(t, len(layout.Groups) == 0)
		tu.Assert(t, len(layout.Segments) == 1)
		tu.Assert(t, layout.Segments[0].Plain != nil)
	})
}

func TestRubyWrappingRuneAccountingAcrossWidths(t *testing.T) {
	faces := loadRubyFaces(t)
	text := []rune("龠ab鬱cd龠ef鬱")
	anns := []RubyAnnotation{
		{Start: 0, End: 1, Text: []rune("とうてつ")}, // overwide
		{Start: 3, End: 4, Text: []rune("うつ")},
		{Start: 6, End: 7, Text: []rune("さく")},
		{Start: 9, End: 10, Text: []rune("あ")},
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, rubyTestConfig(faces))
	tu.AssertNoErr(t, err)

	for _, width := range []int{1, 16, 32, 48, 64, 100, 200, 1000} {
		var rw RubyWrapper
		lines := rw.WrapParagraph(RubyWrapConfig{}, fixed.I(width), layout)
		tu.AssertC(t, len(lines) > 0, "width must produce at least one line")
		total := 0
		prevNext := 0
		for _, line := range lines {
			total += line.runeCount()
			// Lines are contiguous.
			tu.AssertC(t, line.NextLine > prevNext, "lines must advance")
			prevNext = line.NextLine
		}
		tu.AssertC(t, total == len(text), "all runes placed exactly once")
		tu.AssertC(t, prevNext == len(text), "last line ends at text end")
	}
}

// runeCount sums the runes of a line's segments.
func (l RubyLine) runeCount() int {
	n := 0
	for _, s := range l.Segments {
		n += s.Range.Count
	}
	return n
}
