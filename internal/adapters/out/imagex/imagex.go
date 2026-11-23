// Package imagex reduces screenshots before any agent reads them.
//
// The model already scales an image down to about 1.15 megapixels, so a
// full 1080x2400 phone print costs ~1,530 tokens and a 720 px wide one
// almost the same (~1,530: 720x1600 is 1.15 MP). Only a narrower image
// saves: 540 px wide is ~860 tokens. Measured on 23,154 real prints of
// Nobira (Nov 2026): 720 px saved 8.4%, 540 px 43.6%.
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
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// DefaultWidth is the target width for agents: half a 1080 px phone print,
// still readable, ~44% fewer tokens than full resolution.
const DefaultWidth = 540

// Tokens estimates what the model is charged for an image of w x h pixels:
// it scales to at most 1568 px on the long side and about 1.15 MP, then
// counts one token per 750 pixels.
func Tokens(w, h int) int {
	if w <= 0 || h <= 0 {
		return 0
	}
	s := min(1.0, 1568/float64(max(w, h)), math.Sqrt(1_150_000/float64(w*h)))
	w2, h2 := max(1, int(float64(w)*s)), max(1, int(float64(h)*s))
	return (w2*h2 + 749) / 750
}

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
	nh := max(h*width/w, 1)
	out := image.NewRGBA(image.Rect(0, 0, width, nh))
	for y := range nh {
		y0 := b.Min.Y + y*h/nh
		y1 := b.Min.Y + (y+1)*h/nh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := range width {
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

// Cost is what a folder of images costs an agent, at full resolution and
// reduced to a width.
type Cost struct {
	Images     int     `json:"images"`
	FullTokens int     `json:"full_tokens"`
	Width      int     `json:"width"`
	ReducedTok int     `json:"reduced_tokens"`
	SavedPct   float64 `json:"saved_pct"`
}

// MeasureDir walks dir and estimates the tokens of every image in it, as
// read at full resolution and reduced to width. Only the image headers are
// read.
func MeasureDir(dir string, width int) (Cost, error) {
	c := Cost{Width: width}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !IsImage(p) {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			return nil
		}
		w, h := cfg.Width, cfg.Height
		c.Images++
		c.FullTokens += Tokens(w, h)
		if width > 0 && w > width {
			h, w = max(h*width/w, 1), width
		}
		c.ReducedTok += Tokens(w, h)
		return nil
	})
	if c.FullTokens > 0 {
		c.SavedPct = float64(int(1000*(1-float64(c.ReducedTok)/float64(c.FullTokens)))) / 10
	}
	return c, err
}
