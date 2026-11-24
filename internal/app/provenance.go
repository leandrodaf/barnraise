package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/leandrodaf/barnraise/internal/domain"
	"github.com/leandrodaf/barnraise/internal/ports"
)

// Provenance of what steers an agent (ADR-0030). Before a session starts
// the daemon checks that nothing that tells the agent what to do or what it
// may do was changed outside a path the owner sanctions:
//
//   - the hook binary (the barnraise executable the session's hooks run) is
//     the one the daemon started from;
//   - barnraise.yaml and .barnraise/** in the project root, which the daemon
//     reads for prompts and rules, match the hashes recorded when the owner
//     loaded the project (barnraise up, barnraise reload, barnraise trust);
//   - the instruction files the agent CLIs load from the worktree
//     (CLAUDE.md, AGENTS.md, GEMINI.md, .mcp.json, .claude/**, .codex/**,
//     .gemini/**, .barnraise/**) are the main branch's, or were accepted by
//     the owner for that item (barnraise trust I-42): an earlier session
//     cannot plant instructions for the next one;
//   - the session files the daemon writes (settings.json with the hooks,
//     mcp.json, policy.json) are unchanged between writing and starting, and
//     the hook itself refuses a policy.json whose hash is not the one written.
//
// On a mismatch the session is refused, a trust.mismatch event is recorded
// and the owner is told what changed and how to re-trust.

// ErrUntrusted is returned when a session is refused for provenance.
var ErrUntrusted = errors.New("untrusted")

// trustState holds what provenance remembers in memory.
type trustState struct {
	mu         sync.Mutex
	binaryPath string
	binary     string            // sha256 of the binary when the daemon started ("" unknown)
	mainHash   map[string]string // rev + "\x00" + path -> sha256 ("" absent)
	reported   map[string]string // project/scope -> last problem reported
}

func newTrustState(binary string) *trustState {
	t := &trustState{binaryPath: binary, mainHash: map[string]string{}, reported: map[string]string{}}
	if binary != "" {
		t.binary, _ = fileSHA(binary)
	}
	return t
}

// fileSHA is the hex SHA-256 of a file.
func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func bytesSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// rootConfigFile is the project file the daemon reads from the root, with .barnraise/.
const rootConfigFile = "barnraise.yaml"

// RootManifest hashes barnraise.yaml and every file under .barnraise/ in a
// project root, but the repository map: Barnraise writes it itself when an
// item lands, and the checkout of the root follows main.
func RootManifest(root string) (domain.Manifest, error) {
	m := domain.Manifest{}
	if h, err := fileSHA(filepath.Join(root, rootConfigFile)); err == nil {
		m[rootConfigFile] = h
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	err := walkFiles(filepath.Join(root, ".barnraise"), func(rel, abs string) error {
		if ".barnraise/"+rel == domain.RepoMapFile {
			return nil // Barnraise's own output, rewritten at every integration
		}
		h, err := fileSHA(abs)
		if err != nil {
			return err
		}
		m[".barnraise/"+rel] = h
		return nil
	}, nil)
	return m, err
}

// skipDirs are folders never searched for instruction files: big, generated
// or Barnraise's own session folder.
var skipDirs = map[string]bool{
	".git": true, ".barnraise-session": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, ".venv": true, "venv": true, "Pods": true, ".gradle": true, "__pycache__": true,
}

// InstructionManifest hashes the instruction files of a worktree.
func InstructionManifest(dir string) (domain.Manifest, error) {
	m := domain.Manifest{}
	err := walkFiles(dir, func(rel, abs string) error {
		if !domain.InstructionFile(rel) {
			return nil
		}
		h, err := fileSHA(abs)
		if err != nil {
			return err
		}
		m[rel] = h
		return nil
	}, func(rel string) bool {
		name := filepath.Base(rel)
		if skipDirs[name] {
			return false
		}
		// Hidden folders are searched only for the agent CLIs' own.
		top := !strings.Contains(rel, "/")
		return !strings.HasPrefix(name, ".") || (top && domain.InstructionDir(name))
	})
	return m, err
}

// walkFiles calls fn for every regular file under dir (rel is slash
// separated), descending into a folder only when enter allows (nil: all).
// A missing dir is empty. Symbolic links are hashed as files they point to
// only when they stay regular files; links to folders are not followed.
func walkFiles(dir string, fn func(rel, abs string) error, enter func(rel string) bool) error {
	err := filepath.WalkDir(dir, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		rel, _ := filepath.Rel(dir, abs)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if abs != dir && enter != nil && !enter(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(abs); err != nil || !st.Mode().IsRegular() {
				return nil
			}
		} else if !d.Type().IsRegular() {
			return nil
		}
		return fn(rel, abs)
	})
	return err
}

// recordedRoot returns the last recorded root manifest of a project.
func (s *Service) recordedRoot(ctx context.Context, p *Project) (domain.Manifest, bool, error) {
	evs, err := s.d.Store.Query(ctx, ports.EventQuery{Project: p.Name, Types: []string{domain.EvTrustRecorded}, Limit: 1})
	if err != nil || len(evs) == 0 {
		return nil, false, err
	}
	pl, err := evs[0].DecodePayload()
	if err != nil {
		return nil, false, err
	}
	return manifestOf(pl.Extra["files"]), true, nil
}

// accepted returns the path:hash pairs the owner accepted for an item.
func (s *Service) accepted(ctx context.Context, p *Project, itemID string) (map[string]bool, error) {
	evs, err := s.d.Store.Query(ctx, ports.EventQuery{Project: p.Name, Subject: itemID, Types: []string{domain.EvTrustAccepted}})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range evs {
		pl, err := e.DecodePayload()
		if err != nil {
			continue
		}
		for path, h := range manifestOf(pl.Extra["files"]) {
			out[path+"\x00"+h] = true
		}
	}
	return out, nil
}

// manifestOf reads a manifest back from an event's extra field.
func manifestOf(v any) domain.Manifest {
	m := domain.Manifest{}
	switch x := v.(type) {
	case map[string]any:
		for k, h := range x {
			if s, ok := h.(string); ok {
				m[k] = s
			}
		}
	case domain.Manifest:
		for k, h := range x {
			m[k] = h
		}
	case map[string]string:
		for k, h := range x {
			m[k] = h
		}
	}
	return m
}

// TrustOnLoad runs when the daemon loads a project: the first time it
// records the root manifest (trust on first use); afterwards it compares,
// so files changed while the daemon was down are noticed instead of being
// silently trusted by a restart.
func (s *Service) TrustOnLoad(ctx context.Context, project string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	rec, ok, err := s.recordedRoot(ctx, p)
	if err != nil {
		return err
	}
	if !ok {
		_, err := s.recordRoot(ctx, p, "barnraise", "first load of the project")
		return err
	}
	cur, err := RootManifest(p.Cfg().Root)
	if err != nil {
		return err
	}
	if ch := rec.Diff(cur); len(ch) > 0 {
		s.trustMismatch(ctx, p, "", "root", ch, rootFix(p.Name))
	}
	return nil
}

// RecordTrust records the current root manifest: the owner's reload or
// registration (sanctioned paths) calls it.
func (s *Service) RecordTrust(ctx context.Context, project, why string) error {
	p, err := s.Project(project)
	if err != nil {
		return err
	}
	_, err = s.recordRoot(ctx, p, "barnraise", why)
	return err
}

func (s *Service) recordRoot(ctx context.Context, p *Project, actor, why string) (domain.Manifest, error) {
	m, err := RootManifest(p.Cfg().Root)
	if err != nil {
		return nil, err
	}
	if _, err := s.emit(ctx, p, domain.EvTrustRecorded, "", actor, domain.Payload{Note: why, Extra: map[string]any{"files": map[string]string(m)}}); err != nil {
		return nil, err
	}
	s.trust.forget(p.Name + "/root")
	return m, nil
}

func rootFix(project string) string {
	return "check the change, then run `barnraise trust -p " + project + "` (or `barnraise reload`) to trust the files as they are now"
}

func itemFix(project, itemID string) string {
	return "review the files in the item's worktree, then run `barnraise trust " + itemID + " -p " + project + "` to accept them for this item, or fix them on the item's branch"
}

// TrustProblem is one reason a session would be refused.
type TrustProblem struct {
	Scope   string               `json:"scope"` // binary, root, worktree, session
	Dir     string               `json:"dir,omitempty"`
	Changes []domain.TrustChange `json:"changes"`
	Fix     string               `json:"fix"`
}

// checkProvenance runs before a session of itemID starts in dir.
func (s *Service) checkProvenance(ctx context.Context, p *Project, itemID, dir string) error {
	probs, err := s.provenanceProblems(ctx, p, itemID, []string{dir})
	if err != nil {
		return fmt.Errorf("%w: provenance check: %v", ErrUntrusted, err)
	}
	for _, pr := range probs {
		subject := itemID
		if pr.Scope == "root" || pr.Scope == "binary" {
			subject = ""
		}
		s.trustMismatch(ctx, p, subject, pr.Scope, pr.Changes, pr.Fix)
	}
	if len(probs) > 0 {
		pr := probs[0]
		return fmt.Errorf("%w: %s changed outside Barnraise (%s): %s", ErrUntrusted, scopeName(pr.Scope), domain.TrustPaths(pr.Changes, 5), pr.Fix)
	}
	return nil
}

func scopeName(scope string) string {
	switch scope {
	case "binary":
		return "the hook binary"
	case "root":
		return "the project's barnraise.yaml or .barnraise/"
	case "session":
		return "the session files"
	}
	return "the instruction files of the worktree"
}

// provenanceProblems lists what does not hold, for the dirs of an item.
func (s *Service) provenanceProblems(ctx context.Context, p *Project, itemID string, dirs []string) ([]TrustProblem, error) {
	var out []TrustProblem
	if pr := s.trust.binaryProblem(); pr != nil {
		out = append(out, *pr)
	}
	rec, ok, err := s.recordedRoot(ctx, p)
	if err != nil {
		return nil, err
	}
	if ok {
		cur, err := RootManifest(p.Cfg().Root)
		if err != nil {
			return nil, err
		}
		if ch := rec.Diff(cur); len(ch) > 0 {
			out = append(out, TrustProblem{Scope: "root", Changes: ch, Fix: rootFix(p.Name)})
		}
	}
	for _, dir := range dirs {
		ch, err := s.worktreeChanges(ctx, p, itemID, dir)
		if err != nil {
			return nil, err
		}
		if len(ch) > 0 {
			out = append(out, TrustProblem{Scope: "worktree", Dir: dir, Changes: ch, Fix: itemFix(p.Name, itemID)})
		}
	}
	return out, nil
}

// worktreeChanges lists the instruction files of dir that are neither the
// main branch's nor accepted by the owner for the item.
func (s *Service) worktreeChanges(ctx context.Context, p *Project, itemID, dir string) ([]domain.TrustChange, error) {
	g := p.Ports().Git
	if g == nil || dir == "" || filepath.Clean(dir) == filepath.Clean(p.Cfg().Root) {
		return nil, nil // no worktree: the root is checked above
	}
	cur, err := InstructionManifest(dir)
	if err != nil || len(cur) == 0 {
		return nil, err
	}
	main, err := g.MainHead(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := s.accepted(ctx, p, itemID)
	if err != nil {
		return nil, err
	}
	// The main the worktree branched from (or was last rebased on) is main's
	// too: without it, every file main changes since, like the repository
	// map Barnraise rewrites at each integration, would refuse the session.
	base := ""
	if head, err := g.Head(ctx, dir); err == nil && head != "" {
		if b, err := g.MergeBase(ctx, head, main); err == nil && b != main {
			base = b
		}
	}
	var out []domain.TrustChange
	for _, rel := range sortedKeys(cur) {
		got := cur[rel]
		want := s.trust.mainFile(ctx, g, main, rel)
		if got == want || acc[rel+"\x00"+got] || (base != "" && got == s.trust.mainFile(ctx, g, base, rel)) {
			continue
		}
		kind := "changed"
		if want == "" {
			kind = "added"
		}
		out = append(out, domain.TrustChange{Path: rel, Kind: kind, Want: want, Got: got})
	}
	return out, nil
}

// mainFile is the hash of path on the main branch at rev ("" when absent).
func (t *trustState) mainFile(ctx context.Context, g ports.Git, rev, path string) string {
	key := rev + "\x00" + path
	t.mu.Lock()
	h, ok := t.mainHash[key]
	t.mu.Unlock()
	if ok {
		return h
	}
	if b, err := g.Show(ctx, rev, path); err == nil {
		h = bytesSHA(b)
	}
	t.mu.Lock()
	if len(t.mainHash) > 4096 {
		t.mainHash = map[string]string{}
	}
	t.mainHash[key] = h
	t.mu.Unlock()
	return h
}

func (t *trustState) binaryProblem() *TrustProblem {
	if t.binary == "" {
		return nil
	}
	got, err := fileSHA(t.binaryPath)
	if err != nil {
		got = "unreadable: " + err.Error()
	}
	if got == t.binary {
		return nil
	}
	return &TrustProblem{Scope: "binary", Changes: []domain.TrustChange{{Path: t.binaryPath, Kind: "changed", Want: t.binary, Got: got}},
		Fix: "the barnraise binary on disk is not the one this daemon runs: restart the daemon (barnraise down, then barnraise up) after checking where the new binary came from"}
}

// once reports a problem a first time and returns whether it is new.
func (t *trustState) once(key, sig string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reported[key] == sig {
		return false
	}
	t.reported[key] = sig
	return true
}

func (t *trustState) forget(prefix string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.reported {
		if strings.HasPrefix(k, prefix) {
			delete(t.reported, k)
		}
	}
}

// trustMismatch records and tells a mismatch once per distinct change.
func (s *Service) trustMismatch(ctx context.Context, p *Project, subject, scope string, ch []domain.TrustChange, fix string) {
	var sig strings.Builder
	for _, c := range ch {
		sig.WriteString(c.Path + "=" + c.Got + ";")
	}
	if !s.trust.once(p.Name+"/"+scope+"/"+subject, sig.String()) {
		return
	}
	note := scopeName(scope) + " changed outside Barnraise: " + domain.TrustPaths(ch, 10) + ". Sessions are refused until it is fixed: " + fix
	_, _ = s.emit(ctx, p, domain.EvTrustMismatch, subject, "barnraise", domain.Payload{Note: note, Extra: map[string]any{"scope": scope, "changes": ch}})
	go s.notifyOwner(context.WithoutCancel(ctx), p, "trust", "A session was refused: "+scopeName(scope)+" changed", note)
}

// sealFiles hashes the session files right after the daemon writes them.
func sealFiles(paths ...string) (domain.Manifest, error) {
	m := domain.Manifest{}
	for _, f := range paths {
		h, err := fileSHA(f)
		if err != nil {
			return nil, err
		}
		m[f] = h
	}
	return m, nil
}

// verifySeal checks the session files again just before the session opens.
func verifySeal(m domain.Manifest) error {
	cur := domain.Manifest{}
	for f := range m {
		h, err := fileSHA(f)
		if err != nil {
			h = "missing"
		}
		cur[f] = h
	}
	if ch := m.Diff(cur); len(ch) > 0 {
		return fmt.Errorf("%w: the session files changed between writing and starting (%s)", ErrUntrusted, domain.TrustPaths(ch, 5))
	}
	return nil
}

// TrustReport answers barnraise trust.
type TrustReport struct {
	Project  string          `json:"project"`
	Item     string          `json:"item,omitempty"`
	Recorded domain.Manifest `json:"recorded,omitempty"` // what was trusted by this call
	Problems []TrustProblem  `json:"problems"`           // what still does not hold
}

// TrustStatus lists what would refuse a session now: of the project (root
// and binary) and, with itemID, of that item's worktrees.
func (s *Service) TrustStatus(ctx context.Context, project string, a Actor, itemID string) (TrustReport, error) {
	p, err := s.Project(project)
	if err != nil {
		return TrustReport{}, err
	}
	if err := a.Require(ScopeAdmin, p.Name); err != nil {
		return TrustReport{}, err
	}
	rep := TrustReport{Project: p.Name, Item: itemID}
	probs, err := s.provenanceProblems(ctx, p, itemID, s.itemDirs(p, itemID))
	rep.Problems = probs
	return rep, err
}

// Trust is the owner's sanctioned path: without an item it records the
// project root as it is now; with an item it accepts the instruction files
// of that item's worktrees as they are now.
func (s *Service) Trust(ctx context.Context, project string, a Actor, itemID string) (TrustReport, error) {
	p, err := s.Project(project)
	if err != nil {
		return TrustReport{}, err
	}
	if err := a.Require(ScopeAdmin, p.Name); err != nil || a.Kind == ActorAgent {
		return TrustReport{}, fmt.Errorf("%w: only the owner re-trusts files", ErrForbidden)
	}
	rep := TrustReport{Project: p.Name, Item: itemID}
	if itemID == "" {
		if rep.Recorded, err = s.recordRoot(ctx, p, actorName(a), "trusted by the owner"); err != nil {
			return rep, err
		}
	} else {
		if itemID, err = domain.NormalizeItemID(itemID); err != nil {
			return rep, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		rep.Item = itemID
		rep.Recorded = domain.Manifest{}
		for _, dir := range s.itemDirs(p, itemID) {
			ch, err := s.worktreeChanges(ctx, p, itemID, dir)
			if err != nil {
				return rep, err
			}
			for _, c := range ch {
				rep.Recorded[c.Path] = c.Got
			}
		}
		if len(rep.Recorded) > 0 {
			if _, err := s.emit(ctx, p, domain.EvTrustAccepted, itemID, actorName(a), domain.Payload{Note: "instruction files accepted by the owner",
				Extra: map[string]any{"files": map[string]string(rep.Recorded)}}); err != nil {
				return rep, err
			}
		}
		s.trust.forget(p.Name + "/worktree/" + itemID)
	}
	rep.Problems, err = s.provenanceProblems(ctx, p, itemID, s.itemDirs(p, itemID))
	return rep, err
}

// itemDirs are the existing worktrees of an item (none without an item).
func (s *Service) itemDirs(p *Project, itemID string) []string {
	if itemID == "" {
		return nil
	}
	var out []string
	for _, d := range []string{s.WorktreeDir(p.Name, itemID), s.altDir(p.Name, itemID), s.WorktreeDir(p.Name, "_refine-"+itemID)} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
