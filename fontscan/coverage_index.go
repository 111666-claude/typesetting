package fontscan

import (
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"unicode"

	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
)

// This file implements [FontCoverageIndex], an immutable, goroutine-safe
// summary of a font cmap that can be shared between several [Footprint]
// values (for example when the same font file is scanned more than once or
// reached through an alias) and between goroutines performing coverage
// queries.
//
// A [FontCoverageIndex] is a derived view of the existing [RuneSet]: the
// on-disk serialization format is unchanged, so caches produced by previous
// versions of the package remain readable.

// maxCoverageRanges bounds the number of normalized ranges stored in an
// index. A valid OpenType font cannot legitimately produce more disjoint
// ranges than the number of cmap groups, and 65536 groups already covers
// every 256-rune page of the whole Unicode space. Larger inputs are treated
// as corrupt data rather than trusted blindly.
const maxCoverageRanges = 65536

var (
	errEmptyRange       = errors.New("fontscan: empty coverage range")
	errRangeEndTooLarge = errors.New("fontscan: coverage range end exceeds Unicode MaxRune")
	errTooManyRanges    = errors.New("fontscan: too many coverage ranges")
)

// FontCoverageIndex is an immutable index of the code points, scripts and
// languages covered by a font face.
//
// All its query methods are read-only: a *FontCoverageIndex is safe for
// concurrent use by multiple goroutines, as long as no caller mutates the
// slices returned by [FontCoverageIndex.Ranges] (the slices must be treated
// as read-only, mirroring the convention used by [RuneSet]).
//
// The zero value is a valid, empty index.
type FontCoverageIndex struct {
	// id identifies the font resource the index was built from.
	id font.FontID

	// ranges is a sorted, normalized (non overlapping, inclusive bounds)
	// list of covered Unicode scalar values. Surrogates and code points
	// above [unicode.MaxRune] never appear.
	ranges []coverageRange

	// runes is the page-set representation of the same coverage as ranges.
	// It is shared (not copied) with [Footprint.Runes] when the index is
	// attached to a footprint, so queries keep a single backing storage.
	runes RuneSet

	// scripts is the set of scripts deduced from the covered runes.
	scripts ScriptSet

	// langs is the set of languages whose sample runes are fully covered.
	langs LangSet

	// version is the cache generation at which the index was built. It is
	// only meaningful when the index comes from a [CoverageIndexCache].
	version uint64
}

// coverageRange is an inclusive interval of Unicode scalar values.
type coverageRange struct {
	lo, hi rune
}

// NewFontCoverageIndex builds an immutable coverage index from the given
// cmap, which is typically the [font.Font.Cmap] field.
//
// Runes mapped to glyph 0 are not considered covered. Runes outside the
// Unicode scalar value range (surrogates or values above [unicode.MaxRune])
// are ignored, making the result deterministic for corrupt cmaps.
//
// buffer may be provided to reduce allocations when the cmap implements
// [font.CmapRuneRanger]; it is returned to the caller.
func NewFontCoverageIndex(cmap font.Cmap, buffer [][2]rune) (*FontCoverageIndex, [][2]rune, error) {
	runes, scripts, buffer := newCoveragesFromCmap(cmap, buffer)
	ranges, err := normalizedRangesFromRuneSet(runes)
	if err != nil {
		return nil, buffer, err
	}
	return &FontCoverageIndex{
		ranges:  ranges,
		runes:   runes,
		scripts: scripts,
		langs:   newLangsetFromCoverage(runes),
	}, buffer, nil
}

// newFontCoverageIndexFromRunes builds an index from an already computed
// coverage. This is used by the scanner, where [RuneSet] and [ScriptSet] are
// produced together with the language set. Unlike [NewFontCoverageIndex],
// an oversized coverage is truncated deterministically instead of producing
// an error, so a corrupt font does not abort a whole directory scan.
func newFontCoverageIndexFromRunes(id font.FontID, runes RuneSet, scripts ScriptSet, langs LangSet) *FontCoverageIndex {
	ranges, err := normalizedRangesFromRuneSet(runes)
	if err != nil {
		// Keep a bounded but still queryable index: the page-set rune
		// representation is the source of truth for Contains, and the
		// ranges are reduced to the first allowed pages.
		ranges = clampRanges(runes)
	}
	return &FontCoverageIndex{
		id:      id,
		ranges:  ranges,
		runes:   runes,
		scripts: scripts,
		langs:   langs,
	}
}

// FontID returns the identity of the font resource the index was built for.
// The zero [font.FontID] is returned for indexes not attached to a location.
func (idx *FontCoverageIndex) FontID() font.FontID { return idx.id }

// Ranges returns the normalized, sorted and inclusive coverage ranges. The
// returned slice must not be modified.
func (idx *FontCoverageIndex) Ranges() []coverageRange { return idx.ranges }

// RuneSet returns the page-set representation of the coverage. The returned
// value shares its backing storage with the index and must not be mutated.
func (idx *FontCoverageIndex) RuneSet() RuneSet { return idx.runes }

// Scripts returns the scripts deduced from the covered code points.
func (idx *FontCoverageIndex) Scripts() ScriptSet { return idx.scripts }

// Langs returns the languages whose sample runes are fully covered.
func (idx *FontCoverageIndex) Langs() LangSet { return idx.langs }

// Version returns the [CoverageIndexCache] generation at which the index
// was produced. It is 0 for indexes not obtained from a cache.
func (idx *FontCoverageIndex) Version() uint64 { return idx.version }

// Contains reports whether the code point r is covered. Code points that are
// not Unicode scalar values (surrogates or values above [unicode.MaxRune])
// always return false.
func (idx *FontCoverageIndex) Contains(r rune) bool {
	if !isUnicodeScalar(r) {
		return false
	}
	return idx.runes.Contains(r)
}

// CoversRange reports whether every Unicode scalar value in the inclusive
// interval [lo, hi] is covered.
//
// It returns false (never panics) for reversed intervals, for bounds that are
// not Unicode scalar values, and for intervals containing surrogate code
// units: a font can never provide a glyph for a non-scalar code point, so an
// interval that includes one cannot be fully covered. An empty font answers
// false for every interval, including [0, 0].
func (idx *FontCoverageIndex) CoversRange(lo, hi rune) bool {
	if hi < lo || !isUnicodeScalar(lo) || !isUnicodeScalar(hi) {
		return false
	}
	// reject intervals spanning any part of the surrogate block
	if lo <= 0xDFFF && hi >= 0xD800 {
		return false
	}
	if len(idx.ranges) == 0 {
		return false
	}
	return idx.coversScalarRange(lo, hi)
}

// coversScalarRange assumes lo and hi are valid scalar values, lo <= hi.
func (idx *FontCoverageIndex) coversScalarRange(lo, hi rune) bool {
	// locate the first range whose hi >= lo
	i, j := 0, len(idx.ranges)
	for i < j {
		h := int(uint(i+j) >> 1)
		if idx.ranges[h].hi < lo {
			i = h + 1
		} else {
			j = h
		}
	}
	for i < len(idx.ranges) && lo <= hi {
		r := idx.ranges[i]
		if r.lo > lo {
			return false // gap inside the query interval
		}
		if r.hi >= hi {
			return true
		}
		lo = r.hi + 1
		i++
	}
	return lo > hi
}

// CoversScript reports whether the index coverage includes at least one
// code point of the given script. [language.Common], [language.Inherited]
// and [language.Unknown] are handled like any other script.
func (idx *FontCoverageIndex) CoversScript(s language.Script) bool {
	return idx.scripts.contains(s)
}

// CoversLang reports whether the font covers the sample runes defining the
// given language.
func (idx *FontCoverageIndex) CoversLang(l LangID) bool { return idx.langs.Contains(l) }

// CoversCluster reports whether every rune of the cluster is covered. The
// empty cluster returns true: an empty requirement is vacuously satisfied,
// which lets callers combine conjunctions without special casing.
//
// Invalid runes inside the cluster make the answer false, since a font can
// never provide a glyph for a code point that is not a Unicode scalar.
func (idx *FontCoverageIndex) CoversCluster(cluster []rune) bool {
	for _, r := range cluster {
		if !idx.Contains(r) {
			return false
		}
	}
	return true
}

// isUnicodeScalar reports whether r is a Unicode scalar value: it excludes
// surrogates and values above unicode.MaxRune.
func isUnicodeScalar(r rune) bool {
	return r >= 0 && r <= unicode.MaxRune &&
		!(r >= 0xD800 && r <= 0xDFFF)
}

// nextScalar returns the first Unicode scalar value strictly greater than r.
// It returns unicode.MaxRune+1 once the end of the Unicode range is reached,
// so callers always make progress when iterating.
func nextScalar(r rune) rune {
	switch {
	case r < 0xD7FF:
		return r + 1
	case r < 0xE000:
		return 0xE000
	case r < unicode.MaxRune:
		return r + 1
	default:
		return unicode.MaxRune + 1
	}
}

// surrogateStartWithin returns the first surrogate code point in [lo, hi],
// or -1 if the interval contains none.
func surrogateStartWithin(lo, hi rune) rune {
	const surrLo, surrHi = 0xD800, 0xDFFF
	if hi < surrLo || lo > surrHi {
		return -1
	}
	if lo >= surrLo {
		return lo
	}
	return surrLo
}

// normalizedRangesFromRuneSet converts a page-set coverage into a sorted,
// merged list of inclusive scalar-value ranges.
func normalizedRangesFromRuneSet(rs RuneSet) ([]coverageRange, error) {
	return rangesFromRuneSet(rs, maxCoverageRanges)
}

// rangesFromRuneSet is the testable core of [normalizedRangesFromRuneSet]
// with an explicit bound on the produced range count.
func rangesFromRuneSet(rs RuneSet, bound int) ([]coverageRange, error) {
	var out []coverageRange
	for _, page := range rs {
		pageBase := rune(page.ref) << 8
		for j, word := range page.set {
			if word == 0 {
				continue
			}
			wordBase := pageBase | rune(j)<<5
			for word != 0 {
				tz := bits.TrailingZeros32(word)
				first := wordBase + rune(tz)
				word >>= uint(tz)
				run := countTrailingOnes32(word)
				last := first + rune(run) - 1 // run counts the first bit too
				word >>= uint(run)
				appendNormalizedRange(&out, first, last)
				if len(out) > bound {
					return nil, errTooManyRanges
				}
			}
		}
	}
	return out, nil
}

func appendNormalizedRange(out *[]coverageRange, lo, hi rune) {
	// Split the interval into scalar-value sub intervals, handling a
	// possible surrogate span, without iterating over every rune.
	const surrLo, surrHi = 0xD800, 0xDFFF
	for lo <= hi {
		if !isUnicodeScalar(lo) {
			// skip a surrogate prefix in constant time
			if lo >= surrLo && lo <= surrHi {
				lo = surrHi + 1
			} else {
				lo = nextScalar(lo)
			}
			continue
		}
		end := hi
		if s := surrogateStartWithin(lo, hi); s >= 0 {
			end = s - 1
		}
		if end < lo {
			lo = nextScalar(end)
			continue
		}
		if n := len(*out); n > 0 && (*out)[n-1].hi+1 >= lo {
			// merge with the previous range when intervals touch or overlap
			if end > (*out)[n-1].hi {
				(*out)[n-1].hi = end
			}
		} else {
			*out = append(*out, coverageRange{lo: lo, hi: end})
		}
		if end >= hi {
			return
		}
		lo = end + 1
	}
}

// clampRanges is the defensive fallback used when a coverage would exceed
// [maxCoverageRanges]: it returns at most the first bounded ranges, keeping
// the index usable without unbounded memory growth.
func clampRanges(rs RuneSet) []coverageRange {
	out, err := rangesFromRuneSet(rs, maxCoverageRanges-1)
	if err != nil {
		// the overflow leaves exactly maxCoverageRanges entries in out
		if len(out) > maxCoverageRanges {
			out = out[:maxCoverageRanges]
		}
	}
	return out
}

// validateRanges normalizes caller-provided ranges, returning deterministic
// errors for malformed input. It is shared by tests and by constructors
// accepting explicit range data.
func validateRanges(in [][2]rune) ([]coverageRange, error) {
	out := make([]coverageRange, 0, len(in))
	for _, ra := range in {
		lo, hi := ra[0], ra[1]
		if hi < lo {
			return nil, fmt.Errorf("%w: [%d, %d]", errEmptyRange, lo, hi)
		}
		if lo < 0 || hi > unicode.MaxRune {
			return nil, fmt.Errorf("%w: [%d, %d]", errRangeEndTooLarge, lo, hi)
		}
		appendNormalizedRange(&out, lo, hi)
		if len(out) > maxCoverageRanges {
			return nil, errTooManyRanges
		}
	}
	return out, nil
}

// countTrailingOnes32 returns the number of consecutive 1 bits starting at
// bit 0. It is defined as the number of trailing zeros of the complement.
func countTrailingOnes32(v uint32) int {
	return bits.TrailingZeros32(^v)
}

// ---------------- shared, bounded cache ----------------

// CoverageIndexCacheStats reports usage counters of a [CoverageIndexCache].
type CoverageIndexCacheStats struct {
	// Hits is the number of lookups satisfied by a live, current entry.
	Hits uint64
	// Misses is the number of lookups that did not return a cached index.
	Misses uint64
	// Evictions is the number of indexes dropped to respect the capacity.
	Evictions uint64
	// Invalidations is the number of entries (or full generations) dropped
	// through [CoverageIndexCache.Invalidate] or
	// [CoverageIndexCache.InvalidateAll].
	Invalidations uint64
	// Size is the number of indexes currently held.
	Size int
	// Capacity is the maximum number of indexes the cache may hold.
	Capacity int
}

// coverageCacheEntry is one node of the cache LRU list.
type coverageCacheEntry struct {
	next, prev *coverageCacheEntry
	key        font.FontID
	idx        *FontCoverageIndex
}

// CoverageIndexCache is a bounded, goroutine-safe cache of immutable
// [FontCoverageIndex] values keyed by font identity.
//
// Entries are evicted least-recently-used once the configured capacity is
// reached, and a version generation is bumped on every explicit
// invalidation so indexes produced before a file change can be detected as
// stale even if a new entry temporarily reuses the same key.
type CoverageIndexCache struct {
	mu sync.RWMutex

	entries    map[font.FontID]*coverageCacheEntry
	head, tail *coverageCacheEntry

	// maxSize <= 0 means the default capacity is used.
	maxSize int

	generation uint64

	stats CoverageIndexCacheStats
}

// DefaultCoverageIndexCapacity is the capacity used by caches created
// without an explicit size and by the package-global shared cache.
const DefaultCoverageIndexCapacity = 4096

// NewCoverageIndexCache creates a cache holding at most maxSize indexes. A
// non-positive value selects [DefaultCoverageIndexCapacity].
func NewCoverageIndexCache(maxSize int) *CoverageIndexCache {
	c := &CoverageIndexCache{maxSize: maxSize}
	c.reset()
	return c
}

func (c *CoverageIndexCache) reset() {
	if c.maxSize <= 0 {
		c.maxSize = DefaultCoverageIndexCapacity
	}
	c.entries = make(map[font.FontID]*coverageCacheEntry)
	c.head = new(coverageCacheEntry)
	c.tail = new(coverageCacheEntry)
	c.head.prev = c.tail
	c.tail.next = c.head
}

// capacity returns the configured maximum number of cached indexes.
func (c *CoverageIndexCache) capacity() int {
	if c.maxSize <= 0 {
		return DefaultCoverageIndexCapacity
	}
	return c.maxSize
}

// Lookup returns the cached index for id together with its generation, or
// nil if no current entry exists.
func (c *CoverageIndexCache) Lookup(id font.FontID) (*FontCoverageIndex, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[id]; ok {
		c.remove(entry)
		c.insert(entry)
		c.stats.Hits++
		return entry.idx, true
	}
	c.stats.Misses++
	return nil, false
}

// Put stores idx under its [FontCoverageIndex.FontID], evicting the oldest
// entry when the cache is full. The stored index is stamped with the current
// cache generation. Put with the zero [font.FontID] is ignored, since such
// indexes cannot be looked up deterministically.
func (c *CoverageIndexCache) Put(idx *FontCoverageIndex) {
	if idx == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.reset()
	}
	key := idx.id
	if key == (font.FontID{}) {
		return
	}
	if previous := c.entries[key]; previous != nil {
		c.remove(previous)
	} else if len(c.entries) >= c.capacity() {
		oldest := c.tail.next
		c.remove(oldest)
		delete(c.entries, oldest.key)
		c.stats.Evictions++
	}
	stamped := *idx
	stamped.version = c.generation
	entry := &coverageCacheEntry{key: key, idx: &stamped}
	c.entries[key] = entry
	c.insert(entry)
}

// Invalidate drops the entry for id, if any. It is used when a font file is
// removed or modified so that subsequent lookups cannot observe coverage
// data computed from the old content.
func (c *CoverageIndexCache) Invalidate(id font.FontID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[id]; ok {
		c.remove(entry)
		delete(c.entries, id)
		c.stats.Invalidations++
	}
}

// InvalidateAll clears every entry and bumps the generation, rendering all
// previously handed out indexes detectable as stale through
// [FontCoverageIndex.Version].
func (c *CoverageIndexCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.reset()
	c.stats.Invalidations++
}

// Generation returns the current version stamp. Combined with
// [FontCoverageIndex.Version], callers can tell whether an index was built
// from the current cache content.
func (c *CoverageIndexCache) Generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.generation
}

// SetCapacity resizes the cache. Entries beyond the new capacity are
// evicted immediately.
func (c *CoverageIndexCache) SetCapacity(maxSize int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxSize <= 0 {
		maxSize = DefaultCoverageIndexCapacity
	}
	c.maxSize = maxSize
	for len(c.entries) > c.maxSize {
		oldest := c.tail.next
		c.remove(oldest)
		delete(c.entries, oldest.key)
		c.stats.Evictions++
	}
}

// Stats returns a point-in-time snapshot of the cache counters.
func (c *CoverageIndexCache) Stats() CoverageIndexCacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	stats := c.stats
	stats.Size = len(c.entries)
	stats.Capacity = c.capacity()
	return stats
}

func (c *CoverageIndexCache) remove(e *coverageCacheEntry) {
	e.next.prev = e.prev
	e.prev.next = e.next
}

func (c *CoverageIndexCache) insert(e *coverageCacheEntry) {
	e.next = c.head
	e.prev = c.head.prev
	e.prev.next = e
	e.next.prev = e
}

// defaultCoverageIndexCache is shared by all font maps and scans: building
// an index once for a font identity makes it reusable across the process.
// Access goes through [sharedCoverageCache] so swapping the cache stays safe
// for concurrent scans and queries.
var (
	defaultCoverageIndexCache   = NewCoverageIndexCache(DefaultCoverageIndexCapacity)
	defaultCoverageIndexCacheMu sync.RWMutex
)

func sharedCoverageCache() *CoverageIndexCache {
	defaultCoverageIndexCacheMu.RLock()
	defer defaultCoverageIndexCacheMu.RUnlock()
	return defaultCoverageIndexCache
}

// SharedCoverageIndexCache returns the process-wide cache used when scanning
// system fonts and deserializing footprint indexes.
func SharedCoverageIndexCache() *CoverageIndexCache { return sharedCoverageCache() }

// SetSharedCoverageIndexCache replaces the process-wide cache. Passing nil
// restores a fresh cache with the default capacity. It is mainly intended
// for tests and for applications that want to tune the global capacity.
func SetSharedCoverageIndexCache(c *CoverageIndexCache) {
	if c == nil {
		c = NewCoverageIndexCache(DefaultCoverageIndexCapacity)
	}
	defaultCoverageIndexCacheMu.Lock()
	defaultCoverageIndexCache = c
	defaultCoverageIndexCacheMu.Unlock()
}

// attachCoverageIndex ensures fp carries a shared index, reusing the shared
// cache when an entry for its location already exists. It is called both
// after a fresh scan and after deserialization: identical font identities
// therefore resolve to the same immutable index.
func attachCoverageIndex(fp *Footprint) *FontCoverageIndex {
	cache := sharedCoverageCache()
	if idx, ok := cache.Lookup(fp.Location); ok {
		fp.coverage = idx
		return idx
	}
	idx := newFontCoverageIndexFromRunes(fp.Location, fp.Runes, fp.Scripts, fp.Langs)
	cache.Put(idx)
	fp.coverage = idx
	return idx
}
