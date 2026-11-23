// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package harfbuzz

import (
	"reflect"
	"testing"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	tu "github.com/go-text/typesetting/testutils"
)

// TestGSUBGPOSIndependentSelectors verifies that GSUB and GPOS resolve their
// own FeatureVariations table independently: Commissioner-VF carries a GSUB
// feature variation but no GPOS one.
func TestGSUBGPOSIndependentSelectors(t *testing.T) {
	ft := openFontFileTT(t, "common/Commissioner-VF.ttf")

	heavy := []font.VarCoord{tables.NewCoord(0.8)}
	light := []font.VarCoord{tables.NewCoord(0.4)}

	gsubHeavy := ft.GSUB.FeatureVariationSelector(heavy)
	gposHeavy := ft.GPOS.FeatureVariationSelector(heavy)

	tu.Assert(t, gsubHeavy.Matched())
	tu.Assert(t, !gposHeavy.Matched()) // GPOS has no FeatureVariations

	rvrn := ot.MustNewTag("rvrn")
	view, ok := gsubHeavy.FindFeature(rvrn)
	tu.Assert(t, ok)
	tu.Assert(t, view.Alternate && reflect.DeepEqual(view.Lookups, []uint16{83}))

	// at light weights the GSUB record does not match: default feature is used
	gsubLight := ft.GSUB.FeatureVariationSelector(light)
	tu.Assert(t, !gsubLight.Matched())
	view, ok = gsubLight.FindFeature(rvrn)
	tu.Assert(t, ok)
	tu.Assert(t, !view.Alternate && len(view.Lookups) == 0)

	// the shared layout is not mutated by either selector
	tu.Assert(t, len(ft.GSUB.Features[view.FeatureIndex].LookupListIndices) == 0)
}

// TestGetFeatureLookupsFallback locks in the OpenType fallback rule: when the
// matched variation record does not substitute a feature, its default lookup
// list must be used instead of an empty list.
func TestGetFeatureLookupsFallback(t *testing.T) {
	ft := openFontFileTT(t, "common/Commissioner-VF.ttf")

	// pick a feature that exists in the GSUB but is not substituted by the
	// (single) variation record, for example 'liga' when present.
	liga := ot.MustNewTag("liga")
	featureIndex, found := ft.GSUB.FindFeatureIndex(liga)
	if !found {
		// the fixture font may change; use the first non-substituted feature
		for i, f := range ft.GSUB.Features {
			if f.Tag != ot.MustNewTag("rvrn") {
				featureIndex, liga, found = uint16(i), f.Tag, true
				break
			}
		}
	}
	tu.Assert(t, found)
	defaultLookups := ft.GSUB.Features[featureIndex].LookupListIndices

	got := getFeatureLookupsWithVar(&ft.GSUB.Layout, featureIndex, 0)
	tu.Assert(t, reflect.DeepEqual(got, defaultLookups))

	// out of range variation index also falls back
	got = getFeatureLookupsWithVar(&ft.GSUB.Layout, featureIndex, 99)
	tu.Assert(t, reflect.DeepEqual(got, defaultLookups))

	// no variation: same default lookups
	got = getFeatureLookupsWithVar(&ft.GSUB.Layout, featureIndex, noVariationsIndex)
	tu.Assert(t, reflect.DeepEqual(got, defaultLookups))

	// the substituted feature returns the alternate lookup list
	rvrnIndex, ok := ft.GSUB.FindFeatureIndex(ot.MustNewTag("rvrn"))
	tu.Assert(t, ok)
	got = getFeatureLookupsWithVar(&ft.GSUB.Layout, rvrnIndex, 0)
	tu.Assert(t, reflect.DeepEqual(got, []uint16{83}))
}
