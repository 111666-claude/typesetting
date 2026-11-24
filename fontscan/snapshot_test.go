package fontscan

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	tu "github.com/go-text/typesetting/testutils"
)

// ---------------------------- fixtures ----------------------------

// scanFixtureDir copies the given font files into a fresh temporary directory
// and returns it together with a full, reference scan of that directory.
func scanFixtureDir(t *testing.T, files map[string]string) (string, systemFontsIndex) {
	t.Helper()
	dir := t.TempDir()
	for dstName, srcName := range files {
		copyFile(t, srcName, filepath.Join(dir, dstName))
	}
	logger := log.New(io.Discard, "", 0)
	index, err := scanFontFootprints(logger, nil, dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, len(index) == len(files), fmt.Sprintf("expected %d entries, got %d", len(files), len(index)))
	return dir, index
}

func fixtureFonts() map[string]string {
	return map[string]string{
		"font1.ttf": filepath.Join("..", "font", "testdata", "Amiri-Regular.ttf"),
		"font2.ttf": filepath.Join("..", "font", "testdata", "Roboto-Regular.ttf"),
	}
}

func assertIndexEquals(t *testing.T, expected, got systemFontsIndex) {
	t.Helper()
	if err := assertFontsetEquals(expected.flatten(), got.flatten()); err != nil {
		t.Fatalf("index mismatch: %s", err)
	}
	// also compare the per-file metadata
	tu.AssertC(t, len(expected) == len(got), fmt.Sprintf("entry count: expected %d, got %d", len(expected), len(got)))
	for i := range expected {
		e, g := expected[i], got[i]
		tu.AssertC(t, e.path == g.path, fmt.Sprintf("path: %q != %q", e.path, g.path))
		tu.AssertC(t, e.modTime == g.modTime, fmt.Sprintf("mtime mismatch for %q", e.path))
		tu.AssertC(t, e.size == g.size, fmt.Sprintf("size mismatch for %q: %d != %d", e.path, e.size, g.size))
	}
}

// snapshotFromIndex builds a snapshot the same way the production code does.
func mustSnapshot(t *testing.T, index systemFontsIndex) *FontScanSnapshot {
	t.Helper()
	snap, err := NewFontScanSnapshot(snapshotEntriesFromIndex(index))
	tu.AssertNoErr(t, err)
	return snap
}

func saveAndReload(t *testing.T, snap *FontScanSnapshot, cachePath string) *FontScanSnapshot {
	t.Helper()
	tu.AssertNoErr(t, snap.Save(cachePath))
	loaded, err := LoadFontScanSnapshot(log.New(io.Discard, "", 0), cachePath, "")
	tu.AssertNoErr(t, err)
	return loaded
}

// ---------------------------- round trip ----------------------------

func TestSnapshotRoundTrip(t *testing.T) {
	dir, index := scanFixtureDir(t, fixtureFonts())

	snap := mustSnapshot(t, index)
	tu.AssertC(t, snap.Len() == 2, fmt.Sprintf("expected 2 entries, got %d", snap.Len()))

	cachePath := filepath.Join(t.TempDir(), "nested", "snapshot.cache")
	loaded := saveAndReload(t, snap, cachePath)

	tu.AssertC(t, loaded.Version == snapshotFormatVersion, "unexpected version")
	// entries must be sorted by path regardless of the walk order
	paths := make([]string, 0, loaded.Len())
	for _, e := range loaded.Entries {
		paths = append(paths, e.Path)
	}
	tu.AssertC(t, sort.StringsAreSorted(paths), "entries are not sorted")

	assertIndexEquals(t, index, loaded.toSystemFontsIndex())

	// the on-disk file starts with the magic and has the fixed header size
	raw, err := os.ReadFile(cachePath)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, bytes.Equal(raw[:4], []byte(snapshotMagic)), "missing magic number")
	tu.AssertC(t, len(raw) >= snapshotHeaderSize, "file shorter than header")
	tu.AssertC(t, binary.BigEndian.Uint16(raw[4:]) == snapshotFormatVersion, "bad version field")

	// no stale temporary file is left beside the committed snapshot
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	tu.AssertNoErr(t, err)
	for _, e := range entries {
		tu.AssertC(t, filepath.Ext(e.Name()) != "" || e.Name() == filepath.Base(cachePath),
			fmt.Sprintf("unexpected leftover file %q", e.Name()))
	}

	// Entry and Lookup behave as documented
	first := index[0]
	got, ok := loaded.Entry(first.path)
	tu.AssertC(t, ok, "Entry did not find a known path")
	info, err := os.Stat(filepath.Join(dir, filepath.Base(first.path)))
	tu.AssertNoErr(t, err)
	got, ok = loaded.Lookup(first.path, info.Size(), info.ModTime())
	tu.AssertC(t, ok, "Lookup should match an unchanged file")
	tu.AssertC(t, got.Size == info.Size(), "lookup returned the wrong size")

	if _, ok := loaded.Entry(filepath.Join(dir, "missing.ttf")); ok {
		t.Fatal("Entry matched a path that does not exist")
	}
	if _, ok := loaded.Lookup(first.path, info.Size(), info.ModTime().Add(time.Second)); ok {
		t.Fatal("Lookup matched despite an mtime change")
	}
	if _, ok := loaded.Lookup(first.path, info.Size()+1, info.ModTime()); ok {
		t.Fatal("Lookup matched despite a size change")
	}
}

func TestSnapshotEmpty(t *testing.T) {
	snap, err := NewFontScanSnapshot(nil)
	tu.AssertNoErr(t, err)
	loaded := saveAndReload(t, snap, filepath.Join(t.TempDir(), "empty.cache"))
	tu.AssertC(t, loaded.Len() == 0, "empty snapshot should stay empty")
}

func TestSnapshotRejectsDuplicates(t *testing.T) {
	_, index := scanFixtureDir(t, fixtureFonts())
	entries := snapshotEntriesFromIndex(index)
	dup := append(entries, entries[0])
	if _, err := NewFontScanSnapshot(dup); !errors.Is(err, ErrSnapshotDuplicate) {
		t.Fatalf("expected ErrSnapshotDuplicate, got %v", err)
	}

	// the same rule applies to a crafted file
	snap := mustSnapshot(t, index)
	raw := mustEncodeRawSnapshot(t, snap)
	corrupt := duplicateOneEntry(t, raw)
	if _, err := parseSnapshot(corrupt); !errors.Is(err, ErrSnapshotDuplicate) {
		t.Fatalf("expected ErrSnapshotDuplicate from file, got %v", err)
	}
}

// ---------------------------- deterministic encoding ----------------------------

func TestSnapshotDeterministicAcrossOrder(t *testing.T) {
	_, index := scanFixtureDir(t, fixtureFonts())

	entries := snapshotEntriesFromIndex(index)
	snap1, err := NewFontScanSnapshot(entries)
	tu.AssertNoErr(t, err)

	reversed := make([]SnapshotEntry, len(entries))
	for i := range entries {
		reversed[len(entries)-1-i] = entries[i]
	}
	snap2, err := NewFontScanSnapshot(reversed)
	tu.AssertNoErr(t, err)

	raw1, err := encodeSnapshotFrame(mustPayload(t, snap1))
	tu.AssertNoErr(t, err)
	raw2, err := encodeSnapshotFrame(mustPayload(t, snap2))
	tu.AssertNoErr(t, err)
	tu.AssertC(t, bytes.Equal(raw1, raw2), "encoding differs when entry order changes")

	// reloading either frame yields identical content
	p1, err := parseSnapshot(raw1)
	tu.AssertNoErr(t, err)
	p2, err := parseSnapshot(raw2)
	tu.AssertNoErr(t, err)
	if !reflect.DeepEqual(p1.Entries, p2.Entries) {
		t.Fatal("decoded snapshots differ")
	}
}

// TestSnapshotModTimeIsTimeZoneIndependent stores the same instant under two
// different local time zones: the serialized value is an absolute Unix time,
// so it must remain identical.
func TestSnapshotModTimeIsTimeZoneIndependent(t *testing.T) {
	instant := time.Date(2024, 3, 31, 2, 30, 0, 0, time.UTC)
	for _, zone := range []string{"UTC", "America/Los_Angeles", "Asia/Tokyo"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("time zone %s unavailable: %v", zone, err)
		}
		local := instant.In(loc)
		entry := SnapshotEntry{
			Path:    "/fonts/demo.ttf",
			Size:    1234,
			ModTime: local.UnixNano(),
			Footprints: []Footprint{{
				Family: "demo",
			}},
		}
		snap, err := NewFontScanSnapshot([]SnapshotEntry{entry})
		tu.AssertNoErr(t, err)
		loaded := saveAndReload(t, snap, filepath.Join(t.TempDir(), "tz.cache"))
		tu.AssertC(t, loaded.Entries[0].ModTime == instant.UnixNano(),
			fmt.Sprintf("zone %s changed the encoded mtime", zone))
	}
}

// ---------------------------- forward compatibility ----------------------------

func TestSnapshotSkipsUnknownFieldsAndRecords(t *testing.T) {
	_, index := scanFixtureDir(t, fixtureFonts())
	snap := mustSnapshot(t, index)
	payload, err := snap.encodePayload()
	tu.AssertNoErr(t, err)

	// inject an unknown top level record between existing records
	var extended bytes.Buffer
	extended.Write(appendTLV(nil, 9999, []byte("future top level data")))
	extended.Write(payload)
	extended.Write(appendTLV(nil, 9998, []byte{1, 2, 3, 4}))

	raw, err := encodeSnapshotFrame(extended.Bytes())
	tu.AssertNoErr(t, err)
	loaded, err := parseSnapshot(raw)
	tu.AssertNoErr(t, err)
	assertIndexEquals(t, index, loaded.toSystemFontsIndex())

	// unknown fields inside an entry must be skipped as well: rebuild the
	// entries blob, inserting a fake field in the first entry.
	blob := injectUnknownEntryField(t, payload)
	raw2, err := encodeSnapshotFrame(blob)
	tu.AssertNoErr(t, err)
	loaded2, err := parseSnapshot(raw2)
	tu.AssertNoErr(t, err)
	assertIndexEquals(t, index, loaded2.toSystemFontsIndex())
}

// injectUnknownEntryField rewrites the first entry record of the entries blob
// with an extra unknown field, bumping its field count.
func injectUnknownEntryField(t *testing.T, payload []byte) []byte {
	t.Helper()
	// payload = one entries top level record containing entry wrappers
	topTag, topData, topN, err := readTLV(payload)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, topTag == snapshotRecordEntries, "unexpected top level tag")

	entryTag, entryData, entryN, err := readTLV(topData)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, entryTag == snapshotTagEntry, "unexpected entry tag")

	fieldCount := binary.BigEndian.Uint32(entryData[:4])
	var head bytes.Buffer
	_ = binary.Write(&head, binary.BigEndian, fieldCount+1)
	head.Write(appendTLV(nil, 7777, []byte("future entry field")))
	newEntryData := append(head.Bytes(), entryData[4:]...)

	var entriesBlob bytes.Buffer
	entriesBlob.Write(appendTLV(nil, snapshotTagEntry, newEntryData))
	entriesBlob.Write(topData[entryN:]) // remaining entry wrappers

	var out bytes.Buffer
	out.Write(appendTLV(nil, snapshotRecordEntries, entriesBlob.Bytes()))
	out.Write(payload[topN:]) // remaining top level records
	return out.Bytes()
}

// ---------------------------- corruption handling ----------------------------

func mustPayload(t *testing.T, snap *FontScanSnapshot) []byte {
	t.Helper()
	p, err := snap.encodePayload()
	tu.AssertNoErr(t, err)
	return p
}

func mustEncodeRawSnapshot(t *testing.T, snap *FontScanSnapshot) []byte {
	t.Helper()
	raw, err := encodeSnapshotFrame(mustPayload(t, snap))
	tu.AssertNoErr(t, err)
	return raw
}

// encodeSnapshotFrameWithCRC is [encodeSnapshotFrame] with a caller chosen
// checksum, used to forge a file with a stale header checksum.
func encodeSnapshotFrameWithCRC(payload []byte, checksum uint32) ([]byte, error) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	frame := make([]byte, snapshotHeaderSize, snapshotHeaderSize+compressed.Len())
	copy(frame, snapshotMagic)
	binary.BigEndian.PutUint16(frame[4:], snapshotFormatVersion)
	binary.BigEndian.PutUint16(frame[6:], snapshotFlagCompressed)
	binary.BigEndian.PutUint32(frame[8:], uint32(compressed.Len()))
	binary.BigEndian.PutUint32(frame[12:], checksum)
	return append(frame, compressed.Bytes()...), nil
}

func TestSnapshotRejectsTruncation(t *testing.T) {
	raw := mustEncodeRawSnapshot(t, mustSnapshot(t, scanFixtureIndex(t)))

	for _, cut := range []int{0, 1, snapshotHeaderSize - 1, snapshotHeaderSize, len(raw) - 1, len(raw) / 2} {
		truncated := make([]byte, cut)
		copy(truncated, raw[:cut])
		err := checkSnapshotError(truncated, ErrSnapshotTruncated)
		// a file cut inside the header may surface as a format error instead,
		// which is equally acceptable; anything else is not.
		if err != nil && !errors.Is(err, ErrSnapshotFormat) {
			t.Fatalf("truncation at %d bytes: %v", cut, err)
		}
	}

	// a header advertising a longer payload than the file actually contains
	lying := append([]byte(nil), raw...)
	binary.BigEndian.PutUint32(lying[8:], binary.BigEndian.Uint32(lying[8:])+100)
	if err := checkSnapshotError(lying, ErrSnapshotTruncated); err != nil {
		t.Fatal(err)
	}

	// gzip stream cut short: keep the header length honest but truncate the body
	bodyLen := binary.BigEndian.Uint32(raw[8:])
	binary.BigEndian.PutUint32(raw[8:], bodyLen-50)
	short := raw[:snapshotHeaderSize+int(bodyLen)-50]
	if err := checkSnapshotError(short, ErrSnapshotTruncated); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsBadMagicAndVersion(t *testing.T) {
	raw := mustEncodeRawSnapshot(t, mustSnapshot(t, scanFixtureIndex(t)))

	badMagic := append([]byte(nil), raw...)
	copy(badMagic[:4], "XXXX")
	if err := checkSnapshotError(badMagic, ErrSnapshotFormat); err != nil {
		t.Fatal(err)
	}

	badVersion := append([]byte(nil), raw...)
	binary.BigEndian.PutUint16(badVersion[4:], snapshotFormatVersion+1)
	if err := checkSnapshotError(badVersion, ErrSnapshotFormat); err != nil {
		t.Fatal(err)
	}

	// random data is never accepted
	random := make([]byte, 256)
	for i := range random {
		random[i] = byte(i * 7)
	}
	if err := checkSnapshotError(random, ErrSnapshotFormat); err != nil && !errors.Is(err, ErrSnapshotTruncated) {
		t.Fatalf("random data: %v", err)
	}
}

func TestSnapshotRejectsChecksumMismatch(t *testing.T) {
	snap := mustSnapshot(t, scanFixtureIndex(t))
	payload := mustPayload(t, snap)

	// flip a payload byte and re-compress: the gzip stream stays valid, so the
	// failure must be attributed to our own checksum
	corruptPayload := append([]byte(nil), payload...)
	corruptPayload[len(corruptPayload)-1] ^= 0xff
	corrupt, err := encodeSnapshotFrameWithCRC(corruptPayload, crc32.ChecksumIEEE(payload))
	tu.AssertNoErr(t, err)
	if err := checkSnapshotError(corrupt, ErrSnapshotChecksum); err != nil {
		t.Fatal(err)
	}

	// a stale checksum written in the header must be detected as well
	wrongCRC, err := encodeSnapshotFrameWithCRC(payload, crc32.ChecksumIEEE(payload)^0xdeadbeef)
	tu.AssertNoErr(t, err)
	if err := checkSnapshotError(wrongCRC, ErrSnapshotChecksum); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsOversizedInputs(t *testing.T) {
	// lie about the payload length
	raw := mustEncodeRawSnapshot(t, mustSnapshot(t, scanFixtureIndex(t)))
	huge := append([]byte(nil), raw...)
	binary.BigEndian.PutUint32(huge[8:], snapshotMaxPayloadSize+1)
	if err := checkSnapshotError(huge, ErrSnapshotTooLarge); err != nil {
		t.Fatal(err)
	}

	// a decompression bomb: a gzip stream whose declared size is within the
	// header limit but whose decompressed content exceeds the cap
	bombPayload := bytes.Repeat([]byte("A"), snapshotMaxPayloadSize+1024)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = zw.Write(bombPayload)
	_ = zw.Close()
	frame := make([]byte, snapshotHeaderSize)
	copy(frame, snapshotMagic)
	binary.BigEndian.PutUint16(frame[4:], snapshotFormatVersion)
	binary.BigEndian.PutUint16(frame[6:], snapshotFlagCompressed)
	binary.BigEndian.PutUint32(frame[8:], uint32(compressed.Len()))
	frame = append(frame, compressed.Bytes()...)
	if err := checkSnapshotError(frame, ErrSnapshotTooLarge); err != nil && !errors.Is(err, ErrSnapshotTruncated) {
		t.Fatalf("bomb: %v", err)
	}
}

func TestSnapshotErrorCarriesPath(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "broken.cache")
	snap := mustSnapshot(t, scanFixtureIndex(t))
	payload := mustPayload(t, snap)
	corruptPayload := append([]byte(nil), payload...)
	corruptPayload[len(corruptPayload)-1] ^= 0xff // valid gzip, stale payload checksum
	corrupt, err := encodeSnapshotFrameWithCRC(corruptPayload, crc32.ChecksumIEEE(payload))
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, os.WriteFile(cachePath, corrupt, 0o600))

	_, err = LoadFontScanSnapshot(log.New(io.Discard, "", 0), cachePath, "")
	tu.AssertC(t, err != nil, "expected a load error")
	tu.AssertC(t, errors.Is(err, ErrSnapshotChecksum), fmt.Sprintf("expected checksum error, got %v", err))
	tu.AssertC(t, bytes.Contains([]byte(err.Error()), []byte(cachePath)),
		fmt.Sprintf("error %q does not mention the cache path", err))
}

// checkSnapshotError parses data and asserts the result wraps want.
func checkSnapshotError(data []byte, want error) error {
	_, err := parseSnapshot(data)
	if err == nil {
		return fmt.Errorf("expected %v, got nil", want)
	}
	if !errors.Is(err, want) {
		return fmt.Errorf("expected %v, got %v", want, err)
	}
	return nil
}

// duplicateOneEntry rewrites the entries blob so the first entry wrapper
// appears twice, which decoding must reject as a duplicate path.
func duplicateOneEntry(t *testing.T, raw []byte) []byte {
	t.Helper()
	payload, err := decodeSnapshotPayload(binary.BigEndian.Uint16(raw[6:]), raw[snapshotHeaderSize:])
	tu.AssertNoErr(t, err)
	topTag, topData, topN, err := readTLV(payload)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, topTag == snapshotRecordEntries, "unexpected top level tag")
	entryTag, _, entryN, err := readTLV(topData)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, entryTag == snapshotTagEntry, "unexpected entry tag")

	var entriesBlob bytes.Buffer
	entriesBlob.Write(topData[:entryN]) // first wrapper as encoded
	entriesBlob.Write(topData[:entryN]) // exact duplicate of the first wrapper
	entriesBlob.Write(topData[entryN:]) // remaining entry wrappers
	tu.AssertC(t, entriesBlob.Len() > entryN, "entries blob lost its content")

	var blob bytes.Buffer
	blob.Write(appendTLV(nil, snapshotRecordEntries, entriesBlob.Bytes()))
	blob.Write(payload[topN:])

	frame, err := encodeSnapshotFrame(blob.Bytes())
	tu.AssertNoErr(t, err)
	return frame
}

func scanFixtureIndex(t *testing.T) systemFontsIndex {
	t.Helper()
	_, index := scanFixtureDir(t, fixtureFonts())
	return index
}

// ---------------------------- legacy compatibility ----------------------------

func TestSnapshotLoadsLegacyIndex(t *testing.T) {
	dir, index := scanFixtureDir(t, fixtureFonts())
	legacyPath := filepath.Join(t.TempDir(), "font_index_v8.cache")

	// produce a genuine legacy cache through the legacy serializer
	var buf bytes.Buffer
	tu.AssertNoErr(t, index.serializeTo(&buf))
	tu.AssertNoErr(t, os.WriteFile(legacyPath, buf.Bytes(), 0o600))

	snapshotPath := filepath.Join(t.TempDir(), "font_index_snapshot_v1.cache")
	logger := log.New(io.Discard, "", 0)
	snap, err := LoadFontScanSnapshot(logger, snapshotPath, legacyPath)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, snap.Len() == len(index), "legacy entry count changed")

	converted := snap.toSystemFontsIndex()
	tu.AssertC(t, len(converted) == len(index), "converted index length mismatch")
	for i := range converted {
		// legacy entries have no size: lookup must degrade to mtime only
		tu.AssertC(t, converted[i].size == snapshotUnknownSize, "legacy size should be unknown")
		tu.AssertC(t, converted[i].modTime == index[i].modTime, "legacy mtime lost")
		if err := assertFontsetEquals(index[i].footprints, converted[i].footprints); err != nil {
			t.Fatalf("legacy footprints changed: %s", err)
		}
	}

	// an mtime-only lookup still hits
	info, err := os.Stat(filepath.Join(dir, "font1.ttf"))
	tu.AssertNoErr(t, err)
	if _, ok := snap.Lookup(filepath.Join(dir, "font1.ttf"), info.Size()+999, info.ModTime()); !ok {
		t.Fatal("legacy snapshot should match on mtime even when the size differs")
	}
}

func TestSnapshotMissingAndCorruptLegacy(t *testing.T) {
	logger := log.New(io.Discard, "", 0)

	// neither file exists: the new path error is reported
	_, err := LoadFontScanSnapshot(logger,
		filepath.Join(t.TempDir(), "new.cache"),
		filepath.Join(t.TempDir(), "old.cache"))
	tu.AssertC(t, errors.Is(err, os.ErrNotExist), fmt.Sprintf("expected ErrNotExist, got %v", err))

	// corrupt legacy file: error is wrapped and mentions the legacy path
	legacyPath := filepath.Join(t.TempDir(), "old.cache")
	tu.AssertNoErr(t, os.WriteFile(legacyPath, []byte{0, 8, 0, 0, 0, 0, 1, 2, 3}, 0o600))
	_, err = LoadFontScanSnapshot(logger, filepath.Join(t.TempDir(), "new.cache"), legacyPath)
	tu.AssertC(t, err != nil, "expected corrupt legacy error")
	tu.AssertC(t, bytes.Contains([]byte(err.Error()), []byte(legacyPath)),
		fmt.Sprintf("error %q lacks legacy path", err))
}

// ---------------------------- incremental scan behavior ----------------------------

func TestSnapshotIncrementalHitRate(t *testing.T) {
	dir, full := scanFixtureDir(t, fixtureFonts())
	logger := log.New(io.Discard, "", 0)

	// warm the snapshot
	snap := mustSnapshot(t, full)
	cachePath := filepath.Join(t.TempDir(), "snapshot.cache")
	loaded := saveAndReload(t, snap, cachePath)

	// unchanged directory: every entry is reused, nothing is opened
	reused, stats, err := scanFontFootprintsStats(logger, loaded.toSystemFontsIndex(), dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, stats.Reused == len(full), fmt.Sprintf("expected %d reuses, got %d", len(full), stats.Reused))
	tu.AssertC(t, stats.Scanned == 0, fmt.Sprintf("expected 0 scans, got %d", stats.Scanned))
	assertIndexEquals(t, full, reused)

	// add a font: exactly one rescan, two reuses
	time.Sleep(10 * time.Millisecond)
	copyFile(t, filepath.Join("..", "font", "testdata", "UbuntuMono-R.ttf"), filepath.Join(dir, "font3.ttf"))
	indexAfterAdd, stats, err := scanFontFootprintsStats(logger, loaded.toSystemFontsIndex(), dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, stats.Scanned == 1, fmt.Sprintf("add: expected 1 scan, got %d", stats.Scanned))
	tu.AssertC(t, stats.Reused == 2, fmt.Sprintf("add: expected 2 reuses, got %d", stats.Reused))

	fullAfterAdd, err := scanFontFootprints(logger, nil, dir)
	tu.AssertNoErr(t, err)
	assertIndexEquals(t, fullAfterAdd, indexAfterAdd)

	// change mtime without changing the size: must invalidate
	time.Sleep(10 * time.Millisecond)
	target := filepath.Join(dir, "font1.ttf")
	now := time.Now()
	tu.AssertNoErr(t, os.Chtimes(target, now, now))
	currentIndex, stats, err := scanFontFootprintsStats(logger, indexAfterAdd, dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, stats.Scanned == 1, fmt.Sprintf("mtime change: expected 1 scan, got %d", stats.Scanned))

	// change the size without changing the mtime is hard to arrange portably,
	// but Lookup gives us a direct, deterministic check of the rule.
	e, ok := loaded.Entry(target)
	tu.AssertC(t, ok, "target missing from snapshot")
	if _, ok := loaded.Lookup(target, e.Size+1, time.Unix(0, e.ModTime)); ok {
		t.Fatal("size change did not invalidate the entry")
	}

	// remove a font: it simply disappears, remaining entries are reused
	tu.AssertNoErr(t, os.Remove(filepath.Join(dir, "font2.ttf")))
	afterRemove, stats, err := scanFontFootprintsStats(logger, currentIndex, dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, len(afterRemove) == 2, fmt.Sprintf("remove: expected 2 entries, got %d", len(afterRemove)))
	tu.AssertC(t, stats.Reused == 2, fmt.Sprintf("remove: expected 2 reuses, got %d", stats.Reused))
}

func TestSnapshotFullScanEquivalence(t *testing.T) {
	// A snapshot-driven incremental scan over add/remove/mtime/corruption
	// transitions must always equal a fresh, snapshot-less full scan, in
	// content and in order.
	dir := t.TempDir()
	logger := log.New(io.Discard, "", 0)
	font1 := filepath.Join("..", "font", "testdata", "Amiri-Regular.ttf")
	font2 := filepath.Join("..", "font", "testdata", "Roboto-Regular.ttf")
	mono := filepath.Join("..", "font", "testdata", "UbuntuMono-R.ttf")

	copyFile(t, font1, filepath.Join(dir, "font1.ttf"))
	full1, err := scanFontFootprints(logger, nil, dir)
	tu.AssertNoErr(t, err)
	cache := mustSnapshot(t, full1).toSystemFontsIndex()

	step := func(mutate func()) {
		t.Helper()
		time.Sleep(10 * time.Millisecond)
		mutate()
		inc, _, err := scanFontFootprintsStats(logger, cache, dir)
		tu.AssertNoErr(t, err)
		full, err := scanFontFootprints(logger, nil, dir)
		tu.AssertNoErr(t, err)
		assertIndexEquals(t, full, inc)
		cache = inc
	}

	step(func() { copyFile(t, font2, filepath.Join(dir, "font2.ttf")) })
	step(func() { copyFile(t, mono, filepath.Join(dir, "font1.ttf")) }) // overwrite: family changes
	step(func() {
		// corrupt the content: invalid font files are stored with empty
		// footprint lists on both paths
		tu.AssertNoErr(t, os.WriteFile(filepath.Join(dir, "font2.ttf"), []byte("not a font at all"), 0o600))
	})
	step(func() { tu.AssertNoErr(t, os.Remove(filepath.Join(dir, "font1.ttf"))) })
	step(func() { copyFile(t, font1, filepath.Join(dir, "font1.ttf")) })
}

// ---------------------------- atomic save & write failures ----------------------------

type failingWriter struct {
	failAfter int
	written   int
	path      string
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.written+len(p) > f.failAfter {
		n := f.failAfter - f.written
		f.written = f.failAfter
		if n < 0 {
			n = 0
		}
		return n, fmt.Errorf("short write to %q: injected failure", f.path)
	}
	f.written += len(p)
	return len(p), nil
}
func (f *failingWriter) Sync() error  { return nil }
func (f *failingWriter) Close() error { return nil }

func TestSnapshotSaveFailureKeepsOldSnapshot(t *testing.T) {
	dir, index := scanFixtureDir(t, fixtureFonts())
	cachePath := filepath.Join(t.TempDir(), "snapshot.cache")

	// establish a valid snapshot
	snap := mustSnapshot(t, index)
	tu.AssertNoErr(t, snap.Save(cachePath))
	goodRaw, err := os.ReadFile(cachePath)
	tu.AssertNoErr(t, err)

	// inject a writer that fails halfway through the replacement
	failing := &failingWriter{failAfter: 32, path: cachePath + ".tmp"}
	opener := func(dir, pattern string) (writeSyncCloser, string, error) {
		return failing, filepath.Join(dir, ".font-scan-snapshot-fail"), nil
	}
	err = snap.saveAt(cachePath, opener)
	tu.AssertC(t, err != nil, "expected the injected write failure")
	tu.AssertC(t, bytes.Contains([]byte(err.Error()), []byte("snapshot")),
		fmt.Sprintf("error lacks path context: %v", err))

	// the committed file is untouched and still parses to the same content
	preservedRaw, err := os.ReadFile(cachePath)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, bytes.Equal(goodRaw, preservedRaw), "old snapshot was modified")
	preserved, err := LoadFontScanSnapshot(log.New(io.Discard, "", 0), cachePath, "")
	tu.AssertNoErr(t, err)
	assertIndexEquals(t, index, preserved.toSystemFontsIndex())

	// no temporary file survives the failure
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	tu.AssertNoErr(t, err)
	for _, e := range entries {
		tu.AssertC(t, e.Name() == filepath.Base(cachePath),
			fmt.Sprintf("temporary file %q leaked", e.Name()))
	}

	// a retry with the regular opener succeeds, proving the index was not polluted
	tu.AssertNoErr(t, snap.Save(cachePath))
	_, err = LoadFontScanSnapshot(log.New(io.Discard, "", 0), cachePath, "")
	tu.AssertNoErr(t, err)

	// sanity: the scanned dir is unrelated to the cache dir here
	_ = dir
}

func TestSnapshotSavePermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	_, index := scanFixtureDir(t, fixtureFonts())
	blocked := t.TempDir()
	tu.AssertNoErr(t, os.Chmod(blocked, 0o500)) // read only
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	cachePath := filepath.Join(blocked, "nested", "snapshot.cache")
	err := mustSnapshot(t, index).Save(cachePath)
	tu.AssertC(t, err != nil, "expected a permission error")
	tu.AssertC(t, bytes.Contains([]byte(err.Error()), []byte(blocked)),
		fmt.Sprintf("error %q lacks directory context", err))

	// nothing was created under the read-only directory
	if _, statErr := os.Stat(filepath.Join(blocked, "nested")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unexpected directory state: %v", statErr)
	}
}

func TestSnapshotLoadPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "snapshot.cache")
	raw := mustEncodeRawSnapshot(t, mustSnapshot(t, scanFixtureIndex(t)))
	tu.AssertNoErr(t, os.WriteFile(cachePath, raw, 0o000))
	t.Cleanup(func() { _ = os.Chmod(cachePath, 0o600) })

	if _, err := LoadFontScanSnapshot(log.New(io.Discard, "", 0), cachePath, ""); err == nil {
		t.Fatal("expected a permission error")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected os.ErrPermission, got %v", err)
	}
}

// ---------------------------- concurrency ----------------------------

func TestSnapshotConcurrentReadOnly(t *testing.T) {
	snap := saveAndReload(t,
		mustSnapshot(t, scanFixtureIndex(t)),
		filepath.Join(t.TempDir(), "snapshot.cache"))

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				for _, e := range snap.Entries {
					if got, ok := snap.Entry(e.Path); !ok || got.Path != e.Path {
						t.Errorf("concurrent Entry mismatch for %q", e.Path)
						return
					}
					if _, ok := snap.Lookup(e.Path, e.Size, time.Unix(0, e.ModTime)); !ok {
						t.Errorf("concurrent Lookup miss for %q", e.Path)
						return
					}
				}
				_ = snap.Len()
				_ = snap.toSystemFontsIndex()
			}
		}(g)
	}
	wg.Wait()
}

// ---------------------------- cross platform paths ----------------------------

func TestSnapshotCanonicalPaths(t *testing.T) {
	posixPath := "/usr/share/fonts/a.ttf"
	windowsPath := `C:\Windows\Fonts\a.ttf`

	if runtime.GOOS == "windows" {
		// on the host platform the native separator is normalized
		tu.AssertC(t, canonicalSnapshotPath(windowsPath) == "C:/Windows/Fonts/a.ttf",
			"backslashes were not normalized")
		tu.AssertC(t, hostPath("C:/Windows/Fonts/a.ttf") == windowsPath,
			"host separator was not restored")
	} else {
		// on POSIX systems a backslash is an ordinary file name character and
		// must be preserved verbatim: normalization must not rewrite it
		tu.AssertC(t, canonicalSnapshotPath(windowsPath) == windowsPath,
			"POSIX must preserve backslashes in file names")
		tu.AssertC(t, hostPath(windowsPath) == windowsPath,
			"POSIX host path must be unchanged")
	}
	tu.AssertC(t, canonicalSnapshotPath(posixPath) == posixPath,
		"slash paths must be unchanged")

	// round trip through the snapshot using an already canonical, slash based
	// path, which is the portable representation exchanged across platforms
	entry := SnapshotEntry{
		Path:       "C:/Windows/Fonts/arial.ttf",
		Size:       42,
		ModTime:    time.Now().UnixNano(),
		Footprints: []Footprint{{Family: "arial"}},
	}
	snap, err := NewFontScanSnapshot([]SnapshotEntry{entry})
	tu.AssertNoErr(t, err)
	raw, err := encodeSnapshotFrame(mustPayload(t, snap))
	tu.AssertNoErr(t, err)
	loaded, err := parseSnapshot(raw)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, loaded.Entries[0].Path == "C:/Windows/Fonts/arial.ttf", "path not stored with slashes")

	if _, ok := loaded.Entry("C:/Windows/Fonts/arial.ttf"); !ok {
		t.Fatal("canonical slash path did not resolve")
	}

	// native spelling resolves on Windows hosts because the lookup canonicalizes
	if runtime.GOOS == "windows" {
		if _, ok := loaded.Entry(`C:\Windows\Fonts\arial.ttf`); !ok {
			t.Fatal("native Windows path did not resolve after canonicalization")
		}
	}
}

// ---------------------------- end to end cache flow ----------------------------

func TestRefreshSystemFontsIndexUsesSnapshot(t *testing.T) {
	dir, index := scanFixtureDir(t, fixtureFonts())
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, "font_index_snapshot_v1.cache")
	logger := log.New(io.Discard, "", 0)

	// build the snapshot through the production refresh entry point, but
	// pointing the scan at the fixture directory through a small wrapper:
	// refreshSystemFontsIndex uses system dirs, so exercise the same building
	// blocks directly instead.
	snap := mustSnapshot(t, index)
	tu.AssertNoErr(t, snap.Save(cachePath))

	first, err := LoadFontScanSnapshot(logger, cachePath, "")
	tu.AssertNoErr(t, err)

	// second load: fully incremental, zero rescans
	_, stats, err := scanFontFootprintsStats(logger, first.toSystemFontsIndex(), dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, stats.Scanned == 0, fmt.Sprintf("expected a fully cached scan, got stats %+v", stats))
	tu.AssertC(t, stats.Reused == len(index), "not every entry was reused")

	// corrupting the on-disk snapshot must not poison the in-memory index
	payload := mustPayload(t, snap)
	corruptPayload := append([]byte(nil), payload...)
	corruptPayload[len(corruptPayload)-1] ^= 0xff
	corrupt, err := encodeSnapshotFrameWithCRC(corruptPayload, crc32.ChecksumIEEE(payload))
	tu.AssertNoErr(t, err)
	tu.AssertNoErr(t, os.WriteFile(cachePath, corrupt, 0o600))

	if _, err := LoadFontScanSnapshot(logger, cachePath, ""); err == nil {
		t.Fatal("corrupt snapshot was accepted")
	}
	// the previously loaded index remains fully usable
	_, stats, err = scanFontFootprintsStats(logger, first.toSystemFontsIndex(), dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, stats.Scanned == 0, "in-memory index was polluted by the corrupt file")
}

func TestNewFontScanSnapshotIsImmutable(t *testing.T) {
	_, index := scanFixtureDir(t, fixtureFonts())
	entries := snapshotEntriesFromIndex(index)

	snap, err := NewFontScanSnapshot(entries)
	tu.AssertNoErr(t, err)

	// mutate the caller's data in every reachable way
	entries[0].Path = "/tampered/path.ttf"
	entries[0].Footprints[0].Family = "tampered"
	entries[0].Footprints = append(entries[0].Footprints, Footprint{Family: "injected"})
	entries = append(entries, SnapshotEntry{Path: "/tampered/extra.ttf", Size: 1, ModTime: 1})

	got, ok := snap.Entry(index[0].path)
	tu.AssertC(t, ok, "original path disappeared from the snapshot")
	tu.AssertC(t, got.Path == index[0].path, "snapshot path was mutated in place")
	tu.AssertC(t, got.Footprints[0].Family == index[0].footprints[0].Family,
		"snapshot footprint was mutated in place")
	tu.AssertC(t, len(got.Footprints) == len(index[0].footprints), "injected face reached the snapshot")
	tu.AssertC(t, snap.Len() == len(index), "appended entry reached the snapshot")
}

// TestSnapshotLegacyUpgradeRoundTrip exercises the full upgrade path: a legacy
// cache warms the first load, and after a save the versioned snapshot carries
// real sizes while remaining content equivalent.
func TestSnapshotLegacyUpgradeRoundTrip(t *testing.T) {
	dir, index := scanFixtureDir(t, fixtureFonts())
	logger := log.New(io.Discard, "", 0)
	cacheDir := t.TempDir()
	legacyPath := filepath.Join(cacheDir, "font_index_v8.cache")
	snapshotPath := filepath.Join(cacheDir, "font_index_snapshot_v1.cache")

	var legacy bytes.Buffer
	tu.AssertNoErr(t, index.serializeTo(&legacy))
	tu.AssertNoErr(t, os.WriteFile(legacyPath, legacy.Bytes(), 0o600))

	// first launch: only the legacy file exists
	loaded, err := LoadFontScanSnapshot(logger, snapshotPath, legacyPath)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, loaded.Len() == len(index), "legacy entry count changed")

	// rescanning fills in the sizes, after which saving produces a snapshot
	rescanned, stats, err := scanFontFootprintsStats(logger, loaded.toSystemFontsIndex(), dir)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, stats.Scanned == 0, "mtime-only legacy entries should still be reused")
	tu.AssertNoErr(t, saveSystemFontsSnapshot(rescanned, snapshotPath))

	upgraded, err := LoadFontScanSnapshot(logger, snapshotPath, legacyPath)
	tu.AssertNoErr(t, err)
	assertIndexEquals(t, index, upgraded.toSystemFontsIndex())
	// reuse fills the legacy unknown sizes with the current file sizes, so a
	// single mtime-only pass already produces fully size-validated entries
	for _, e := range upgraded.Entries {
		tu.AssertC(t, e.Size > 0, fmt.Sprintf("upgraded entry %q has no real size: %d", e.Path, e.Size))
		tu.AssertC(t, e.ModTime != 0, fmt.Sprintf("upgraded entry %q lost its mtime", e.Path))
	}

	// the legacy file may now be removed without losing anything
	tu.AssertNoErr(t, os.Remove(legacyPath))
	again, err := LoadFontScanSnapshot(logger, snapshotPath, legacyPath)
	tu.AssertNoErr(t, err)
	assertIndexEquals(t, index, again.toSystemFontsIndex())
}

// TestCorruptNewSnapshotIsNotMaskedByLegacy makes sure a corrupt versioned
// snapshot is reported as such instead of silently falling back to a stale
// legacy cache: callers must decide whether to rescan.
func TestCorruptNewSnapshotIsNotMaskedByLegacy(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	cacheDir := t.TempDir()
	legacyPath := filepath.Join(cacheDir, "font_index_v8.cache")
	snapshotPath := filepath.Join(cacheDir, "font_index_snapshot_v1.cache")

	_, index := scanFixtureDir(t, fixtureFonts())
	var legacy bytes.Buffer
	tu.AssertNoErr(t, index.serializeTo(&legacy))
	tu.AssertNoErr(t, os.WriteFile(legacyPath, legacy.Bytes(), 0o600))
	tu.AssertNoErr(t, os.WriteFile(snapshotPath, []byte("FTSN-but-broken"), 0o600))

	_, err := LoadFontScanSnapshot(logger, snapshotPath, legacyPath)
	tu.AssertC(t, err != nil, "corrupt new snapshot must not be silently ignored")
	tu.AssertC(t, errors.Is(err, ErrSnapshotFormat) || errors.Is(err, ErrSnapshotTruncated),
		fmt.Sprintf("expected a snapshot sentinel error, got %v", err))
	tu.AssertC(t, bytes.Contains([]byte(err.Error()), []byte(snapshotPath)),
		fmt.Sprintf("error %q lacks the snapshot path", err))

	// removing the corrupt file restores the legacy fallback
	tu.AssertNoErr(t, os.Remove(snapshotPath))
	loaded, err := LoadFontScanSnapshot(logger, snapshotPath, legacyPath)
	tu.AssertNoErr(t, err)
	tu.AssertC(t, loaded.Len() == len(index), "legacy fallback lost entries")
}
