package knowledge

import (
	"regexp/syntax"
	"slices"
	"strings"
	"unicode/utf8"
)

// triQuery is a condition on the trigrams of a file: every file the
// pattern can match satisfies it, so it only removes files that cannot
// match. The idea of deriving it from the regular expression comes from
// Russ Cox's "Regular Expression Matching with a Trigram Index" (Google
// Code Search) and from zoekt; this is a fresh, smaller implementation.
type triQuery struct {
	op  int
	tri []uint32    // qAnd: all of them; qOr: any of them
	sub []*triQuery // combined the same way
}

const (
	qAll = iota // no condition: every file
	qAnd
	qOr
)

var allQuery = &triQuery{op: qAll}

// literalQuery requires every trigram of s.
func literalQuery(s string) *triQuery {
	var ts []uint32
	for i := 0; i+2 < len(s); i++ {
		if t := tri(s[i], s[i+1], s[i+2]); indexable(t) {
			ts = append(ts, t)
		}
	}
	slices.Sort(ts)
	ts = slices.Compact(ts)
	if len(ts) == 0 {
		return allQuery
	}
	return &triQuery{op: qAnd, tri: ts}
}

func andQ(qs ...*triQuery) *triQuery {
	out := &triQuery{op: qAnd}
	for _, q := range qs {
		switch q.op {
		case qAll:
		case qAnd:
			out.tri = append(out.tri, q.tri...)
			out.sub = append(out.sub, q.sub...)
		default:
			out.sub = append(out.sub, q)
		}
	}
	slices.Sort(out.tri)
	out.tri = slices.Compact(out.tri)
	if len(out.tri) == 0 && len(out.sub) == 0 {
		return allQuery
	}
	if len(out.tri) == 0 && len(out.sub) == 1 {
		return out.sub[0]
	}
	return out
}

func orQ(qs ...*triQuery) *triQuery {
	out := &triQuery{op: qOr}
	for _, q := range qs {
		switch q.op {
		case qAll:
			return allQuery
		case qOr:
			out.tri = append(out.tri, q.tri...)
			out.sub = append(out.sub, q.sub...)
		case qAnd:
			if len(q.tri) == 1 && len(q.sub) == 0 {
				out.tri = append(out.tri, q.tri[0])
			} else {
				out.sub = append(out.sub, q)
			}
		default:
			out.sub = append(out.sub, q)
		}
	}
	slices.Sort(out.tri)
	out.tri = slices.Compact(out.tri)
	if len(out.tri) == 0 && len(out.sub) == 0 {
		return allQuery
	}
	if len(out.tri) == 0 && len(out.sub) == 1 {
		return out.sub[0]
	}
	return out
}

// reInfo is what is known about the strings a piece of a pattern matches:
// either their exact (small) set, or only a condition.
type reInfo struct {
	exact []string // nil: not known exactly
	q     *triQuery
}

const (
	maxExact    = 64 // strings kept in an exact set
	maxClassLen = 12 // runes of a character class turned into an exact set
)

// full turns what is known into one condition.
func (i reInfo) full() *triQuery {
	if i.exact == nil {
		return i.q
	}
	alts := make([]*triQuery, 0, len(i.exact))
	for _, s := range i.exact {
		if len(s) < 3 {
			return i.q // this alternative needs no trigram at all
		}
		alts = append(alts, literalQuery(s))
	}
	return andQ(i.q, orQ(alts...))
}

func unknown(q *triQuery) reInfo { return reInfo{q: q} }

// regexQuery returns the trigram condition of a parsed pattern.
func regexQuery(re *syntax.Regexp) *triQuery {
	return analyze(re.Simplify()).full()
}

func analyze(re *syntax.Regexp) reInfo {
	switch re.Op {
	case syntax.OpLiteral:
		s := string(re.Rune)
		if re.Flags&syntax.FoldCase != 0 && !isASCII(s) {
			return unknown(allQuery) // only ASCII is folded in the index
		}
		return reInfo{exact: []string{lowerString(s)}, q: allQuery}
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return reInfo{exact: []string{""}, q: allQuery}
	case syntax.OpCharClass:
		var set []string
		n := 0
		for k := 0; k+1 < len(re.Rune); k += 2 {
			lo, hi := re.Rune[k], re.Rune[k+1]
			n += int(hi-lo) + 1
			if n > maxClassLen {
				return unknown(allQuery)
			}
			for r := lo; r <= hi; r++ {
				set = append(set, lowerString(string(r)))
			}
		}
		slices.Sort(set)
		return reInfo{exact: slices.Compact(set), q: allQuery}
	case syntax.OpCapture:
		return analyze(re.Sub[0])
	case syntax.OpStar, syntax.OpQuest:
		if re.Op == syntax.OpQuest {
			sub := analyze(re.Sub[0])
			if sub.exact != nil && len(sub.exact) < maxExact {
				return reInfo{exact: dedupe(append([]string{""}, sub.exact...)), q: allQuery}
			}
		}
		return unknown(allQuery)
	case syntax.OpPlus:
		return unknown(analyze(re.Sub[0]).full())
	case syntax.OpRepeat:
		if re.Min == 0 {
			return unknown(allQuery)
		}
		return unknown(analyze(re.Sub[0]).full())
	case syntax.OpConcat:
		cur := reInfo{exact: []string{""}, q: allQuery}
		for _, sub := range re.Sub {
			cur = concatInfo(cur, analyze(sub))
		}
		return cur
	case syntax.OpAlternate:
		infos := make([]reInfo, len(re.Sub))
		exact := true
		total := 0
		for k, sub := range re.Sub {
			infos[k] = analyze(sub)
			if infos[k].exact == nil {
				exact = false
			}
			total += len(infos[k].exact)
		}
		if exact && total <= maxExact {
			var set []string
			qs := make([]*triQuery, 0, len(infos))
			for _, in := range infos {
				set = append(set, in.exact...)
				qs = append(qs, in.q)
			}
			return reInfo{exact: dedupe(set), q: orQ(qs...)}
		}
		qs := make([]*triQuery, 0, len(infos))
		for _, in := range infos {
			qs = append(qs, in.full())
		}
		return unknown(orQ(qs...))
	}
	// any character, any byte, and whatever else: nothing is known
	return unknown(allQuery)
}

func concatInfo(a, b reInfo) reInfo {
	if a.exact != nil && b.exact != nil && len(a.exact)*len(b.exact) <= maxExact {
		set := make([]string, 0, len(a.exact)*len(b.exact))
		for _, x := range a.exact {
			for _, y := range b.exact {
				set = append(set, x+y)
			}
		}
		return reInfo{exact: dedupe(set), q: andQ(a.q, b.q)}
	}
	return unknown(andQ(a.full(), b.full()))
}

func dedupe(ss []string) []string {
	slices.Sort(ss)
	return slices.Compact(ss)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// lowerString folds ASCII letters, the way trigrams are indexed.
func lowerString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		b.WriteByte(lowerASCII(s[i]))
	}
	return b.String()
}
