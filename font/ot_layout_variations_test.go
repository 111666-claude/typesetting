// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	tu "github.com/go-text/typesetting/testutils"
)

var (
	tagRvrn = ot.MustNewTag("rvrn")
	tagLiga = ot.MustNewTag("liga")
	tagMiss = ot.MustNewTag("zzzz")
)

// defaultLanguageIndex selects the default language system of a script.
const defaultLanguageIndex = 0xFFFF

// parseFixtureLayout parses a hand-built layout blob through the regular
// production parser and wraps it in the high level Layout.
func parseFixtureLayout(t *testing.T, blob []byte) Layout {
	t.Helper()
	table, _, err := tables.ParseLayout(blob)
	tu.AssertNoErr(t, err)
	return newLayout(table)
}

// twoAxisConfig builds the canonical fixture used by most tests:
// two axes (wght=0, wdth=1), default features rvrn -> [0] and liga -> [1],
// and one variation record whose condition set constrains both axes and
// substitutes rvrn with lookup [2] and liga with lookup [3].
func twoAxisConfig() fvFixtureConfig {
	return fvFixtureConfig{
		scriptTag:      ot.MustNewTag("DFLT"),
		featureTags:    []ot.Tag{tagRvrn, tagLiga},
		defaultLookups: [][]uint16{{0}, {1}},
		conditions: [][]byte{
			conditionBlob(1, 0, tables.NewCoord(0.25), tables.NewCoord(0.75)), // wght in [0.25, 0.75]
			conditionBlob(1, 1, tables.NewCoord(-0.5), tables.NewCoord(0.5)),  // wdth in [-0.5, 0.5]
		},
		substitutions: []fvSubstitution{
			{featureIndex: 0, lookups: []uint16{2}},
			{featureIndex: 1, lookups: []uint16{3}},
		},
	}
}

func TestFeatureVariationSelectorTwoAxes(t *testing.T) {
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(twoAxisConfig()))

	type coordCase struct {
		coords  []VarCoord
		matched bool
	}
	cases := []coordCase{
		{[]VarCoord{tables.NewCoord(0.5), tables.NewCoord(0)}, true},     // both inside
		{[]VarCoord{tables.NewCoord(0.25), tables.NewCoord(-0.5)}, true}, // inclusive boundaries
		{[]VarCoord{tables.NewCoord(0.75), tables.NewCoord(0.5)}, true},  // inclusive boundaries
		{[]VarCoord{tables.NewCoord(0.2), tables.NewCoord(0)}, false},    // wght below min
		{[]VarCoord{tables.NewCoord(0.8), tables.NewCoord(0)}, false},    // wght above max
		{[]VarCoord{tables.NewCoord(0.5), tables.NewCoord(0.6)}, false},  // wdth above max
		{[]VarCoord{tables.NewCoord(0.5), tables.NewCoord(-0.6)}, false}, // wdth below min
		{nil, false}, // default coords: axis read as 0 which is inside... actually both ranges contain 0 for wdth but wght 0 < 0.25
	}
	for _, tc := range cases {
		selector := layout.FeatureVariationSelector(tc.coords)
		if selector.Matched() != tc.matched {
			t.Fatalf("coords %v: matched=%v want %v", tc.coords, selector.Matched(), tc.matched)
		}

		rvrn, ok := selector.FindFeature(tagRvrn)
		tu.Assert(t, ok)
		liga, ok := selector.FindFeature(tagLiga)
		tu.Assert(t, ok)

		if tc.matched {
			tu.Assert(t, selector.RecordIndex() == 0)
			tu.Assert(t, rvrn.Alternate && reflect.DeepEqual(rvrn.Lookups, []uint16{2}))
			tu.Assert(t, liga.Alternate && reflect.DeepEqual(liga.Lookups, []uint16{3}))
		} else {
			tu.Assert(t, selector.RecordIndex() == -1)
			tu.Assert(t, !rvrn.Alternate && reflect.DeepEqual(rvrn.Lookups, []uint16{0}))
			tu.Assert(t, !liga.Alternate && reflect.DeepEqual(liga.Lookups, []uint16{1}))
		}
	}
}

func TestFeatureVariationBoundaryCoords(t *testing.T) {
	// Exact fixed 2.14 boundary values, including negative coordinates.
	cfg := twoAxisConfig()
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))
	minWght := tables.Coord(tables.NewCoord(0.25))
	maxWdth := tables.Coord(tables.NewCoord(0.5))
	selector := layout.FeatureVariationSelector([]VarCoord{minWght, maxWdth})
	tu.Assert(t, selector.Matched())

	// one step (1/16384) outside the interval must not match
	selector = layout.FeatureVariationSelector([]VarCoord{minWght - 1, maxWdth})
	tu.Assert(t, !selector.Matched())
	selector = layout.FeatureVariationSelector([]VarCoord{minWght, maxWdth + 1})
	tu.Assert(t, !selector.Matched())
}

func TestFeatureVariationMissingFeatureAndDefaultFallback(t *testing.T) {
	cfg := twoAxisConfig()
	// only rvrn is substituted: liga must keep its default lookups even when
	// the variation record matches.
	cfg.substitutions = []fvSubstitution{{featureIndex: 0, lookups: []uint16{2}}}
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))

	selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5), 0})
	tu.Assert(t, selector.Matched())

	rvrn, ok := selector.FindFeature(tagRvrn)
	tu.Assert(t, ok)
	tu.Assert(t, rvrn.Alternate && reflect.DeepEqual(rvrn.Lookups, []uint16{2}))

	liga, ok := selector.FindFeature(tagLiga)
	tu.Assert(t, ok)
	tu.Assert(t, !liga.Alternate && reflect.DeepEqual(liga.Lookups, []uint16{1}))

	// unknown tag
	_, ok = selector.FindFeature(tagMiss)
	tu.Assert(t, !ok)

	// no match at all: everything falls back to defaults
	selector = layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(-1), 0})
	tu.Assert(t, !selector.Matched())
	rvrn, _ = selector.FindFeature(tagRvrn)
	tu.Assert(t, !rvrn.Alternate && reflect.DeepEqual(rvrn.Lookups, []uint16{0}))
}

func TestFeatureVariationScriptLanguage(t *testing.T) {
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(twoAxisConfig()))
	scriptIndex := layout.FindScript(ot.MustNewTag("DFLT"))
	tu.Assert(t, scriptIndex == 0)

	selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5), 0})
	view, ok := selector.FindFeatureForLang(scriptIndex, defaultLanguageIndex, tagRvrn)
	tu.Assert(t, ok)
	tu.Assert(t, view.Alternate && view.Tag == tagRvrn)

	// invalid script index
	_, ok = selector.FindFeatureForLang(42, defaultLanguageIndex, tagRvrn)
	tu.Assert(t, !ok)
	// feature not referenced by the language system
	_, ok = selector.FindFeatureForLang(scriptIndex, defaultLanguageIndex, tagMiss)
	tu.Assert(t, !ok)
}

func TestFeatureVariationFormat2AndNullCondition(t *testing.T) {
	cfg := fvFixtureConfig{
		scriptTag:      ot.MustNewTag("DFLT"),
		featureTags:    []ot.Tag{tagRvrn},
		defaultLookups: [][]uint16{{0}},
		// format 2 axis value condition on axis 0 requiring exactly 0.5
		// (no axis value table), plus a null condition which always applies.
		conditions: [][]byte{
			conditionBlob(2, 0, tables.NewCoord(0.5), 0),
			nil,
		},
		substitutions: []fvSubstitution{{featureIndex: 0, lookups: []uint16{2}}},
	}
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))

	selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5)})
	tu.Assert(t, selector.Matched())
	view, _ := selector.FindFeature(tagRvrn)
	tu.Assert(t, view.Alternate)

	selector = layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.49)})
	tu.Assert(t, !selector.Matched())
}

func TestFeatureVariationNullConditionSet(t *testing.T) {
	cfg := twoAxisConfig()
	cfg.nullConditionSet = true
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))

	// a null condition set matches every coordinate set
	for _, coords := range [][]VarCoord{nil, {tables.NewCoord(1)}, {tables.NewCoord(-1), tables.NewCoord(1)}} {
		selector := layout.FeatureVariationSelector(coords)
		if !selector.Matched() {
			t.Fatalf("null condition set should match coords %v", coords)
		}
	}
}

func TestFeatureVariationLastRecordWins(t *testing.T) {
	// two substitution records targeting the same feature: the last one wins,
	// per the OpenType FeatureTableSubstitution semantics.
	cfg := twoAxisConfig()
	cfg.conditions = [][]byte{conditionBlob(1, 0, tables.NewCoord(0), tables.NewCoord(1))}
	cfg.substitutions = []fvSubstitution{
		{featureIndex: 0, lookups: []uint16{2}},
		{featureIndex: 0, lookups: []uint16{3}},
	}
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))
	selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5), 0})
	view, ok := selector.FindFeature(tagRvrn)
	tu.Assert(t, ok)
	tu.Assert(t, view.Alternate && reflect.DeepEqual(view.Lookups, []uint16{3}))
}

func TestFeatureVariationStableSnapshotAndCoordChange(t *testing.T) {
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(twoAxisConfig()))

	coords := []VarCoord{tables.NewCoord(0.5), 0}
	selector := layout.FeatureVariationSelector(coords)
	view, _ := selector.FindFeature(tagRvrn)
	tu.Assert(t, view.Alternate)

	// mutating the caller coordinate slice after construction must not change
	// the selector (coordinates are copied)
	coords[0] = tables.NewCoord(-1)
	tu.Assert(t, selector.Matched())
	view, _ = selector.FindFeature(tagRvrn)
	tu.Assert(t, view.Alternate)

	// a snapshot stays stable; a new selector reflects the new coordinates
	selector2 := layout.FeatureVariationSelector(coords)
	tu.Assert(t, !selector2.Matched())
	view2, _ := selector2.FindFeature(tagRvrn)
	tu.Assert(t, !view2.Alternate)
}

func TestFeatureVariationMultipleFontInstances(t *testing.T) {
	// one shared layout, two independent selectors representing two font
	// instances at different coordinates: each selector keeps its own view.
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(twoAxisConfig()))

	instanceA := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5), 0})
	instanceB := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(-1), 0})

	viewA, _ := instanceA.FindFeature(tagRvrn)
	viewB, _ := instanceB.FindFeature(tagRvrn)
	tu.Assert(t, viewA.Alternate && !viewB.Alternate)
	tu.Assert(t, reflect.DeepEqual(viewA.Lookups, []uint16{2}))
	tu.Assert(t, reflect.DeepEqual(viewB.Lookups, []uint16{0}))
}

func TestFeatureVariationConcurrentReadOnly(t *testing.T) {
	layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(twoAxisConfig()))

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			coords := []VarCoord{tables.NewCoord(float64(i%2) * 0.5), 0}
			selector := layout.FeatureVariationSelector(coords)
			for range 100 {
				view, ok := selector.FindFeature(tagRvrn)
				if !ok {
					t.Error("feature lost under concurrent read")
					return
				}
				if selector.Matched() != view.Alternate {
					t.Error("inconsistent selector state")
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// the shared layout must be unchanged after concurrent access
	defaultView, ok := layout.FeatureVariationSelector(nil).FindFeature(tagRvrn)
	tu.Assert(t, ok)
	tu.Assert(t, !defaultView.Alternate && reflect.DeepEqual(defaultView.Lookups, []uint16{0}))
}

// TestFeatureVariationGSUBAndGPOS builds two independent layout tables
// (one GSUB-like, one GPOS-like) sharing the same feature tags but with
// different substitutions, and verifies each table is queried separately.
func TestFeatureVariationGSUBAndGPOS(t *testing.T) {
	gsubCfg := twoAxisConfig()
	gsubCfg.substitutions = []fvSubstitution{
		{featureIndex: 0, lookups: []uint16{2}},
	}
	gposCfg := twoAxisConfig()
	gposCfg.substitutions = []fvSubstitution{
		{featureIndex: 0, lookups: []uint16{3}},
	}
	gsub := parseFixtureLayout(t, buildLayoutWithFeatureVariations(gsubCfg))
	gpos := parseFixtureLayout(t, buildLayoutWithFeatureVariations(gposCfg))

	coords := []VarCoord{tables.NewCoord(0.5), 0}
	gsubView, ok := gsub.FeatureVariationSelector(coords).FindFeature(tagRvrn)
	tu.Assert(t, ok)
	gposView, ok := gpos.FeatureVariationSelector(coords).FindFeature(tagRvrn)
	tu.Assert(t, ok)

	tu.Assert(t, reflect.DeepEqual(gsubView.Lookups, []uint16{2}))
	tu.Assert(t, reflect.DeepEqual(gposView.Lookups, []uint16{3}))

	// a coordinate set matching neither leaves both on their defaults
	off := gsub.FeatureVariationSelector([]VarCoord{tables.NewCoord(-1), 0})
	tu.Assert(t, !off.Matched())
	gsubView, _ = off.FindFeature(tagRvrn)
	tu.Assert(t, reflect.DeepEqual(gsubView.Lookups, []uint16{0}))
}

func TestFeatureVariationBadTableFixtures(t *testing.T) {
	t.Run("bad condition entry", func(t *testing.T) {
		cfg := twoAxisConfig()
		// a condition offset pointing past the table: the parser keeps an
		// invalid condition which never matches, instead of rejecting the font.
		cfg.conditions = [][]byte{{0, 1, 0, 0}} // truncated 4-byte condition body
		layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))
		selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5), 0})
		tu.Assert(t, !selector.Matched())
		// default feature still usable
		view, ok := selector.FindFeature(tagRvrn)
		tu.Assert(t, ok && !view.Alternate)
	})

	t.Run("unknown condition format", func(t *testing.T) {
		cfg := twoAxisConfig()
		cfg.conditions = [][]byte{
			conditionBlob(99, 0, tables.NewCoord(0), tables.NewCoord(1)),
		}
		layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))
		selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5)})
		tu.Assert(t, !selector.Matched())
	})

	t.Run("illegal substitution feature index", func(t *testing.T) {
		cfg := twoAxisConfig()
		cfg.badFeatureIndex = true
		layout := parseFixtureLayout(t, buildLayoutWithFeatureVariations(cfg))
		// the table still parses; validation surfaces the contextual error
		selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5), 0})
		err := selector.validate(4) // the fixture layout exposes 4 lookups
		tu.Assert(t, errors.Is(err, errInvalidFeatureIndex))
		// the bad record does not substitute any real feature: defaults hold
		rvrn, ok := selector.FindFeature(tagRvrn)
		tu.Assert(t, ok && !rvrn.Alternate)
	})

	t.Run("oversized feature variation count", func(t *testing.T) {
		cfg := twoAxisConfig()
		cfg.oversizedRecordCount = true
		// The declared count is clamped to what the blob can hold, so the
		// parser never allocates a huge slice. The clamped loop then reaches
		// the truncated end and returns a contextual error instead of
		// panicking; a font loader drops the table and keeps going.
		_, _, err := tables.ParseLayout(buildLayoutWithFeatureVariations(cfg))
		tu.Assert(t, err != nil)
	})

	t.Run("oversized substitution count", func(t *testing.T) {
		cfg := twoAxisConfig()
		cfg.oversizedSubstitutionCount = true
		// Same contract for the feature table substitution count.
		_, _, err := tables.ParseLayout(buildLayoutWithFeatureVariations(cfg))
		tu.Assert(t, err != nil)
	})

	t.Run("circular condition offset", func(t *testing.T) {
		// A condition offset pointing back inside the condition set itself:
		// conditions are parsed inline (never recursively), so there is no
		// infinite recursion, and the resulting malformed condition does not
		// match the coordinates.
		blob := buildLayoutWithFeatureVariations(twoAxisConfig())
		patched := patchFirstConditionOffset(blob, 2)
		table, _, err := tables.ParseLayout(patched)
		tu.AssertNoErr(t, err)
		layout := newLayout(table)
		selector := layout.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.5), 0})
		// must terminate and safely report a non-matching record
		tu.Assert(t, !selector.Matched())
		// default features stay available
		rvrn, ok := selector.FindFeature(tagRvrn)
		tu.Assert(t, ok && !rvrn.Alternate)
	})

	t.Run("truncated condition set blob", func(t *testing.T) {
		blob := buildLayoutWithFeatureVariations(twoAxisConfig())
		// truncate the last bytes (condition bodies live at the end of the FV)
		_, _, err := tables.ParseLayout(blob[:len(blob)-4])
		// parsing must either fail cleanly or survive with invalid conditions,
		// but never panic
		_ = err
	})
}

// TestFeatureVariationRealFont cross-checks the selector against a real
// variable font (Commissioner-VF), whose GSUB substitutes the rvrn feature
// above normalized wght 0.5789.
func TestFeatureVariationRealFont(t *testing.T) {
	ft := readFontFile(t, "common/Commissioner-VF.ttf")
	gsubT, _, err := tables.ParseLayout(readTable(t, ft, "GSUB"))
	tu.AssertNoErr(t, err)
	gsub := newLayout(gsubT)

	tu.Assert(t, gsub.FindVariationIndex([]VarCoord{tables.NewCoord(0.8)}) == 0)
	tu.Assert(t, gsub.FindVariationIndex([]VarCoord{tables.NewCoord(0.4)}) == -1)

	// inside the range: rvrn is substituted by lookup 83
	selector := gsub.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.8)})
	tu.Assert(t, selector.Matched())
	view, ok := selector.FindFeature(tagRvrn)
	tu.Assert(t, ok)
	tu.Assert(t, view.Alternate && reflect.DeepEqual(view.Lookups, []uint16{83}))

	// outside the range: the default (empty lookup list) rvrn is used
	selector = gsub.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.4)})
	tu.Assert(t, !selector.Matched())
	view, ok = selector.FindFeature(tagRvrn)
	tu.Assert(t, ok)
	tu.Assert(t, !view.Alternate && len(view.Lookups) == 0)

	// GPOS has no FeatureVariations: its selector always falls back to defaults
	gposT, _, err := tables.ParseLayout(readTable(t, ft, "GPOS"))
	tu.AssertNoErr(t, err)
	gpos := newLayout(gposT)
	gposSelector := gpos.FeatureVariationSelector([]VarCoord{tables.NewCoord(0.8)})
	tu.Assert(t, !gposSelector.Matched())
}
