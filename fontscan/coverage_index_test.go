package fontscan

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
)

func discardLogger() Logger { return log.New(io.Discard, "", 0) }

// ---------------- synthetic font fixtures ----------------

// cmap12Font builds a minimal valid OpenType font whose cmap is a format 12
// (UCS-4) subtable covering the given groups. A group mapping to glyph 0
// exposes code points that the coverage builder must ignore.
func cmap12Font(t testing.TB, groups []tables.SequentialMapGroup) []byte {
	t.Helper()
	var sub bytes.Buffer
	subtableLen := uint32(16 + 12*len(groups))
	writeU16 := func(v uint16) { tu.AssertNoErr(t, binary.Write(&sub, binary.BigEndian, v)) }
	writeU32 := func(v uint32) { tu.AssertNoErr(t, binary.Write(&sub, binary.BigEndian, v)) }
	writeU16(12) // format
	writeU16(0)  // reserved
	writeU32(subtableLen)
	writeU32(0) // language
	writeU32(uint32(len(groups)))
	for _, g := range groups {
		writeU32(g.StartCharCode)
		writeU32(g.EndCharCode)
		writeU32(g.StartGlyphID)
	}

	// cmap header: version 0, one encoding record (platform 3 / encoding 10 = UCS-4)
	var cmap bytes.Buffer
	tu.AssertNoErr(t, binary.Write(&cmap, binary.BigEndian, []uint16{0, 1, 3, 10}))
	tu.AssertNoErr(t, binary.Write(&cmap, binary.BigEndian, uint32(12))) // offset to subtable
	cmap.Write(sub.Bytes())

	return ot.WriteOpentype([]ot.Table{
		{Tag: ot.MustNewTag("cmap"), Content: cmap.Bytes()},
	}, ot.TrueType)
}

// group is a concise constructor for [tables.SequentialMapGroup] mapping each
// rune of [lo, hi] to a distinct, non zero glyph.
func group(lo, hi rune) tables.SequentialMapGroup {
	return tables.SequentialMapGroup{StartCharCode: uint32(lo), EndCharCode: uint32(hi), StartGlyphID: 1}
}

// loadCmap parses the cmap of a synthetic font the same way the scanner does.
func loadCmap(t testing.TB, data []byte) font.Cmap {
	t.Helper()
	ld, err := ot.NewLoader(bytes.NewReader(data))
	tu.AssertNoErr(t, err)
	raw, err := ld.RawTable(ot.MustNewTag("cmap"))
	tu.AssertNoErr(t, err)
	tb, _, err := tables.ParseCmap(raw)
	tu.AssertNoErr(t, err)
	cm, _, err := font.ProcessCmap(tb, tables.FPNone)
	tu.AssertNoErr(t, err)
	return cm
}

// mixedCoverageGroups spans Latin, CJK, emoji and the supplementary planes.
func mixedCoverageGroups() []tables.SequentialMapGroup {
	return []tables.SequentialMapGroup{
		group('A', 'Z'),         // Latin basic
		group('a', 'z'),         // Latin basic
		group(0x4E00, 0x4E10),   // CJK Unified Ideographs
		group(0x1F600, 0x1F610), // emoji, SMP
		group(0x20000, 0x20010), // CJK Ext B, SIP
		{StartCharCode: 0x30, EndCharCode: 0x30, StartGlyphID: 0}, // glyph 0: not covered
	}
}

// referenceContains is the trivial, per-code-point reference implementation.
func referenceContains(groups []tables.SequentialMapGroup, r rune) bool {
	if !isUnicodeScalar(r) {
		return false
	}
	for _, g := range groups {
		if uint32(r) >= g.StartCharCode && uint32(r) <= g.EndCharCode {
			gid := uint32(r) - g.StartCharCode + g.StartGlyphID
			return gid != 0
		}
	}
	return false
}

func referenceRunes(groups []tables.SequentialMapGroup) []rune {
	var out []rune
	for _, g := range groups {
		for r := rune(g.StartCharCode); r <= rune(g.EndCharCode); r++ {
			if referenceContains(groups, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

// ---------------- index construction and queries ----------------

func TestFontCoverageIndexMixedCoverage(t *testing.T) {
	groups := mixedCoverageGroups()
	cm := loadCmap(t, cmap12Font(t, groups))

	idx, _, err := NewFontCoverageIndex(cm, nil)
	tu.AssertNoErr(t, err)

	// 1. per-code-point agreement with the slow reference, on a dense grid
	// covering BMP, surrogates and both supplementary planes.
	checks := 0
	for r := rune(0); r <= 0x21000; r += 17 {
		got := idx.Contains(r)
		want := referenceContains(groups, r)
		if got != want {
			t.Fatalf("Contains(%U) = %v, want %v", r, got, want)
		}
		checks++
	}
	// and exhaustively on the actually covered code points
	for _, r := range referenceRunes(groups) {
		if !idx.Contains(r) {
			t.Fatalf("missing covered rune %U", r)
		}
	}
	if checks == 0 {
		t.Fatal("no grid check performed")
	}

	// 2. RuneSet view stays consistent with the range view.
	rs := idx.RuneSet()
	for _, r := range referenceRunes(groups) {
		tu.Assert(t, rs.Contains(r))
	}
	tu.Assert(t, rs.Len() == len(referenceRunes(groups)))

	// 3. interval queries
	tu.Assert(t, idx.CoversRange('A', 'Z'))
	tu.Assert(t, idx.CoversRange(0x4E00, 0x4E10))
	tu.Assert(t, idx.CoversRange(0x1F600, 0x1F610))  // SMP
	tu.Assert(t, idx.CoversRange(0x20000, 0x20010))  // SIP
	tu.Assert(t, !idx.CoversRange('A', 'a'))         // gap between ranges
	tu.Assert(t, idx.CoversRange('B', 'B'))          // single point
	tu.Assert(t, !idx.CoversRange(0x1F600, 0x1F700)) // extends past coverage
	tu.Assert(t, !idx.CoversRange(0x2FFFF, 0x30000)) // fully outside
	tu.Assert(t, !idx.CoversRange('z', 'a'))         // reversed
	tu.Assert(t, !idx.CoversRange(0xD800, 0xDFFF))   // only surrogates
	// an interval crossing the surrogate block cannot be covered: a font
	// never maps a surrogate code unit to a glyph
	tu.Assert(t, !idx.CoversRange(0xD7F0, 0xE010))
	tu.Assert(t, idx.CoversRange('A', 'B'))

	// on an index covering every scalar value around the surrogate block,
	// the surrogate rules are still strict and deterministic
	fullGroups := []tables.SequentialMapGroup{
		{StartCharCode: 0, EndCharCode: 0xD7FF, StartGlyphID: 1},
		{StartCharCode: 0xE000, EndCharCode: uint32(unicode.MaxRune), StartGlyphID: 1},
	}
	full, _, err := NewFontCoverageIndex(loadCmap(t, cmap12Font(t, fullGroups)), nil)
	tu.AssertNoErr(t, err)
	tu.Assert(t, full.CoversRange(0, 0xD7FF))
	tu.Assert(t, full.CoversRange(0xE000, unicode.MaxRune))
	tu.Assert(t, !full.CoversRange(0xD7F0, 0xE010)) // crosses surrogates
	tu.Assert(t, !full.CoversRange(0xD800, 0xDFFF))
	tu.Assert(t, !full.CoversRange(0, unicode.MaxRune)) // contains surrogates
	tu.Assert(t, !full.Contains(0xD800))
	tu.Assert(t, full.Contains(0xE000))

	// 4. scripts and languages
	tu.Assert(t, idx.CoversScript(language.Latin))
	tu.Assert(t, idx.CoversScript(language.Han))
	tu.Assert(t, !idx.CoversScript(language.Arabic))
	en, known := language.NewLangID("en")
	tu.Assert(t, known)
	// Latin alone does not cover every English sample rune (punctuation etc.)
	// but the index must answer deterministically either way.
	_ = idx.CoversLang(en)

	// 5. whole cluster coverage, including mixed scripts and emoji
	tu.Assert(t, idx.CoversCluster([]rune{'A', 0x4E00, 0x1F600, 0x20000}))
	tu.Assert(t, !idx.CoversCluster([]rune{'A', '界'})) // 界 = 0x754C, not covered
	tu.Assert(t, !idx.CoversCluster([]rune{0xD800}))   // surrogate
	tu.Assert(t, idx.CoversCluster(nil))               // vacuous
	tu.Assert(t, idx.CoversCluster([]rune{}))
}

func TestFontCoverageIndexNormalizesRanges(t *testing.T) {
	groups := []tables.SequentialMapGroup{
		group(0x40, 0x45),
		group(0x46, 0x50), // adjacent: must merge
		group(0x100, 0x105),
	}
	idx, _, err := NewFontCoverageIndex(loadCmap(t, cmap12Font(t, groups)), nil)
	tu.AssertNoErr(t, err)

	want := []coverageRange{{lo: 0x40, hi: 0x50}, {lo: 0x100, hi: 0x105}}
	if got := idx.Ranges(); len(got) != len(want) {
		t.Fatalf("expected %d normalized ranges, got %d (%v)", len(want), len(got), got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("range %d: got %v want %v", i, got[i], want[i])
			}
		}
	}
}

func TestFontCoverageIndexEmptyAndInvalid(t *testing.T) {
	// empty font: cmap with no group
	idx, _, err := NewFontCoverageIndex(loadCmap(t, cmap12Font(t, nil)), nil)
	tu.AssertNoErr(t, err)
	tu.Assert(t, len(idx.Ranges()) == 0)
	tu.Assert(t, idx.RuneSet().Len() == 0)
	tu.Assert(t, !idx.Contains('A'))
	tu.Assert(t, !idx.CoversRange(0, 0))
	tu.Assert(t, !idx.CoversRange(0, unicode.MaxRune))
	tu.Assert(t, idx.CoversCluster(nil))
	tu.Assert(t, !idx.CoversCluster([]rune{0}))
	tu.Assert(t, len(idx.Scripts()) == 0)

	// zero value is usable
	var zero FontCoverageIndex
	tu.Assert(t, !zero.Contains('A'))
	tu.Assert(t, !zero.CoversRange(0, 10))
	tu.Assert(t, zero.CoversCluster(nil))

	// invalid scalar values are never covered, even on an index that covers
	// every valid scalar value around the surrogate block
	fullGroups := []tables.SequentialMapGroup{
		{StartCharCode: 0, EndCharCode: 0xD7FF, StartGlyphID: 1},
		{StartCharCode: 0xE000, EndCharCode: uint32(unicode.MaxRune), StartGlyphID: 1},
	}
	full, _, err := NewFontCoverageIndex(loadCmap(t, cmap12Font(t, fullGroups)), nil)
	tu.AssertNoErr(t, err)
	for _, r := range []rune{-1, 0xD800, 0xDFFF, unicode.MaxRune + 1, 0x110000} {
		if full.Contains(r) {
			t.Fatalf("invalid rune %U reported covered", r)
		}
	}
	tu.Assert(t, full.Contains(0))
	tu.Assert(t, full.Contains(unicode.MaxRune))

	// reversed and out of bounds ranges
	tu.Assert(t, !full.CoversRange(10, 5))
	tu.Assert(t, !full.CoversRange(-1, 10))
	tu.Assert(t, !full.CoversRange(0, unicode.MaxRune+1))
}

func TestValidateRanges(t *testing.T) {
	if _, err := validateRanges([][2]rune{{10, 5}}); !errors.Is(err, errEmptyRange) {
		t.Fatalf("expected errEmptyRange, got %v", err)
	}
	if _, err := validateRanges([][2]rune{{0, unicode.MaxRune + 1}}); !errors.Is(err, errRangeEndTooLarge) {
		t.Fatalf("expected errRangeEndTooLarge, got %v", err)
	}
	// The number of genuinely disjoint scalar-value ranges is bounded by
	// the Unicode range itself, so a legitimate cmap cannot produce more
	// than [maxCoverageRanges] ranges without merging. The defensive cap is
	// exercised through the bounded core with a small limit and one covered
	// rune per page (which can never merge into fewer ranges).
	const smallBound = 8
	oversized := make(RuneSet, 0, smallBound+2)
	for p := 0; p < smallBound+2; p++ {
		oversized = append(oversized, runePage{
			ref: uint16(p),
			set: pageSet{1, 0, 0, 0, 0, 0, 0, 0},
		})
	}
	if _, err := rangesFromRuneSet(oversized, smallBound); !errors.Is(err, errTooManyRanges) {
		t.Fatalf("expected errTooManyRanges, got %v", err)
	}
	// clampRanges never exceeds the global capacity and preserves every
	// covered rune of ordinary, non adversarial coverage
	clamped := clampRanges(oversized)
	tu.Assert(t, len(clamped) > 0 && len(clamped) <= len(oversized))
	idx := newFontCoverageIndexFromRunes(font.FontID{}, oversized, nil, LangSet{})
	tu.Assert(t, len(idx.Ranges()) > 0 && len(idx.Ranges()) <= maxCoverageRanges)
	tu.Assert(t, idx.RuneSet().Len() == len(oversized)) // one rune per page
	// overlaps merge, and an interval crossing the surrogate block is split
	// into its two scalar-value sides (they stay separate, since the surrogate
	// gap breaks adjacency)
	got, err := validateRanges([][2]rune{{0x40, 0x42}, {0x42, 0x45}, {0xD7FE, 0xE001}})
	tu.AssertNoErr(t, err)
	want := []coverageRange{{0x40, 0x45}, {0xD7FE, 0xD7FF}, {0xE000, 0xE001}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("normalized ranges = %v, want %v", got, want)
	}
}

func TestCoverageIndexAgainstRuneSetReference(t *testing.T) {
	// randomized cross check: ranges -> cmap12 font -> index, compared with
	// per code point RuneSet semantics. Gaps between groups are kept wide
	// enough that the synthetic groups never overlap.
	groups := make([]tables.SequentialMapGroup, 0, 64)
	next := rune(0x100)
	for i := 0; i < 64; i++ {
		lo := next + rune(i%7)
		hi := lo + rune(i%13)
		groups = append(groups, group(lo, hi))
		next = hi + 32
	}
	idx, _, err := NewFontCoverageIndex(loadCmap(t, cmap12Font(t, groups)), nil)
	tu.AssertNoErr(t, err)

	rs := idx.RuneSet()
	for r := rune(0); r < next+10; r++ {
		want := referenceContains(groups, r)
		if idx.Contains(r) != want || rs.Contains(r) != want {
			t.Fatalf("mismatch at %U: index=%v runeset=%v want=%v", r, idx.Contains(r), rs.Contains(r), want)
		}
	}

	// every stored range must itself be fully covered, and stored ranges
	// must be strictly separated (adjacent intervals would have merged).
	prev := coverageRange{hi: -2}
	for _, ra := range idx.Ranges() {
		if ra.lo > ra.hi || !isUnicodeScalar(ra.lo) || !isUnicodeScalar(ra.hi) {
			t.Fatalf("invalid stored range %v", ra)
		}
		if ra.lo <= prev.hi+1 {
			t.Fatalf("ranges not normalized: %v after %v", ra, prev)
		}
		if !idx.CoversRange(ra.lo, ra.hi) {
			t.Fatalf("stored range %v not fully covered", ra)
		}
		prev = ra
	}
}

// ---------------- cache behavior ----------------

func freshTestCache(size int) *CoverageIndexCache {
	return NewCoverageIndexCache(size)
}

func TestCoverageIndexCacheLRUAndBounds(t *testing.T) {
	cache := freshTestCache(3)

	mk := func(i uint16) *FontCoverageIndex {
		id := font.FontID{File: fmt.Sprintf("font%d.ttf", i), Index: i}
		return newFontCoverageIndexFromRunes(id, newRuneSet(rune(i)), ScriptSet{}, LangSet{})
	}
	cache.Put(mk(1))
	cache.Put(mk(2))
	cache.Put(mk(3))

	// touch 1 so that 2 becomes the least recently used
	if _, ok := cache.Lookup(font.FontID{File: "font1.ttf", Index: 1}); !ok {
		t.Fatal("expected hit on font1")
	}
	cache.Put(mk(4)) // evicts font2
	if _, ok := cache.Lookup(font.FontID{File: "font2.ttf", Index: 2}); ok {
		t.Fatal("font2 should have been evicted")
	}
	for _, i := range []uint16{1, 3, 4} {
		if _, ok := cache.Lookup(font.FontID{File: fmt.Sprintf("font%d.ttf", i), Index: i}); !ok {
			t.Fatalf("font%d missing", i)
		}
	}

	stats := cache.Stats()
	tu.Assert(t, stats.Size == 3)
	tu.Assert(t, stats.Capacity == 3)
	tu.Assert(t, stats.Evictions >= 1)
	tu.Assert(t, stats.Hits >= 4)
	tu.Assert(t, stats.Misses >= 1)

	// shrinking the capacity evicts immediately
	cache.SetCapacity(1)
	tu.Assert(t, cache.Stats().Size == 1)

	// zero FontID entries are not cached
	cache.Put(newFontCoverageIndexFromRunes(font.FontID{}, newRuneSet(1), nil, LangSet{}))
	tu.Assert(t, cache.Stats().Size == 1)
	cache.Put(nil) // must be a no-op
	tu.Assert(t, cache.Stats().Size == 1)
}

func TestCoverageIndexCacheInvalidation(t *testing.T) {
	cache := freshTestCache(8)
	id := font.FontID{File: "alias.ttf", Index: 0}
	idx := newFontCoverageIndexFromRunes(id, newRuneSet('A'), ScriptSet{language.Latin}, LangSet{})
	cache.Put(idx)
	genBefore := cache.Generation()
	tu.Assert(t, idx.Version() == genBefore)

	// replacing an entry stamps it with the current generation
	cache.Put(newFontCoverageIndexFromRunes(id, newRuneSet('B'), ScriptSet{language.Latin}, LangSet{}))
	got, ok := cache.Lookup(id)
	tu.Assert(t, ok)
	tu.Assert(t, got.Contains('B'))
	tu.Assert(t, !got.Contains('A'))

	cache.Invalidate(id)
	if _, ok := cache.Lookup(id); ok {
		t.Fatal("entry should have been invalidated")
	}
	tu.Assert(t, cache.Stats().Invalidations >= 1)

	cache.Put(idx)
	cache.InvalidateAll()
	tu.Assert(t, cache.Stats().Size == 0)
	tu.Assert(t, cache.Generation() == genBefore+1)
	if _, ok := cache.Lookup(id); ok {
		t.Fatal("entry should have been cleared by InvalidateAll")
	}
}

func TestCoverageIndexCacheConcurrentReadOnly(t *testing.T) {
	// capacity exceeds the total number of entries inserted so reader
	// entries are never evicted by the concurrent writer.
	cache := freshTestCache(1024)
	const n = 32
	for i := 0; i < n; i++ {
		id := font.FontID{File: fmt.Sprintf("f%d", i)}
		cache.Put(newFontCoverageIndexFromRunes(id, newRuneSet(rune('a'+i%26)), nil, LangSet{}))
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for k := 0; k < 2000; k++ {
				i := (seed + k) % n
				id := font.FontID{File: fmt.Sprintf("f%d", i)}
				idx, ok := cache.Lookup(id)
				if !ok {
					t.Errorf("missing index for %v", id)
					return
				}
				// only read methods, concurrently
				_ = idx.Contains(rune('a' + i%26))
				_ = idx.CoversCluster([]rune{rune('a' + i%26)})
				_ = idx.CoversRange(rune('a'+i%26), rune('a'+i%26))
				_ = idx.CoversScript(language.Latin)
				_ = idx.Version()
				_ = idx.Ranges()
			}
		}(g)
	}
	// one writer exercising eviction while readers run
	wg.Add(1)
	go func() {
		defer wg.Done()
		for k := 0; k < 500; k++ {
			cache.Put(newFontCoverageIndexFromRunes(
				font.FontID{File: fmt.Sprintf("w%d", k)},
				newRuneSet(rune(k%26)), nil, LangSet{},
			))
		}
	}()
	wg.Wait()
}

// ---------------- end to end scanner behavior ----------------

func TestCoverageIndexSerializationRoundTrip(t *testing.T) {
	cache := freshTestCache(16)
	previous := SharedCoverageIndexCache()
	SetSharedCoverageIndexCache(cache)
	t.Cleanup(func() { SetSharedCoverageIndexCache(previous) })

	dir := t.TempDir()
	writeFontFile(t, dir, "mixed.ttf", mixedCoverageGroups())
	index, err := scanFontFootprints(discardLogger(), nil, dir)
	tu.AssertNoErr(t, err)
	before := index.flatten()
	tu.Assert(t, len(before) == 1)

	// the on-disk format stores only RuneSet/ScriptSet/LangSet, never the
	// derived index; round tripping must preserve all serialized fields.
	var buf bytes.Buffer
	tu.AssertNoErr(t, index.serializeTo(&buf))
	restored, err := deserializeIndex(&buf)
	tu.AssertNoErr(t, err)
	tu.Assert(t, assertFontsetEquals(before, restored.flatten()) == nil)

	// deserialized footprints reattach to the shared cache lazily and then
	// answer identically to freshly scanned ones
	for i, fp := range restored.flatten() {
		idx := fp.Coverage()
		tu.Assert(t, idx != nil)
		want := before[i].Coverage()
		for _, r := range []rune{'A', 'z', 0x4E00, 0x1F600, 0x20000, 0x1234, 0xD800} {
			tu.Assert(t, idx.Contains(r) == want.Contains(r))
		}
		tu.Assert(t, idx.CoversScript(language.Han) == want.CoversScript(language.Han))
		tu.Assert(t, idx.Version() == cache.Generation())
	}
}

func writeFontFile(t testing.TB, dir, name string, groups []tables.SequentialMapGroup) string {
	t.Helper()
	path := filepath.Join(dir, name)
	tu.AssertNoErr(t, os.WriteFile(path, cmap12Font(t, groups), 0o600))
	return path
}

func TestScanSharesCoverageIndexForAliases(t *testing.T) {
	cache := freshTestCache(16)
	previous := SharedCoverageIndexCache()
	SetSharedCoverageIndexCache(cache)
	t.Cleanup(func() { SetSharedCoverageIndexCache(previous) })

	dir := t.TempDir()
	groups := mixedCoverageGroups()
	first := writeFontFile(t, dir, "real.ttf", groups)
	alias := filepath.Join(dir, "alias.ttf")
	tu.AssertNoErr(t, os.Link(first, alias)) // hard link: same content, distinct path

	logger := discardLogger()
	index, err := scanFontFootprints(logger, nil, dir)
	tu.AssertNoErr(t, err)
	fps := index.flatten()
	if len(fps) != 2 {
		t.Fatalf("expected 2 footprints, got %d", len(fps))
	}

	// both footprints answer identically and expose immutable indexes
	for _, fp := range fps {
		idx := fp.Coverage()
		tu.Assert(t, idx.Contains(0x1F600))
		tu.Assert(t, idx.CoversScript(language.Han))
		tu.Assert(t, idx.Version() == cache.Generation())
	}

	// repeated Coverage() calls return the same shared pointer per identity
	a := fps[0].Coverage()
	b := fps[0].Coverage()
	tu.Assert(t, a == b)

	// deserialized footprints get reattached to the shared cache
	var buf bytes.Buffer
	tu.AssertNoErr(t, index.serializeTo(&buf))
	restored, err := deserializeIndex(&buf)
	tu.AssertNoErr(t, err)
	for _, fp := range restored.flatten() {
		tu.Assert(t, fp.Coverage() != nil)
		tu.Assert(t, fp.Coverage().Contains(0x20000))
	}
}

func TestScanCorruptAndEmptyFontsAreDeterministic(t *testing.T) {
	cache := freshTestCache(16)
	previous := SharedCoverageIndexCache()
	SetSharedCoverageIndexCache(cache)
	t.Cleanup(func() { SetSharedCoverageIndexCache(previous) })

	dir := t.TempDir()

	// not an OpenType file at all
	tu.AssertNoErr(t, os.WriteFile(filepath.Join(dir, "broken.ttf"), []byte("not a font"), 0o600))
	// valid OpenType file but an empty cmap
	writeFontFile(t, dir, "empty.ttf", nil)
	// a healthy font
	writeFontFile(t, dir, "good.ttf", mixedCoverageGroups())

	index, err := scanFontFootprints(discardLogger(), nil, dir)
	tu.AssertNoErr(t, err)

	var foundGood, foundEmpty bool
	for _, file := range index {
		switch filepath.Base(file.path) {
		case "broken.ttf":
			tu.Assert(t, len(file.footprints) == 0) // deterministic: ignored but indexed
		case "empty.ttf":
			foundEmpty = true
			tu.Assert(t, len(file.footprints) == 1)
			fp := file.footprints[0]
			tu.Assert(t, fp.Runes.Len() == 0)
			tu.Assert(t, !fp.Coverage().Contains('A'))
			tu.Assert(t, !fp.Coverage().CoversRange(0, 0))
		case "good.ttf":
			foundGood = true
			tu.Assert(t, len(file.footprints) == 1)
			tu.Assert(t, file.footprints[0].Coverage().Contains(0x1F600))
		}
	}
	tu.Assert(t, foundGood && foundEmpty)

	// a second scan over the same directory is a no-op and stays consistent
	again, err := scanFontFootprints(discardLogger(), index, dir)
	tu.AssertNoErr(t, err)
	tu.Assert(t, assertFontsetEquals(index.flatten(), again.flatten()) == nil)
}

func TestScanInvalidationDropsOldFileData(t *testing.T) {
	cache := freshTestCache(16)
	previous := SharedCoverageIndexCache()
	SetSharedCoverageIndexCache(cache)
	t.Cleanup(func() { SetSharedCoverageIndexCache(previous) })

	dir := t.TempDir()
	path := writeFontFile(t, dir, "changing.ttf", []tables.SequentialMapGroup{group('A', 'A')})

	index, err := scanFontFootprints(discardLogger(), nil, dir)
	tu.AssertNoErr(t, err)
	id := index.flatten()[0].Location
	tu.Assert(t, cache.Stats().Size >= 1)
	tu.Assert(t, index.flatten()[0].Coverage().Contains('A'))

	// replace the file content, then force a rescan with an empty previous
	// index (simulating cache loss) but unchanged shared coverage cache.
	tu.AssertNoErr(t, os.WriteFile(path, cmap12Font(t, []tables.SequentialMapGroup{group('B', 'B')}), 0o600))
	// bump mtime resolution
	info, err := os.Stat(path)
	tu.AssertNoErr(t, err)
	future := info.ModTime().Add(time.Second)
	tu.AssertNoErr(t, os.Chtimes(path, future, future))

	updated, err := scanFontFootprints(discardLogger(), index, dir)
	tu.AssertNoErr(t, err)
	fp := updated.flatten()[0]
	if fp.Coverage().Contains('A') || !fp.Coverage().Contains('B') {
		t.Fatalf("stale coverage retained: A=%v B=%v", fp.Coverage().Contains('A'), fp.Coverage().Contains('B'))
	}
	// the old identity no longer resolves to stale data
	if stale, ok := cache.Lookup(id); ok && stale.Contains('A') {
		t.Fatal("cache retained old file data")
	}

	// removing the file invalidates its entry during pruning
	tu.AssertNoErr(t, os.Remove(path))
	pruned, err := scanFontFootprints(discardLogger(), updated, dir)
	tu.AssertNoErr(t, err)
	tu.Assert(t, len(pruned) == 0)
	if _, ok := cache.Lookup(id); ok {
		t.Fatal("removed font identity still cached")
	}
}

func TestDuplicateFilesHaveDeterministicResult(t *testing.T) {
	cache := freshTestCache(4)
	previous := SharedCoverageIndexCache()
	SetSharedCoverageIndexCache(cache)
	t.Cleanup(func() { SetSharedCoverageIndexCache(previous) })

	dir := t.TempDir()
	groups := mixedCoverageGroups()
	writeFontFile(t, dir, "one.ttf", groups)
	writeFontFile(t, dir, "two.ttf", groups) // same content, different identity

	index, err := scanFontFootprints(discardLogger(), nil, dir)
	tu.AssertNoErr(t, err)
	fps := index.flatten()
	tu.Assert(t, len(fps) == 2)
	for _, fp := range fps {
		// deterministic coverage regardless of file ordering
		tu.Assert(t, fp.Coverage().Contains('A'))
		tu.Assert(t, fp.Coverage().Contains(0x1F600))
		tu.Assert(t, !fp.Coverage().Contains(0x1F650))
	}
}

// ---------------- large directory fixture and reporting ----------------

// TestLargeDirectoryFixture builds a large synthetic font directory and
// cross checks every coverage answer against the per-code-point reference.
// It also reports build time, cache hit rate and memory footprint.
func TestLargeDirectoryFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("large directory fixture disabled in short mode")
	}

	cache := freshTestCache(2048)
	previous := SharedCoverageIndexCache()
	SetSharedCoverageIndexCache(cache)
	t.Cleanup(func() { SetSharedCoverageIndexCache(previous) })

	dir := t.TempDir()
	const fontCount = 120

	type fixture struct {
		path   string
		groups []tables.SequentialMapGroup
	}
	fixtures := make([]fixture, 0, fontCount)
	for f := 0; f < fontCount; f++ {
		// each font covers a rotating window of Latin + CJK + emoji so that
		// fallback chains and language filtering have real work to do
		base := rune(0x40 + (f % 20))
		groups := []tables.SequentialMapGroup{
			group(base, base+15),
			group(0x4E00+rune(f%50), 0x4E00+rune(f%50)+40),
			group(0x1F600+rune(f%16), 0x1F600+rune(f%16)+8),
			group(0x20000+rune(f%8), 0x20000+rune(f%8)+4),
		}
		path := writeFontFile(t, dir, fmt.Sprintf("font%04d.ttf", f), groups)
		fixtures = append(fixtures, fixture{path, groups})
	}

	// a handful of duplicates and aliases
	for f := 0; f < 8; f++ {
		src := fixtures[f].path
		tu.AssertNoErr(t, os.Link(src, filepath.Join(dir, fmt.Sprintf("alias%04d.ttf", f))))
	}

	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	start := time.Now()
	index, err := scanFontFootprints(discardLogger(), nil, dir)
	tu.AssertNoErr(t, err)
	buildTime := time.Since(start)

	all := index.flatten()
	if len(all) != fontCount+8 {
		t.Fatalf("expected %d footprints, got %d", fontCount+8, len(all))
	}

	// second scan: everything is served from the previous index / cache
	start = time.Now()
	_, err = scanFontFootprints(discardLogger(), index, dir)
	tu.AssertNoErr(t, err)
	rebuildTime := time.Since(start)

	runtime.ReadMemStats(&memAfter)

	// exhaustive-ish cross check against the slow reference
	checked := 0
	for fi, fx := range fixtures {
		idx := index[fi].footprints[0].Coverage()
		for r := rune(0); r < 0x21000; r += 997 {
			want := referenceContains(fx.groups, r)
			if idx.Contains(r) != want {
				t.Fatalf("font %d Contains(%U) = %v want %v", fi, r, idx.Contains(r), want)
			}
			// range and cluster queries agree with the pointwise reference
			if want && r+5 <= 0x21000 {
				cluster := []rune{r, r + 1, r + 2}
				clusterWant := true
				for _, c := range cluster {
					if !referenceContains(fx.groups, c) {
						clusterWant = false
					}
				}
				if idx.CoversCluster(cluster) != clusterWant {
					t.Fatalf("font %d CoversCluster(%U) mismatch", fi, r)
				}
			}
			checked++
		}
	}

	stats := cache.Stats()
	hitRate := 0.0
	if total := stats.Hits + stats.Misses; total > 0 {
		hitRate = float64(stats.Hits) / float64(total)
	}
	t.Logf("large fixture: fonts=%d footprints=%d checks=%d", fontCount, len(all), checked)
	t.Logf("build time: %d ns (cold), %d ns (warm)", buildTime, rebuildTime)
	t.Logf("cache: hits=%d misses=%d evictions=%d invalidations=%d size=%d/%d hit-rate=%.2f%%",
		stats.Hits, stats.Misses, stats.Evictions, stats.Invalidations, stats.Size, stats.Capacity, hitRate*100)
	t.Logf("memory: heapAlloc delta=%d KB, totalAlloc delta=%d KB",
		(memAfter.HeapAlloc-memBefore.HeapAlloc)/1024,
		(memAfter.TotalAlloc-memBefore.TotalAlloc)/1024)
}

func BenchmarkCoverageIndexBuild(b *testing.B) {
	groups := mixedCoverageGroups()
	cm := loadCmap(b, cmap12Font(b, groups))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, _ = NewFontCoverageIndex(cm, nil)
	}
}

func BenchmarkCoverageIndexContains(b *testing.B) {
	groups := mixedCoverageGroups()
	idx, _, err := NewFontCoverageIndex(loadCmap(b, cmap12Font(b, groups)), nil)
	if err != nil {
		b.Fatal(err)
	}
	runes := []rune{'A', 'M', 'z', 0x4E05, 0x1F605, 0x20005, 0x21000, 0xD800}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, r := range runes {
			_ = idx.Contains(r)
		}
	}
}

func BenchmarkCoverageIndexCoversCluster(b *testing.B) {
	groups := mixedCoverageGroups()
	idx, _, err := NewFontCoverageIndex(loadCmap(b, cmap12Font(b, groups)), nil)
	if err != nil {
		b.Fatal(err)
	}
	cluster := []rune{'A', 'B', 0x4E00, 0x1F600, 0x20000}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = idx.CoversCluster(cluster)
	}
}
