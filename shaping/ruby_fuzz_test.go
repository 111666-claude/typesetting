// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"bytes"
	"testing"

	td "github.com/go-text/typesetting-utils/opentype"
	"github.com/go-text/typesetting/font"
	"golang.org/x/image/math/fixed"
)

func loadRubyFuzzFace(f *testing.F) *font.Face {
	f.Helper()
	b, err := td.Files.ReadFile("common/mplus-1p-regular.ttf")
	if err != nil {
		f.Fatal(err)
	}
	face, err := font.ParseTTF(bytes.NewReader(b))
	if err != nil {
		f.Fatal(err)
	}
	return face
}

// FuzzRubyLayout builds ruby layouts and wraps them, checking that every base
// rune is placed exactly once and that no group is ever split.
func FuzzRubyLayout(f *testing.F) {
	face := loadRubyFuzzFace(f)
	f.Add("ab龠cd鬱ef", byte(40), byte(1))
	f.Add("饕餮とうてつ龠", byte(120), byte(0))
	f.Add("a", byte(1), byte(2))
	f.Fuzz(func(t *testing.T, input string, widthByte byte, policyByte byte) {
		text := []rune(input)
		if len(text) == 0 {
			return
		}
		// Build a deterministic set of non-overlapping single-rune annotations
		// from the fuzz text, covering every third rune at most.
		var anns []RubyAnnotation
		for i := 0; i < len(text); i += 3 {
			anns = append(anns, RubyAnnotation{
				Start: i,
				End:   i + 1,
				Text:  []rune{'と', 'う'},
			})
		}

		var rs RubyShaper
		layout, err := rs.Layout(text, anns, RubyConfig{
			Face:           face,
			Size:           fixed.I(16),
			AnnotationFace: face,
			AnnotationSize: fixed.I(8),
			Overwidth:      RubyOverwidthPolicy(policyByte % 2),
		})
		if err != nil {
			return // invalid fuzz input is acceptable
		}

		// Segment ranges cover the whole text exactly once.
		covered := 0
		for _, s := range layout.Segments {
			covered += s.Range.Count
		}
		if covered != len(text) {
			t.Fatalf("segments cover %d runes, text has %d", covered, len(text))
		}

		var rw RubyWrapper
		width := fixed.I(int(widthByte)) + fixed.I(1) // always strictly positive
		lines := rw.WrapParagraph(RubyWrapConfig{
			BreakPolicy: LineBreakPolicy(policyByte % 3),
		}, width, layout)

		totalRunes := 0
		for _, line := range lines {
			for _, seg := range line.Segments {
				totalRunes += seg.Range.Count
				if seg.Group != nil && seg.Group.Range.Count != 1 {
					t.Fatalf("ruby group was split: range=%+v", seg.Group.Range)
				}
			}
		}
		if totalRunes != len(text) {
			t.Fatalf("wrapped lines cover %d runes, text has %d", totalRunes, len(text))
		}

		// The glyph-position view must never panic and must stay consistent.
		var glyphCount int
		for _, line := range lines {
			glyphCount += len(line.PositionedGlyphs())
		}
		if glyphCount == 0 {
			t.Fatal("no glyphs produced")
		}
	})
}
