// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"errors"
)

// Errors returned by the feature variation selector. They are always wrapped
// with contextual information and never mutate the shared [Layout] tables.
var (
	// errInvalidFeatureIndex is returned when a feature table substitution
	// record points at a feature index outside the layout feature list.
	errInvalidFeatureIndex = errors.New("invalid feature index in feature table substitution")
	// errInvalidLookupIndex is returned when an alternate feature references
	// a lookup outside the layout lookup list.
	errInvalidLookupIndex = errors.New("invalid lookup index in alternate feature")
)

// FeatureView is a stable, read-only view of one layout feature (identified by
// its tag) once the feature variations matching the current normalized
// coordinates have been resolved.
//
// When the selected feature variation record substitutes the feature,
// Alternate is true and Lookups holds the alternate feature lookup indices;
// otherwise the default feature of the layout is exposed and Alternate is false.
//
// FeatureView values are snapshots: keeping them and changing the font
// coordinates afterwards does not alter the view. Recompute a view from the
// [FeatureVariationSelector] after a coordinate change.
type FeatureView struct {
	// Tag is the requested feature tag.
	Tag Tag
	// FeatureIndex is the index of the feature in the layout feature list.
	FeatureIndex uint16
	// Lookups are the lookup list indices resolved for this feature.
	Lookups []uint16
	// Alternate is true when Lookups come from a feature table substitution
	// record rather than from the default feature list.
	Alternate bool
}

// FeatureVariationSelector resolves the feature variations of one GSUB or
// GPOS layout table for a set of normalized variation coordinates.
//
// The selector is a read-only, composable view over the underlying [Layout]:
// it never modifies the shared layout tables and a single [Layout] may be
// queried concurrently through several selectors (for example one per font
// instance, each with its own coordinates).
//
// A zero value is not usable; build one with [Layout.FeatureVariationSelector].
type FeatureVariationSelector struct {
	layout *Layout
	coords []VarCoord
	// record is the index of the first matching feature variation record,
	// or -1 when no record matches (the default feature list is used).
	record int
}

// FeatureVariationSelector returns a selector resolving the feature
// variation records of the layout against the given normalized coordinates
// (as returned by [Font.NormalizeVariations]). The coordinates are copied, so
// later changes to the caller slice do not affect the selector.
//
// GSUB and GPOS expose independent selectors (each layout table owns its own
// FeatureVariations table), so the substitution and positioning views can be
// queried separately.
func (la *Layout) FeatureVariationSelector(coords []VarCoord) *FeatureVariationSelector {
	return &FeatureVariationSelector{
		layout: la,
		coords: append([]VarCoord(nil), coords...),
		record: la.findVariationIndex(coords),
	}
}

// findVariationIndex returns the index of the first feature variation record
// whose condition set matches the coordinates, or -1 when none matches.
// Records are evaluated in file order, as mandated by the OpenType spec.
func (la *Layout) findVariationIndex(coords []VarCoord) int {
	for i, record := range la.FeatureVariations {
		if record.ConditionSet.Eval(coords) {
			return i
		}
	}
	return -1
}

// Matched reports whether the selector coordinates match at least one
// feature variation record. When false, every resolved view falls back to
// the default feature list.
func (s *FeatureVariationSelector) Matched() bool { return s.record != -1 }

// RecordIndex returns the index of the matched feature variation record in
// the layout FeatureVariations slice, or -1 when no record matches.
func (s *FeatureVariationSelector) RecordIndex() int { return s.record }

// FindFeature resolves the feature identified by featureTag, applying the
// feature table substitution of the matched variation record when relevant.
//
// Resolution follows the OpenType rules:
//   - the first matching variation record wins, records being scanned in
//     file order;
//   - a substitution record targeting featureTag replaces the default
//     feature for this view;
//   - if the matched record does not substitute the feature, or if no record
//     matches the coordinates, the default feature of the layout is used;
//   - when the tag is absent from the layout, ok is false.
func (s *FeatureVariationSelector) FindFeature(featureTag Tag) (FeatureView, bool) {
	featureIndex, ok := s.layout.FindFeatureIndex(featureTag)
	if !ok {
		return FeatureView{}, false
	}
	return s.viewForIndex(featureIndex, featureTag), true
}

// FindFeatureForLang resolves a feature as selected for the given script and
// language, then applies the matched feature variation substitution.
//
// languageIndex is an index into the language systems of the script; pass
// 0xFFFF to select its default language system. The returned view carries the
// feature tag actually referenced by the language system.
//
// ok is false when the script index is invalid or the language system does
// not reference any feature with featureTag.
func (s *FeatureVariationSelector) FindFeatureForLang(scriptIndex, languageIndex int, featureTag Tag) (FeatureView, bool) {
	if scriptIndex < 0 || scriptIndex >= len(s.layout.Scripts) {
		return FeatureView{}, false
	}
	langSys := s.layout.Scripts[scriptIndex].GetLangSys(uint16(languageIndex))
	for _, featureIndex := range langSys.FeatureIndices {
		if int(featureIndex) >= len(s.layout.Features) {
			continue // illegal index in the language system: skip safely
		}
		if s.layout.Features[featureIndex].Tag != featureTag {
			continue
		}
		return s.viewForIndex(featureIndex, featureTag), true
	}
	return FeatureView{}, false
}

// viewForIndex builds the resolved view for a validated feature index,
// applying the substitution of the matched variation record when present and
// falling back to the default feature otherwise.
func (s *FeatureVariationSelector) viewForIndex(featureIndex uint16, tag Tag) FeatureView {
	view := FeatureView{
		Tag:          tag,
		FeatureIndex: featureIndex,
		Lookups:      s.layout.Features[featureIndex].LookupListIndices,
	}
	if s.record == -1 {
		return view
	}
	substitutions := s.layout.FeatureVariations[s.record].Substitutions.Substitutions
	// Substitution records are stored in file order; the spec states that the
	// last record with a given FeatureIndex wins, so scan in order and let a
	// later record override an earlier one.
	for _, sub := range substitutions {
		if sub.FeatureIndex != featureIndex {
			continue
		}
		view.Alternate = true
		view.Lookups = sub.AlternateFeature.LookupListIndices
	}
	return view
}

// validate checks every feature and lookup index reachable through the
// matched variation record. It returns the first contextual error found and
// never modifies the shared layout. It is meant for tests and for callers
// that want to reject malformed fonts up front.
func (s *FeatureVariationSelector) validate(lookupCount int) error {
	featureCount := len(s.layout.Features)
	if s.record == -1 {
		return nil
	}
	for _, sub := range s.layout.FeatureVariations[s.record].Substitutions.Substitutions {
		if int(sub.FeatureIndex) >= featureCount {
			return errInvalidFeatureIndex
		}
		for _, lookupIndex := range sub.AlternateFeature.LookupListIndices {
			if int(lookupIndex) >= lookupCount {
				return errInvalidLookupIndex
			}
		}
	}
	return nil
}

// FindVariationIndex returns the first feature variation matching the
// specified variation coordinates, as an index in the FeatureVariations
// field. It returns -1 if not found (or if the layout has no feature
// variations).
//
// New code should prefer [Layout.FeatureVariationSelector], which returns a
// composable selector able to resolve substituted feature views.
func (la *Layout) FindVariationIndex(coords []VarCoord) int {
	return la.findVariationIndex(coords)
}
