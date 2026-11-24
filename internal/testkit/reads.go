package testkit

import (
	"context"
	"sort"

	"github.com/leandrodaf/barnraise/internal/ports"
)

var _ ports.FileReadLog = (*Store)(nil)

func readKey(project, itemID string) string { return project + "\x00" + itemID }

// NoteFiles records files read or changed by an item's sessions, like the SQLite adapter.
func (s *Store) NoteFiles(_ context.Context, project, itemID string, files []ports.FileTouch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reads == nil {
		s.reads = map[string]map[string]ports.FileTouch{}
	}
	k := readKey(project, itemID)
	if s.reads[k] == nil {
		s.reads[k] = map[string]ports.FileTouch{}
	}
	for _, f := range files {
		cur, ok := s.reads[k][f.Path]
		if ok {
			cur.Write = cur.Write || f.Write
			s.reads[k][f.Path] = cur
			continue
		}
		s.reads[k][f.Path] = f
	}
	return nil
}

// TouchedFiles lists the files noted for an item, by path.
func (s *Store) TouchedFiles(_ context.Context, project, itemID string) ([]ports.FileTouch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.FileTouch
	for _, f := range s.reads[readKey(project, itemID)] {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadItems lists the items of a project with files noted.
func (s *Store) ReadItems(_ context.Context, project string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k, files := range s.reads {
		if len(files) > 0 && len(k) > len(project) && k[:len(project)+1] == project+"\x00" {
			out = append(out, k[len(project)+1:])
		}
	}
	sort.Strings(out)
	return out, nil
}

// ForgetFiles drops what was noted for an item.
func (s *Store) ForgetFiles(_ context.Context, project, itemID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reads, readKey(project, itemID))
	return nil
}
