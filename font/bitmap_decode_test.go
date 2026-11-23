// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"sync"
	"testing"

	td "github.com/go-text/typesetting-utils/opentype"
	ot "github.com/go-text/typesetting/font/opentype"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/tiff"
)

// innerImage removes the origin translation wrapper applied when bearings
// are non zero.
func innerImage(img image.Image) image.Image {
	if o, ok := img.(*originImage); ok {
		return o.img
	}
	return img
}

// ---------------- synthetic payload builders ----------------

// encodePNG returns a PNG encoding of a tiny RGBA image whose corners are
// transparent, exercising alpha handling and transparent edges.
func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(20 * (x + 1)),
				G: uint8(40 * (y + 1)),
				B: 90,
				A: 255,
			})
		}
	}
	// transparent edges
	img.SetNRGBA(0, 0, color.NRGBA{0, 0, 0, 0})
	img.SetNRGBA(w-1, h-1, color.NRGBA{0, 0, 0, 0})
	var buf bytes.Buffer
	tu.AssertNoErr(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func encodeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, G: 10, B: 20, A: 255})
	img.Set(w-1, h-1, color.RGBA{R: 10, G: 200, B: 30, A: 255})
	var buf bytes.Buffer
	tu.AssertNoErr(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}

func encodeTIFF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 12, G: 34, B: 56, A: 255})
	img.Set(w-1, h-1, color.RGBA{R: 78, G: 90, B: 123, A: 255})
	var buf bytes.Buffer
	tu.AssertNoErr(t, tiff.Encode(&buf, img, &tiff.Options{Compression: tiff.Deflate}))
	return buf.Bytes()
}

// ---------------- direct DecodeGlyphBitmap tests ----------------

func TestDecodeMonochromeByteAligned(t *testing.T) {
	// 10x3 bitmap; rows are 10 bits padded to 2 bytes.
	// Row 0: 10101010 10...... ; Row 1: all zero ; Row 2: 11111111 11......
	const w, h = 10, 3
	data := []byte{
		0b10101010, 0b10000000,
		0b00000000, 0b00000000,
		0b11111111, 0b11000000,
	}
	img, err := DecodeGlyphBitmap(GlyphBitmap{
		Data: data, Format: BlackAndWhiteByteAligned, Width: w, Height: h,
	})
	tu.AssertNoErr(t, err)
	pal, ok := img.(*image.Paletted)
	tu.Assert(t, ok)
	tu.Assert(t, pal.Bounds() == image.Rect(0, 0, w, h))
	tu.Assert(t, pal.Stride == w) // one palette index per pixel

	want := func(x, y int) uint8 {
		switch y {
		case 0:
			if x%2 == 0 {
				return 1
			}
		case 2:
			return 1
		}
		return 0
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			tu.AssertC(t, pal.ColorIndexAt(x, y) == want(x, y), "pixel")
		}
	}
	// palette preserves the 0=white / 1=black color model
	tu.Assert(t, bwPalette[0] == color.RGBA{255, 255, 255, 255})
	tu.Assert(t, bwPalette[1] == color.RGBA{0, 0, 0, 255})
}

func TestDecodeMonochromeBitAligned(t *testing.T) {
	// width 9, 2 rows: the bit stream is continuous, with 6 padding bits
	// appended only after the 18th pixel.
	const w, h = 9, 2
	// row0: all ink (9 ones); row1: first 7 empty then 2 ink
	// bits: 111111111 | 000000011 | 000000
	// bytes: 11111111 | 10000000 | 11000000
	data := []byte{0xFF, 0b10000000, 0b11000000}
	img, err := DecodeGlyphBitmap(GlyphBitmap{
		Data: data, Format: BlackAndWhite, Width: w, Height: h,
	})
	tu.AssertNoErr(t, err)
	pal := img.(*image.Paletted)
	for x := 0; x < w; x++ {
		tu.AssertC(t, pal.ColorIndexAt(x, 0) == 1, "row0 all ink")
	}
	for x := 0; x < 7; x++ {
		tu.AssertC(t, pal.ColorIndexAt(x, 1) == 0, "row1 empty start")
	}
	tu.AssertC(t, pal.ColorIndexAt(7, 1) == 1 && pal.ColorIndexAt(8, 1) == 1, "row1 end ink")
}

func TestDecodeGray(t *testing.T) {
	const w, h = 3, 2
	data := []byte{0, 64, 128, 192, 255, 17}
	img, err := DecodeGlyphBitmap(GlyphBitmap{
		Data: data, Format: Gray, Width: w, Height: h,
	})
	tu.AssertNoErr(t, err)
	alpha, ok := img.(*image.Alpha)
	tu.Assert(t, ok)
	tu.Assert(t, alpha.Stride == w)
	tu.Assert(t, alpha.Bounds() == image.Rect(0, 0, w, h))
	for i, v := range data {
		tu.AssertC(t, alpha.Pix[i] == v, "gray sample")
	}
	tu.Assert(t, img.ColorModel() == color.AlphaModel)
}

func TestDecodeBGRA(t *testing.T) {
	const w, h = 2, 2
	// stored B,G,R,A per pixel
	data := []byte{
		10, 20, 30, 40,
		50, 60, 70, 80,
		90, 100, 110, 120,
		130, 140, 150, 0, // fully transparent edge
	}
	img, err := DecodeGlyphBitmap(GlyphBitmap{
		Data: data, Format: BGRA, Width: w, Height: h,
	})
	tu.AssertNoErr(t, err)
	nrgba, ok := img.(*image.NRGBA) // non-premultiplied, alpha preserved
	tu.Assert(t, ok)
	tu.Assert(t, nrgba.Stride == w*4)
	tu.Assert(t, img.ColorModel() == color.NRGBAModel)

	got := nrgba.NRGBAAt(0, 0)
	tu.Assert(t, got == color.NRGBA{R: 30, G: 20, B: 10, A: 40})
	// transparent pixel keeps its (arbitrary) color channels and zero alpha
	got = nrgba.NRGBAAt(1, 1)
	tu.Assert(t, got == color.NRGBA{R: 150, G: 140, B: 130, A: 0})
}

func TestDecodeCompressedFormats(t *testing.T) {
	const w, h = 4, 3
	cases := []struct {
		name   string
		format BitmapFormat
		data   []byte
		model  color.Model
	}{
		{"png", PNG, encodePNG(t, w, h), color.NRGBAModel},
		{"jpg", JPG, encodeJPEG(t, w, h), color.YCbCrModel},
		{"tiff", TIFF, encodeTIFF(t, w, h), color.RGBAModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := DecodeGlyphBitmap(GlyphBitmap{
				Data: tc.data, Format: tc.format, Width: w, Height: h,
			})
			tu.AssertNoErr(t, err)
			tu.Assert(t, img.Bounds() == image.Rect(0, 0, w, h))
			tu.Assert(t, img.ColorModel() == tc.model)

			// transparent edges are preserved for formats supporting alpha
			if tc.format == PNG {
				_, _, _, a := img.At(0, 0).RGBA()
				tu.Assert(t, a == 0)
				_, _, _, a = img.At(w-1, h-1).RGBA()
				tu.Assert(t, a == 0)
				// an opaque pixel keeps full coverage (no unwanted
				// premultiplication in the returned model)
				inner := img.(*image.NRGBA).NRGBAAt(1, 1)
				tu.Assert(t, inner.A == 255 && inner.R == uint8(20*2))
			}
		})
	}
}

func TestDecodeOriginAndBounds(t *testing.T) {
	const w, h = 3, 2
	data := []byte{255, 128, 0, 64, 32, 16}
	img, err := DecodeGlyphBitmap(GlyphBitmap{
		Data: data, Format: Gray, Width: w, Height: h,
		BearingX: 4, BearingY: 7,
	})
	tu.AssertNoErr(t, err)
	// screen-style coordinates: min is (BearingX, -BearingY)
	tu.Assert(t, img.Bounds() == image.Rect(4, -7, 4+w, -7+h))

	// pixel access is translated back into the inner image coordinates;
	// RGBA() reports 16-bit values
	_, _, _, a := img.At(4, -7).RGBA()
	tu.Assert(t, a == 0xffff)
	_, _, _, a = img.At(6, -6).RGBA()
	tu.Assert(t, a == 16*0x101)

	// points outside the bitmap are the zero (transparent) color
	_, _, _, a = img.At(0, 0).RGBA()
	tu.Assert(t, a == 0)

	// negative origins are valid and produce negative bounds
	img2, err := DecodeGlyphBitmap(GlyphBitmap{
		Data: data, Format: Gray, Width: w, Height: h,
		BearingX: -2, BearingY: -3,
	})
	tu.AssertNoErr(t, err)
	tu.Assert(t, img2.Bounds() == image.Rect(-2, 3, -2+w, 3+h))
	_, _, _, a = img2.At(-2, 3).RGBA()
	tu.Assert(t, a == 0xffff)
}

func TestDecodeBitmapErrors(t *testing.T) {
	pngData := encodePNG(t, 4, 3)
	cases := []struct {
		name string
		bm   GlyphBitmap
	}{
		{"empty data", GlyphBitmap{Format: Gray, Width: 1, Height: 1}},
		{"zero width", GlyphBitmap{Format: Gray, Width: 0, Height: 1, Data: []byte{0}}},
		{"zero height", GlyphBitmap{Format: Gray, Width: 1, Height: 0, Data: []byte{0}}},
		{"negative width", GlyphBitmap{Format: Gray, Width: -1, Height: 1}},
		{"truncated gray", GlyphBitmap{Format: Gray, Width: 4, Height: 4, Data: []byte{1, 2, 3}}},
		{"truncated bgra", GlyphBitmap{Format: BGRA, Width: 2, Height: 2, Data: make([]byte, 7)}},
		{"truncated mono aligned", GlyphBitmap{Format: BlackAndWhiteByteAligned, Width: 9, Height: 2, Data: []byte{0xFF, 0xFF}}},
		{"truncated mono packed", GlyphBitmap{Format: BlackAndWhite, Width: 16, Height: 2, Data: []byte{0xFF, 0xFF, 0xFF}}},
		{"truncated png", GlyphBitmap{Format: PNG, Width: 4, Height: 3, Data: pngData[:len(pngData)-4]}},
		{"garbage png", GlyphBitmap{Format: PNG, Width: 4, Height: 3, Data: []byte{1, 2, 3, 4, 5, 6, 7, 8}}},
		{"empty png", GlyphBitmap{Format: PNG, Width: 4, Height: 3}},
		{"garbage jpg", GlyphBitmap{Format: JPG, Width: 2, Height: 2, Data: []byte{0xFF, 0xD8, 0, 1, 2, 3}}},
		{"garbage tiff", GlyphBitmap{Format: TIFF, Width: 2, Height: 2, Data: []byte("II*\x00garbage")}},
		{"unknown format", GlyphBitmap{Format: BitmapFormat(42), Width: 1, Height: 1, Data: []byte{0}}},
		{"encoded size mismatch", GlyphBitmap{Format: PNG, Width: 5, Height: 5, Data: pngData}},
		{"oversized width", GlyphBitmap{Format: Gray, Width: maxBitmapDimension + 1, Height: 1}},
		{"oversized area", GlyphBitmap{Format: Gray, Width: 4096, Height: 4096, Data: make([]byte, 1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img, err := DecodeGlyphBitmap(tc.bm)
			tu.Assert(t, err != nil)
			tu.Assert(t, img == nil)
		})
	}
}

// repeated calls must be independent and data must not be mutated
func TestBitmapFormatString(t *testing.T) {
	cases := map[BitmapFormat]string{
		BlackAndWhite:            "BlackAndWhite",
		BlackAndWhiteByteAligned: "BlackAndWhiteByteAligned",
		Gray:                     "Gray",
		BGRA:                     "BGRA",
		PNG:                      "PNG",
		JPG:                      "JPG",
		TIFF:                     "TIFF",
		BitmapFormat(42):         "BitmapFormat(42)",
	}
	for format, want := range cases {
		tu.Assert(t, format.String() == want)
	}
}

// repeated calls must be independent and data must not be mutated
func TestDecodeRepeatedCalls(t *testing.T) {
	const w, h = 4, 2
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	original := append([]byte(nil), data...)
	bm := GlyphBitmap{Data: data, Format: Gray, Width: w, Height: h, BearingX: 1, BearingY: 1}

	first, err := DecodeGlyphBitmap(bm)
	tu.AssertNoErr(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			img, err := DecodeGlyphBitmap(bm)
			tu.Assert(t, err == nil)
			tu.Assert(t, img.Bounds() == image.Rect(1, -1, 1+w, -1+h))
		}()
	}
	wg.Wait()

	tu.Assert(t, bytes.Equal(data, original))
	// mutating one decoded image does not affect another
	innerImage(first).(*image.Alpha).Pix[0] = 99
	again, err := DecodeGlyphBitmap(bm)
	tu.AssertNoErr(t, err)
	tu.Assert(t, innerImage(again).(*image.Alpha).Pix[0] == 1)
}

// ---------------- synthetic CBLC/CBDT end to end ----------------

type cbdtEntry struct {
	indexFormat uint16 // 1, 2 or 5
	imageFormat uint16 // EBDT/CBDT image format
	width       uint8
	height      uint8
	bearingX    int8
	bearingY    int8
	data        []byte // image bytes, excluding metrics headers
}

// buildCBLCFont wraps [source] with synthetic CBLC/CBDT tables containing
// one strike and one subtable covering only [gid].
func buildCBLCFont(t *testing.T, source []byte, gid GID, e cbdtEntry) *Face {
	t.Helper()
	// ---- index subtable data and image block ----
	var indexData, imageBlock []byte
	switch e.indexFormat {
	case 1: // 32-bit offsets, numGlyphs+1 entries for the single covered gid
		indexData = make([]byte, 8)
		var metrics []byte
		if e.imageFormat == 17 || e.imageFormat == 18 {
			// PNG formats prefix the image with a 4-byte length after the
			// 5-byte SmallGlyphMetrics header.
			metrics = make([]byte, 5+4)
			metrics[0] = e.height
			metrics[1] = e.width
			metrics[2] = byte(e.bearingX)
			metrics[3] = byte(e.bearingY)
			binary.BigEndian.PutUint32(metrics[5:], uint32(len(e.data)))
			metrics = append(metrics, e.data...)
		} else {
			// SmallGlyphMetrics layout: Height, Width, BearingX, BearingY, Advance
			metrics = append([]byte{e.height, e.width, byte(e.bearingX), byte(e.bearingY), 0}, e.data...)
		}
		binary.BigEndian.PutUint32(indexData[0:], 0)
		binary.BigEndian.PutUint32(indexData[4:], uint32(len(metrics)))
		imageBlock = metrics
	case 2: // fixed image size, big metrics shared in the index subtable
		indexData = make([]byte, 16)
		binary.BigEndian.PutUint32(indexData, uint32(len(e.data)))
		indexData[4] = e.height
		indexData[5] = e.width
		indexData[6] = byte(e.bearingX)
		indexData[7] = byte(e.bearingY)
		imageBlock = e.data
	case 5: // sorted glyph ids, fixed image size, big metrics in index
		indexData = make([]byte, 18)
		binary.BigEndian.PutUint32(indexData, uint32(len(e.data)))
		indexData[4] = e.height
		indexData[5] = e.width
		indexData[6] = byte(e.bearingX)
		indexData[7] = byte(e.bearingY)
		binary.BigEndian.PutUint32(indexData[12:], 1) // one glyph id
		binary.BigEndian.PutUint16(indexData[16:], uint16(gid))
		imageBlock = e.data
	default:
		t.Fatalf("unsupported index format %d", e.indexFormat)
	}

	// ---- CBDT: version + index data + image data ----
	cbdt := []byte{0, 2, 0, 0}
	imageDataOffset := uint32(len(cbdt) + len(indexData))
	cbdt = append(cbdt, indexData...)
	cbdt = append(cbdt, imageBlock...)

	// ---- index sub header ----
	indexSubHeader := make([]byte, 8+len(indexData))
	binary.BigEndian.PutUint16(indexSubHeader, e.indexFormat)
	binary.BigEndian.PutUint16(indexSubHeader[2:], e.imageFormat)
	binary.BigEndian.PutUint32(indexSubHeader[4:], imageDataOffset)
	copy(indexSubHeader[8:], indexData)

	// ---- CBLC ----
	const (
		indexSubTableArrayOffset = 56
		indexSubTableArraySize   = 8
	)
	cblc := make([]byte, indexSubTableArrayOffset+indexSubTableArraySize)
	binary.BigEndian.PutUint16(cblc, 3) // version 3
	binary.BigEndian.PutUint32(cblc[4:], 1)
	// the BitmapSize record starts at absolute offset 8
	binary.BigEndian.PutUint32(cblc[8:], indexSubTableArrayOffset) // indexSubTableArrayOffset
	binary.BigEndian.PutUint32(cblc[12:], uint32(8+len(indexSubHeader)))
	binary.BigEndian.PutUint32(cblc[16:], 1) // one index subtable
	cblc[24] = 16                            // Hori.Ascender (SbitLineMetrics at offset 16)
	cblc[48] = byte(gid)                     // startGlyphIndex
	cblc[50] = byte(gid)                     // endGlyphIndex
	cblc[52] = 16                            // PpemX
	cblc[53] = 16                            // PpemY
	cblc[54] = 8                             // bitDepth
	// index subtable array record (at offset 56)
	binary.BigEndian.PutUint16(cblc[56:], uint16(gid))
	binary.BigEndian.PutUint16(cblc[58:], uint16(gid))
	binary.BigEndian.PutUint32(cblc[60:], 8) // relative offset to sub header
	cblc = append(cblc, indexSubHeader...)

	ld, err := ot.NewLoader(bytes.NewReader(source))
	tu.AssertNoErr(t, err)
	var ts []ot.Table
	for _, tag := range ld.Tables() {
		raw, err := ld.RawTable(tag)
		tu.AssertNoErr(t, err)
		ts = append(ts, ot.Table{Tag: tag, Content: raw})
	}
	ts = append(ts,
		ot.Table{Tag: ot.MustNewTag("CBLC"), Content: cblc},
		ot.Table{Tag: ot.MustNewTag("CBDT"), Content: cbdt},
	)
	face, err := ParseTTF(bytes.NewReader(ot.WriteOpentype(ts, ot.TrueType)))
	tu.AssertNoErr(t, err)
	return face
}

func readLocalFont(t *testing.T, path string) []byte {
	t.Helper()
	source, err := td.Files.ReadFile(path)
	if err != nil {
		source, err = os.ReadFile(path)
	}
	tu.AssertNoErr(t, err)
	return source
}

func TestSyntheticCBDTFormats(t *testing.T) {
	source := readLocalFont(t, "testdata/Roboto-Regular.ttf")
	base, err := ParseTTF(bytes.NewReader(source))
	tu.AssertNoErr(t, err)
	gid, ok := base.NominalGlyph('A')
	tu.Assert(t, ok)

	const w, h = 4, 3
	pngBytes := encodePNG(t, w, h)
	grayBytes := []byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120}
	bgraBytes := make([]byte, w*h*4)
	for i := range bgraBytes {
		bgraBytes[i] = byte(i + 1)
	}
	monoBytes := make([]byte, ((w+7)/8)*h)
	monoBytes[0] = 0b10101010

	cases := []struct {
		name       string
		entry      cbdtEntry
		wantFormat BitmapFormat
		check      func(t *testing.T, img image.Image)
	}{
		{
			"gray index1 fmt6", cbdtEntry{1, 6, w, h, 2, 5, grayBytes}, Gray,
			func(t *testing.T, img image.Image) {
				a := img.(*image.Alpha)
				tu.Assert(t, a.Pix[0] == 10 && a.Pix[len(a.Pix)-1] == 120)
			},
		},
		{
			"gray index2 fmt7", cbdtEntry{2, 7, w, h, 2, 5, grayBytes}, Gray,
			func(t *testing.T, img image.Image) {
				tu.Assert(t, img.(*image.Alpha).Pix[5] == 60)
			},
		},
		{
			"bgra index5 fmt9", cbdtEntry{5, 9, w, h, -1, 6, bgraBytes}, BGRA,
			func(t *testing.T, img image.Image) {
				n := img.(*image.NRGBA)
				// first pixel stored B=1,G=2,R=3,A=4
				tu.Assert(t, n.NRGBAAt(0, 0) == color.NRGBA{R: 3, G: 2, B: 1, A: 4})
			},
		},
		{
			"bgra index1 fmt8", cbdtEntry{1, 8, w, h, 2, 5, bgraBytes}, BGRA,
			func(t *testing.T, img image.Image) {
				tu.Assert(t, img.ColorModel() == color.NRGBAModel)
			},
		},
		{
			"mono fmt1", cbdtEntry{1, 1, w, h, 2, 5, monoBytes}, BlackAndWhiteByteAligned,
			func(t *testing.T, img image.Image) {
				p := img.(*image.Paletted)
				tu.Assert(t, p.ColorIndexAt(0, 0) == 1 && p.ColorIndexAt(1, 0) == 0)
			},
		},
		{
			"png fmt17", cbdtEntry{1, 17, w, h, 2, 5, pngBytes}, PNG,
			func(t *testing.T, img image.Image) {
				tu.Assert(t, img.Bounds().Dx() == w && img.Bounds().Dy() == h)
				_, _, _, a := img.At(0, 0).RGBA()
				tu.Assert(t, a == 0)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			face := buildCBLCFont(t, source, gid, tc.entry)
			face.SetPpem(16, 16)

			bm, ok := face.GlyphDataBitmap(gid)
			tu.Assert(t, ok)
			tu.Assert(t, bm.Format == tc.wantFormat)
			tu.Assert(t, bm.Width == w && bm.Height == h)
			tu.Assert(t, bm.BearingX == int(tc.entry.bearingX))
			tu.Assert(t, bm.BearingY == int(tc.entry.bearingY))
			// outline fallback is still attached
			tu.Assert(t, bm.Outline != nil)

			img, has, err := face.GlyphBitmapImage(gid)
			tu.AssertNoErr(t, err)
			tu.Assert(t, has)
			// bounds encode the origin
			tu.Assert(t, img.Bounds() == image.Rect(int(tc.entry.bearingX), -int(tc.entry.bearingY),
				int(tc.entry.bearingX)+w, -int(tc.entry.bearingY)+h))
			tc.check(t, innerImage(img))

			// a glyph absent from the subtable falls back to the outline
			missing := GID(1)
			if gid == 1 {
				missing = 2
			}
			_, hasBitmap, err := face.GlyphBitmapImage(missing)
			tu.Assert(t, err == nil)
			tu.Assert(t, !hasBitmap)
			_, hasOutline := face.GlyphDataOutline(missing)
			tu.Assert(t, hasOutline)
		})
	}
}

func TestSyntheticCBDTTruncated(t *testing.T) {
	source := readLocalFont(t, "testdata/Roboto-Regular.ttf")
	base, err := ParseTTF(bytes.NewReader(source))
	tu.AssertNoErr(t, err)
	gid, ok := base.NominalGlyph('A')
	tu.Assert(t, ok)
	face := buildCBLCFont(t, source, gid, cbdtEntry{
		indexFormat: 1, imageFormat: 6, width: 8, height: 8,
		bearingX: 0, bearingY: 8,
		data: []byte{1, 2, 3}, // truncated: 64 bytes expected
	})
	face.SetPpem(16, 16)
	// the font layer rejects the truncated record; the unified entry then
	// reports "no bitmap", letting the caller fall back to the outline
	_, has, err := face.GlyphBitmapImage(gid)
	tu.Assert(t, err == nil)
	tu.Assert(t, !has)
	// at the decoder level, a truncated raw payload is a hard error rather
	// than an out-of-bounds read
	img, err := DecodeGlyphBitmap(GlyphBitmap{
		Data: []byte{1, 2, 3}, Format: Gray, Width: 8, Height: 8,
	})
	tu.Assert(t, err != nil)
	tu.Assert(t, img == nil)
}

// ---------------- synthetic sbix end to end ----------------

type sbixGlyph struct {
	gid     GID
	originX int16
	originY int16
	graphic ot.Tag
	data    []byte
	dupeOf  GID  // when nonzero, emit a 'dupe' entry pointing at it
	isEmpty bool // emit no data (zero-length strike entry)
}

func buildRawSbixTable(t *testing.T, numGlyphs int, ppems []uint16, strikes map[uint16][]sbixGlyph) []byte {
	t.Helper()
	header := make([]byte, 8+4*len(ppems))
	binary.BigEndian.PutUint16(header, 1)
	binary.BigEndian.PutUint32(header[4:], uint32(len(ppems)))

	// build every strike body first, recording its absolute offset
	var bodies [][]byte
	offset := uint32(len(header))
	for i, ppem := range ppems {
		binary.BigEndian.PutUint32(header[8+4*i:], offset)

		offsets := make([]byte, 4*(numGlyphs+1))
		var glyphData []byte
		dataStart := uint32(4 + len(offsets)) // offset relative to the strike
		cursor := dataStart
		// entries indexed by gid, marking the data region [start,end)
		type region struct{ start, end uint32 }
		regions := make(map[GID]region)
		for _, g := range strikes[ppem] {
			var entry []byte
			switch {
			case g.isEmpty:
				continue // zero-length entry: no region
			case g.dupeOf != 0:
				entry = make([]byte, 10)
				binary.BigEndian.PutUint32(entry[4:], uint32(dupe))
				binary.BigEndian.PutUint16(entry[8:], uint16(g.dupeOf))
			default:
				entry = make([]byte, 8+len(g.data))
				binary.BigEndian.PutUint16(entry, uint16(g.originX))
				binary.BigEndian.PutUint16(entry[2:], uint16(g.originY))
				binary.BigEndian.PutUint32(entry[4:], uint32(g.graphic))
				copy(entry[8:], g.data)
			}
			regions[g.gid] = region{cursor, cursor + uint32(len(entry))}
			cursor += uint32(len(entry))
			glyphData = append(glyphData, entry...)
		}
		// emit a monotonic loca array: empty glyphs reuse the current data
		// boundary instead of resetting to zero
		last := dataStart
		for k := 0; k <= numGlyphs; k++ {
			if r, ok := regions[GID(k)]; ok {
				binary.BigEndian.PutUint32(offsets[4*k:], r.start)
				last = r.end
			} else {
				binary.BigEndian.PutUint32(offsets[4*k:], last)
			}
		}

		body := make([]byte, 4)
		binary.BigEndian.PutUint16(body, ppem)
		binary.BigEndian.PutUint16(body[2:], 72) // ppi
		body = append(body, offsets...)
		body = append(body, glyphData...)
		bodies = append(bodies, body)
		offset += uint32(len(body))
	}

	table := append([]byte(nil), header...)
	for _, body := range bodies {
		table = append(table, body...)
	}
	return table
}

func buildSbixFont(t *testing.T, source []byte, numGlyphs int, ppems []uint16, strikes map[uint16][]sbixGlyph) *Face {
	t.Helper()
	table := buildRawSbixTable(t, numGlyphs, ppems, strikes)

	ld, err := ot.NewLoader(bytes.NewReader(source))
	tu.AssertNoErr(t, err)
	var ts []ot.Table
	for _, tag := range ld.Tables() {
		raw, err := ld.RawTable(tag)
		tu.AssertNoErr(t, err)
		ts = append(ts, ot.Table{Tag: tag, Content: raw})
	}
	ts = append(ts, ot.Table{Tag: ot.MustNewTag("sbix"), Content: table})
	face, err := ParseTTF(bytes.NewReader(ot.WriteOpentype(ts, ot.TrueType)))
	tu.AssertNoErr(t, err)
	return face
}

func TestSyntheticSbix(t *testing.T) {
	source := readLocalFont(t, "testdata/Roboto-Regular.ttf")
	base, err := ParseTTF(bytes.NewReader(source))
	tu.AssertNoErr(t, err)
	numGlyphs := base.nGlyphs

	const w, h = 5, 4
	pngBytes := encodePNG(t, w, h)
	jpgBytes := encodeJPEG(t, w, h)
	tiffBytes := encodeTIFF(t, w, h)
	truncatedPNG := pngBytes[:len(pngBytes)-6]

	strikes := map[uint16][]sbixGlyph{
		16: {
			{gid: 1, originX: 2, originY: -1, graphic: tagPNG, data: pngBytes},
			{gid: 2, originX: 0, originY: 0, graphic: tagJPG, data: jpgBytes},
			{gid: 3, originX: -3, originY: 1, graphic: tagTIFF, data: tiffBytes},
			{gid: 4, originX: 0, originY: 0, graphic: tagPNG, data: truncatedPNG},
			{gid: 5, isEmpty: true},
			{gid: 6, dupeOf: 1},
		},
		32: {
			{gid: 1, originX: 4, originY: 2, graphic: tagPNG, data: pngBytes},
		},
	}
	face := buildSbixFont(t, source, numGlyphs, []uint16{16, 32}, strikes)

	t.Run("formats and origin", func(t *testing.T) {
		face.SetPpem(16, 16)
		// PNG with negative bottom origin: BearingY = originY + height
		bm, ok := face.GlyphDataBitmap(1)
		tu.Assert(t, ok)
		tu.Assert(t, bm.Format == PNG && bm.Width == w && bm.Height == h)
		tu.Assert(t, bm.BearingX == 2 && bm.BearingY == -1+h)
		img, has, err := face.GlyphBitmapImage(1)
		tu.AssertNoErr(t, err)
		tu.Assert(t, has)
		tu.Assert(t, img.Bounds() == image.Rect(2, -(h-1), 2+w, -(h-1)+h))

		bm, ok = face.GlyphDataBitmap(2)
		tu.Assert(t, ok && bm.Format == JPG)
		img, has, err = face.GlyphBitmapImage(2)
		tu.AssertNoErr(t, err)
		tu.Assert(t, has)
		tu.Assert(t, img.ColorModel() == color.YCbCrModel)

		bm, ok = face.GlyphDataBitmap(3)
		tu.Assert(t, ok && bm.Format == TIFF && bm.BearingX == -3)
		img, has, err = face.GlyphBitmapImage(3)
		tu.AssertNoErr(t, err)
		tu.Assert(t, has)
		// negative origin yields negative min X
		tu.Assert(t, img.Bounds().Min.X == -3)
	})

	t.Run("truncated compressed data", func(t *testing.T) {
		face.SetPpem(16, 16)
		bm, ok := face.GlyphDataBitmap(4)
		tu.Assert(t, ok) // raw value stays available
		tu.Assert(t, bytes.Equal(bm.Data, truncatedPNG))
		_, has, err := face.GlyphBitmapImage(4)
		tu.Assert(t, has && err != nil)
	})

	t.Run("missing glyph and empty data", func(t *testing.T) {
		face.SetPpem(16, 16)
		// gid 5 has a zero-length strike entry: no bitmap, outline fallback
		_, has, err := face.GlyphBitmapImage(5)
		tu.Assert(t, err == nil && !has)
		if _, hasOutline := face.GlyphDataOutline(5); hasOutline {
			tu.Assert(t, face.GlyphData(5) != nil)
		}
		// gid without any entry
		_, has, err = face.GlyphBitmapImage(9)
		tu.Assert(t, err == nil && !has)
	})

	t.Run("dupe resolution", func(t *testing.T) {
		face.SetPpem(16, 16)
		bm, ok := face.GlyphDataBitmap(6)
		tu.Assert(t, ok)
		tu.Assert(t, bytes.Equal(bm.Data, pngBytes))
		img, has, err := face.GlyphBitmapImage(6)
		tu.AssertNoErr(t, err)
		tu.Assert(t, has)
		tu.Assert(t, img.Bounds().Dx() == w)
	})

	t.Run("multi resolution selection", func(t *testing.T) {
		// request 32ppem: the second strike with larger bearings is selected
		face.SetPpem(32, 32)
		bm, ok := face.GlyphDataBitmap(1)
		tu.Assert(t, ok)
		tu.Assert(t, bm.BearingX == 4 && bm.BearingY == 2+h)
		// request 10ppem: the smallest (16) strike is the best match
		face.SetPpem(10, 10)
		bm, ok = face.GlyphDataBitmap(1)
		tu.Assert(t, ok)
		tu.Assert(t, bm.BearingX == 2)
	})
}

// ---------------- real font fixtures ----------------

func TestRealBitmapFontsDecode(t *testing.T) {
	cases := []struct {
		path string
		ppem uint16
	}{
		{"toys/CBLC1.ttf", 94},
		{"toys/CBLC2.ttf", 94},
		{"bitmap/NotoColorEmoji.ttf", 128},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			font := loadFont(t, tc.path)
			face := NewFace(font)
			face.SetPpem(tc.ppem, tc.ppem)
			count := 0
			iter := font.Cmap.Iter()
			for iter.Next() && count < 5 {
				_, g := iter.Char()
				bm, ok := face.GlyphDataBitmap(g)
				if !ok {
					continue
				}
				count++
				img, has, err := face.GlyphBitmapImage(g)
				tu.AssertNoErr(t, err)
				tu.Assert(t, has)
				tu.Assert(t, img.Bounds().Dx() == bm.Width)
				tu.Assert(t, img.Bounds().Dy() == bm.Height)
				// bounds encode the bearings exposed on GlyphBitmap
				tu.Assert(t, img.Bounds().Min.X == bm.BearingX)
				tu.Assert(t, img.Bounds().Min.Y == -bm.BearingY)
				// color model matches the payload format
				switch bm.Format {
				case PNG:
					switch innerImage(img).(type) {
					case *image.NRGBA, *image.RGBA, *image.Alpha, *image.Gray, *image.Paletted:
					default:
						t.Fatalf("unexpected PNG image type %T", innerImage(img))
					}
				case BlackAndWhite, BlackAndWhiteByteAligned:
					_, isPaletted := innerImage(img).(*image.Paletted)
					tu.Assert(t, isPaletted)
				}
			}
			tu.Assert(t, count > 0)
		})
	}
}

func TestRealMonochromeFontsPixels(t *testing.T) {
	for _, path := range []string{"bitmap/simsun.ttc", "collections/Gacha_9.dfont", "bitmap/cherry-10-r.otb"} {
		t.Run(path, func(t *testing.T) {
			content, err := td.Files.ReadFile(path)
			tu.AssertNoErr(t, err)
			faces, err := ParseTTC(bytes.NewReader(content))
			tu.AssertNoErr(t, err)
			face := faces[0]
			face.SetPpem(12, 12)

			decoded := 0
			iter := face.Cmap.Iter()
			for iter.Next() && decoded < 20 {
				_, g := iter.Char()
				bm, ok := face.GlyphDataBitmap(g)
				if !ok {
					continue
				}
				tu.Assert(t, bm.Format == BlackAndWhite || bm.Format == BlackAndWhiteByteAligned)
				// space-like glyphs legitimately carry an empty bitmap
				if bm.Width == 0 || bm.Height == 0 {
					_, _, err := face.GlyphBitmapImage(g)
					tu.Assert(t, err != nil)
					continue
				}
				img, has, err := face.GlyphBitmapImage(g)
				tu.AssertNoErr(t, err)
				tu.Assert(t, has)
				pal, ok := innerImage(img).(*image.Paletted)
				tu.Assert(t, ok)
				tu.Assert(t, pal.Bounds().Dx() == bm.Width && pal.Bounds().Dy() == bm.Height)
				// every pixel is a valid palette index (0 or 1): no OOB read
				for i := range pal.Pix {
					tu.Assert(t, pal.Pix[i] < 2)
				}
				decoded++
			}
			tu.Assert(t, decoded > 0)
		})
	}
}

func TestRealSbixMultiPpem(t *testing.T) {
	for _, path := range []string{"toys/Sbix1.ttf", "toys/Sbix2.ttf", "toys/Sbix3.ttf", "toys/Feat.ttf"} {
		t.Run(path, func(t *testing.T) {
			font := loadFont(t, path)
			face := NewFace(font)
			for _, ppem := range []uint16{10, 100, 0} { // 0 selects the largest strike
				face.SetPpem(ppem, ppem)
				iter := font.Cmap.Iter()
				decoded := 0
				for iter.Next() && decoded < 3 {
					_, g := iter.Char()
					bm, ok := face.GlyphDataBitmap(g)
					if !ok {
						continue
					}
					img, has, err := face.GlyphBitmapImage(g)
					tu.AssertNoErr(t, err)
					tu.Assert(t, has)
					tu.Assert(t, img.Bounds().Dx() == bm.Width && img.Bounds().Dy() == bm.Height)
					decoded++
				}
				tu.Assert(t, decoded > 0)
			}
		})
	}
}

// the original GlyphDataBitmap return contract is unchanged: the same
// glyphs are exposed and decode through the new entry point
func TestGlyphDataBitmapCompatibility(t *testing.T) {
	font := loadFont(t, "toys/CBLC1.ttf")
	face := NewFace(font)
	face.SetPpem(94, 94)
	for gid := GID(0); gid < GID(font.nGlyphs); gid++ {
		bm, ok := face.GlyphDataBitmap(gid)
		img, hasImg, err := face.GlyphBitmapImage(gid)
		tu.Assert(t, ok == hasImg)
		if !ok {
			continue
		}
		tu.AssertNoErr(t, err)
		tu.Assert(t, img != nil)
		tu.Assert(t, len(bm.Data) > 0)
		// GlyphData still returns the bitmap variant
		if data := face.GlyphData(gid); data != nil {
			_, isBitmap := data.(GlyphBitmap)
			_, isOutline := data.(GlyphOutline)
			tu.Assert(t, isBitmap || isOutline)
		}
	}
}
