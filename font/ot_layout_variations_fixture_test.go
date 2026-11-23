// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"encoding/binary"

	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
)

// This file implements a small, self-contained builder of GSUB/GPOS layout
// table blobs containing FeatureVariations. It is the independent parser
// counterpart of the production code: fixtures are assembled field by field
// from raw bytes rather than round-tripped through the tables package, so a
// parser bug cannot make a test pass against itself.

// blobWriter accumulates big-endian bytes and records placeholder positions
// to be patched with offsets once the final table layout is known.
type blobWriter struct {
	data []byte
}

func (b *blobWriter) u16(v uint16) {
	b.data = binary.BigEndian.AppendUint16(b.data, v)
}

func (b *blobWriter) u32(v uint32) {
	b.data = binary.BigEndian.AppendUint32(b.data, v)
}

func (b *blobWriter) tag(v ot.Tag) {
	b.u32(uint32(v))
}

// place returns the current position, to be filled later with [patch32].
func (b *blobWriter) place32() int {
	pos := len(b.data)
	b.u32(0)
	return pos
}

func (b *blobWriter) patch32(pos int, offset uint32) {
	binary.BigEndian.PutUint32(b.data[pos:], offset)
}

func (b *blobWriter) len() int { return len(b.data) }

// featureBlob builds a Feature table (featureParams=0, lookup indices).
func featureBlob(lookups ...uint16) []byte {
	var w blobWriter
	w.u16(0) // featureParamsOffset
	w.u16(uint16(len(lookups)))
	for _, lk := range lookups {
		w.u16(lk)
	}
	return w.data
}

// lookupBlob builds a minimal (empty) Lookup table of the given type
// (1 = GSUB single substitution, 1 = GPOS single positioning are enough for
// index bookkeeping tests).
func lookupBlob(kind uint16) []byte {
	var w blobWriter
	w.u16(kind)
	w.u16(0) // lookup flag
	w.u16(0) // subtable count
	return w.data
}

// conditionBlob builds a format 1 (interval) or format 2 (axis value)
// condition.
func conditionBlob(format, axisIndex uint16, min, max tables.Coord) []byte {
	var w blobWriter
	w.u16(format)
	w.u16(axisIndex)
	if format == 2 {
		// axis value condition: DefaultValue followed by a null
		// VarIndexTable offset (0), so evaluation compares against DefaultValue
		w.u16(uint16(min))
		w.u16(0)
	} else {
		w.u16(uint16(min))
		w.u16(uint16(max))
	}
	return w.data
}

// fvFixtureConfig drives buildLayoutWithFeatureVariations.
type fvFixtureConfig struct {
	// defaultLookups[i] are the lookup indices of the default feature i.
	defaultLookups [][]uint16
	// featureTags[i] is the tag of default feature i.
	featureTags []ot.Tag
	// scriptTag selects the single script (default lang sys references every
	// default feature).
	scriptTag ot.Tag
	// conditions describe the condition set, one blob per condition.
	// A nil entry produces a null condition offset (always-true condition).
	conditions [][]byte
	// nullConditionSet makes the record use a null condition set offset.
	nullConditionSet bool
	// substitutions are (featureIndex, alternate lookups) pairs, in file order.
	substitutions []fvSubstitution
	// badFeatureIndex, when true, emits an out of range FeatureIndex.
	badFeatureIndex bool
	// oversizedRecordCount makes the FeatureVariations count claim more
	// records than the blob contains.
	oversizedRecordCount bool
	// oversizedSubstitutionCount inflates the substitution count.
	oversizedSubstitutionCount bool
}

type fvSubstitution struct {
	featureIndex uint16
	lookups      []uint16
}

// buildLayoutWithFeatureVariations assembles a complete version 1.1 layout
// table (10-byte header + FeatureVariations offset) with one feature
// variation record.
func buildLayoutWithFeatureVariations(cfg fvFixtureConfig) []byte {
	featureCount := len(cfg.defaultLookups)

	// ---- LookupList (4 lookups available to both default and alternate) ----
	var lookupList blobWriter
	lookupList.u16(4)
	lookupBodyOffset := 2 + 4*2
	for range 4 {
		lookupList.u16(0) // placeholder offsets, patched below
	}
	lookupBodies := make([][]byte, 4)
	for i := range lookupBodies {
		lookupBodies[i] = lookupBlob(1)
	}

	// ---- FeatureList ----
	var featureList blobWriter
	featureList.u16(uint16(featureCount))
	featureRecordSize := 6
	featureBodyStart := 2 + featureCount*featureRecordSize
	featureBodies := make([][]byte, featureCount)
	cursor := featureBodyStart
	for i, lookups := range cfg.defaultLookups {
		featureList.tag(cfg.featureTags[i])
		featureList.u16(uint16(cursor))
		featureBodies[i] = featureBlob(lookups...)
		cursor += len(featureBodies[i])
	}
	for _, body := range featureBodies {
		featureList.data = append(featureList.data, body...)
	}

	// ---- ScriptList (one script, default lang sys, all features) ----
	var langSys blobWriter
	langSys.u16(0)                    // lookupOrderOffset
	langSys.u16(0xFFFF)               // required feature index
	langSys.u16(uint16(featureCount)) // feature index count
	for i := 0; i < featureCount; i++ {
		langSys.u16(uint16(i))
	}
	var script blobWriter
	langSysOffset := 4 // defaultLangSys offset immediately after the 4-byte script header
	script.u16(uint16(langSysOffset))
	script.u16(0) // lang sys count
	script.data = append(script.data, langSys.data...)
	var scriptList blobWriter
	scriptList.u16(1)
	scriptList.tag(cfg.scriptTag)
	scriptBodyOffset := 2 + 6
	scriptList.u16(uint16(scriptBodyOffset))
	scriptList.data = append(scriptList.data, script.data...)

	// ---- FeatureVariations ----
	fv := buildFeatureVariations(cfg)

	// ---- Layout header and patching ----
	var layout blobWriter
	layout.u16(1) // major version
	layout.u16(1) // minor version 1.1 -> FeatureVariations offset present
	slPos := layout.len()
	layout.u16(0)
	flPos := layout.len()
	layout.u16(0)
	llPos := layout.len()
	layout.u16(0)
	fvPos := layout.place32()

	scriptListOffset := uint32(layout.len())
	layout.data = append(layout.data, scriptList.data...)
	featureListOffset := uint32(layout.len())
	layout.data = append(layout.data, featureList.data...)
	lookupListOffset := uint32(layout.len())
	// patch lookup list internal offsets and append bodies
	binary.BigEndian.PutUint16(lookupList.data[2:], uint16(lookupBodyOffset))
	for i := 1; i < 4; i++ {
		binary.BigEndian.PutUint16(lookupList.data[2+i*2:], uint16(lookupBodyOffset+i*len(lookupBodies[0])))
	}
	layout.data = append(layout.data, lookupList.data...)
	for _, body := range lookupBodies {
		layout.data = append(layout.data, body...)
	}
	fvOffset := uint32(layout.len())
	layout.data = append(layout.data, fv...)

	binary.BigEndian.PutUint16(layout.data[slPos:], uint16(scriptListOffset))
	binary.BigEndian.PutUint16(layout.data[flPos:], uint16(featureListOffset))
	binary.BigEndian.PutUint16(layout.data[llPos:], uint16(lookupListOffset))
	layout.patch32(fvPos, fvOffset)

	return layout.data
}

// patchFirstConditionOffset rewrites the first condition offset of the
// condition set embedded in a layout blob, used to inject a circular
// (self-referential) offset.
func patchFirstConditionOffset(blob []byte, conditionOffset uint32) []byte {
	out := append([]byte(nil), blob...)
	fvOffset := binary.BigEndian.Uint32(out[10:])
	recordStart := int(fvOffset) + 8 // after FeatureVariations header
	conditionSetOffset := int(binary.BigEndian.Uint32(out[recordStart:]))
	// first condition offset sits 2 bytes into the ConditionSet
	pos := int(fvOffset) + conditionSetOffset + 2
	binary.BigEndian.PutUint32(out[pos:], conditionOffset)
	return out
}

// buildFeatureVariations assembles the FeatureVariations table and its
// referenced condition set and feature table substitution.
func buildFeatureVariations(cfg fvFixtureConfig) []byte {
	// ---- condition table bodies ----
	conditionBodies := make([][]byte, len(cfg.conditions))
	condOffset := make([]uint32, len(cfg.conditions))
	cursor := 2 + len(cfg.conditions)*4 // after ConditionSet header + offset array
	for i, c := range cfg.conditions {
		if c == nil {
			continue // null offset placeholder stays 0
		}
		conditionBodies[i] = c
		condOffset[i] = uint32(cursor)
		cursor += len(c)
	}
	var conditionSet blobWriter
	conditionSet.u16(uint16(len(cfg.conditions)))
	for _, off := range condOffset {
		conditionSet.u32(off)
	}
	for _, body := range conditionBodies {
		conditionSet.data = append(conditionSet.data, body...)
	}

	// ---- feature table substitution ----
	var substitution blobWriter
	substitution.u16(1) // major
	substitution.u16(0) // minor
	count := len(cfg.substitutions)
	if cfg.oversizedSubstitutionCount {
		count = 0xFFFF
	}
	substitution.u16(uint16(count))
	recordSize := 6
	bodyStart := 6 + len(cfg.substitutions)*recordSize
	bodyCursor := bodyStart
	bodies := make([][]byte, len(cfg.substitutions))
	for i, sub := range cfg.substitutions {
		idx := sub.featureIndex
		if cfg.badFeatureIndex {
			idx = 0x7FFF
		}
		substitution.u16(idx)
		substitution.u32(uint32(bodyCursor))
		bodies[i] = featureBlob(sub.lookups...)
		bodyCursor += len(bodies[i])
	}
	for _, body := range bodies {
		substitution.data = append(substitution.data, body...)
	}

	// ---- feature variation header (8 bytes) then two records-worth of data ----
	var fv blobWriter
	fv.u16(1) // major
	fv.u16(0) // minor
	recordCount := 1
	if cfg.oversizedRecordCount {
		recordCount = 0x7FFFFFFF
	}
	fv.u32(uint32(recordCount))
	recordStart := fv.len()
	fv.u32(0) // condition set offset placeholder
	fv.u32(0) // substitution offset placeholder
	// both sub-tables follow the 8-byte record; the substitution table starts
	// right after the condition set (or right after the record when the
	// condition set is null)
	condSetOffset := uint32(0)
	if !cfg.nullConditionSet {
		condSetOffset = uint32(recordStart) + 8
	}
	substitutionOffset := uint32(recordStart) + 8 + uint32(conditionSet.len())
	binary.BigEndian.PutUint32(fv.data[recordStart:], condSetOffset)
	binary.BigEndian.PutUint32(fv.data[recordStart+4:], substitutionOffset)
	fv.data = append(fv.data, conditionSet.data...)
	fv.data = append(fv.data, substitution.data...)
	return fv.data
}
