package knowledge

import (
	"context"
	"encoding/binary"
	"path"
	"slices"
	"strings"
)

// The trigram index (ADR-0038) keeps, for every indexed text file of a
// project, its content and the set of its three-byte sequences, and for
// every sequence the files that contain it. A substring or a regular
// expression is answered by intersecting the postings of the trigrams it
// needs and then checking only those files.
//
// Trigrams are case-folded (ASCII) so one index serves case-sensitive and
// case-insensitive searches; matching is line by line, so trigrams across
// a line break and blank ones are not indexed.

// textExts are the files indexed for search only (no symbols).
var textExts = map[string]bool{
	".md": true, ".txt": true, ".rst": true, ".adoc": true, ".yaml": true, ".yml": true, ".json": true, ".toml": true,
	".ini": true, ".cfg": true, ".conf": true, ".properties": true, ".sql": true, ".sh": true,
	".bash": true, ".zsh": true, ".fish": true, ".ps1": true, ".proto": true, ".graphql": true, ".gql": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".sass": true, ".less": true, ".xml": true,
	".gradle": true, ".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".m": true, ".mm": true,
	".cs": true, ".fs": true, ".rb": true, ".php": true, ".scala": true, ".lua": true, ".r": true, ".ex": true,
	".exs": true, ".erl": true, ".hs": true, ".ml": true, ".clj": true, ".vue": true, ".svelte": true, ".astro": true,
	".tf": true, ".hcl": true, ".mk": true, ".cmake": true, ".nix": true, ".zig": true, ".jl": true, ".pl": true, ".tmpl": true,
	".tpl": true, ".j2": true, ".csv": true,
}

var textNames = map[string]bool{
	"Makefile": true, "Dockerfile": true, "Justfile": true, "Rakefile": true, "Gemfile": true, "Procfile": true,
	"Containerfile": true, "go.mod": true, "CODEOWNERS": true, "LICENSE": true,
}

// lockFiles are generated and large; nobody searches them.
var lockFiles = map[string]bool{
	"go.sum": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "Cargo.lock": true,
	"poetry.lock": true, "Podfile.lock": true, "composer.lock": true, "Gemfile.lock": true, "bun.lockb": true,
}

// textFile reports whether a path that is not code is still indexed for
// search.
func textFile(rel string) bool {
	base := path.Base(rel)
	if lockFiles[base] || strings.HasPrefix(base, ".") {
		return false
	}
	return textNames[base] || textExts[strings.ToLower(path.Ext(base))]
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

func tri(a, b, c byte) uint32 {
	return uint32(lowerASCII(a))<<16 | uint32(lowerASCII(b))<<8 | uint32(lowerASCII(c))
}

// indexable leaves out the trigrams that are never indexed.
func indexable(t uint32) bool {
	a, b, c := byte(t>>16), byte(t>>8), byte(t)
	if a == '\n' || b == '\n' || c == '\n' || a == '\r' || b == '\r' || c == '\r' {
		return false
	}
	blank := func(x byte) bool { return x == ' ' || x == '\t' }
	return !(blank(a) && blank(b) && blank(c))
}

// trigramsOf returns the sorted, distinct, indexable trigrams of s.
func trigramsOf(s []byte) []uint32 {
	if len(s) < 3 {
		return nil
	}
	seen := make(map[uint32]struct{}, min(len(s), 1<<14))
	for i := 0; i+2 < len(s); i++ {
		t := tri(s[i], s[i+1], s[i+2])
		if indexable(t) {
			seen[t] = struct{}{}
		}
	}
	out := make([]uint32, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

func encodeTrigrams(ts []uint32) []byte {
	out := make([]byte, 0, len(ts)*2)
	prev := uint32(0)
	for _, t := range ts {
		out = binary.AppendUvarint(out, uint64(t-prev))
		prev = t
	}
	return out
}

func decodeTrigrams(b []byte) []uint32 {
	var out []uint32
	prev := uint32(0)
	for len(b) > 0 {
		d, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		prev += uint32(d)
		out = append(out, prev)
		b = b[n:]
	}
	return out
}

// postingsBatch is how many postings one statement writes or deletes.
const postingsBatch = 300

// indexGrep replaces the trigram index entry of a file.
func indexGrep(ctx context.Context, tx *txw, project, rel string, src []byte) error {
	if err := dropGrep(ctx, tx, project, rel); err != nil {
		return err
	}
	ts := trigramsOf(src)
	res, err := tx.exec(ctx, `INSERT INTO grep_files(project, path, content, trigrams, purpose) VALUES(?, ?, ?, ?, ?)`,
		project, rel, src, encodeTrigrams(ts), filePurpose(rel, src))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for len(ts) > 0 {
		n := min(postingsBatch, len(ts))
		q := `INSERT OR IGNORE INTO grep_postings(tri, file_id) VALUES ` + strings.TrimSuffix(strings.Repeat("(?, ?), ", n), ", ")
		args := make([]any, 0, 2*n)
		for _, t := range ts[:n] {
			args = append(args, int64(t), id)
		}
		if _, err := tx.exec(ctx, q, args...); err != nil {
			return err
		}
		ts = ts[n:]
	}
	return nil
}

// dropGrep removes a file from the trigram index.
func dropGrep(ctx context.Context, tx *txw, project, rel string) error {
	rows, err := tx.query(ctx, `SELECT id, trigrams FROM grep_files WHERE project = ? AND path = ?`, project, rel)
	if err != nil {
		return err
	}
	type entry struct {
		id  int64
		tri []uint32
	}
	var es []entry
	for rows.Next() {
		var e entry
		var blob []byte
		if err := rows.Scan(&e.id, &blob); err != nil {
			rows.Close()
			return err
		}
		e.tri = decodeTrigrams(blob)
		es = append(es, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range es {
		ts := e.tri
		for len(ts) > 0 {
			n := min(postingsBatch, len(ts))
			args := make([]any, 0, n+1)
			args = append(args, e.id)
			for _, t := range ts[:n] {
				args = append(args, int64(t))
			}
			if _, err := tx.exec(ctx, `DELETE FROM grep_postings WHERE file_id = ? AND tri IN (`+placeholders(n)+`)`, args...); err != nil {
				return err
			}
			ts = ts[n:]
		}
		if _, err := tx.exec(ctx, `DELETE FROM grep_files WHERE id = ?`, e.id); err != nil {
			return err
		}
	}
	return nil
}
