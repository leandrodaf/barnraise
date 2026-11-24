package outfilter

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/leandrodaf/barnraise/internal/domain"
)

// Store keeps the full text of filtered outputs so that compression loses
// nothing: <dir>/<id>.log plus <dir>/index.jsonl, bounded by age and size.
type Store struct {
	Dir       string
	Keep      time.Duration // records older than this are removed (0: the default)
	KeepBytes int64         // most bytes of kept outputs (0: the default)
	Now       func() time.Time
}

// StoreFromEnv is a store in dir with the limits of the session environment.
func StoreFromEnv(dir string) Store {
	s := Store{Dir: dir}
	if d, err := time.ParseDuration(os.Getenv(domain.EnvOutputKeep)); err == nil && d > 0 {
		s.Keep = d
	}
	if n, err := strconv.ParseInt(os.Getenv(domain.EnvOutputKeepBytes), 10, 64); err == nil && n > 0 {
		s.KeepBytes = n
	}
	return s
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Store) limits() (time.Duration, int64) {
	d := domain.DefaultOutfilter()
	keep, max := s.Keep, s.KeepBytes
	if keep <= 0 {
		keep = d.Keep
	}
	if max <= 0 {
		max = d.KeepBytes
	}
	return keep, max
}

var idRe = regexp.MustCompile(`^o[0-9a-f]{8}$`)

// ValidID reports whether id has the shape of an output handle.
func ValidID(id string) bool { return idRe.MatchString(id) }

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "o" + hex.EncodeToString(b[:])
}

// Path is where the full text of an output lives.
func (s Store) Path(id string) string { return filepath.Join(s.Dir, id+".log") }

func (s Store) index() string { return filepath.Join(s.Dir, domain.OutputIndex) }

// add appends a record to the index and prunes old outputs.
func (s Store) add(rec domain.OutputRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.index(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(data, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return s.Prune()
}

// List returns the records, newest first.
func (s Store) List() ([]domain.OutputRecord, error) {
	data, err := os.ReadFile(s.index())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	recs := domain.ParseOutputIndex(data)
	slices.Reverse(recs)
	return recs, nil
}

// Get returns one record.
func (s Store) Get(id string) (domain.OutputRecord, error) {
	if !ValidID(id) {
		return domain.OutputRecord{}, fmt.Errorf("%q is not an output id (they look like o1a2b3c4)", id)
	}
	recs, err := s.List()
	if err != nil {
		return domain.OutputRecord{}, err
	}
	for _, r := range recs {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.OutputRecord{}, fmt.Errorf("no output %s in %s (kept %s; `barnraise output list` shows what is kept)", id, s.Dir, func() time.Duration { k, _ := s.limits(); return k }())
}

// Prune removes outputs older than Keep, then the oldest ones until the
// rest fit in KeepBytes. The newest output always stays.
func (s Store) Prune() error {
	keep, max := s.limits()
	data, err := os.ReadFile(s.index())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	recs := domain.ParseOutputIndex(data)
	cutoff := s.now().Add(-keep)
	var total int64
	for _, r := range recs {
		total += r.Bytes
	}
	var kept []domain.OutputRecord
	for i, r := range recs {
		last := i == len(recs)-1
		if !last && (r.At.Before(cutoff) || total > max) {
			total -= r.Bytes
			_ = os.Remove(s.Path(r.ID))
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == len(recs) {
		return nil
	}
	var b bytes.Buffer
	for _, r := range kept {
		line, _ := json.Marshal(r)
		b.Write(append(line, '\n'))
	}
	tmp := s.index() + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.index())
}

// Show returns the kept text of an output: all of it, the lines from..to
// (1-based, inclusive; 0 means the start or the end), or only the lines
// matching grep, each with its line number.
func (s Store) Show(id, grep string, from, to int) (string, error) {
	if _, err := s.Get(id); err != nil {
		return "", err
	}
	data, err := os.ReadFile(s.Path(id))
	if err != nil {
		return "", fmt.Errorf("output %s: %w", id, err)
	}
	if grep == "" && from <= 0 && to <= 0 {
		return string(data), nil
	}
	var re *regexp.Regexp
	if grep != "" {
		if re, err = regexp.Compile(grep); err != nil {
			return "", fmt.Errorf("--grep: %w", err)
		}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if from <= 0 {
		from = 1
	}
	if to <= 0 || to > len(lines) {
		to = len(lines)
	}
	var b strings.Builder
	for i := from; i <= to; i++ {
		l := lines[i-1]
		if re != nil {
			if !re.MatchString(l) {
				continue
			}
			fmt.Fprintf(&b, "%d: %s\n", i, l)
			continue
		}
		b.WriteString(l + "\n")
	}
	return b.String(), nil
}
