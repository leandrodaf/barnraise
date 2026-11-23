// Package imagex reduces screenshots before any agent reads them: a full
// phone print costs ~3,350 tokens, a 720 px wide one ~1,500.
package imagex

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoders
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// DefaultWidth is the target width for agents.
const DefaultWidth = 720

// IsImage reports whether a path looks like a raster image.
func IsImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif":
		return true
	}
	return false
}

// Reduce scales img to at most width pixels wide (keeping the ratio) with a
// box filter; images already narrow enough are returned unchanged.
func Reduce(img image.Image, width int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if width <= 0 || w <= width {
		return img
	}
	nh := h * width / w
	if nh < 1 {
		nh = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, width, nh))
	for y := 0; y < nh; y++ {
		y0 := b.Min.Y + y*h/nh
		y1 := b.Min.Y + (y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < width; x++ {
			x0 := b.Min.X + x*w/width
			x1 := b.Min.X + (x+1)*w/width
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := img.At(xx, yy).RGBA()
					r, g, bl, a = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca)
					n++
				}
			}
			out.Set(x, y, color.RGBA64{R: uint16(r / n), G: uint16(g / n), B: uint16(bl / n), A: uint16(a / n)})
		}
	}
	return out
}

// ReduceFile writes a reduced copy of src into dir (cached by content hash)
// and returns its path; src itself is returned when it is already small.
func ReduceFile(src, dir string, width int) (string, bool, error) {
	f, err := os.Open(src)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	sum := hex.EncodeToString(h.Sum(nil))[:16]
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", false, err
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return "", false, fmt.Errorf("decode %s: %w", src, err)
	}
	if cfg.Width <= width {
		return src, false, nil
	}
	ext := ".jpg"
	if strings.EqualFold(filepath.Ext(src), ".png") {
		ext = ".png"
	}
	dst := filepath.Join(dir, fmt.Sprintf("%s-%dw%s", sum, width, ext))
	if _, err := os.Stat(dst); err == nil {
		return dst, true, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", false, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return "", false, err
	}
	small := Reduce(img, width)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", false, err
	}
	if ext == ".png" {
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(out, small)
	} else {
		err = jpeg.Encode(out, small, &jpeg.Options{Quality: 80})
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", false, err
	}
	return dst, true, os.Rename(tmp, dst)
}

// EstimateTokens approximates image tokens (width*height/750).
func EstimateTokens(w, h int) int { return w * h / 750 }

// Crop writes the region (x, y, w, h) of src at full resolution into dst
// (flagged screens come back in high resolution, cut to the region that changed).
func Crop(src, dst string, x, y, w, h int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	r := image.Rect(x, y, x+w, y+h).Intersect(img.Bounds())
	if r.Empty() {
		return fmt.Errorf("crop region %d,%d %dx%d is outside the image %v", x, y, w, h, img.Bounds())
	}
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for yy := 0; yy < r.Dy(); yy++ {
		for xx := 0; xx < r.Dx(); xx++ {
			out.Set(xx, yy, img.At(r.Min.X+xx, r.Min.Y+yy))
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := png.Encode(o, out); err != nil {
		o.Close()
		return err
	}
	return o.Close()
}
