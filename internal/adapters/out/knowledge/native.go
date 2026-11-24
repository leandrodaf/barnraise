package knowledge

import (
	"bytes"
	"strings"
)

// rawDecl is a declaration found in a file, before it is qualified by its
// container. Lines are 0-based and inclusive.
type rawDecl struct {
	name, kind, recv string
	line, end        int
	depth            int // brace depth (or indentation in Python) of the declaration line
	container        bool
	symbol           bool
	inside           bool // only valid directly inside a container
}

// maxSpan bounds how far a body is followed when its end is never found.
const maxSpan = 5000

// extractNative is the extractor used when no syntax tree is available: a
// small tokenizer blanks comments and literals, then declarations are found
// line by line and their bodies followed by braces (or by indentation in
// Python). A declaration inside a function body is a local and is left out;
// one inside a class-like container is qualified by it.
func extractNative(lang string, src []byte) []codeSymbol {
	spec := langSpecs[lang]
	if spec == nil {
		return nil
	}
	norm := bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	lex := lexOf(lang)
	clean := sanitize(norm, lex)
	orig := strings.Split(string(norm), "\n")
	cl := strings.Split(string(clean), "\n")
	var decls []rawDecl
	if lex.indent {
		decls = indentDecls(spec, cl)
	} else {
		decls = braceDecls(spec, lex, cl)
	}
	return finishDecls(spec, lex, orig, cl, decls)
}

// matchDecl applies the declaration patterns of a language to one
// sanitized line.
func matchDecl(spec *langSpec, ln string) (rawDecl, bool) {
	for _, p := range spec.decls {
		m := p.re.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		d := rawDecl{kind: p.kind, container: p.container, symbol: p.symbol, inside: p.inside}
		for gi, g := range p.re.SubexpNames() {
			switch g {
			case "name":
				d.name = m[gi]
			case "kind":
				d.kind = m[gi]
			case "recv":
				d.recv = m[gi]
			}
		}
		if g := p.re.SubexpIndex("type"); g >= 0 && (typeKeyword[m[g]] || spec.keywords[m[g]]) {
			continue
		}
		if d.name == "" || spec.keywords[d.name] {
			continue
		}
		return d, true
	}
	return rawDecl{}, false
}

// braceDecls finds the declarations of a brace language and where each ends.
func braceDecls(spec *langSpec, lex *lexRules, cl []string) []rawDecl {
	depth := make([]int, len(cl))
	d := 0
	for i, ln := range cl {
		depth[i] = d
		for k := 0; k < len(ln); k++ {
			switch ln[k] {
			case '{':
				d++
			case '}':
				if d > 0 {
					d--
				}
			}
		}
	}
	var hits []rawDecl
	for i, ln := range cl {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if h, ok := matchDecl(spec, ln); ok {
			h.line, h.depth = i, depth[i]
			hits = append(hits, h)
		}
	}
	for k := range hits {
		limit := len(cl)
		for m := k + 1; m < len(hits); m++ {
			if hits[m].depth <= hits[k].depth {
				limit = hits[m].line
				break
			}
		}
		hits[k].end = braceEnd(lex, cl, hits[k], limit)
	}
	return hits
}

// braceEnd follows a declaration to the brace that closes its body, the
// semicolon that ends it or, in languages with expression bodies, the line
// break that completes it. Without a body it stops before the next
// declaration at the same depth (limit).
func braceEnd(lex *lexRules, cl []string, h rawDecl, limit int) int {
	cur, paren := h.depth, 0
	opened := false
	for li := h.line; li < len(cl) && li < h.line+maxSpan; li++ {
		if !opened && li >= limit {
			return lastCode(cl, h.line, li-1)
		}
		ln := cl[li]
		for k := 0; k < len(ln); k++ {
			switch ln[k] {
			case '(', '[':
				paren++
			case ')', ']':
				if paren > 0 {
					paren--
				}
			case '{':
				if !opened && paren == 0 && cur == h.depth {
					opened = true
				}
				cur++
			case '}':
				cur--
				if opened && cur == h.depth {
					return li
				}
				if cur < h.depth { // the enclosing block closed first
					if li == h.line {
						return li
					}
					return lastCode(cl, h.line, li-1)
				}
			case ';':
				if !opened && paren == 0 && cur == h.depth {
					return li
				}
			}
		}
		if !opened && lex.exprBodies && paren == 0 && cur == h.depth && lineComplete(cl, li) {
			return li
		}
	}
	return lastCode(cl, h.line, min(len(cl), h.line+maxSpan)-1)
}

// lineComplete guesses whether a declaration of a language without
// semicolons ends at the end of line li.
func lineComplete(cl []string, li int) bool {
	t := strings.TrimRight(cl[li], " \t")
	if t == "" {
		return false
	}
	if strings.HasSuffix(t, "->") || strings.ContainsRune("=,(+-*/.:&|?<[{\\", rune(t[len(t)-1])) {
		return false
	}
	for j := li + 1; j < len(cl); j++ {
		next := strings.TrimSpace(cl[j])
		if next == "" {
			continue
		}
		for _, p := range []string{".", ":", "=", "?", "{", "->", "&&", "||", "where", "throws", "rethrows", "async", "extends", "implements", "with"} {
			if strings.HasPrefix(next, p) {
				return false
			}
		}
		return true
	}
	return true
}

// lastCode returns the last line in [from, to] that has code, or from.
func lastCode(cl []string, from, to int) int {
	for i := to; i > from; i-- {
		if strings.TrimSpace(cl[i]) != "" {
			return i
		}
	}
	return from
}

// indentDecls finds the declarations of an indentation language (Python):
// a block ends before the next logical line indented no deeper than it.
func indentDecls(spec *langSpec, cl []string) []rawDecl {
	logical := make([]bool, len(cl))
	indent := make([]int, len(cl))
	paren, cont := 0, false
	for i, ln := range cl {
		t := strings.TrimSpace(ln)
		logical[i] = t != "" && paren == 0 && !cont
		indent[i] = indentOf(ln)
		for k := 0; k < len(ln); k++ {
			switch ln[k] {
			case '(', '[', '{':
				paren++
			case ')', ']', '}':
				if paren > 0 {
					paren--
				}
			}
		}
		cont = strings.HasSuffix(strings.TrimRight(ln, " \t"), "\\")
	}
	var hits []rawDecl
	for i, ln := range cl {
		if !logical[i] {
			continue
		}
		h, ok := matchDecl(spec, ln)
		if !ok {
			continue
		}
		h.line, h.depth = i, indent[i]
		next := len(cl)
		for j := i + 1; j < len(cl); j++ {
			if logical[j] && (h.kind == "const" || indent[j] <= indent[i]) {
				next = j
				break
			}
		}
		h.end = lastCode(cl, i, next-1)
		hits = append(hits, h)
	}
	return hits
}

// finishDecls qualifies the declarations by their containers, leaves out
// locals and builds the symbols.
func finishDecls(spec *langSpec, lex *lexRules, orig, cl []string, hits []rawDecl) []codeSymbol {
	var out []codeSymbol
	var stack []rawDecl
	for _, h := range hits {
		for len(stack) > 0 && stack[len(stack)-1].end < h.line {
			stack = stack[:len(stack)-1]
		}
		var top *rawDecl
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		if top != nil && !top.container {
			continue // a local inside a function body
		}
		if h.inside && (top == nil || (!lex.indent && h.depth != top.depth+1)) {
			continue
		}
		full, kind := h.name, h.kind
		switch {
		case h.recv != "":
			full, kind = h.recv+"."+h.name, "method"
		case !h.container && top != nil:
			full = top.name + "." + h.name
			if kind == "func" {
				kind = "method"
			}
		}
		stack = append(stack, h)
		if !h.symbol {
			continue
		}
		s := codeSymbol{
			Name: full, Kind: kind, Line: h.line + 1, EndLine: h.end + 1,
			Signature: declSignature(lex, orig, cl, h),
			Text:      clip(strings.Join(orig[docStart(orig, cl, h.line):h.end+1], "\n")),
		}
		if kind == "func" || kind == "method" {
			s.Calls = regexCalls(spec, strings.Join(cl[h.line:h.end+1], "\n"), shortName(full))
		}
		out = append(out, s)
	}
	return out
}

// docStart walks up from a declaration over its doc comments, annotations
// and decorators: they belong to its region.
func docStart(orig, cl []string, line int) int {
	start := line
	for i := line - 1; i >= 0; i-- {
		o := strings.TrimSpace(orig[i])
		if o == "" {
			break
		}
		c := strings.TrimSpace(cl[i])
		if c != "" && !strings.HasPrefix(c, "@") && !strings.HasPrefix(c, "#[") {
			break
		}
		start = i
	}
	return start
}

// declSignature is the declaration text up to its body: following lines
// are joined while parentheses are open.
func declSignature(lex *lexRules, orig, cl []string, h rawDecl) string {
	var so, sc strings.Builder
	paren := 0
	for i := h.line; i <= h.end && i < h.line+8; i++ {
		if i > h.line {
			so.WriteByte(' ')
			sc.WriteByte(' ')
		}
		so.WriteString(orig[i])
		sc.WriteString(cl[i])
		for k := 0; k < len(cl[i]); k++ {
			switch cl[i][k] {
			case '(', '[':
				paren++
			case ')', ']':
				if paren > 0 {
					paren--
				}
			}
		}
		if paren == 0 {
			break
		}
	}
	o, c := so.String(), sc.String()
	cut := len(c)
	paren = 0
	for k := 0; k < len(c); k++ {
		switch c[k] {
		case '(', '[':
			paren++
		case ')', ']':
			if paren > 0 {
				paren--
			}
		case '{':
			if !lex.indent && paren == 0 && k > 0 {
				cut = k
			}
		}
		if cut != len(c) {
			break
		}
	}
	if lex.indent {
		t := strings.TrimRight(c, " \t")
		if strings.HasSuffix(t, ":") && (h.kind == "func" || h.kind == "class") {
			cut = len(t) - 1
		}
	}
	return oneLine(strings.TrimSpace(o[:cut]), 300)
}
