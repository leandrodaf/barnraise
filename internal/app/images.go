package app

import (
	"context"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// The images an item's work made: every image file its branch (or, once
// integrated, its commit on main) added or changed, read from git as it is
// there, with the version before when there was one. Nothing has to be
// attached: a sprite the agent drew, a screenshot it committed, an icon it
// replaced all show on the item as they are.

// imageExts are the files the panel shows as pictures.
var imageExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".avif"}

// maxItemImages bounds the list; maxImageBytes one picture.
const (
	maxItemImages = 120
	maxImageBytes = 20 << 20
)

// IsImagePath tells whether path names a picture by its extension.
func IsImagePath(p string) bool {
	return slices.Contains(imageExts, strings.ToLower(path.Ext(p)))
}

// ItemImage is one picture of an item's work.
type ItemImage struct {
	Path string `json:"path"`
	// Added: new with the item; otherwise it replaced the one before.
	Added bool `json:"added"`
}

// ItemImagesView lists them.
type ItemImagesView struct {
	Images []ItemImage `json:"images"`
	// More is how many were left out of a very long list.
	More int `json:"more,omitempty"`
	// Note says why there is nothing to show yet, when that is the case.
	Note string `json:"note,omitempty"`
	// Rev is the commit the pictures are read at: an address that names it
	// never changes, so the panel and the browser keep each picture.
	Rev string `json:"rev,omitempty"`
}

// imageRevs is where an item's pictures are read from, kept per version of
// the item: listing them fetches the branch once, and each picture after
// that reads git only for its own bytes.
type imageRevSet struct {
	base, head string
	files      map[string]bool
	at         time.Time
	// list is the listing built from them, once.
	mu   sync.Mutex
	list *ItemImagesView
}

// imageRevsTTL bounds how long a listing is reused for one item version
// (its branch may move on another machine meanwhile).
const imageRevsTTL = 2 * time.Minute

func (s *Service) imageRevsOf(ctx context.Context, p *Project, it *domain.Item, fetch bool) (*imageRevSet, error) {
	key := p.Name + "/" + it.ID + "/" + strconv.Itoa(it.Version)
	if v, ok := s.imageRevs.Load(key); ok {
		if rs := v.(*imageRevSet); time.Since(rs.at) < imageRevsTTL {
			return rs, nil
		}
	}
	base, head, err := s.itemRevs(ctx, p, it, fetch)
	if err != nil || head == "" {
		return &imageRevSet{}, err
	}
	g := p.Ports().Git
	// Name the commits themselves: a branch moves, a commit does not.
	for _, r := range []*string{&base, &head} {
		if cs, err := g.Log(ctx, "-1", "--no-walk", *r); err == nil && len(cs) == 1 && cs[0].SHA != "" {
			*r = cs[0].SHA
		}
	}
	files, err := g.ChangedFiles(ctx, base, head)
	if err != nil {
		return nil, err
	}
	rs := &imageRevSet{base: base, head: head, files: map[string]bool{}, at: time.Now()}
	for _, f := range files {
		rs.files[f] = true
	}
	s.imageRevs.Store(key, rs)
	return rs, nil
}

// itemRevs are the item's work and where it started: its commit on main
// once integrated, else its worktree here or its branch (fetched when it
// is worked on at another machine).
func (s *Service) itemRevs(ctx context.Context, p *Project, it *domain.Item, fetch bool) (base, head string, err error) {
	g := p.Ports().Git
	if g == nil {
		return "", "", fmt.Errorf("%w: %s has no repository", ErrInvalid, p.Name)
	}
	if it.MergeCommit != "" {
		return it.MergeCommit + "^", it.MergeCommit, nil
	}
	main, err := g.MainHead(ctx)
	if err != nil {
		return "", "", err
	}
	if dir := s.WorktreeDir(p.Name, it.ID); dir != "" {
		if _, serr := os.Stat(dir); serr == nil {
			if h, herr := g.Head(ctx, dir); herr == nil && h != "" {
				head = h
			}
		}
	}
	if head == "" && it.Branch != "" {
		if fetch {
			_, _ = g.FetchBranch(ctx, it.Branch)
		}
		for _, rev := range []string{"refs/heads/" + it.Branch, "refs/remotes/barnraise/" + it.Branch, "refs/remotes/origin/" + it.Branch} {
			if _, merr := g.MergeBase(ctx, main, rev); merr == nil {
				head = rev
				break
			}
		}
	}
	if head == "" {
		return "", "", nil
	}
	base, err = g.MergeBase(ctx, main, head)
	if err != nil {
		base = main
	}
	return base, head, nil
}

// ItemImages lists the pictures an item's work added or changed.
func (s *Service) ItemImages(ctx context.Context, project string, a Actor, id string) (*ItemImagesView, error) {
	p, it, err := s.readableItem(project, a, id)
	if err != nil {
		return nil, err
	}
	out := &ItemImagesView{Images: []ItemImage{}}
	rs, err := s.imageRevsOf(ctx, p, it, true)
	if err != nil {
		return nil, err
	}
	if rs.head == "" {
		out.Note = "no work of this item on this machine or its remote yet"
		return out, nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.list != nil {
		return rs.list, nil
	}
	out.Rev = rs.head
	g := p.Ports().Git
	files := make([]string, 0, len(rs.files))
	for f := range rs.files {
		files = append(files, f)
	}
	slices.Sort(files)
	for _, f := range files {
		if !IsImagePath(f) || domain.Contains(ItemFiles, f) || !g.ExistsAt(ctx, rs.head, f) {
			continue // a removed picture has nothing to show
		}
		if len(out.Images) == maxItemImages {
			out.More++
			continue
		}
		out.Images = append(out.Images, ItemImage{Path: f, Added: !g.ExistsAt(ctx, rs.base, f)})
	}
	rs.list = out
	return out, nil
}

// ItemImageBytes is one picture of an item's work: as the item left it, or
// with before, as it was before. Only a picture the item changed is read:
// this is not a way into the rest of the repository.
func (s *Service) ItemImageBytes(ctx context.Context, project string, a Actor, id, file string, before bool) ([]byte, string, error) {
	p, it, err := s.readableItem(project, a, id)
	if err != nil {
		return nil, "", err
	}
	if !IsImagePath(file) {
		return nil, "", fmt.Errorf("%w: %s is not a picture", ErrInvalid, file)
	}
	// Each picture reads git for its bytes only: the listing fetched.
	rs, err := s.imageRevsOf(ctx, p, it, false)
	if err != nil {
		return nil, "", err
	}
	if rs.head == "" {
		return nil, "", fmt.Errorf("%w: no work of %s yet", ErrNotFound, it.ID)
	}
	if !rs.files[file] {
		return nil, "", fmt.Errorf("%w: %s did not change %s", ErrNotFound, it.ID, file)
	}
	rev := rs.head
	if before {
		rev = rs.base
	}
	b, err := p.Ports().Git.Show(ctx, rev, file)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s at %s", ErrNotFound, file, rev)
	}
	if len(b) > maxImageBytes {
		return nil, "", fmt.Errorf("%w: %s is larger than %d MB", ErrInvalid, file, maxImageBytes>>20)
	}
	return b, rs.head, nil
}

// readableItem is the item, for an actor who may read the project's items.
func (s *Service) readableItem(project string, a Actor, id string) (*Project, *domain.Item, error) {
	p, err := s.Project(project)
	if err != nil {
		return nil, nil, err
	}
	if err := a.Require(ScopeItemsRead, p.Name); err != nil {
		return nil, nil, err
	}
	nid, err := domain.NormalizeItemID(id)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	it := p.Snapshot().Items[nid]
	if it == nil {
		return nil, nil, fmt.Errorf("%w: item %s", ErrNotFound, nid)
	}
	return p, it, nil
}
