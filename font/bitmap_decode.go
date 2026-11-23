// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package font

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"

	"golang.org/x/image/tiff"
)

// Resource limits shared by every bitmap decoder. They guard against
// malicious fonts advertising dimensions that would allocate huge images.
const (
	// maxBitmapDimension is the maximum accepted width or height, in pixels,
	// of a decoded bitmap glyph.
	maxBitmapDimension = 16384
	// maxBitmapPixels bounds the number of pixels of a decoded bitmap glyph,
	// including the padding bytes used by row-aligned formats.
	maxBitmapPixels = 1 << 27 // 128 million pixels, at least 128 MiB at 1 byte/pixel
)

// ErrBitmapNotSupported is returned when a bitmap glyph uses a format the
// decoder does not know how to decode. Callers may then fall back to the
// glyph outline, if any.
var ErrBitmapNotSupported = errors.New("unsupported bitmap glyph format")

// palette used for 1-bit black and white bitmaps. OpenType stores 1 for an
// inked pixel and 0 for the background, so the black entry comes second.
var bwPalette = color.Palette{
	color.RGBA{R: 255, G: 255, B: 255, A: 255}, // index 0: white background
	color.RGBA{R: 0, G: 0, B: 0, A: 255},       // index 1: black ink
}

// DecodeGlyphBitmap decodes the raw data of a [GlyphBitmap] into a ready to
// draw [image.Image].
//
// The returned image covers the glyph bitmap, positioned in a screen-style
// coordinate system whose origin is the glyph origin: y grows downward and
// its [image.Image.Bounds] Min point is (BearingX, -BearingY), so that the
// image may be drawn directly at the pen position without an extra
// translation.
//
// Every format exposed by [GlyphBitmap.Format] is supported:
//   - [BlackAndWhite] and [BlackAndWhiteByteAligned] 1-bit bitmaps are
//     decoded as [*image.Paletted] images using a black/white palette,
//   - [Gray] and [BGRA] bitmaps are decoded as [*image.Alpha] and
//     [*image.NRGBA] images respectively,
//   - [PNG], [JPG] and [TIFF] are decoded with the standard library
//     decoders, preserving their native color model.
//
// The decoder is stateless and read-only: calling it repeatedly with the
// same input yields identical images and never mutates [GlyphBitmap.Data].
// An error is returned for empty data, truncated encodings, dimensions
// exceeding the resource limits or unsupported formats; it never panics on
// malformed input.
func DecodeGlyphBitmap(bm GlyphBitmap) (image.Image, error) {
	width, height := bm.Width, bm.Height
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid bitmap dimensions %dx%d", width, height)
	}
	if width > maxBitmapDimension || height > maxBitmapDimension {
		return nil, fmt.Errorf("bitmap dimensions %dx%d exceed the %d pixels limit", width, height, maxBitmapDimension)
	}
	if width*height > maxBitmapPixels {
		return nil, fmt.Errorf("bitmap of %d pixels exceeds the %d pixels limit", width*height, maxBitmapPixels)
	}

	var (
		img image.Image
		err error
	)
	switch bm.Format {
	case BlackAndWhite, BlackAndWhiteByteAligned:
		img, err = decodeMonochromeBitmap(bm)
	case Gray:
		img, err = decodeGrayBitmap(bm)
	case BGRA:
		img, err = decodeBGRABitmap(bm)
	case PNG:
		img, err = decodeCompressedBitmap(png.Decode, bm)
	case JPG:
		img, err = decodeCompressedBitmap(jpeg.Decode, bm)
	case TIFF:
		img, err = decodeCompressedBitmap(tiff.Decode, bm)
	default:
		return nil, fmt.Errorf("%w: %d", ErrBitmapNotSupported, bm.Format)
	}
	if err != nil {
		return nil, err
	}

	// Shift the image so that its bounds encode the glyph origin. A zero
	// bearing returns the image untouched, so repeated calls with a zero
	// origin do not wrap images in additional layers.
	if bm.BearingX == 0 && bm.BearingY == 0 {
		return img, nil
	}
	return newOriginImage(img, bm.BearingX, bm.BearingY), nil
}

// decodeCompressedBitmap decodes PNG, JPEG and TIFF payloads. The decoded
// dimensions are cross-checked against the metrics advertised by the font,
// which prevents a swapped or corrupted payload from silently producing an
// image with unexpected bounds.
func decodeCompressedBitmap(decode func(r io.Reader) (image.Image, error), bm GlyphBitmap) (image.Image, error) {
	if len(bm.Data) == 0 {
		return nil, errors.New("empty compressed bitmap data")
	}
	img, err := decode(bytes.NewReader(bm.Data))
	if err != nil {
		return nil, fmt.Errorf("decoding %s bitmap: %w", bm.Format, err)
	}
	bounds := img.Bounds()
	if w, h := bounds.Dx(), bounds.Dy(); w != bm.Width || h != bm.Height {
		return nil, fmt.Errorf("bitmap size mismatch: font metrics %dx%d, encoded image %dx%d", bm.Width, bm.Height, w, h)
	}
	return img, nil
}

// decodeMonochromeBitmap turns a 1-bit per pixel bitmap into a paletted
// image. The most significant bit of each byte is the leftmost pixel, bits
// are stored big-endian and rows are padded to byte boundaries when the
// format is [BlackAndWhiteByteAligned].
func decodeMonochromeBitmap(bm GlyphBitmap) (image.Image, error) {
	width, height := bm.Width, bm.Height
	rowLen := (width + 7) / 8 // bytes per row, rows are byte aligned
	if bm.Format == BlackAndWhiteByteAligned {
		if len(bm.Data) < rowLen*height {
			return nil, fmt.Errorf("truncated monochrome bitmap: expected %d bytes, got %d", rowLen*height, len(bm.Data))
		}
	} else {
		// bit-aligned data is a continuous bit stream, padded only at the
		// end of the bitmap.
		if needed := width * height; len(bm.Data)*8 < needed {
			return nil, fmt.Errorf("truncated monochrome bitmap: expected %d bits, got %d", needed, len(bm.Data)*8)
		}
	}

	img := image.NewPaletted(image.Rect(0, 0, width, height), bwPalette)
	if bm.Format == BlackAndWhiteByteAligned {
		// each row starts on a fresh byte, so the padding bits are skipped
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				bitIndex := y*rowLen*8 + x
				ink := (bm.Data[bitIndex/8] >> (7 - uint(bitIndex%8))) & 1
				img.SetColorIndex(x, y, ink)
			}
		}
		return img, nil
	}
	// bit-aligned data is one continuous big-endian bit stream spanning
	// the whole bitmap, padded only after the final row.
	for i := 0; i < width*height; i++ {
		ink := (bm.Data[i/8] >> (7 - uint(i%8))) & 1
		img.SetColorIndex(i%width, i/width, ink)
	}
	return img, nil
}

// decodeGrayBitmap decodes an 8-bit per pixel grayscale bitmap (EBDT/CBDT
// image formats 6 and 7). Rows are byte aligned.
func decodeGrayBitmap(bm GlyphBitmap) (image.Image, error) {
	width, height := bm.Width, bm.Height
	rowLen := width // 8-bit samples are always byte aligned
	expected := rowLen * height
	if len(bm.Data) < expected {
		return nil, fmt.Errorf("truncated grayscale bitmap: expected %d bytes, got %d", expected, len(bm.Data))
	}
	img := image.NewAlpha(image.Rect(0, 0, width, height))
	img.Stride = rowLen
	copy(img.Pix, bm.Data[:expected])
	return img, nil
}

// decodeBGRABitmap decodes a 32-bit per pixel color bitmap (EBDT/CBDT image
// formats 8 and 9). Samples are stored as blue, green, red, alpha, each one
// byte, big-endian within the font. The output is non-premultiplied RGBA.
func decodeBGRABitmap(bm GlyphBitmap) (image.Image, error) {
	width, height := bm.Width, bm.Height
	rowLen := width * 4
	expected := rowLen * height
	if len(bm.Data) < expected {
		return nil, fmt.Errorf("truncated BGRA bitmap: expected %d bytes, got %d", expected, len(bm.Data))
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.Stride = rowLen
	for y := 0; y < height; y++ {
		src := bm.Data[y*rowLen : (y+1)*rowLen]
		dst := img.Pix[y*rowLen : (y+1)*rowLen]
		for x := 0; x < width; x++ {
			b, g, r, a := src[4*x], src[4*x+1], src[4*x+2], src[4*x+3]
			dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = r, g, b, a
		}
	}
	return img, nil
}

// originImage translates an image so that its bounds encode the glyph
// origin: Bounds().Min equals (bearingX, -bearingY). Pixel data is shared
// with the wrapped image and is never copied.
type originImage struct {
	img                    image.Image
	bearingX, bearingY     int
	minX, minY, maxX, maxY int
}

func newOriginImage(img image.Image, bearingX, bearingY int) *originImage {
	b := img.Bounds()
	return &originImage{
		img:      img,
		bearingX: bearingX,
		bearingY: bearingY,
		minX:     bearingX,
		minY:     -bearingY,
		maxX:     bearingX + b.Dx(),
		maxY:     -bearingY + b.Dy(),
	}
}

func (o *originImage) ColorModel() color.Model { return o.img.ColorModel() }

func (o *originImage) Bounds() image.Rectangle {
	return image.Rect(o.minX, o.minY, o.maxX, o.maxY)
}

func (o *originImage) At(x, y int) color.Color {
	return o.img.At(x-o.bearingX, y+o.bearingY)
}

// String helps error messages report the bitmap format by name.
func (f BitmapFormat) String() string {
	switch f {
	case BlackAndWhite:
		return "BlackAndWhite"
	case BlackAndWhiteByteAligned:
		return "BlackAndWhiteByteAligned"
	case Gray:
		return "Gray"
	case BGRA:
		return "BGRA"
	case PNG:
		return "PNG"
	case JPG:
		return "JPG"
	case TIFF:
		return "TIFF"
	default:
		return fmt.Sprintf("BitmapFormat(%d)", uint8(f))
	}
}
