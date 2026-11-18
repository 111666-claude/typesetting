// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	td "github.com/go-text/typesetting-utils/opentype"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

// drawRubyLine renders one wrapped ruby line into an RGBA image. Base glyph
// boxes are green, annotation boxes are blue, dots are black and the base
// baseline is a red horizontal line. The image height is derived from the
// line bounds so the annotation band is visibly above the base band.
func drawRubyLine(line RubyLine, file string) error {
	ascent := line.Bounds.Ascent.Round()
	descent := line.Bounds.Descent.Round() // negative
	height := ascent - descent
	width := line.Advance.Round() + 2
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Rect, image.NewUniform(color.White), image.Point{}, draw.Src)

	blue := color.RGBA{B: 0xCF, A: 0xFF}
	green := color.RGBA{R: 0xCF, G: 0xFF, B: 0xCF, A: 0xFF}
	red := color.RGBA{R: 0xFF, A: 0xFF}
	black := color.RGBA{A: 0xFF}

	// Base baseline at Y = ascent (image Y grows downward).
	for x := 0; x < width; x++ {
		img.SetRGBA(x, ascent, red)
	}

	for _, p := range line.PositionedGlyphs() {
		// Dot in image coordinates. Positive Y (up) becomes smaller image Y.
		dotX := p.X.Round()
		dotY := ascent - p.Y.Round()
		dotWithOffset := image.Pt(dotX+p.Glyph.XOffset.Round(), dotY-p.Glyph.YOffset.Round())

		minX := dotWithOffset.X + p.Glyph.XBearing.Round()
		maxX := minX + p.Glyph.Width.Round()
		minY := dotWithOffset.Y - p.Glyph.YBearing.Round()
		maxY := minY - p.Glyph.Height.Round()

		c := green
		if p.IsAnnotation {
			c = blue
		}
		drawRect(img, image.Pt(minX, minY), image.Pt(maxX, maxY), c)
		drawPoint(img, image.Pt(dotX, dotY), black)
	}

	f, err := os.Create(file)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func TestRubyRenderWrappedLines(t *testing.T) {
	faces := loadRubyFaces(t)
	cfg := RubyConfig{
		Face:           faces.base,
		Size:           fixed.I(32),
		AnnotationFace: faces.ann,
		AnnotationSize: fixed.I(16),
		Script:         language.Han,
		Language:       language.NewLanguage("ja"),
	}
	// Mixed Chinese/English paragraph with three groups, one of them overwide.
	text := []rune("ab龠cd鬱ef饕餮gh")
	anns := []RubyAnnotation{
		{Start: 2, End: 3, Text: []rune("とう")},
		{Start: 5, End: 6, Text: []rune("うつ")},
		{Start: 8, End: 10, Text: []rune("とうてつ")}, // overwide group
	}
	var rs RubyShaper
	layout, err := rs.Layout(text, anns, cfg)
	tu.AssertNoErr(t, err)

	var rw RubyWrapper
	lines := rw.WrapParagraph(RubyWrapConfig{}, fixed.I(140), layout)
	tu.Assert(t, len(lines) >= 2)

	for i, line := range lines {
		// Every line's reported bounds must contain all its glyph boxes.
		for _, p := range line.PositionedGlyphs() {
			top := (p.Y + p.Glyph.YOffset + p.Glyph.YBearing).Round()
			bottom := (p.Y + p.Glyph.YOffset + p.Glyph.YBearing + p.Glyph.Height).Round()
			tu.AssertC(t, top <= line.Bounds.Ascent.Round(), "glyph top above line ascent")
			tu.AssertC(t, bottom >= line.Bounds.Descent.Round(), "glyph bottom below line descent")
		}
		// Annotation boxes must sit entirely above the base baseline.
		for _, p := range line.PositionedGlyphs() {
			if !p.IsAnnotation {
				continue
			}
			bottom := (p.Y + p.Glyph.YOffset + p.Glyph.YBearing + p.Glyph.Height).Round()
			tu.AssertC(t, bottom > 0, "annotation must stay above the base baseline")
		}
		path := filepath.Join(os.TempDir(), "ruby_wrap_line.png")
		if i == 0 {
			path = filepath.Join(os.TempDir(), "ruby_wrap_line0.png")
		}
		tu.AssertNoErr(t, drawRubyLine(line, path))
	}

	// Also render the single-line layout for direct visual comparison.
	single := RubyLine{
		Segments: layout.Segments,
		Bounds:   layout.LineBounds,
		Advance:  layout.Advance,
	}
	tu.AssertNoErr(t, drawRubyLine(single, filepath.Join(os.TempDir(), "ruby_single.png")))
}

// TestRubyFixedFacesCoverage documents and pins the coverage assumptions the
// ruby tests rely on: mplus covers the common kanji and latin but not the rare
// 饕/餮, while Noto CJK covers all kana and kanji used by the annotations.
func TestRubyFixedFacesCoverage(t *testing.T) {
	b, err := td.Files.ReadFile("common/mplus-1p-regular.ttf")
	tu.AssertNoErr(t, err)
	mplus, err := font.ParseTTF(bytes.NewReader(b))
	tu.AssertNoErr(t, err)
	b, err = td.Files.ReadFile("common/NotoSansCJKjp-VF.otf")
	tu.AssertNoErr(t, err)
	noto, err := font.ParseTTF(bytes.NewReader(b))
	tu.AssertNoErr(t, err)

	for _, r := range []rune("龠鬱") {
		_, has := mplus.NominalGlyph(r)
		tu.AssertC(t, has, "mplus should cover base kanji")
	}
	for _, r := range []rune("饕餮") {
		_, has := mplus.NominalGlyph(r)
		tu.AssertC(t, !has, "mplus should lack rare kanji used by missing-glyph tests")
	}
	for _, r := range []rune("とうてつうつさく") {
		_, has := noto.NominalGlyph(r)
		tu.AssertC(t, has, "Noto CJK should cover annotation kana")
	}
	_, has := noto.NominalGlyph(0x30000)
	tu.AssertC(t, !has, "Noto CJK should lack U+30000 used by missing-glyph tests")
}
