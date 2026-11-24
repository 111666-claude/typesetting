// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"errors"
	"fmt"
	"math"

	"github.com/go-text/typesetting/font/opentype/tables"
)

// Errors returned by the variation plan API. They may be wrapped with
// additional context and tested against with [errors.Is].
var (
	// ErrNotVariable is returned when the font has no valid 'fvar' table.
	ErrNotVariable = errors.New("font has no 'fvar' table: not a variable font")

	// ErrUnknownAxis is returned when a variation references a tag not
	// declared in the font 'fvar' table.
	ErrUnknownAxis = errors.New("unknown variation axis tag")

	// ErrDuplicateAxis is returned when the same axis is set more than
	// once in the same variation list.
	ErrDuplicateAxis = errors.New("duplicate variation axis")

	// ErrInvalidCoordinate is returned when a variation value is NaN or Inf.
	ErrInvalidCoordinate = errors.New("invalid variation coordinate")

	// ErrTooManyVariations is returned when more variations are provided
	// than the number of axes declared in 'fvar'.
	ErrTooManyVariations = errors.New("more variations than variation axes")

	// ErrInstanceNotFound is returned when a named instance index is out
	// of the range of the 'fvar' named instances.
	ErrInstanceNotFound = errors.New("named variation instance not found")

	// ErrAxisMismatch is returned when a plan built for one axis layout
	// is applied to a face exposing a different axis layout.
	ErrAxisMismatch = errors.New("variation plan axis layout does not match the face")

	// ErrCoordinateCount is returned when design-space coordinates do not
	// have exactly one value per font axis.
	ErrCoordinateCount = errors.New("wrong number of variation coordinates")
)

// Axis describes one variation axis declared in the 'fvar' table,
// with its bounds expressed in design-space (user-space) units.
type Axis struct {
	Tag                       Tag
	Minimum, Default, Maximum float32
}

// NamedInstance is one of the named coordinate sets declared in the
// 'fvar' table. Coordinates are expressed in design-space units and
// follow the order of the font axes.
type NamedInstance struct {
	// Name is the subfamily name resolved from the 'name' table,
	// or an empty string if the name is missing.
	Name string

	// SubfamilyNameID is the 'fvar' subfamily name identifier.
	SubfamilyNameID uint16

	// PostScriptName is the PostScript name resolved from the 'name'
	// table, or an empty string when no PostScript name is provided.
	PostScriptName string

	// PostScriptNameID is 0 or 0xFFFF when the instance has no
	// PostScript name.
	PostScriptNameID uint16

	// Coordinates lists one value per font axis, in 'fvar' order,
	// expressed in design-space units.
	Coordinates []float32
}

// VariationPlan is an immutable, reusable resolution of a set of user
// variation settings against the axis layout of a variable font.
//
// A plan is computed once and stores the same location under the three
// coordinate spaces used by OpenType:
//   - user coordinates, as provided by the caller ([VariationPlan.UserCoords]),
//   - design coordinates, aligned on the 'fvar' axes, defaulted and
//     clamped to the axis bounds ([VariationPlan.DesignCoords]),
//   - normalized coordinates, with the 'avar' mapping applied, ready
//     to drive gvar, MVAR, HVAR/VVAR, CFF2 and COLR variation
//     ([VariationPlan.NormalizedCoords]).
//
// Plans do not own any face state: the same plan can be applied to
// several [Face] values sharing the same axis layout, and a plan is
// safe for concurrent use. All slices returned by its getters are
// copies: mutating them never alters the plan, and applying a plan
// never mutates the slices originally passed by the caller.
type VariationPlan struct {
	axes       []Axis
	instances  []NamedInstance
	user       []Variation
	design     []float32
	normalized []VarCoord
	clamped    []Tag // axes whose user value was outside of [Minimum, Maximum]
}

// Axes returns the font axes, in 'fvar' order. The returned slice is a copy.
func (p *VariationPlan) Axes() []Axis {
	out := make([]Axis, len(p.axes))
	copy(out, p.axes)
	return out
}

// NamedInstances returns the named instances declared in 'fvar'.
// The returned slice, including each instance coordinate slice, is a
// fully independent copy that may be freely modified.
func (p *VariationPlan) NamedInstances() []NamedInstance {
	out := make([]NamedInstance, len(p.instances))
	copy(out, p.instances)
	for i := range out {
		out[i].Coordinates = append([]float32(nil), p.instances[i].Coordinates...)
	}
	return out
}

// UserCoords returns the validated variation list used to build the plan,
// in the order in which it was provided. The returned slice is a copy.
func (p *VariationPlan) UserCoords() []Variation {
	out := make([]Variation, len(p.user))
	copy(out, p.user)
	return out
}

// DesignCoords returns the location in design-space units, one value
// per font axis in 'fvar' order, with unset axes defaulted and out of
// range values clamped. The returned slice is a copy.
func (p *VariationPlan) DesignCoords() []float32 {
	out := make([]float32, len(p.design))
	copy(out, p.design)
	return out
}

// NormalizedCoords returns the normalized location (fixed 2.14 values in
// [-1, 1]), with the 'avar' segment maps applied. The returned slice is
// a copy and can be passed safely to [Face.SetCoords].
func (p *VariationPlan) NormalizedCoords() []VarCoord {
	out := make([]VarCoord, len(p.normalized))
	copy(out, p.normalized)
	return out
}

// ClampedAxes returns the tags of the axes for which the provided user
// value was outside of the 'fvar' [Minimum, Maximum] range and was
// clamped during the plan computation. The returned slice is a copy.
func (p *VariationPlan) ClampedAxes() []Tag {
	out := make([]Tag, len(p.clamped))
	copy(out, p.clamped)
	return out
}

// IsDefault reports whether the plan resolves to the default location,
// i.e. all the normalized coordinates are zero.
func (p *VariationPlan) IsDefault() bool {
	for _, c := range p.normalized {
		if c != 0 {
			return false
		}
	}
	return true
}

// Apply installs the plan location on the given face. It returns
// [ErrAxisMismatch] if the face does not expose the exact same axis
// layout as the font used to build the plan.
//
// The face receives its own copy of the normalized coordinates, so the
// plan stays immutable and the caller's slices are never retained.
func (p *VariationPlan) Apply(face *Face) error {
	if face == nil || !p.matchesFont(face.Font) {
		return fmt.Errorf("%w: plan has %d axes, face has %d", ErrAxisMismatch, len(p.axes), axisCount(face))
	}
	face.SetCoords(p.NormalizedCoords())
	return nil
}

func axisCount(face *Face) int {
	if face == nil || face.Font == nil {
		return 0
	}
	return len(face.Font.fvar)
}

// matchesFont reports whether the plan axis layout matches the given font,
// comparing the axis count, tags and bounds in 'fvar' order.
func (p *VariationPlan) matchesFont(f *Font) bool {
	if f == nil || len(f.fvar) != len(p.axes) {
		return false
	}
	for i, axis := range p.axes {
		got := f.fvar[i]
		if got.Tag != axis.Tag ||
			got.Minimum != axis.Minimum ||
			got.Default != axis.Default ||
			got.Maximum != axis.Maximum {
			return false
		}
	}
	return true
}

// Axes returns the variation axes declared in the font 'fvar' table,
// in 'fvar' order. It returns an empty slice for non variable fonts.
// The returned slice is a copy.
func (f *Font) Axes() []Axis {
	out := make([]Axis, len(f.fvar))
	for i, a := range f.fvar {
		out[i] = Axis{Tag: a.Tag, Minimum: a.Minimum, Default: a.Default, Maximum: a.Maximum}
	}
	return out
}

// NamedInstances returns the named instances declared in 'fvar', or an
// empty slice for non variable fonts. The returned slice, including each
// instance coordinate slice, is a fully independent copy.
func (f *Font) NamedInstances() []NamedInstance {
	out := make([]NamedInstance, len(f.fvarInstances))
	copy(out, f.fvarInstances)
	for i := range out {
		out[i].Coordinates = append([]float32(nil), f.fvarInstances[i].Coordinates...)
	}
	return out
}

// NewVariationPlan resolves the given user variations into an immutable
// [VariationPlan].
//
// Axes absent from variations are set to their 'fvar' default value.
// Values outside of the axis [Minimum, Maximum] range are clamped, and
// the clamped axes are reachable through [VariationPlan.ClampedAxes].
//
// An error is returned (leaving the caller's slices untouched) when the
// font is not variable, when a variation references an unknown or
// duplicated axis tag, when a value is NaN/Inf, or when more variations
// than axes are provided. Fonts exposing several axes under the same
// tag (allowed by the OpenType spec) should be addressed through
// [NewVariationPlanFromInstance] or design-space coordinates.
func (f *Font) NewVariationPlan(variations []Variation) (*VariationPlan, error) {
	if len(f.fvar) == 0 {
		return nil, ErrNotVariable
	}
	if len(variations) > len(f.fvar) {
		return nil, fmt.Errorf("%w: got %d variations, font declares %d axes",
			ErrTooManyVariations, len(variations), len(f.fvar))
	}

	// Validate the input before allocating the plan, so that a failure
	// never produces a partially initialized result.
	seen := make(map[Tag]bool, len(variations))
	for _, v := range variations {
		if math.IsNaN(float64(v.Value)) || math.IsInf(float64(v.Value), 0) {
			return nil, fmt.Errorf("%w: axis %s value %g", ErrInvalidCoordinate, v.Tag, v.Value)
		}
		if seen[v.Tag] {
			return nil, fmt.Errorf("%w: tag %s", ErrDuplicateAxis, v.Tag)
		}
		seen[v.Tag] = true
		if f.indexOfAxis(v.Tag) < 0 {
			return nil, fmt.Errorf("%w: tag %s", ErrUnknownAxis, v.Tag)
		}
	}

	design := make([]float32, len(f.fvar))
	for i, a := range f.fvar {
		design[i] = a.Default
	}
	for _, v := range variations {
		design[f.indexOfAxis(v.Tag)] = v.Value
	}

	user := make([]Variation, len(variations))
	copy(user, variations)
	return f.newPlanFromDesign(design, user)
}

// NewVariationPlanFromInstance builds a [VariationPlan] from one of the
// named instances declared in 'fvar'. It returns [ErrInstanceNotFound]
// if index is out of range.
//
// Unlike [NewVariationPlan], this constructor resolves axes by position,
// so it also works for fonts declaring several axes under the same tag.
func (f *Font) NewVariationPlanFromInstance(index int) (*VariationPlan, error) {
	if len(f.fvar) == 0 {
		return nil, ErrNotVariable
	}
	if index < 0 || index >= len(f.fvarInstances) {
		return nil, fmt.Errorf("%w: index %d, font has %d instances",
			ErrInstanceNotFound, index, len(f.fvarInstances))
	}
	inst := f.fvarInstances[index]
	design := append([]float32(nil), inst.Coordinates...)
	user := make([]Variation, len(f.fvar))
	for i, axis := range f.fvar {
		user[i] = Variation{Tag: axis.Tag, Value: design[i]}
	}
	return f.newPlanFromDesign(design, user)
}

// newPlanFromDesign builds a plan from design-space coordinates already
// aligned on the 'fvar' axes. Out of range values are clamped and the
// 'avar' mapping is applied. The provided variations are only stored for
// later inspection through [VariationPlan.UserCoords].
func (f *Font) newPlanFromDesign(design []float32, user []Variation) (*VariationPlan, error) {
	axes := f.Axes()
	clampedDesign := make([]float32, len(design))
	copy(clampedDesign, design)

	var clamped []Tag
	for i, a := range axes {
		if clampedDesign[i] < a.Minimum {
			clampedDesign[i] = a.Minimum
			clamped = append(clamped, a.Tag)
		} else if clampedDesign[i] > a.Maximum {
			clampedDesign[i] = a.Maximum
			clamped = append(clamped, a.Tag)
		}
	}

	normalized := f.normalizeDesign(clampedDesign)

	return &VariationPlan{
		axes:       axes,
		instances:  f.fvarInstances,
		user:       user,
		design:     clampedDesign,
		normalized: normalized,
		clamped:    clamped,
	}, nil
}

// NewVariationPlanFromDesign builds a [VariationPlan] directly from
// design-space coordinates, one value per font axis in 'fvar' order.
//
// It is the entry point to use when the caller already knows the axis
// layout, in particular for fonts exposing several axes under the same
// tag, where tag based settings cannot address axes individually.
// Out of range values are clamped (see [VariationPlan.ClampedAxes]) and
// [ErrInvalidCoordinate] is returned for NaN or Inf values. The input
// slice is copied and never retained.
func (f *Font) NewVariationPlanFromDesign(design []float32) (*VariationPlan, error) {
	if len(f.fvar) == 0 {
		return nil, ErrNotVariable
	}
	if len(design) != len(f.fvar) {
		return nil, fmt.Errorf("%w: got %d coordinates, font declares %d axes",
			ErrCoordinateCount, len(design), len(f.fvar))
	}
	for i, v := range design {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, fmt.Errorf("%w: axis %s value %g",
				ErrInvalidCoordinate, f.fvar[i].Tag, v)
		}
	}
	user := make([]Variation, len(f.fvar))
	for i, axis := range f.fvar {
		user[i] = Variation{Tag: axis.Tag, Value: design[i]}
	}
	return f.newPlanFromDesign(design, user)
}

// indexOfAxis returns the index of tag in 'fvar', or -1 if it is not declared.
func (f *Font) indexOfAxis(tag Tag) int {
	for i, axis := range f.fvar {
		if axis.Tag == tag {
			return i
		}
	}
	return -1
}

// normalizeDesign converts design-space coordinates into normalized
// 2.14 coordinates: linear normalization against the [min, default, max]
// bounds, then the 'avar' segment maps.
func (f *Font) normalizeDesign(design []float32) []VarCoord {
	normalized := f.fvar.normalizeCoordinates(design)
	for i, av := range f.avar.AxisSegmentMaps {
		normalized[i] = av.Map(normalized[i])
	}
	return normalized
}

// newNamedInstances resolves the raw 'fvar' instance records, copying
// the coordinates so that the plan layer never shares the parser slices.
func newNamedInstances(records []tables.InstanceRecord, axes fvar, names tables.Name) []NamedInstance {
	if len(records) == 0 {
		return nil
	}
	out := make([]NamedInstance, 0, len(records))
	for _, record := range records {
		// A malformed 'fvar' table may provide fewer coordinates than
		// axes: fall back to the axis defaults instead of panicking.
		coords := make([]float32, len(axes))
		for i, axis := range axes {
			coords[i] = axis.Default
		}
		for i, c := range record.Coordinates {
			if i >= len(coords) {
				break
			}
			coords[i] = c
		}
		inst := NamedInstance{
			SubfamilyNameID:  record.SubfamilyNameID,
			PostScriptNameID: record.PostScriptNameID,
			Coordinates:      coords,
			Name:             names.Name(tables.NameID(record.SubfamilyNameID)),
		}
		// The parser leaves PostScriptNameID to 0 when the optional field
		// is absent from 'fvar'; 0xFFFF also means "no PostScript name".
		const noPostScriptName = 0xFFFF
		if record.PostScriptNameID != 0 && record.PostScriptNameID != noPostScriptName {
			inst.PostScriptName = names.Name(tables.NameID(record.PostScriptNameID))
		}
		out = append(out, inst)
	}
	return out
}

// ApplyVariationPlan is a convenience wrapper around [VariationPlan.Apply].
func (face *Face) ApplyVariationPlan(plan *VariationPlan) error {
	return plan.Apply(face)
}
