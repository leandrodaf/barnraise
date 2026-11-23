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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
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

// The default width has to save tokens for real: the model already scales
// big images to ~1.15 MP, so 720 px wide saved almost nothing on a phone
// print. These are the numbers measured on Nobira's prints.
func TestDefaultWidthSavesTokensOnAPhonePrint(t *testing.T) {
	full := Tokens(1080, 2400)
	if full < 1450 || full > 1600 {
		t.Fatalf("full phone print = %d tokens, want ~1,530", full)
	}
	rw, rh := DefaultWidth, 2400*DefaultWidth/1080
	reduced := Tokens(rw, rh)
	if saving := 1 - float64(reduced)/float64(full); saving < 0.40 {
		t.Fatalf("default width %d: %d -> %d tokens, saves %.0f%%, want at least 40%%", DefaultWidth, full, reduced, saving*100)
	}
	if Tokens(720, 1600) < full*95/100 {
		t.Fatal("720 px wide is supposed to cost about as much as full resolution")
	}
	if Tokens(0, 10) != 0 {
		t.Fatal("empty image")
	}
}

func TestMeasureDir(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), 1080, 2400)
	writePNG(t, filepath.Join(dir, "sub", "b.png"), 300, 600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	c, err := MeasureDir(dir, DefaultWidth)
	if err != nil {
		t.Fatal(err)
	}
	if c.Images != 2 || c.FullTokens != Tokens(1080, 2400)+Tokens(300, 600) || c.ReducedTok != Tokens(540, 1200)+Tokens(300, 600) || c.SavedPct <= 30 {
		t.Fatalf("%+v", c)
	}
}
