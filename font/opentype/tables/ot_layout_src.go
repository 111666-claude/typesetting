// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package tables

import (
	"encoding/binary"
	"fmt"
)

// Layout represents the common layout table used by GPOS and GSUB.
// The Features field contains all the features for this layout. However,
// the script and language determines which feature is used.
//
// See https://learn.microsoft.com/typography/opentype/spec/chapter2#organization
// See https://learn.microsoft.com/typography/opentype/spec/gpos
// See https://www.microsoft.com/typography/otspec/GSUB.htm
type Layout struct {
	majorVersion      uint16            // Major version of the GPOS table, = 1
	minorVersion      uint16            // Minor version of the GPOS table, = 0 or 1
	ScriptList        ScriptList        `offsetSize:"Offset16"` // Offset to ScriptList table, from beginning of GPOS table
	FeatureList       FeatureList       `offsetSize:"Offset16"` // Offset to FeatureList table, from beginning of GPOS table
	LookupList        lookupList        `offsetSize:"Offset16"` // Offset to LookupList table, from beginning of GPOS table
	FeatureVariations *FeatureVariation `isOpaque:""`           // Offset to FeatureVariations table, from beginning of GPOS table (may be NULL)
}

func (lt *Layout) parseFeatureVariations(src []byte) (int, error) {
	const layoutHeaderSize = 2 + 2 + 2 + 2 + 2
	if lt.minorVersion != 1 {
		return 0, nil
	}
	if L := len(src); L < layoutHeaderSize+4 {
		return 0, fmt.Errorf("reading Layout: EOF: expected length: 4, got %d", L)
	}
	offset := binary.BigEndian.Uint32(src[layoutHeaderSize:])
	if offset == 0 {
		return 4, nil
	}
	if L := len(src); L < int(offset) {
		return 0, fmt.Errorf("reading Layout: EOF: expected length: %d, got %d", offset, L)
	}
	fv, _, err := ParseFeatureVariation(src[offset:])
	if err != nil {
		return 0, err
	}
	lt.FeatureVariations = &fv

	return 4, nil
}

type TagOffsetRecord struct {
	Tag    Tag    // 4-byte script tag identifier
	Offset uint16 // Offset to object from beginning of list
}

type ScriptList struct {
	Records []TagOffsetRecord `arrayCount:"FirstUint16"` // Array of ScriptRecords, listed alphabetically by script tag
	Scripts []Script          `isOpaque:""`
}

func (sl *ScriptList) parseScripts(src []byte) error {
	sl.Scripts = make([]Script, len(sl.Records))
	for i, rec := range sl.Records {
		var err error
		if L := len(src); L < int(rec.Offset) {
			return fmt.Errorf("EOF: expected length: %d, got %d", rec.Offset, L)
		}
		sl.Scripts[i], _, err = ParseScript(src[rec.Offset:])
		if err != nil {
			return err
		}
	}
	return nil
}

type Script struct {
	DefaultLangSys *LangSys          `offsetSize:"Offset16"`    // Offset to default LangSys table, from beginning of Script table — may be NULL
	LangSysRecords []TagOffsetRecord `arrayCount:"FirstUint16"` // [langSysCount]	Array of LangSysRecords, listed alphabetically by LangSys tag
	LangSys        []LangSys         `isOpaque:""`              // same length as langSysRecords
}

func (sc *Script) parseLangSys(src []byte) error {
	sc.LangSys = make([]LangSys, len(sc.LangSysRecords))
	for i, rec := range sc.LangSysRecords {
		var err error
		if L := len(src); L < int(rec.Offset) {
			return fmt.Errorf("EOF: expected length: %d, got %d", rec.Offset, L)
		}
		sc.LangSys[i], _, err = ParseLangSys(src[rec.Offset:])
		if err != nil {
			return err
		}
	}
	return nil
}

type LangSys struct {
	lookupOrderOffset    uint16   // = NULL (reserved for an offset to a reordering table)
	RequiredFeatureIndex uint16   // Index of a feature required for this language system; if no required features = 0xFFFF
	FeatureIndices       []uint16 `arrayCount:"FirstUint16"` // [featureIndexCount]	Array of indices into the FeatureList, in arbitrary order
}

type FeatureList struct {
	Records  []TagOffsetRecord `arrayCount:"FirstUint16"` // Array of FeatureRecords — zero-based (first feature has FeatureIndex = 0), listed alphabetically by feature tag
	Features []Feature         `isOpaque:""`
}

func (fl *FeatureList) parseFeatures(src []byte) error {
	fl.Features = make([]Feature, len(fl.Records))
	for i, rec := range fl.Records {
		var err error
		if L := len(src); L < int(rec.Offset) {
			return fmt.Errorf("EOF: expected length: %d, got %d", rec.Offset, L)
		}
		fl.Features[i], _, err = ParseFeature(src[rec.Offset:])
		if err != nil {
			return err
		}
	}
	return nil
}

type Feature struct {
	featureParamsOffset uint16   // Offset from start of Feature table to FeatureParams table, if defined for the feature and present, else NULL
	LookupListIndices   []uint16 `arrayCount:"FirstUint16"` // [lookupIndexCount]	Array of indices into the LookupList — zero-based (first lookup is LookupListIndex = 0)
}

type lookupList struct {
	Lookups []Lookup `arrayCount:"FirstUint16" offsetsArray:"Offset16"` // Array of offsets to Lookup tables, from beginning of LookupList — zero based (first lookup is Lookup index = 0)
}

// Lookup is the common format for GSUB and GPOS lookups
type Lookup struct {
	lookupType       uint16     // Different enumerations for GSUB and GPOS
	LookupFlag       uint16     // Lookup qualifiers
	subtableOffsets  []Offset16 `arrayCount:"FirstUint16"` // [subTableCount] Array of offsets to lookup subtables, from beginning of Lookup table
	MarkFilteringSet uint16     // Index (base 0) into GDEF mark glyph sets structure. This field is only present if the USE_MARK_FILTERING_SET lookup flag is set.
	rawData          []byte     `subsliceStart:"AtStart" arrayCount:"ToEnd"`
}

type FeatureVariation struct {
	majorVersion            uint16                   // Major version of the FeatureVariations table — set to 1.
	minorVersion            uint16                   // Minor version of the FeatureVariations table — set to 0.
	FeatureVariationRecords []FeatureVariationRecord `arrayCount:"FirstUint32"` //[featureVariationRecordCount]	Array of feature variation records.
}

type FeatureVariationRecord struct {
	ConditionSet  ConditionSet             `offsetSize:"Offset32" offsetRelativeTo:"Parent"` // Offset to a condition set table, from beginning of FeatureVariations table.
	Substitutions FeatureTableSubstitution `offsetSize:"Offset32" offsetRelativeTo:"Parent"` // Offset to a feature table substitution table, from beginning of the FeatureVariations table.
}

// Condition is the common interface implemented by the condition formats
// used inside a [ConditionSet].
//   - [ConditionFormat1]: axis interval condition
//   - [ConditionFormat2]: axis value condition
//
// Invalid or unsupported condition tables are represented by a
// [ConditionFormat1] with [ConditionFormat1.invalid] set to true.
type Condition interface {
	// Eval returns whether the normalized coordinate of the condition axis
	// satisfies the condition.
	eval(coords []Coord) bool
	isCondition()
}

type ConditionSet struct {
	// conditionCount: number of Conditions for this condition set.
	// A null condition offset is kept as a nil entry and treated as a
	// condition that always evaluates to true, as per the OpenType spec;
	// a malformed condition is kept as an entry with [ConditionFormat1.invalid]
	// set, which never evaluates to true.
	Conditions []Condition `isOpaque:""`
}

// parseConditionSet parses a condition set from `src`, which is the bytes of
// the ConditionSet table itself.
// Malformed condition entries are kept as invalid conditions and never cause
// the whole layout table to be rejected.
func (cs *ConditionSet) parseConditionSet(src []byte) error {
	if L := len(src); L < 2 {
		return fmt.Errorf("reading ConditionSet: EOF: expected length: 2, got %d", L)
	}
	count := int(binary.BigEndian.Uint16(src[0:]))
	// guard against an oversized count declaring more entries than the table can hold
	if max := (len(src) - 2) / 4; count > max {
		count = max
	}
	cs.Conditions = make([]Condition, count)
	for i := 0; i < count; i++ {
		offset := int(binary.BigEndian.Uint32(src[2+i*4:]))
		if offset == 0 { // null offset: condition that always applies
			continue
		}
		if offset > len(src) {
			cs.Conditions[i] = ConditionFormat1{invalid: true}
			continue
		}
		cs.Conditions[i] = parseCondition(src[offset:])
	}
	return nil
}

// parseCondition parses a single condition table, returning an invalid
// [ConditionFormat1] instead of an error when the data is truncated or uses
// an unknown format.
func parseCondition(src []byte) Condition {
	if len(src) < 2 {
		return ConditionFormat1{invalid: true}
	}
	format := binary.BigEndian.Uint16(src[0:])
	switch format {
	case 1:
		if len(src) < 8 {
			return ConditionFormat1{invalid: true}
		}
		return ConditionFormat1{
			format:              1,
			AxisIndex:           binary.BigEndian.Uint16(src[2:]),
			FilterRangeMinValue: Coord(binary.BigEndian.Uint16(src[4:])),
			FilterRangeMaxValue: Coord(binary.BigEndian.Uint16(src[6:])),
		}
	case 2:
		if len(src) < 8 {
			return ConditionFormat1{invalid: true}
		}
		return ConditionFormat2{
			format:       2,
			AxisIndex:    binary.BigEndian.Uint16(src[2:]),
			DefaultValue: Coord(binary.BigEndian.Uint16(src[4:])),
			// the last two bytes are an offset (from the start of the
			// condition) to an AxisValueTable; 0 means there is none.
			VarIndexTableOffset: uint32(binary.BigEndian.Uint16(src[6:])),
		}
	default:
		return ConditionFormat1{invalid: true}
	}
}

// ConditionFormat1 defines an axis interval condition: it matches when the
// normalized coordinate of axis AxisIndex lies within the (inclusive)
// filter range.
type ConditionFormat1 struct {
	format              uint16 // Format, = 1
	invalid             bool   // true when the underlying condition table is malformed
	AxisIndex           uint16 // Index (zero-based) for the variation axis within the 'fvar' table.
	FilterRangeMinValue Coord  // Minimum value of the font variation instances that satisfy this condition.
	FilterRangeMaxValue Coord  // Maximum value of the font variation instances that satisfy this condition.
}

func (ConditionFormat1) isCondition() {}

// Eval reports whether `coords` satisfy the axis interval condition.
// An invalid condition never matches. An axis index outside the provided
// coordinates is read as 0 (the default normalized coordinate).
func (c ConditionFormat1) Eval(coords []Coord) bool { return c.eval(coords) }

func (c ConditionFormat1) eval(coords []Coord) bool {
	if c.invalid {
		return false
	}
	coord := conditionAxisCoord(c.AxisIndex, coords)
	return c.FilterRangeMinValue <= coord && coord <= c.FilterRangeMaxValue
}

// ConditionFormat2 defines an axis value condition. It matches when the
// normalized coordinate of axis AxisIndex equals the value derived from the
// default value and the referenced AxisValueTable (if any); with no axis
// value table, the condition matches when the coordinate equals DefaultValue.
type ConditionFormat2 struct {
	format              uint16 // Format, = 2
	AxisIndex           uint16 // Index (zero-based) for the variation axis within the 'fvar' table.
	DefaultValue        Coord  // Coordinate value used when no AxisValueTable is provided.
	VarIndexTableOffset uint32 // Offset (from the start of the condition) to an AxisValueTable; 0 if none.
}

func (ConditionFormat2) isCondition() {}

// Eval reports whether `coords` satisfy the axis value condition.
// AxisValueTable resolution (which requires 'fvar'/'avar' context) is not
// available at this layer, so a condition referencing one conservatively does
// not match; without an axis value table the condition matches when the axis
// coordinate equals DefaultValue.
func (c ConditionFormat2) Eval(coords []Coord) bool { return c.eval(coords) }

func (c ConditionFormat2) eval(coords []Coord) bool {
	if c.VarIndexTableOffset != 0 {
		// an axis value table cannot be resolved without the font variation
		// tables: skip the condition rather than match on wrong data
		return false
	}
	return conditionAxisCoord(c.AxisIndex, coords) == c.DefaultValue
}

// conditionAxisCoord returns the normalized coordinate for the axis,
// defaulting to 0 (the default instance) when the index is not provided.
func conditionAxisCoord(axisIndex uint16, coords []Coord) Coord {
	if int(axisIndex) < len(coords) {
		return coords[axisIndex]
	}
	return 0
}

// Eval reports whether every condition of the set is satisfied.
// A condition set without any condition always matches, and a null
// condition entry is treated as a condition that always matches.
func (cs ConditionSet) Eval(coords []Coord) bool {
	for _, c := range cs.Conditions {
		if c == nil { // null condition offset: always true
			continue
		}
		if !c.eval(coords) {
			return false
		}
	}
	return true
}

type FeatureTableSubstitution struct {
	majorVersion  uint16                           // Major version of the feature table substitution table — set to 1
	minorVersion  uint16                           // Minor version of the feature table substitution table — set to 0.
	Substitutions []FeatureTableSubstitutionRecord `arrayCount:"FirstUint16"` // [substitutionCount]	Array of feature table substitution records.
}

type FeatureTableSubstitutionRecord struct {
	FeatureIndex     uint16  //	The feature table index to match.
	AlternateFeature Feature `offsetSize:"Offset32" offsetRelativeTo:"Parent"` //	Offset to an alternate feature table, from start of the FeatureTableSubstitution table.
}
