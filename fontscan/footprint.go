package fontscan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
)

// Location identifies where a font.Face is stored.
type Location = font.FontID

// Footprint is a condensed summary of the main information
// about a font, serving as a lightweight surrogate
// for the original font file.
type Footprint struct {
	// Location stores the adress of the font resource.
	Location Location

	// Family is the general nature of the font, like
	// "Arial"
	// Note that, for performance reason, we store the
	// normalized version of the family name.
	Family string

	// Runes is the set of runes supported by the font.
	Runes RuneSet

	// Scripts is the set of scripts deduced from [Runes]
	Scripts ScriptSet

	// Langs is the set of languages deduced from [Runes]
	Langs LangSet

	// Aspect precises the visual characteristics
	// of the font among a family, like "Bold Italic"
	Aspect font.Aspect

	// coverage is the immutable, potentially shared coverage index derived
	// from [Runes], [Scripts] and [Langs]. It is lazily attached through
	// [Footprint.Coverage] and is not serialized: the on-disk format only
	// stores the underlying [RuneSet], [ScriptSet] and [LangSet].
	// It may be shared between footprints pointing at the same font identity,
	// including fonts reached through aliased paths.
	coverage *FontCoverageIndex

	// isUserProvided is set to true for fonts add manually to
	// a FontMap
	// User fonts will always be tried if no other fonts match,
	// and will have priority among font with same family name.
	//
	// This field is not serialized in the index, since it is always false
	// for system fonts.
	isUserProvided bool
}

func newFootprintFromFont(f *font.Font, location Location, md font.Description) (out Footprint) {
	out.Runes, out.Scripts, _ = newCoveragesFromCmap(f.Cmap, nil)
	out.Langs = newLangsetFromCoverage(out.Runes)
	out.Family = font.NormalizeFamily(md.Family)
	out.Aspect = md.Aspect
	out.Location = location
	out.isUserProvided = true
	return out
}

// Coverage returns the immutable coverage index of the footprint, building
// and registering it in the shared [CoverageIndexCache] on first use.
// The returned pointer may be shared with other footprints describing the
// same font identity and is safe for concurrent read-only use.
func (fp *Footprint) Coverage() *FontCoverageIndex {
	if fp.coverage != nil {
		return fp.coverage
	}
	return attachCoverageIndex(fp)
}

// coversRune reports whether the footprint covers r, going through the
// shared coverage index when available and falling back to the serialized
// [RuneSet] otherwise. Invalid Unicode scalar values are never covered.
func (fp *Footprint) coversRune(r rune) bool {
	if idx := fp.coverage; idx != nil {
		return idx.Contains(r)
	}
	return fp.Runes.Contains(r)
}

// fontPageFromOs2 returns the legacy font page of a raw OS/2 table,
// or FPNone if the table is missing or invalid.
func fontPageFromOs2(raw []byte) tables.FontPage {
	if os2, _, err := tables.ParseOs2(raw); err == nil {
		return os2.FontPage()
	}
	return tables.FPNone
}

func newFootprintFromLoader(ld *ot.Loader, isUserProvided bool, buffer scanBuffer) (out Footprint, _ scanBuffer, err error) {
	raw := buffer.tableBuffer

	// since raw is shared, special car must be taken in the parsing order

	raw, _ = ld.RawTableTo(ot.MustNewTag("OS/2"), raw)
	fp := fontPageFromOs2(raw)

	// we can use the buffer since ProcessCmap do not keep any reference on
	// the input slice
	raw, err = ld.RawTableTo(ot.MustNewTag("cmap"), raw)
	if err != nil {
		return Footprint{}, buffer, err
	}
	tb, _, err := tables.ParseCmap(raw)
	if err != nil {
		return Footprint{}, buffer, err
	}
	cmap, _, err := font.ProcessCmap(tb, fp)
	if err != nil {
		return Footprint{}, buffer, err
	}

	out.Runes, out.Scripts, buffer.cmapBuffer = newCoveragesFromCmap(cmap, buffer.cmapBuffer) // ... and build the corresponding rune set

	out.Langs = newLangsetFromCoverage(out.Runes)

	desc, raw := font.Describe(ld, raw)
	out.Family = font.NormalizeFamily(desc.Family)
	out.Aspect = desc.Aspect
	out.isUserProvided = isUserProvided

	buffer.tableBuffer = raw

	return out, buffer, nil
}

// returns true for .ttf and .ttc font files
func (fp *Footprint) isTruetypeHint() bool {
	switch strings.ToLower(filepath.Ext(fp.Location.File)) {
	case ".ttf", ".ttc":
		return true
	default:
		return false
	}
}

// isMonoHint returns true if "mono" is included in the family name
// this is not very precise but much more efficient than using [font.Font.IsMonospace]
func (fp *Footprint) isMonoHint() bool {
	return strings.Contains(fp.Family, "mono")
}

// loadFromDisk assume the footprint location refers to the file system
func (fp *Footprint) loadFromDisk() (*font.Face, error) {
	location := fp.Location

	file, err := os.Open(location.File)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	loaders, err := ot.NewLoaders(file)
	if err != nil {
		return nil, err
	}

	if index := int(location.Index); len(loaders) <= index {
		// this should only happen if the font file as changed
		// since the last scan (very unlikely)
		return nil, fmt.Errorf("invalid font index in collection: %d >= %d", index, len(loaders))
	}

	ft, err := font.NewFont(loaders[location.Index])
	if err != nil {
		return nil, fmt.Errorf("reading font at %s: %s", location.File, err)
	}

	return font.NewFace(ft), nil
}
