package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxFileBytes = 512 << 10
	docsDir      = ".barnraise"
	areasFile    = ".barnraise/areas.yaml"
)

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "build": true, "vendor": true, "dist": true, ".gradle": true,
	".idea": true, ".vscode": true, "__pycache__": true, ".venv": true, "venv": true, "target": true,
	"Pods": true, ".dart_tool": true, ".next": true, ".cache": true, "coverage": true, "DerivedData": true,
}

// tracked reports whether a root-relative path is indexed: markdown and
// areas.yaml under .barnraise/, or a source file of a known language.
func tracked(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		if skipDirs[p] {
			return false
		}
	}
	if parts[0] == docsDir {
		return rel == areasFile || strings.EqualFold(path.Ext(rel), ".md")
	}
	for _, p := range parts[:len(parts)-1] {
		if strings.HasPrefix(p, ".") {
			return false
		}
	}
	return langOf(rel) != ""
}

type fileWork struct {
	rel   string
	sha   string
	src   []byte
	known bool // indexed before: its old rows must go first
}

// Reindex refreshes the index of a project from the working tree at root.
// With changed set, only those paths are looked at (missing ones are
// deleted from the index); otherwise the whole tree is walked and files
// whose sha256 did not change are skipped. Rows record the commit.
func (ix *Index) Reindex(ctx context.Context, project, root, commit string, changed []string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	known, err := ix.fileHashes(ctx, project)
	if err != nil {
		return err
	}
	var work []fileWork
	var deleted []string
	consider := func(rel string) error {
		src, ok, err := readSource(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if !ok {
			if _, had := known[rel]; had {
				deleted = append(deleted, rel)
			}
			return nil
		}
		sum := sha256.Sum256(src)
		sha := hex.EncodeToString(sum[:])
		if old, had := known[rel]; old != sha {
			work = append(work, fileWork{rel: rel, sha: sha, src: src, known: had})
		}
		return nil
	}
	if len(changed) > 0 {
		seen := map[string]bool{}
		for _, c := range changed {
			rel := relPath(root, c)
			if rel == "" || seen[rel] || !tracked(rel) {
				continue
			}
			seen[rel] = true
			if err := consider(rel); err != nil {
				return err
			}
		}
	} else {
		seen := map[string]bool{}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return err
				}
				return nil // unreadable entries are skipped
			}
			rel := relPath(root, p)
			if d.IsDir() {
				if p != root && (skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && rel != docsDir)) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !tracked(rel) {
				return nil
			}
			seen[rel] = true
			return consider(rel)
		})
		if err != nil {
			return err
		}
		for rel := range known {
			if !seen[rel] {
				deleted = append(deleted, rel)
			}
		}
	}
	sort.Strings(deleted)
	sort.Slice(work, func(i, j int) bool { return work[i].rel < work[j].rel })

	areas, err := ix.loadAreas(ctx, project)
	if err != nil {
		return err
	}
	areasChanged := false
	for _, w := range work {
		if w.rel == areasFile {
			if a, err := parseAreas(w.src); err == nil {
				areas = a
			} else {
				areas = map[string]areaSpec{}
			}
			areasChanged = true
		}
	}
	for _, d := range deleted {
		if d == areasFile {
			areas, areasChanged = map[string]areaSpec{}, true
		}
	}
	now := ix.now().UTC()
	// The files go in batches, each in its own short transaction, so other
	// writers of the database wait milliseconds, not the whole reindex.
	// Each file is dropped, indexed and hashed in one batch: an interrupted
	// reindex leaves the done files done and the next one does the rest.
	first := true
	for len(work) > reindexBatch {
		batch := work[:reindexBatch]
		work = work[reindexBatch:]
		if err := ix.write(ctx, func(tx *txw) error {
			if first {
				first = false
				if err := ix.reindexStart(ctx, tx, project, commit, deleted, areas, areasChanged); err != nil {
					return err
				}
			}
			return ix.reindexFiles(ctx, tx, project, commit, batch, areas, now)
		}); err != nil {
			return err
		}
	}
	return ix.write(ctx, func(tx *txw) error {
		if first {
			if err := ix.reindexStart(ctx, tx, project, commit, deleted, areas, areasChanged); err != nil {
				return err
			}
		}
		if err := ix.reindexFiles(ctx, tx, project, commit, work, areas, now); err != nil {
			return err
		}
		if areasChanged {
			if err := reassignCodeAreas(ctx, tx, project, areas); err != nil {
				return err
			}
		}
		_, err := tx.exec(ctx, `INSERT INTO index_state(project, commit_sha, indexed) VALUES(?, ?, ?)
			ON CONFLICT(project) DO UPDATE SET commit_sha = excluded.commit_sha, indexed = excluded.indexed`, project, commit, now.UnixNano())
		return err
	})
}

// reindexBatch is how many files one reindex transaction takes.
const reindexBatch = 200

// reindexStart drops the deleted files and rewrites the areas, once, in the
// first batch.
func (ix *Index) reindexStart(ctx context.Context, tx *txw, project, commit string, deleted []string, areas map[string]areaSpec, areasChanged bool) error {
	for _, rel := range deleted {
		if err := dropOrigin(ctx, tx, project, rel); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM file_hashes WHERE project = ? AND path = ?`, project, rel); err != nil {
			return err
		}
	}
	if areasChanged {
		if err := writeAreas(ctx, tx, project, commit, areas); err != nil {
			return err
		}
	}
	return nil
}

// reindexFiles indexes one batch of changed files.
func (ix *Index) reindexFiles(ctx context.Context, tx *txw, project, commit string, work []fileWork, areas map[string]areaSpec, now time.Time) error {
	for _, w := range work {
		if w.known && w.rel != areasFile { // areas.yaml was rewritten above
			if err := dropOrigin(ctx, tx, project, w.rel); err != nil {
				return err
			}
		}
		switch {
		case w.rel == areasFile:
		case strings.HasPrefix(w.rel, docsDir+"/"):
			if err := indexDoc(ctx, tx, project, commit, w, now); err != nil {
				return err
			}
		default:
			if err := indexCode(ctx, tx, project, commit, w, areas, now); err != nil {
				return err
			}
		}
		if _, err := tx.exec(ctx, `INSERT INTO file_hashes(project, path, sha, commit_sha, indexed) VALUES(?, ?, ?, ?, ?)
				ON CONFLICT(project, path) DO UPDATE SET sha = excluded.sha, commit_sha = excluded.commit_sha, indexed = excluded.indexed`,
			project, w.rel, w.sha, commit, now.UnixNano()); err != nil {
			return err
		}
	}
	return nil
}

// readSource reads a file if it is small, regular and not binary.
func readSource(p string) ([]byte, bool, error) {
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxFileBytes {
		return nil, false, nil
	}
	src, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	head := src
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, false, nil
	}
	return src, true, nil
}

// relPath returns p relative to root with forward slashes ("" if outside).
func relPath(root, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (ix *Index) fileHashes(ctx context.Context, project string) (map[string]string, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path, sha FROM file_hashes WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, s string
		if err := rows.Scan(&p, &s); err != nil {
			return nil, err
		}
		out[p] = s
	}
	return out, rows.Err()
}

func (ix *Index) loadAreas(ctx context.Context, project string) (map[string]areaSpec, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT area, paths, screens, contracts, standard FROM areas WHERE project = ?`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]areaSpec{}
	for rows.Next() {
		var name, paths, screens, contracts string
		var spec areaSpec
		if err := rows.Scan(&name, &paths, &screens, &contracts, &spec.Standard); err != nil {
			return nil, err
		}
		// The lists are written by this package; a malformed one reads as empty.
		_ = json.Unmarshal([]byte(paths), &spec.Paths)
		_ = json.Unmarshal([]byte(screens), &spec.Screens)
		_ = json.Unmarshal([]byte(contracts), &spec.Contracts)
		out[name] = spec
	}
	return out, rows.Err()
}

func writeAreas(ctx context.Context, tx *txw, project, commit string, areas map[string]areaSpec) error {
	if _, err := tx.exec(ctx, `DELETE FROM areas WHERE project = ?`, project); err != nil {
		return err
	}
	if err := dropOrigin(ctx, tx, project, areasFile); err != nil {
		return err
	}
	names := make([]string, 0, len(areas))
	for n := range areas {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := areas[name]
		j := func(v []string) string {
			if v == nil {
				v = []string{}
			}
			b, _ := json.Marshal(v)
			return string(b)
		}
		std := standardID(name, spec)
		if _, err := tx.exec(ctx, `INSERT INTO areas(project, area, paths, screens, contracts, standard) VALUES(?, ?, ?, ?, ?, ?)`,
			project, name, j(spec.Paths), j(spec.Screens), j(spec.Contracts), std); err != nil {
			return err
		}
		var body strings.Builder
		fmt.Fprintf(&body, "Area %s.\n", name)
		if len(spec.Paths) > 0 {
			fmt.Fprintf(&body, "Paths: %s\n", strings.Join(spec.Paths, ", "))
		}
		if len(spec.Screens) > 0 {
			fmt.Fprintf(&body, "Screens: %s\n", strings.Join(spec.Screens, ", "))
		}
		if len(spec.Contracts) > 0 {
			fmt.Fprintf(&body, "Contracts: %s\n", strings.Join(spec.Contracts, ", "))
		}
		fmt.Fprintf(&body, "Standard: %s\n", std)
		d := &docRow{
			ID: "A-" + name, Kind: "area", Title: name, Summary: oneLine("Area "+name+": "+strings.Join(spec.Paths, ", "), 300),
			Body: body.String(), Path: areasFile, Areas: []string{name}, Commit: commit, Origin: areasFile,
			Links: []linkRow{{std, "standard"}},
		}
		for _, c := range spec.Contracts {
			d.Links = append(d.Links, linkRow{c, "contract"})
		}
		for _, s := range spec.Screens {
			d.Links = append(d.Links, linkRow{s, "screen"})
		}
		if err := putDoc(ctx, tx, project, d); err != nil {
			return err
		}
	}
	return nil
}

// areasFor returns the areas whose paths contain the file or folder p.
func areasFor(areas map[string]areaSpec, p string) []string {
	var out []string
	for name, spec := range areas {
		for _, pat := range spec.Paths {
			if pathInArea(pat, p) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func indexDoc(ctx context.Context, tx *txw, project, commit string, w fileWork, now time.Time) error {
	rel := strings.TrimPrefix(w.rel, docsDir+"/")
	d, err := parseDoc(rel, string(w.src))
	if err != nil {
		// a broken header never hides the document: index it as plain text
		var perr error
		if d, perr = parseDoc(rel, stripHeader(string(w.src))); perr != nil {
			return nil
		}
		d.Meta["front_matter_error"] = err.Error()
	}
	d.Commit, d.Origin = commit, w.rel
	if d.SourceSHA == "" {
		d.SourceSHA = w.sha
	}
	if d.Updated.IsZero() {
		d.Updated = now
	}
	return putDoc(ctx, tx, project, d)
}

func stripHeader(src string) string {
	_, body := splitFrontMatter(src)
	if body == src && strings.HasPrefix(src, "---\n") {
		return strings.TrimPrefix(src, "---\n")
	}
	return body
}

func symbolID(file, name string) string { return "sym:" + file + "#" + name }

func indexCode(ctx context.Context, tx *txw, project, commit string, w fileWork, areas map[string]areaSpec, now time.Time) error {
	syms := extractSymbols(w.rel, w.src)
	fileAreas := areasFor(areas, w.rel)
	lang := langOf(w.rel)
	used := map[string]bool{}
	var sigs, plain, names []string
	for _, s := range syms {
		id := symbolID(w.rel, s.Name)
		if used[id] {
			id = fmt.Sprintf("%s@%d", id, s.Line)
		}
		used[id] = true
		if _, err := tx.exec(ctx, `INSERT OR REPLACE INTO symbols(project, file, name, short, kind, line, end_line, signature, doc_id, commit_sha)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, project, w.rel, s.Name, shortName(s.Name), s.Kind, s.Line, s.EndLine, s.Signature, id, commit); err != nil {
			return err
		}
		for _, c := range s.Calls {
			if _, err := tx.exec(ctx, `INSERT OR IGNORE INTO calls(project, file, caller, callee) VALUES(?, ?, ?, ?)`, project, w.rel, s.Name, c); err != nil {
				return err
			}
		}
		d := &docRow{
			ID: id, Kind: "symbol", Title: s.Name, Summary: s.Signature, Body: s.Text, Path: w.rel, Areas: fileAreas,
			Meta:    map[string]string{"symbol_kind": s.Kind, "line": fmt.Sprint(s.Line), "end_line": fmt.Sprint(s.EndLine), "lang": lang, "signature": s.Signature},
			Updated: now, SourceSHA: w.sha, Commit: commit, Origin: w.rel,
			Links:  []linkRow{{"file:" + w.rel, "defined_in"}},
			Chunks: []chunk{{Heading: s.Name, Line: s.Line, Text: splitIdent(s.Name) + "\n" + s.Text}},
		}
		if err := putDocFresh(ctx, tx, project, d, true); err != nil {
			return err
		}
		sigs = append(sigs, fmt.Sprintf("%d: %s", s.Line, s.Signature))
		plain = append(plain, s.Signature)
		names = append(names, s.Name)
	}
	summary := fmt.Sprintf("%s file, %d symbols", lang, len(syms))
	if len(names) > 0 {
		summary += ": " + strings.Join(names, ", ")
	}
	body := strings.Join(sigs, "\n")
	fd := &docRow{
		ID: "file:" + w.rel, Kind: "file", Title: w.rel, Summary: oneLine(summary, 300), Body: body, Path: w.rel, Areas: fileAreas,
		Meta: map[string]string{"lang": lang}, Updated: now, SourceSHA: w.sha, Commit: commit, Origin: w.rel,
		Chunks: []chunk{{Text: splitIdent(w.rel) + "\n" + strings.Join(plain, "\n")}},
	}
	for _, a := range fileAreas {
		fd.Links = append(fd.Links, linkRow{"A-" + a, "area"})
	}
	return putDocFresh(ctx, tx, project, fd, true)
}

// reassignCodeAreas refreshes the areas of code documents after areas.yaml
// changed, without reparsing the code.
func reassignCodeAreas(ctx context.Context, tx *txw, project string, areas map[string]areaSpec) error {
	rows, err := tx.query(ctx, `SELECT id, path FROM docs WHERE project = ? AND kind IN ('file', 'symbol')`, project)
	if err != nil {
		return err
	}
	type pair struct{ id, path string }
	var docs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.path); err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range docs {
		as := areasFor(areas, d.path)
		aj, _ := json.Marshal(append([]string{}, as...))
		first := ""
		if len(as) > 0 {
			first = as[0]
		}
		if _, err := tx.exec(ctx, `UPDATE docs SET area = ?, areas = ? WHERE project = ? AND id = ?`, first, string(aj), project, d.id); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM doc_areas WHERE project = ? AND doc_id = ?`, project, d.id); err != nil {
			return err
		}
		if _, err := tx.exec(ctx, `DELETE FROM links WHERE project = ? AND src = ? AND kind = 'area'`, project, d.id); err != nil {
			return err
		}
		for _, a := range as {
			if _, err := tx.exec(ctx, `INSERT OR IGNORE INTO doc_areas(project, doc_id, area) VALUES(?, ?, ?)`, project, d.id, a); err != nil {
				return err
			}
			if strings.HasPrefix(d.id, "file:") {
				if _, err := tx.exec(ctx, `INSERT OR IGNORE INTO links(project, src, dst, kind, origin) VALUES(?, ?, ?, 'area', ?)`,
					project, d.id, "A-"+a, d.path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
