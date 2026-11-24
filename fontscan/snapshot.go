package fontscan

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// This file implements FontScanSnapshot, a versioned, self-describing
// serialization of the system font index.
//
// Unlike the legacy gzip stream (see [systemFontsIndex.serializeTo]), a snapshot
// carries a magic number, a format version, a payload checksum and length
// prefixed, tag identified records. Unknown top level records and unknown entry
// fields are skipped instead of aborting the decode, which lets older readers
// load files produced by newer writers as long as the fields they know are
// present. Corruption, truncation and checksum mismatches are detected and
// reported with the offending file path, and never silently replace a valid
// in-memory index.

// On-disk layout (all integers big endian):
//
//	+-------------------- header (24 bytes) --------------------+
//	| magic  [4]byte = "FTSN"                                    |
//	| version uint16 (snapshotFormatVersion)                     |
//	| flags   uint16 (bit 0: payload is gzip compressed)         |
//	| payloadLen  uint32 (length of the payload section below)   |
//	| payloadCRC  uint32 (IEEE CRC-32 of the payload section)    |
//	| reserved    uint64 (0)                                     |
//	+-------------------------------------------------------------+
//	| payload, optionally gzip compressed: a sequence of records  |
//	|   record  = tag uint16 | length uint32 | data [length]      |
//	+-------------------------------------------------------------+
//
// The payload contains a snapshotRecordEntries top level record, whose data
// is a concatenation of snapshotTagEntry wrappers. Each wrapper holds a field
// count (uint32) followed by that many TLV fields (path, size, modification
// time and one field per footprint), so unknown fields can be skipped.

const (
	snapshotMagic = "FTSN"

	// snapshotFormatVersion is the version of the framing understood by this
	// package. It is independent from [cacheFormatVersion], which identifies
	// the legacy index format: bumping either one invalidates the other.
	snapshotFormatVersion uint16 = 1

	snapshotHeaderSize = 4 + 2 + 2 + 4 + 4 + 8 // 24 bytes

	// snapshotFlagCompressed marks a gzip compressed payload.
	snapshotFlagCompressed uint16 = 1 << 0

	// Defensive limits, protecting the reader from malformed or hostile files.
	snapshotMaxPayloadSize = 512 << 20 // 512 MiB decompressed
	snapshotMaxRecordCount = 1 << 20   // entries and top level records
	snapshotMaxFieldSize   = 64 << 20  // 64 MiB for a single TLV field
	snapshotMaxFaces       = 1 << 16   // faces per font file
	snapshotMaxPathLength  = 1 << 16

	// snapshotUnknownSize marks entries converted from a legacy cache, which
	// did not record file sizes. Such entries are validated by modification
	// time only.
	snapshotUnknownSize int64 = -1
)

// Top level record tags.
const (
	// snapshotRecordEntries marks the top level record holding every font file
	// entry. Its data is a concatenation of entry records, each encoded as
	// tag [snapshotTagEntry] | length | field count + TLV fields.
	snapshotRecordEntries uint16 = 1
	// snapshotTagEntry wraps a single font file entry inside the entries
	// record.
	snapshotTagEntry uint16 = 2
)

// Entry field tags.
const (
	snapshotFieldPath      uint16 = 1 // length prefixed path, slash separated
	snapshotFieldSize      uint16 = 2 // int64 file size
	snapshotFieldModTime   uint16 = 3 // int64 modification time, Unix nanoseconds (UTC based)
	snapshotFieldFootprint uint16 = 4 // one serialized Footprint
)

// Sentinel errors describing why a snapshot could not be loaded. They are
// always wrapped with the cache file path (and, where relevant, the offending
// font path) so callers can log actionable diagnostics.
var (
	// ErrSnapshotFormat is returned for files that are not recognizable
	// snapshots: bad magic, unsupported version or structurally invalid data.
	ErrSnapshotFormat = errors.New("invalid font scan snapshot format")
	// ErrSnapshotTruncated is returned when the file ends before all the
	// advertised data has been read.
	ErrSnapshotTruncated = errors.New("truncated font scan snapshot")
	// ErrSnapshotChecksum is returned when the payload checksum does not match
	// the value stored in the header.
	ErrSnapshotChecksum = errors.New("font scan snapshot checksum mismatch")
	// ErrSnapshotTooLarge is returned when the file, a record or a field
	// exceeds the built-in safety limits.
	ErrSnapshotTooLarge = errors.New("font scan snapshot exceeds size limits")
	// ErrSnapshotDuplicate is returned when the same font path appears in more
	// than one entry.
	ErrSnapshotDuplicate = errors.New("duplicate font path in font scan snapshot")
)

// SnapshotEntry is the versioned summary of one font file. Paths use slash
// separators regardless of the host operating system, and modification times
// are stored as Unix nanoseconds, so the representation is independent from
// path separators and time zones.
type SnapshotEntry struct {
	// Path identifies the font file, using slash separators.
	Path string
	// Size is the file size in bytes. A negative value means unknown,
	// which only occurs for entries converted from the legacy cache format.
	Size int64
	// ModTime is the file modification time, expressed as Unix nanoseconds.
	ModTime int64
	// Footprints are the font faces contained in the file, in collection
	// order.
	Footprints []Footprint
}

// FontScanSnapshot is an immutable, versioned snapshot of a font scan.
// A zero value is not a valid snapshot; build one with [NewFontScanSnapshot]
// or load one with [LoadFontScanSnapshot]. The exported fields are a
// read-only view and must not be modified while the snapshot is in use.
//
// FontScanSnapshot values are safe for concurrent read-only use.
type FontScanSnapshot struct {
	// Version is the snapshot format version the data was written with.
	Version uint16
	// Entries are the scanned font files, sorted by [SnapshotEntry.Path].
	Entries []SnapshotEntry

	// byPath indexes Entries by canonical path; it is populated at load time
	// and never mutated afterwards, which makes concurrent reads lock free.
	byPath map[string]int
}

// NewFontScanSnapshot validates the given entries and returns an immutable
// snapshot. Entries are copied and sorted by path, and duplicate paths are
// rejected. A nil entry set produces a valid empty snapshot.
func NewFontScanSnapshot(entries []SnapshotEntry) (*FontScanSnapshot, error) {
	snap := &FontScanSnapshot{
		Version: snapshotFormatVersion,
		Entries: make([]SnapshotEntry, len(entries)),
		byPath:  make(map[string]int, len(entries)),
	}
	copy(snap.Entries, entries)
	for i := range snap.Entries {
		snap.Entries[i].Path = canonicalSnapshotPath(snap.Entries[i].Path)
		// deep copy the footprints so later mutations of the caller's slice
		// cannot reach the supposedly immutable snapshot
		if fps := snap.Entries[i].Footprints; fps != nil {
			snap.Entries[i].Footprints = append([]Footprint(nil), fps...)
		}
	}
	sort.SliceStable(snap.Entries, func(i, j int) bool {
		return snap.Entries[i].Path < snap.Entries[j].Path
	})
	for i, entry := range snap.Entries {
		if entry.Path == "" {
			return nil, fmt.Errorf("%w: entry %d has an empty path", ErrSnapshotFormat, i)
		}
		if len(entry.Path) > snapshotMaxPathLength {
			return nil, fmt.Errorf("%w: path %q is longer than %d bytes", ErrSnapshotTooLarge, entry.Path, snapshotMaxPathLength)
		}
		if len(entry.Footprints) > snapshotMaxFaces {
			return nil, fmt.Errorf("%w: %q declares %d faces", ErrSnapshotTooLarge, entry.Path, len(entry.Footprints))
		}
		if _, exists := snap.byPath[entry.Path]; exists {
			return nil, fmt.Errorf("%w: %q", ErrSnapshotDuplicate, entry.Path)
		}
		snap.byPath[entry.Path] = i
	}
	return snap, nil
}

// Entry returns the entry for the given file path and true if it is part of
// the snapshot. The path may use the host separator; it is canonicalized the
// same way as during the scan. The returned entry must not be modified.
func (snap *FontScanSnapshot) Entry(path string) (SnapshotEntry, bool) {
	idx, ok := snap.byPath[canonicalSnapshotPath(path)]
	if !ok {
		return SnapshotEntry{}, false
	}
	return snap.Entries[idx], true
}

// Lookup reports whether the snapshot contains an up-to-date entry for path,
// that is an entry whose size and modification time match the file info.
// Entries without a known size (legacy conversions) fall back to the
// modification time only. On a match the cached entry is returned.
func (snap *FontScanSnapshot) Lookup(path string, size int64, modTime time.Time) (SnapshotEntry, bool) {
	entry, ok := snap.Entry(path)
	if !ok {
		return SnapshotEntry{}, false
	}
	if entry.ModTime != modTime.UnixNano() {
		return SnapshotEntry{}, false
	}
	if entry.Size != snapshotUnknownSize && entry.Size != size {
		return SnapshotEntry{}, false
	}
	return entry, true
}

// Len returns the number of font file entries held by the snapshot.
func (snap *FontScanSnapshot) Len() int { return len(snap.Entries) }

// Save atomically writes the snapshot to cachePath. The destination directory
// is created if needed. On failure the previous file at cachePath, if any, is
// left untouched: the new content is first written to a temporary file in the
// same directory, checksummed and only then renamed into place.
func (snap *FontScanSnapshot) Save(cachePath string) error {
	return snap.saveAt(cachePath, nil)
}

// tempFileOpener creates a writable temporary file next to the final
// destination. It is an indirection used by the tests to inject write
// failures; production code always passes nil.
type tempFileOpener func(dir, pattern string) (writeSyncCloser, string, error)

type writeSyncCloser interface {
	io.Writer
	Sync() error
	Close() error
}

func defaultTempFileOpener(dir, pattern string) (writeSyncCloser, string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	return f, f.Name(), nil
}

// saveAt is [FontScanSnapshot.Save] with an injectable temporary file opener.
func (snap *FontScanSnapshot) saveAt(cachePath string, opener tempFileOpener) (err error) {
	if opener == nil {
		opener = defaultTempFileOpener
	}

	dir, _ := filepath.Split(cachePath)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating snapshot directory %q: %w", dir, err)
	}

	payload, err := snap.encodePayload()
	if err != nil {
		return err
	}
	frame, err := encodeSnapshotFrame(payload)
	if err != nil {
		return err
	}

	tmp, tmpPath, err := opener(dir, ".font-scan-snapshot-*")
	if err != nil {
		return fmt.Errorf("creating temporary snapshot next to %q: %w", cachePath, err)
	}
	// On any error before the rename, remove the partial temporary file so a
	// failed save can never be mistaken for a valid snapshot.
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	// checkedWriter turns short writes into explicit errors instead of silently
	// truncating the frame.
	if _, err = io.Copy(&checkedWriter{w: tmp, path: tmpPath}, bytes.NewReader(frame)); err != nil {
		return fmt.Errorf("writing snapshot %q: %w", cachePath, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("syncing snapshot %q: %w", cachePath, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("closing snapshot %q: %w", cachePath, err)
	}
	if err = os.Rename(tmpPath, cachePath); err != nil {
		return fmt.Errorf("committing snapshot %q: %w", cachePath, err)
	}
	committed = true
	return nil
}

// checkedWriter fails when an underlying Write accepts fewer bytes than
// requested without reporting an error.
type checkedWriter struct {
	w    io.Writer
	path string
}

func (c *checkedWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, fmt.Errorf("short write to %q: %d/%d bytes", c.path, n, len(p))
	}
	return n, nil
}

// LoadFontScanSnapshot reads and validates the snapshot stored at cachePath.
// Files written by the legacy, unversioned index serialization remain
// supported and are converted on the fly.
//
// A missing file returns an error wrapping [os.ErrNotExist]. Any other failure
// wraps one of the snapshot sentinel errors together with cachePath, leaving
// any previously loaded index untouched.
func LoadFontScanSnapshot(logger Logger, cachePath, legacyCachePath string) (*FontScanSnapshot, error) {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) || legacyCachePath == "" || legacyCachePath == cachePath {
			return nil, err
		}
		// First run after the upgrade: transparently read the legacy cache.
		return loadLegacySnapshot(logger, cachePath, legacyCachePath)
	}
	snap, err := parseSnapshot(data)
	if err != nil {
		return nil, fmt.Errorf("loading snapshot %q: %w", cachePath, err)
	}
	return snap, nil
}

// loadLegacySnapshot converts a legacy gzip index file into a snapshot.
func loadLegacySnapshot(logger Logger, snapshotPath, legacyPath string) (*FontScanSnapshot, error) {
	index, err := deserializeIndexFile(legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Surface the missing *new* path so callers see the expected
			// fs.ErrNotExist on a genuine first run.
			return nil, &os.PathError{Op: "open", Path: snapshotPath, Err: os.ErrNotExist}
		}
		return nil, fmt.Errorf("loading legacy index %q: %w", legacyPath, err)
	}
	if logger != nil {
		logger.Printf("loaded legacy font index %q; converting to versioned snapshot", legacyPath)
	}
	return newSnapshotFromIndex(index), nil
}

// canonicalSnapshotPath normalizes a file system path for stable storage and
// lookup: Windows style backslashes are converted to slashes so the same file
// produces the same key on every platform. The path is otherwise stored as
// given, since scans already work with absolute paths.
func canonicalSnapshotPath(path string) string {
	if filepath.Separator == '/' {
		return path
	}
	return filepath.ToSlash(path)
}

// hostPath reverses [canonicalSnapshotPath].
func hostPath(path string) string {
	if filepath.Separator == '/' {
		return path
	}
	return filepath.FromSlash(path)
}

// ---------------------------- TLV primitives ----------------------------

// appendTLV encodes tag and data as a length prefixed record.
func appendTLV(dst []byte, tag uint16, data []byte) []byte {
	var head [6]byte
	binary.BigEndian.PutUint16(head[:], tag)
	binary.BigEndian.PutUint32(head[2:], uint32(len(data)))
	dst = append(dst, head[:]...)
	return append(dst, data...)
}

func appendInt64Field(dst []byte, tag uint16, v int64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	return appendTLV(dst, tag, buf[:])
}

// readTLV returns the tag, the field data and the number of bytes consumed
// from src. Unknown fields are skipped by the caller using the returned
// length.
func readTLV(src []byte) (tag uint16, data []byte, n int, err error) {
	if len(src) < 6 {
		return 0, nil, 0, ErrSnapshotTruncated
	}
	tag = binary.BigEndian.Uint16(src)
	length := int(binary.BigEndian.Uint32(src[2:]))
	if length > snapshotMaxFieldSize || 6+length > len(src) {
		if length > snapshotMaxFieldSize {
			return 0, nil, 0, fmt.Errorf("%w: field tag %d length %d", ErrSnapshotTooLarge, tag, length)
		}
		return 0, nil, 0, fmt.Errorf("%w: field tag %d needs %d bytes, %d available", ErrSnapshotTruncated, tag, length, len(src)-6)
	}
	return tag, src[6 : 6+length], 6 + length, nil
}

func readInt64Field(data []byte) (int64, error) {
	if len(data) != 8 {
		return 0, fmt.Errorf("%w: expected 8 byte integer, got %d", ErrSnapshotFormat, len(data))
	}
	return int64(binary.BigEndian.Uint64(data)), nil
}

// ---------------------------- encoding ----------------------------

// encodePayload builds the (uncompressed) TLV payload for the snapshot.
func (snap *FontScanSnapshot) encodePayload() ([]byte, error) {
	if len(snap.Entries) > snapshotMaxRecordCount {
		return nil, fmt.Errorf("%w: %d entries", ErrSnapshotTooLarge, len(snap.Entries))
	}

	// One top level record holds all entries, itself framed as a TLV so that
	// future writers may add other top level records.
	var entriesBlob bytes.Buffer
	for _, entry := range snap.Entries {
		if err := encodeEntry(&entriesBlob, entry); err != nil {
			return nil, err
		}
	}

	payload := appendTLV(nil, snapshotRecordEntries, entriesBlob.Bytes())
	if len(payload) > snapshotMaxPayloadSize {
		return nil, fmt.Errorf("%w: payload is %d bytes", ErrSnapshotTooLarge, len(payload))
	}
	return payload, nil
}

func encodeEntry(dst *bytes.Buffer, entry SnapshotEntry) error {
	// A field count prefix lets readers iterate without relying on the blob end.
	var fields [4]byte
	fieldCount := 3 + len(entry.Footprints) // path, size, modtime + faces
	if fieldCount > snapshotMaxRecordCount {
		return fmt.Errorf("%w: %q declares %d faces", ErrSnapshotTooLarge, entry.Path, fieldCount-3)
	}
	binary.BigEndian.PutUint32(fields[:], uint32(fieldCount))

	var blob bytes.Buffer
	blob.Write(fields[:])
	blob.Write(appendTLV(nil, snapshotFieldPath, []byte(entry.Path)))
	blob.Write(appendInt64Field(nil, snapshotFieldSize, entry.Size))
	blob.Write(appendInt64Field(nil, snapshotFieldModTime, entry.ModTime))
	for i := range entry.Footprints {
		blob.Write(appendTLV(nil, snapshotFieldFootprint, entry.Footprints[i].serializeTo(nil)))
	}

	// wrap the entry so it can be skipped wholesale by an older reader
	record := appendTLV(nil, snapshotTagEntry, blob.Bytes())
	if _, err := dst.Write(record); err != nil {
		return fmt.Errorf("encoding snapshot entry %q: %w", entry.Path, err)
	}
	return nil
}

// encodeSnapshotFrame wraps the payload with the checksummed header and gzip
// compression.
func encodeSnapshotFrame(payload []byte) ([]byte, error) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(payload); err != nil {
		return nil, fmt.Errorf("compressing snapshot payload: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compressing snapshot payload: %w", err)
	}

	frame := make([]byte, snapshotHeaderSize, snapshotHeaderSize+compressed.Len())
	copy(frame, snapshotMagic)
	binary.BigEndian.PutUint16(frame[4:], snapshotFormatVersion)
	binary.BigEndian.PutUint16(frame[6:], snapshotFlagCompressed)
	binary.BigEndian.PutUint32(frame[8:], uint32(compressed.Len()))
	binary.BigEndian.PutUint32(frame[12:], crc32.ChecksumIEEE(payload))
	// frame[16:24] reserved, already zero
	return append(frame, compressed.Bytes()...), nil
}

// ---------------------------- decoding ----------------------------

// parseSnapshot decodes a complete snapshot file.
func parseSnapshot(file []byte) (*FontScanSnapshot, error) {
	if len(file) < snapshotHeaderSize {
		return nil, fmt.Errorf("%w: %d byte header expected, got %d", ErrSnapshotTruncated, snapshotHeaderSize, len(file))
	}
	if string(file[:4]) != snapshotMagic {
		return nil, fmt.Errorf("%w: bad magic %q", ErrSnapshotFormat, string(file[:4]))
	}
	version := binary.BigEndian.Uint16(file[4:])
	if version != snapshotFormatVersion {
		return nil, fmt.Errorf("%w: unsupported version %d (want %d)", ErrSnapshotFormat, version, snapshotFormatVersion)
	}
	flags := binary.BigEndian.Uint16(file[6:])
	payloadLen := int(binary.BigEndian.Uint32(file[8:]))
	wantCRC := binary.BigEndian.Uint32(file[12:])
	// reserved bytes must be zero in version 1; a non-zero value means a newer
	// meaning this reader cannot interpret safely.
	if binary.BigEndian.Uint64(file[16:]) != 0 {
		return nil, fmt.Errorf("%w: nonzero reserved header fields", ErrSnapshotFormat)
	}
	if payloadLen > snapshotMaxPayloadSize {
		return nil, fmt.Errorf("%w: payload length %d", ErrSnapshotTooLarge, payloadLen)
	}
	if snapshotHeaderSize+payloadLen != len(file) {
		return nil, fmt.Errorf("%w: header advertises %d payload bytes, file has %d",
			ErrSnapshotTruncated, payloadLen, len(file)-snapshotHeaderSize)
	}

	payload, err := decodeSnapshotPayload(flags, file[snapshotHeaderSize:])
	if err != nil {
		return nil, err
	}
	if got := crc32.ChecksumIEEE(payload); got != wantCRC {
		return nil, fmt.Errorf("%w: computed %#08x, header has %#08x", ErrSnapshotChecksum, got, wantCRC)
	}

	return parseSnapshotEntries(payload)
}

func decodeSnapshotPayload(flags uint16, compressed []byte) ([]byte, error) {
	if flags&snapshotFlagCompressed == 0 {
		return compressed, nil
	}
	if flags&^snapshotFlagCompressed != 0 {
		return nil, fmt.Errorf("%w: unknown header flags %#04x", ErrSnapshotFormat, flags)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSnapshotFormat, err)
	}
	defer zr.Close()

	// LimitReader bounds decompression against zip bomb style inputs.
	payload, err := io.ReadAll(io.LimitReader(zr, snapshotMaxPayloadSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSnapshotTruncated, err)
	}
	if len(payload) > snapshotMaxPayloadSize {
		return nil, fmt.Errorf("%w: decompressed payload exceeds %d bytes", ErrSnapshotTooLarge, snapshotMaxPayloadSize)
	}
	return payload, nil
}

// parseSnapshotEntries walks the top level TLV records, skipping unknown ones.
func parseSnapshotEntries(payload []byte) (*FontScanSnapshot, error) {
	snap := &FontScanSnapshot{
		Version: snapshotFormatVersion,
		byPath:  make(map[string]int),
	}
	// seenPaths detects duplicates while entries are being decoded, before
	// byPath is populated at the end of the parse.
	seenPaths := make(map[string]bool)
	recordCount := 0
	for len(payload) > 0 {
		tag, data, n, err := readTLV(payload)
		if err != nil {
			return nil, err
		}
		payload = payload[n:]
		recordCount++
		if recordCount > snapshotMaxRecordCount {
			return nil, fmt.Errorf("%w: more than %d top level records", ErrSnapshotTooLarge, snapshotMaxRecordCount)
		}
		if tag != snapshotRecordEntries {
			continue // forward compatibility: ignore records this version does not know
		}
		if err := parseEntriesRecord(data, snap, seenPaths); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(snap.Entries, func(i, j int) bool {
		return snap.Entries[i].Path < snap.Entries[j].Path
	})
	for i, entry := range snap.Entries {
		snap.byPath[entry.Path] = i
	}
	return snap, nil
}

// parseEntriesRecord decodes every entry stored inside the entries top level
// record, skipping unknown entry wrappers.
func parseEntriesRecord(blob []byte, snap *FontScanSnapshot, seenPaths map[string]bool) error {
	entryCount := 0
	for len(blob) > 0 {
		tag, data, n, err := readTLV(blob)
		if err != nil {
			return err
		}
		blob = blob[n:]
		if tag != snapshotTagEntry {
			continue // forward compatibility: unknown entry kinds are skipped
		}
		entryCount++
		if entryCount > snapshotMaxRecordCount {
			return fmt.Errorf("%w: more than %d entries", ErrSnapshotTooLarge, snapshotMaxRecordCount)
		}
		if err := parseEntry(data, snap, seenPaths); err != nil {
			return err
		}
	}
	return nil
}

// parseEntry decodes one font file entry: a field count followed by TLV fields.
func parseEntry(blob []byte, snap *FontScanSnapshot, seenPaths map[string]bool) error {
	if len(blob) < 4 {
		return fmt.Errorf("%w: entry record is missing its field count", ErrSnapshotTruncated)
	}
	fieldCount := int(binary.BigEndian.Uint32(blob[:4]))
	blob = blob[4:]
	if fieldCount > snapshotMaxRecordCount {
		return fmt.Errorf("%w: entry declares %d fields", ErrSnapshotTooLarge, fieldCount)
	}

	var (
		entry          SnapshotEntry
		hasPath, hasMT bool
		sizeFieldSeen  bool
		footprintCount int
	)
	for i := 0; i < fieldCount; i++ {
		tag, data, n, err := readTLV(blob)
		if err != nil {
			return err
		}
		blob = blob[n:]
		switch tag {
		case snapshotFieldPath:
			if len(data) == 0 {
				return fmt.Errorf("%w: entry with an empty path", ErrSnapshotFormat)
			}
			if len(data) > snapshotMaxPathLength {
				return fmt.Errorf("%w: path length %d", ErrSnapshotTooLarge, len(data))
			}
			entry.Path = string(data)
			hasPath = true
		case snapshotFieldSize:
			v, err := readInt64Field(data)
			if err != nil {
				return err
			}
			entry.Size = v
			sizeFieldSeen = true
		case snapshotFieldModTime:
			v, err := readInt64Field(data)
			if err != nil {
				return err
			}
			entry.ModTime = v
			hasMT = true
		case snapshotFieldFootprint:
			if footprintCount >= snapshotMaxFaces {
				return fmt.Errorf("%w: more than %d faces for %q", ErrSnapshotTooLarge, snapshotMaxFaces, entry.Path)
			}
			var fp Footprint
			read, err := fp.deserializeFrom(data)
			if err != nil {
				return fmt.Errorf("%w: footprint for %q: %s", ErrSnapshotFormat, entry.Path, err)
			}
			if read != len(data) {
				return fmt.Errorf("%w: trailing %d bytes in footprint for %q", ErrSnapshotFormat, len(data)-read, entry.Path)
			}
			entry.Footprints = append(entry.Footprints, fp)
			footprintCount++
		default:
			// forward compatibility: unknown entry fields are skipped.
		}
	}
	if len(blob) != 0 {
		return fmt.Errorf("%w: %d trailing bytes in entry %q", ErrSnapshotFormat, len(blob), entry.Path)
	}
	if !hasPath {
		return fmt.Errorf("%w: entry is missing its path", ErrSnapshotFormat)
	}
	if !hasMT {
		return fmt.Errorf("%w: entry %q is missing its modification time", ErrSnapshotFormat, entry.Path)
	}
	if !sizeFieldSeen {
		return fmt.Errorf("%w: entry %q is missing its size", ErrSnapshotFormat, entry.Path)
	}
	if seenPaths[entry.Path] {
		return fmt.Errorf("%w: %q", ErrSnapshotDuplicate, entry.Path)
	}
	seenPaths[entry.Path] = true
	snap.Entries = append(snap.Entries, entry)
	return nil
}

// ---------------------------- index conversion ----------------------------

// newSnapshotFromIndex converts an in-memory legacy index into a validated
// snapshot. Entries loaded through [deserializeIndexFile] carry
// [snapshotUnknownSize], because the legacy serialization did not record file
// sizes; such entries are validated by modification time only.
func newSnapshotFromIndex(index systemFontsIndex) *FontScanSnapshot {
	snap, err := NewFontScanSnapshot(snapshotEntriesFromIndex(index))
	if err != nil {
		// Indexes produced by this package always satisfy the invariants.
		panic(fmt.Sprintf("fontscan: internal index is not snapshotable: %v", err))
	}
	return snap
}

// toSystemFontsIndex converts a snapshot back into the index consumed by the
// scanner, restoring host specific path separators.
func (snap *FontScanSnapshot) toSystemFontsIndex() systemFontsIndex {
	out := make(systemFontsIndex, 0, len(snap.Entries))
	for _, entry := range snap.Entries {
		ff := fileFootprints{
			path:       hostPath(entry.Path),
			size:       entry.Size,
			modTime:    timeStamp(entry.ModTime),
			footprints: append([]Footprint(nil), entry.Footprints...),
		}
		out = append(out, ff)
	}
	return out
}

// snapshotEntriesFromIndex is the scan-time conversion used when saving a
// freshly scanned index.
func snapshotEntriesFromIndex(index systemFontsIndex) []SnapshotEntry {
	entries := make([]SnapshotEntry, 0, len(index))
	for _, file := range index {
		entries = append(entries, SnapshotEntry{
			Path:       canonicalSnapshotPath(file.path),
			Size:       file.size,
			ModTime:    int64(file.modTime),
			Footprints: append([]Footprint(nil), file.footprints...),
		})
	}
	return entries
}
