// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"

	hb "github.com/go-text/typesetting-utils/harfbuzz"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	tu "github.com/go-text/typesetting/testutils"
)

func loadFontBytes(t testing.TB, path string, fs interface {
	ReadFile(string) ([]byte, error)
},
) *Font {
	t.Helper()
	b, err := fs.ReadFile(path)
	tu.AssertNoErr(t, err)
	ld, err := ot.NewLoader(bytes.NewReader(b))
	tu.AssertNoErr(t, err)
	f, err := NewFont(ld)
	tu.AssertNoErr(t, err)
	return f
}

func TestVariationPlanAxesAndInstances(t *testing.T) {
	// GVAR-no-HVAR: 2 axes, 36 named instances, avar on wght
	f := loadFont(t, "toys/GVAR-no-HVAR.ttf")

	axes := f.Axes()
	tu.Assert(t, len(axes) == 2)
	tu.Assert(t, axes[0] == Axis{Tag: ot.MustNewTag("wght"), Minimum: 100, Default: 400, Maximum: 900})
	tu.Assert(t, axes[1] == Axis{Tag: ot.MustNewTag("wdth"), Minimum: 70, Default: 100, Maximum: 100})

	// mutating the returned slice must not affect the font
	axes[0].Minimum = -9999
	axesAgain := f.Axes()
	tu.Assert(t, axesAgain[0].Minimum == 100)

	insts := f.NamedInstances()
	tu.Assert(t, len(insts) == 36)
	first := insts[0]
	tu.Assert(t, first.Name == "Thin")
	tu.Assert(t, first.SubfamilyNameID == 258)
	tu.Assert(t, len(first.Coordinates) == 2 && first.Coordinates[0] == 100 && first.Coordinates[1] == 100)
	// this font has no PostScript names for its instances
	tu.Assert(t, first.PostScriptName == "")

	// a font without fvar exposes no axes or instances
	plain := loadFont(t, "common/DejaVuSans.ttf")
	tu.Assert(t, len(plain.Axes()) == 0)
	tu.Assert(t, len(plain.NamedInstances()) == 0)
}

func TestVariationPlanNamedInstanceNames(t *testing.T) {
	// SourceSans-VF-HVAR instances carry both subfamily and PostScript name IDs.
	f := loadFont(t, "common/SourceSans-VF-HVAR.ttf")
	insts := f.NamedInstances()
	tu.Assert(t, len(insts) == 6)
	for _, inst := range insts {
		tu.Assert(t, inst.Name != "")
		tu.Assert(t, inst.PostScriptName != "")
		tu.Assert(t, len(inst.Coordinates) == 1)
	}
	tu.Assert(t, insts[0].Name == "ExtraLight" && insts[0].Coordinates[0] == 200)
}

func TestVariationPlanCoordinateSpaces(t *testing.T) {
	f := loadFont(t, "toys/GVAR-no-HVAR.ttf")
	wght := ot.MustNewTag("wght")
	wdth := ot.MustNewTag("wdth")

	// no variation: all axes defaulted, normalized location is zero
	plan, err := f.NewVariationPlan(nil)
	tu.AssertNoErr(t, err)
	tu.Assert(t, plan.IsDefault())
	tu.Assert(t, len(plan.UserCoords()) == 0)
	tu.Assert(t, plan.DesignCoords()[0] == 400 && plan.DesignCoords()[1] == 100)
	tu.Assert(t, plan.NormalizedCoords()[0] == 0 && plan.NormalizedCoords()[1] == 0)
	tu.Assert(t, len(plan.ClampedAxes()) == 0)

	// unset axis falls back to its default (wdth=100)
	plan, err = f.NewVariationPlan([]Variation{{Tag: wght, Value: 600}})
	tu.AssertNoErr(t, err)
	tu.Assert(t, plan.DesignCoords()[0] == 600 && plan.DesignCoords()[1] == 100)
	// linear normalized: (600-400)/(900-400) = 0.4 -> 6553.6 = 6554,
	// then avar maps 0.4000244 to 0.3800049 -> 6226
	tu.Assert(t, plan.NormalizedCoords()[0] == 6226)
	tu.Assert(t, plan.NormalizedCoords()[1] == 0)
	// the same value must be produced by the pre-existing normalization API
	legacy := f.NormalizeVariations([]float32{600, 100})
	tu.Assert(t, legacy[0] == plan.NormalizedCoords()[0])

	// named instance resolution reaches the same location
	instancePlan, err := f.NewVariationPlanFromInstance(5) // wght=600, wdth=100
	tu.AssertNoErr(t, err)
	tu.Assert(t, instancePlan.DesignCoords()[0] == 600)
	tu.Assert(t, instancePlan.NormalizedCoords()[0] == plan.NormalizedCoords()[0])

	// out of range values are clamped and reported
	under, err := f.NewVariationPlan([]Variation{{Tag: wght, Value: 50}})
	tu.AssertNoErr(t, err)
	tu.Assert(t, under.DesignCoords()[0] == 100)
	tu.Assert(t, under.NormalizedCoords()[0] == tables.NewCoord(-1))
	tu.Assert(t, len(under.ClampedAxes()) == 1 && under.ClampedAxes()[0] == wght)

	over, err := f.NewVariationPlan([]Variation{{Tag: wdth, Value: 400}, {Tag: wght, Value: 1000}})
	tu.AssertNoErr(t, err)
	tu.Assert(t, over.DesignCoords()[0] == 900 && over.DesignCoords()[1] == 100)
	tu.Assert(t, over.NormalizedCoords()[0] == tables.NewCoord(1))
	tu.Assert(t, len(over.ClampedAxes()) == 2)
}

func TestVariationPlanAvarAndNegativeRange(t *testing.T) {
	// TestAVAR: TEST axis 100..400..900 with an avar segment map flattening
	// the whole [-0.5, 0.5] normalized region to 0.
	f := loadFontBytes(t, "harfbuzz_reference/text-rendering-tests/fonts/TestAVAR.ttf", hb.Files)
	tu.Assert(t, len(f.avar.AxisSegmentMaps) == 1)

	for _, tc := range []struct {
		user   float32
		design float32
		norm   VarCoord
	}{
		{100, 100, -16384},
		{400, 400, 0},
		{650, 650, 0}, // (650-400)/500 = 0.5, avar maps it back to 0
		{900, 900, 16384},
	} {
		plan, err := f.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("TEST"), Value: tc.user}})
		tu.AssertNoErr(t, err)
		tu.Assert(t, plan.DesignCoords()[0] == tc.design)
		tu.AssertC(t, plan.NormalizedCoords()[0] == tc.norm, fmt.Sprintf("TEST=%v: got %d, want %d", tc.user, plan.NormalizedCoords()[0], tc.norm))
	}

	// Commissioner exposes a negative-range slnt axis: -12 .. 0 .. 0.
	c := loadFont(t, "common/Commissioner-VF.ttf")
	slnt := ot.MustNewTag("slnt")
	for _, tc := range []struct {
		user float32
		norm VarCoord
	}{
		{0, 0},
		{-6, -8192},
		{-12, -16384},
		{-20, -16384}, // clamped to the minimum
	} {
		plan, err := c.NewVariationPlan([]Variation{{Tag: slnt, Value: tc.user}})
		tu.AssertNoErr(t, err)
		tu.AssertC(t, plan.NormalizedCoords()[1] == tc.norm, fmt.Sprintf("slnt=%v: got %d, want %d", tc.user, plan.NormalizedCoords()[1], tc.norm))
	}
	// the -20 value must also be reported as clamped and reduced to -12
	plan, err := c.NewVariationPlan([]Variation{{Tag: slnt, Value: -20}})
	tu.AssertNoErr(t, err)
	tu.Assert(t, plan.DesignCoords()[1] == -12)
	tu.Assert(t, len(plan.ClampedAxes()) == 1 && plan.ClampedAxes()[0] == slnt)
}

func TestVariationPlanErrors(t *testing.T) {
	f := loadFont(t, "toys/GVAR-no-HVAR.ttf")
	wght := ot.MustNewTag("wght")
	unknown := ot.MustNewTag("nope")

	// non variable font
	plain := loadFont(t, "common/DejaVuSans.ttf")
	_, err := plain.NewVariationPlan([]Variation{{Tag: wght, Value: 400}})
	tu.Assert(t, errors.Is(err, ErrNotVariable))
	_, err = plain.NewVariationPlanFromInstance(0)
	tu.Assert(t, errors.Is(err, ErrNotVariable))

	// unknown tag
	_, err = f.NewVariationPlan([]Variation{{Tag: unknown, Value: 400}})
	tu.Assert(t, errors.Is(err, ErrUnknownAxis))

	// duplicated axis
	_, err = f.NewVariationPlan([]Variation{
		{Tag: wght, Value: 400},
		{Tag: wght, Value: 600},
	})
	tu.Assert(t, errors.Is(err, ErrDuplicateAxis))

	// NaN and Inf
	for _, bad := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		_, err = f.NewVariationPlan([]Variation{{Tag: wght, Value: bad}})
		tu.Assert(t, errors.Is(err, ErrInvalidCoordinate))
	}

	// more variations than axes
	_, err = f.NewVariationPlan([]Variation{
		{Tag: wght, Value: 400},
		{Tag: ot.MustNewTag("wdth"), Value: 90},
		{Tag: ot.MustNewTag("wdth"), Value: 80},
	})
	tu.Assert(t, errors.Is(err, ErrTooManyVariations))

	// out of range instance index
	_, err = f.NewVariationPlanFromInstance(len(f.NamedInstances()))
	tu.Assert(t, errors.Is(err, ErrInstanceNotFound))
	_, err = f.NewVariationPlanFromInstance(-1)
	tu.Assert(t, errors.Is(err, ErrInstanceNotFound))

	// an error must leave the caller input untouched
	input := []Variation{{Tag: unknown, Value: 1}}
	_, _ = f.NewVariationPlan(input)
	tu.Assert(t, input[0].Value == 1)
}

func TestVariationPlanImmutabilityAndIsolation(t *testing.T) {
	f := loadFont(t, "toys/GVAR-no-HVAR.ttf")
	plan, err := f.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("wght"), Value: 600}})
	tu.AssertNoErr(t, err)

	// mutate every slice returned by the getters and check the plan is stable
	user := plan.UserCoords()
	user[0].Value = 900
	design := plan.DesignCoords()
	design[0] = -1
	norm := plan.NormalizedCoords()
	norm[0] = -16384
	clamped := plan.ClampedAxes()
	_ = clamped
	tu.Assert(t, plan.UserCoords()[0].Value == 600)
	tu.Assert(t, plan.DesignCoords()[0] == 600)
	tu.Assert(t, plan.NormalizedCoords()[0] == 6226)

	// the same plan drives two independent faces without sharing state
	face1 := NewFace(f)
	face2 := NewFace(f)
	tu.AssertNoErr(t, plan.Apply(face1))
	tu.AssertNoErr(t, plan.Apply(face2))
	tu.Assert(t, face1.Coords()[0] == 6226 && face2.Coords()[0] == 6226)
	// face coordinate slices are independent from each other and from the plan
	face1.SetCoords(nil)
	tu.Assert(t, len(face1.Coords()) == 0)
	tu.Assert(t, face2.Coords()[0] == 6226)
	tu.Assert(t, plan.NormalizedCoords()[0] == 6226)

	// Face.ApplyVariationPlan is the convenience entry point
	face3 := NewFace(f)
	tu.AssertNoErr(t, face3.ApplyVariationPlan(plan))
	tu.Assert(t, face3.Coords()[0] == 6226)

	// a plan built for another axis layout is rejected, not silently applied
	other := loadFont(t, "common/SourceSans-VF-HVAR.ttf")
	otherPlan, err := other.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("wght"), Value: 500}})
	tu.AssertNoErr(t, err)
	wrong := NewFace(f)
	err = otherPlan.Apply(wrong)
	tu.Assert(t, errors.Is(err, ErrAxisMismatch))
	tu.Assert(t, len(wrong.Coords()) == 0) // face left untouched on error
}

func TestVariationPlanDrivesGvarAndAdvances(t *testing.T) {
	// GVAR-no-HVAR has no HVAR table: advances are computed from gvar phantom
	// points, so a plan location must match the legacy SetVariations path.
	f := loadFont(t, "toys/GVAR-no-HVAR.ttf")
	instancePlan, err := f.NewVariationPlanFromInstance(5) // wght=600, wdth=100
	tu.AssertNoErr(t, err)

	planFace := NewFace(f)
	legacyFace := NewFace(f)
	tu.AssertNoErr(t, instancePlan.Apply(planFace))
	legacyFace.SetVariations([]Variation{{Tag: ot.MustNewTag("wght"), Value: 600}})

	for gid := GID(0); int(gid) < f.nGlyphs; gid++ {
		planAdv := planFace.HorizontalAdvance(gid)
		legacyAdv := legacyFace.HorizontalAdvance(gid)
		tu.AssertC(t, planAdv == legacyAdv, fmt.Sprintf("gid %d: plan advance %v, legacy %v", gid, planAdv, legacyAdv))
	}
	// spot check the first two advances, the base advance and the first
	// gvar-derived one.
	tu.Assert(t, planFace.HorizontalAdvance(0) == 600)

	// the strict plan API rejects duplicates, while the lenient
	// SetVariations path collapses them (last value wins, then clamps):
	// wght 80 -> 100. Build the equivalent plan from the clamped value.
	dupPlan, err := f.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("wght"), Value: 100}})
	tu.AssertNoErr(t, err)
	dupFace := NewFace(f)
	tu.AssertNoErr(t, dupPlan.Apply(dupFace))
	legacyFace.SetVariations([]Variation{
		{Tag: ot.MustNewTag("wght"), Value: 600},
		{Tag: ot.MustNewTag("wght"), Value: 80},
	})
	exps := [15]float32{600, 1164, 1170, 813, 741, 1164, 1170, 813, 741, 270, 270, 0, 0, 0, 0}
	for gid, exp := range exps {
		got := dupFace.HorizontalAdvance(GID(gid))
		tu.AssertC(t, got == exp && got == legacyFace.HorizontalAdvance(GID(gid)),
			fmt.Sprintf("gid %d: advance %v, want %v", gid, got, exp))
	}
}

func TestVariationPlanDrivesHvarVvarAndMetrics(t *testing.T) {
	// SourceSerifVariable provides HVAR and VVAR tables; reference advances
	// are the integer values used by the harfbuzz test suite.
	f := loadFontBytes(t, "fonts/SourceSerifVariable-Roman-VVAR.abc.ttf", hb.Files)

	defaultPlan, err := f.NewVariationPlan(nil)
	tu.AssertNoErr(t, err)
	face := NewFace(f)
	tu.AssertNoErr(t, defaultPlan.Apply(face))
	tu.Assert(t, math.Round(float64(face.HorizontalAdvance(1))) == 508)
	tu.Assert(t, math.Round(float64(face.VerticalAdvance(1))) == -1000)

	plan700, err := f.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("wght"), Value: 700}})
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, plan700.Apply(face))
	tu.Assert(t, plan700.NormalizedCoords()[0] == 11821)
	tu.Assert(t, math.Round(float64(face.HorizontalAdvance(1))) == 531)
	tu.Assert(t, math.Round(float64(face.VerticalAdvance(1))) == -1012)

	// MVAR: SourceSans-VF-HVAR exposes metrics deltas (xHeight varies).
	h := loadFont(t, "common/SourceSans-VF-HVAR.ttf")
	metricsFace := NewFace(h)
	defaultMetrics, err := h.NewVariationPlan(nil)
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, defaultMetrics.Apply(metricsFace))
	defaultXHeight := metricsFace.LineMetric(XHeight)

	heavy, err := h.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("wght"), Value: 900}})
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, heavy.Apply(metricsFace))
	tu.Assert(t, metricsFace.Coords()[0] == tables.NewCoord(1))
	tu.Assert(t, metricsFace.LineMetric(XHeight) != defaultXHeight)
	tu.Assert(t, metricsFace.LineMetric(XHeight) == 500 && defaultXHeight == 478)

	// gvar outlines: per-glyph extents at wght=900, compared against the
	// harfbuzz reference values already used by TestGlyphExtentsVar.
	ext, ok := metricsFace.GlyphExtents(2)
	tu.Assert(t, ok)
	ext.XBearing = float32(math.Round(float64(ext.XBearing)))
	ext.YBearing = float32(math.Round(float64(ext.YBearing)))
	ext.Width = float32(math.Round(float64(ext.Width)))
	ext.Height = float32(math.Round(float64(ext.Height)))
	tu.Assert(t, ext == GlyphExtents{XBearing: 44, YBearing: 662, Width: 630, Height: -674})
}

func TestSetVariationsKeepsLegacyBehavior(t *testing.T) {
	f := loadFont(t, "toys/GVAR-no-HVAR.ttf")
	wght := ot.MustNewTag("wght")
	wdth := ot.MustNewTag("wdth")

	// empty variations clear the coordinates
	face := NewFace(f)
	face.SetVariations(nil)
	tu.Assert(t, len(face.Coords()) == 0)

	// duplicated axes: last value wins, exactly like the old implementation
	face.SetVariations([]Variation{
		{Tag: wght, Value: 600},
		{Tag: wght, Value: 80},
	})
	tu.Assert(t, face.Coords()[0] == f.NormalizeVariations([]float32{80, 100})[0])

	// unknown tags are ignored and do not prevent the remaining axes
	face.SetVariations([]Variation{
		{Tag: ot.MustNewTag("zzzz"), Value: 12},
		{Tag: wght, Value: 600},
	})
	tu.Assert(t, face.Coords()[0] == 6226)

	// out of range values are clamped through the plan
	face.SetVariations([]Variation{{Tag: wght, Value: 9999}})
	tu.Assert(t, face.Coords()[0] == tables.NewCoord(1))

	// the lenient SetVariations path does not sanitize NaN/Inf itself;
	// non-finite values are rejected up front by the strict plan API,
	// which is the documented entry point for validated settings.
	// Here the remaining finite axis is still applied.
	face.SetVariations([]Variation{{Tag: wdth, Value: 80}})
	tu.Assert(t, face.Coords()[1] != 0)
	for _, bad := range []float32{float32(math.NaN()), float32(math.Inf(1))} {
		_, err := f.NewVariationPlan([]Variation{{Tag: wght, Value: bad}})
		tu.Assert(t, errors.Is(err, ErrInvalidCoordinate))
	}

	// non variable fonts stay on the "no coordinates" behavior
	plain := loadFont(t, "common/DejaVuSans.ttf")
	plainFace := NewFace(plain)
	plainFace.SetVariations([]Variation{{Tag: wght, Value: 400}})
	tu.Assert(t, len(plainFace.Coords()) == 0)
}

func TestVariationPlanDrivesCOLR(t *testing.T) {
	// VAR-COLRv1 is built from GVAR-no-HVAR.ttf (fvar: wght + wdth, avar)
	// with a COLR version 1 table whose ItemVariationStore carries a +42
	// delta active when the wght normalized coordinate reaches +1.
	file, err := os.Open("testdata/VAR-COLRv1.ttf")
	tu.AssertNoErr(t, err)
	defer file.Close()
	ld, err := ot.NewLoader(file)
	tu.AssertNoErr(t, err)
	f, err := NewFont(ld)
	tu.AssertNoErr(t, err)
	tu.Assert(t, f.COLR != nil && f.CPAL != nil)
	store := f.COLR.ItemVariationStore
	tu.Assert(t, store != nil && store.AxisCount() == 2)

	index := tables.VariationStoreIndex{DeltaSetOuter: 0, DeltaSetInner: 0}
	face := NewFace(f)

	defaultPlan, err := f.NewVariationPlan(nil)
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, defaultPlan.Apply(face))
	tu.Assert(t, store.GetDelta(index, face.Coords()) == 0)

	maxPlan, err := f.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("wght"), Value: 900}})
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, maxPlan.Apply(face))
	tu.Assert(t, face.Coords()[0] == tables.NewCoord(1))
	tu.Assert(t, store.GetDelta(index, face.Coords()) == 42)

	// the negative side is outside of the region peak, so no delta applies
	minPlan, err := f.NewVariationPlan([]Variation{{Tag: ot.MustNewTag("wght"), Value: 100}})
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, minPlan.Apply(face))
	tu.Assert(t, store.GetDelta(index, face.Coords()) == 0)
}

func TestVariationPlanSharedTagAxes(t *testing.T) {
	// This in-house harfbuzz fixture declares three axes under the same
	// '0001' tag, which the OpenType spec allows.
	b, err := hb.Files.ReadFile("harfbuzz_reference/in-house/fonts/e8691822f6a705e3e9fb48a0405c645b1a036590.ttf")
	tu.AssertNoErr(t, err)
	ld, err := ot.NewLoader(bytes.NewReader(b))
	tu.AssertNoErr(t, err)
	f, err := NewFont(ld)
	tu.AssertNoErr(t, err)

	axes := f.Axes()
	tu.Assert(t, len(axes) == 3)
	for _, a := range axes {
		tu.Assert(t, a.Tag == ot.MustNewTag("0001"))
	}

	// the strict plan API rejects a duplicated tag in the input
	_, err = f.NewVariationPlan([]Variation{
		{Tag: ot.MustNewTag("0001"), Value: 500},
		{Tag: ot.MustNewTag("0001"), Value: 600},
	})
	tu.Assert(t, errors.Is(err, ErrDuplicateAxis))

	// the lenient SetVariations path keeps its historical behavior and
	// drives every axis sharing the tag
	face := NewFace(f)
	face.SetVariations([]Variation{{Tag: ot.MustNewTag("0001"), Value: 500}})
	coords := face.Coords()
	tu.Assert(t, len(coords) == 3)
	for _, c := range coords {
		tu.Assert(t, c == 8192) // 500 on a 0..0..1000 axis normalizes to 0.5
	}
}

func TestVariationPlanFromDesign(t *testing.T) {
	f := loadFont(t, "toys/GVAR-no-HVAR.ttf")

	// design coordinates are aligned on the axes and clamped in place
	plan, err := f.NewVariationPlanFromDesign([]float32{1000, 50})
	tu.AssertNoErr(t, err)
	tu.Assert(t, plan.DesignCoords()[0] == 900 && plan.DesignCoords()[1] == 70)
	tu.Assert(t, len(plan.ClampedAxes()) == 2)
	tu.Assert(t, plan.NormalizedCoords()[0] == tables.NewCoord(1))
	tu.Assert(t, plan.NormalizedCoords()[1] == tables.NewCoord(-1))

	// the caller slice is not retained
	input := []float32{600, 100}
	plan, err = f.NewVariationPlanFromDesign(input)
	tu.AssertNoErr(t, err)
	input[0] = 1
	tu.Assert(t, plan.DesignCoords()[0] == 600)

	// wrong coordinate count
	_, err = f.NewVariationPlanFromDesign([]float32{600})
	tu.Assert(t, errors.Is(err, ErrCoordinateCount))

	// non finite values are rejected
	_, err = f.NewVariationPlanFromDesign([]float32{float32(math.NaN()), 100})
	tu.Assert(t, errors.Is(err, ErrInvalidCoordinate))

	// non variable font
	plain := loadFont(t, "common/DejaVuSans.ttf")
	_, err = plain.NewVariationPlanFromDesign([]float32{0})
	tu.Assert(t, errors.Is(err, ErrNotVariable))

	// shared-tag font: each axis can now be addressed individually
	b, err := hb.Files.ReadFile("harfbuzz_reference/in-house/fonts/e8691822f6a705e3e9fb48a0405c645b1a036590.ttf")
	tu.AssertNoErr(t, err)
	ld, err := ot.NewLoader(bytes.NewReader(b))
	tu.AssertNoErr(t, err)
	shared, err := NewFont(ld)
	tu.AssertNoErr(t, err)
	sharedPlan, err := shared.NewVariationPlanFromDesign([]float32{0, 500, 1000})
	tu.AssertNoErr(t, err)
	tu.Assert(t, reflect.DeepEqual(sharedPlan.NormalizedCoords(), []VarCoord{0, 8192, 16384}))
}
