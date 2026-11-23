package imagex

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, path string, w, h int) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestReduceFile(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "shot.png")
	writePNG(t, big, 1440, 3040)
	out, reduced, err := ReduceFile(big, filepath.Join(dir, "cache"), 720)
	if err != nil || !reduced {
		t.Fatal(err, reduced)
	}
	f, _ := os.Open(out)
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != 720 || cfg.Height != 1520 {
		t.Fatalf("%+v %v", cfg, err)
	}
	again, _, _ := ReduceFile(big, filepath.Join(dir, "cache"), 720)
	if again != out {
		t.Fatal("cache by content hash")
	}
	small := filepath.Join(dir, "small.png")
	writePNG(t, small, 300, 600)
	if p, reduced, err := ReduceFile(small, dir, 720); err != nil || reduced || p != small {
		t.Fatal("small images stay")
	}
	if _, _, err := ReduceFile(filepath.Join(dir, "nope.png"), dir, 720); err == nil {
		t.Fatal("missing file")
	}
	if !IsImage("a.JPG") || IsImage("a.txt") {
		t.Fatal("IsImage")
	}
	if EstimateTokens(1440, 3040) < 3000 || EstimateTokens(720, 1520) > 1500 {
		t.Fatal("estimate")
	}
}
