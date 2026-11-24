package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

func TestFileReadLog(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r.db")
	s := open(t, path)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := s.NoteFiles(ctx, "p", "I-1", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteFiles(ctx, "p", "I-1", []ports.FileTouch{{Path: "b.go", At: at}, {Path: "a.go", Write: true, At: at}}); err != nil {
		t.Fatal(err)
	}
	// Noted again: a read never clears a change, a change marks a read.
	if err := s.NoteFiles(ctx, "p", "I-1", []ports.FileTouch{{Path: "a.go"}, {Path: "b.go", Write: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteFiles(ctx, "p", "I-2", []ports.FileTouch{{Path: "c.go"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, path) // survives a restart
	got, err := s.TouchedFiles(ctx, "p", "I-1")
	if err != nil || len(got) != 2 || got[0].Path != "a.go" || !got[0].Write || !got[1].Write || !got[0].At.Equal(at) {
		t.Fatalf("touched %+v %v", got, err)
	}
	items, err := s.ReadItems(ctx, "p")
	if err != nil || len(items) != 2 || items[0] != "I-1" {
		t.Fatalf("items %v %v", items, err)
	}
	if err := s.ForgetFiles(ctx, "p", "I-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TouchedFiles(ctx, "p", "I-1"); len(got) != 0 {
		t.Fatalf("forgotten %+v", got)
	}
	if items, _ := s.ReadItems(ctx, "other"); len(items) != 0 {
		t.Fatalf("other project %v", items)
	}
}
