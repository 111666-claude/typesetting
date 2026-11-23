package fontscan

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	ot "github.com/go-text/typesetting/font/opentype"
)

// DefaultFontDirectories return the OS-dependent usual directories for
// fonts, or an error if no one exists.
// These are the directories used by `FontMap.UseSystemFonts` to locate fonts.
func DefaultFontDirectories(logger Logger) ([]string, error) {
	var dirs []string
	switch runtime.GOOS {
	case "windows":
		sysRoot := os.Getenv("SYSTEMROOT")
		if sysRoot == "" {
			sysRoot = os.Getenv("SYSTEMDRIVE")
		}
		if sysRoot == "" { // try with the common C:
			sysRoot = "C:"
		}
		dir := filepath.Join(filepath.VolumeName(sysRoot), `\Windows`, "Fonts")
		dirs = []string{
			dir,
			filepath.Join(os.Getenv("windir"), "Fonts"),
			filepath.Join(os.Getenv("localappdata"), "Microsoft", "Windows", "Fonts"),
		}
	case "darwin":
		dirs = []string{
			"/System/Library/Fonts",
			"/Library/Fonts",
			"~/Library/Fonts",
			"/Network/Library/Fonts",
			"/System/Library/Assets/com_apple_MobileAsset_Font3",
			"/System/Library/Assets/com_apple_MobileAsset_Font4",
			"/System/Library/Assets/com_apple_MobileAsset_Font5",
		}
	case "linux", "openbsd", "freebsd":
		dirs = []string{
			"/usr/X11R6/lib/X11/fonts",
			"/usr/local/share/fonts",
			"/usr/share/fonts",
			"/usr/share/texmf/fonts/opentype/public",
			"~/.fonts/",
			filepath.Join(getEnvWithDefault("XDG_DATA_HOME", "~/.local/share"), "fonts"),
		}

		if dataPaths := os.Getenv("XDG_DATA_DIRS"); dataPaths != "" {
			for _, dataPath := range filepath.SplitList(dataPaths) {
				dirs = append(dirs, filepath.Join(dataPath, "fonts"))
			}
		}
		fc := fcVarsFromEnv()
		fcDirs, err := fc.parseFcConfig(logger)
		if err != nil {
			logger.Printf("unable to process fontconfig config file: %s", err)
		} else {
			dirs = append(dirs, fcDirs...)
		}
	case "android":
		dirs = []string{
			"/system/fonts",
			"/system/font",
			"/data/fonts",
		}
	case "ios":
		dirs = []string{
			"/System/Library/Fonts",
			"/System/Library/Fonts/Cache",
		}
	default:
		return nil, fmt.Errorf("unsupported plaform %s", runtime.GOOS)
	}

	var (
		validDirs []string
		seen      = map[string]bool{}
	)
	for _, dir := range dirs {
		dir = expandUser(dir)
		dir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}

		if seen[dir] {
			continue
		}
		seen[dir] = true

		info, err := os.Stat(dir)
		if err != nil { // ignore the non existent directory
			continue
		}

		if !info.IsDir() {
			logger.Printf("font dir is not a directory: %q", dir)
			continue
		}

		validDirs = append(validDirs, dir)
	}
	sort.Strings(validDirs)

	if len(validDirs) == 0 {
		return nil, errors.New("no font directory found")
	}

	return validDirs, nil
}

func expandUser(path string) (expandedPath string) {
	if strings.HasPrefix(path, "~") {
		if u, err := user.Current(); err == nil {
			return strings.Replace(path, "~", u.HomeDir, -1)
		}
	}
	return path
}

// rejects several extensions which are for sure not supported font files
// return `true` is the file should be ignored
func ignoreFontFile(name string) bool {
	// ignore hidden file
	if name == "" || name[0] == '.' {
		return true
	} else if strings.HasSuffix(name, ".enc.gz") || // encodings
		strings.HasSuffix(name, ".afm") || // metrics (ascii)
		strings.HasSuffix(name, ".pfm") || // metrics (binary)
		strings.HasSuffix(name, ".dir") || // summary
		strings.HasSuffix(name, ".scale") ||
		strings.HasSuffix(name, ".alias") ||
		strings.HasSuffix(name, ".pcf") || strings.HasSuffix(name, ".pcf.gz") || // Bitmap
		strings.HasSuffix(name, ".pfb") /* Type1 */ {
		return true
	}

	return false
}

// --------------------- footprint mode -----------------------

// timeStamp is the (UnixNano) modification time of a font file,
// used to trigger or not the scan of a font file
type timeStamp int64

func newTimeStamp(file os.FileInfo) timeStamp { return timeStamp(file.ModTime().UnixNano()) }

func (fh timeStamp) serialize() []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(fh))
	return buf[:]
}

// assume len(src) >= 8
func (fh *timeStamp) deserialize(src []byte) {
	*fh = timeStamp(binary.BigEndian.Uint64(src))
}

// systemFontsIndex stores the footprint comming from the file system
type systemFontsIndex []fileFootprints

func (sfi systemFontsIndex) flatten() fontSet {
	var out fontSet
	for _, file := range sfi {
		for _, fp := range file.footprints {
			out = append(out, fp)
		}
	}
	return out
}

// assertValid makes sur at least one face is valid
func (sfi systemFontsIndex) assertValid() error {
	for _, file := range sfi {
		for _, fp := range file.footprints {
			_, err := fp.loadFromDisk()
			if err == nil {
				return nil
			}
		}
	}

	return errors.New("no valid font")
}

// groups the footprints by origin file
type fileFootprints struct {
	path string // file path

	footprints []Footprint // font content for the path

	// modification time for the file
	modTime timeStamp
}

type footprintScanner struct {
	previousIndex map[string]fileFootprints // reference index, to be updated

	dst systemFontsIndex // accumulated footprints

	// keptPaths records the paths surviving the current scan; it is used
	// to invalidate shared coverage indexes of files that disappeared.
	keptPaths map[string]bool

	// used to reduce allocations
	scanBuffer
}

type scanBuffer struct {
	tableBuffer []byte
	cmapBuffer  [][2]rune
}

func newFootprintAccumulator(currentIndex systemFontsIndex) footprintScanner {
	// map font files to their footprints
	out := footprintScanner{
		previousIndex: make(map[string]fileFootprints, len(currentIndex)),
		keptPaths:     make(map[string]bool),
	}
	for _, fp := range currentIndex {
		out.previousIndex[fp.path] = fp
	}
	return out
}

// pruneCoverageCache drops shared coverage indexes that no longer correspond
// to a file present in the freshly scanned [dst]. Font identities of removed
// files are invalidated so the bounded cache cannot retain coverage computed
// from stale content, and its capacity remains available for live fonts.
func (fa *footprintScanner) pruneCoverageCache() {
	cache := sharedCoverageCache()
	for _, previous := range fa.previousIndex {
		if _, stillPresent := fa.keptPaths[previous.path]; stillPresent {
			continue
		}
		for _, fp := range previous.footprints {
			cache.Invalidate(fp.Location)
		}
	}
}

func (fa *footprintScanner) consume(path string, info os.FileInfo) error {
	modTime := newTimeStamp(info)
	fa.keptPaths[path] = true

	// try to avoid scanning the file
	if indexedFile, has := fa.previousIndex[path]; has && indexedFile.modTime == modTime {
		// we already have an up to date scan of the file:
		// skip the scan and add the current footprints, reusing or creating
		// their shared coverage indexes. Going through the cache records hits
		// and makes aliased files converge on a single immutable index.
		reused := indexedFile
		cache := sharedCoverageCache()
		for i := range reused.footprints {
			if idx, ok := cache.Lookup(reused.footprints[i].Location); ok {
				reused.footprints[i].coverage = idx
			} else {
				fa.attachCoverage(&reused.footprints[i])
			}
		}
		fa.dst = append(fa.dst, reused)
		return nil
	}

	// The file is new or modified: any coverage index built from the old
	// content must not be served anymore. New entries are stamped below.
	if previous, has := fa.previousIndex[path]; has {
		cache := sharedCoverageCache()
		for _, fp := range previous.footprints {
			cache.Invalidate(fp.Location)
		}
	}

	// do the actual scan

	file, err := os.Open(path)
	if err != nil {
		return err
	}

	ff := fileFootprints{
		path:    path,
		modTime: modTime,
	}

	// fetch the loaders for the given font file, or nil if is not
	// an Opentype font.
	loaders, _ := ot.NewLoaders(file)

	for i, ld := range loaders {
		var fp Footprint
		fp, fa.scanBuffer, err = newFootprintFromLoader(ld, false, fa.scanBuffer)
		// the font won't be usable, just ignore it
		if err != nil {
			continue
		}

		fp.Location.File = path
		fp.Location.Index = uint16(i)
		// TODO: for now, we do not handle variable fonts

		// build and share the immutable coverage index eagerly so that the
		// scan cost is paid once per font identity, including aliased files
		fa.attachCoverage(&fp)

		ff.footprints = append(ff.footprints, fp)
	}

	// newFootprintFromLoader still uses file, do not close earlier
	file.Close()

	// if the file is not a valid Opentype file,
	// we store an empty list of footprints but still adds the entry to the index
	// so that subsequent calls won't try to open it again
	fa.dst = append(fa.dst, ff)

	return nil
}

// attachCoverage registers the footprint coverage in the shared cache,
// reusing an existing index when another file (for example an alias) already
// exposed the same font identity.
func (fa *footprintScanner) attachCoverage(fp *Footprint) {
	cache := sharedCoverageCache()
	if idx, ok := cache.Lookup(fp.Location); ok {
		fp.coverage = idx
		return
	}
	idx := newFontCoverageIndexFromRunes(fp.Location, fp.Runes, fp.Scripts, fp.Langs)
	cache.Put(idx)
	fp.coverage = idx
}

// scanFontFootprints walk through the given directories
// and scan each font file to extract its footprint.
// An error is returned if the directory traversal fails, not for invalid font files,
// which are simply ignored.
// `currentIndex` may be passed to avoid scanning font files that are
// already present in `currentIndex` and up to date, and directly duplicating
// the footprint in `currentIndex`
func scanFontFootprints(logger Logger, currentIndex systemFontsIndex, dirs ...string) (systemFontsIndex, error) {
	// visited records file paths already scanned, so overlapping directories
	// do not add a file twice. The walk does not resolve symlinks.
	visited := make(map[string]bool)

	accu := newFootprintAccumulator(currentIndex)
	for _, dir := range dirs {
		err := accu.scanDirectory(logger, dir, visited)
		if err != nil {
			return nil, err
		}
	}
	// evict coverage indexes belonging to files that no longer exist, so the
	// bounded cache cannot retain stale font data
	accu.pruneCoverageCache()
	return accu.dst, nil
}
