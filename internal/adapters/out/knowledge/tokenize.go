package knowledge

import "unicode/utf8"

// lexRules are the lexical rules of a language that matter for finding
// declarations: where comments and string literals start and end. The
// native extractor blanks both before it looks for declarations, so a
// brace inside a string or a function inside a comment is never seen.
type lexRules struct {
	lineComments []string // "//", "#"
	block        bool     // /* ... */
	nested       bool     // block comments nest (Rust, Swift, Kotlin, Dart)
	triple       []string // multi-line string delimiters: `"""`, `'''`
	single       bool     // '...' is a string (false: Rust, where ' also starts a lifetime)
	multiLine    bool     // "..." may span lines (Rust)
	template     bool     // `...${code}...` (JavaScript, TypeScript)
	rustRaw      bool     // r"..." and r#"..."#
	regex        bool     // /.../ literals (JavaScript, TypeScript)
	indent       bool     // blocks are indentation (Python)
	exprBodies   bool     // declarations may end at a line break (Kotlin, Swift, Dart)
	rawBacktick  bool     // `...` is a raw, multi-line string (Go)
}

var (
	cLex      = &lexRules{lineComments: []string{"//"}, block: true, single: true}
	lexByLang = map[string]*lexRules{
		"go":         {lineComments: []string{"//"}, block: true, single: true, rawBacktick: true},
		"java":       {lineComments: []string{"//"}, block: true, single: true, triple: []string{`"""`}},
		"kotlin":     {lineComments: []string{"//"}, block: true, nested: true, single: true, triple: []string{`"""`}, exprBodies: true},
		"swift":      {lineComments: []string{"//"}, block: true, nested: true, single: true, triple: []string{`"""`}, exprBodies: true},
		"dart":       {lineComments: []string{"//"}, block: true, nested: true, single: true, triple: []string{`"""`, `'''`}, exprBodies: true},
		"rust":       {lineComments: []string{"//"}, block: true, nested: true, multiLine: true, rustRaw: true},
		"typescript": {lineComments: []string{"//"}, block: true, single: true, template: true, regex: true},
		"javascript": {lineComments: []string{"//"}, block: true, single: true, template: true, regex: true},
		"python":     {lineComments: []string{"#"}, single: true, triple: []string{`"""`, `'''`}, indent: true},
	}
)

func lexOf(lang string) *lexRules {
	if r, ok := lexByLang[lang]; ok {
		return r
	}
	return cLex
}

// sanitize returns a copy of src of the same length where the text of
// comments and the contents of string, character, template and regular
// expression literals are spaces. Line breaks stay, so line numbers and
// byte offsets of the result are those of the source. String delimiters
// stay too, so `x = "..."` still reads as an assignment.
func sanitize(src []byte, r *lexRules) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	n := len(src)
	blank := func(from, to int) {
		for k := from; k < to && k < n; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	// tmpl holds, for each open `${` of a template literal, how many
	// braces were opened inside it since.
	var tmpl []int
	inTemplate := false
	// last significant code byte, to tell a regex literal from a division.
	var prev byte
	prevWord := ""
	i := 0
	for i < n {
		c := src[i]
		if inTemplate {
			switch {
			case c == '\\':
				blank(i, i+2)
				i += 2
			case c == '`':
				inTemplate = false
				prev = '`'
				i++
			case c == '$' && i+1 < n && src[i+1] == '{':
				blank(i, i+2)
				tmpl = append(tmpl, 0)
				inTemplate = false
				prev = '{'
				i += 2
			default:
				blank(i, i+1)
				i++
			}
			continue
		}
		// line comments
		if p := hasPrefixAt(src, i, r.lineComments); p != "" {
			j := i
			for j < n && src[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
			continue
		}
		// block comments
		if r.block && c == '/' && i+1 < n && src[i+1] == '*' {
			depth := 1
			j := i + 2
			for j < n && depth > 0 {
				switch {
				case r.nested && src[j] == '/' && j+1 < n && src[j+1] == '*':
					depth++
					j += 2
				case src[j] == '*' && j+1 < n && src[j+1] == '/':
					depth--
					j += 2
				default:
					j++
				}
			}
			blank(i, j)
			i = j
			continue
		}
		// multi-line strings
		if d := hasPrefixAt(src, i, r.triple); d != "" {
			j := i + len(d)
			for j < n && !(src[j] == d[0] && hasPrefixAt(src, j, []string{d}) != "") {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			end := min(j+len(d), n)
			blank(i+len(d), min(j, n))
			i = end
			prev, prevWord = '"', ""
			continue
		}
		// Rust raw strings: r"..", r#".."#, br#".."#
		if r.rustRaw && (c == 'r' || c == 'b') && (i == 0 || !isIdentByte(src[i-1])) {
			j := i
			if src[j] == 'b' && j+1 < n && src[j+1] == 'r' {
				j++
			}
			if src[j] == 'r' {
				k := j + 1
				hashes := 0
				for k < n && src[k] == '#' {
					hashes++
					k++
				}
				if k < n && src[k] == '"' {
					closeAt := -1
					for m := k + 1; m < n; m++ {
						if src[m] == '"' && countHashes(src, m+1) >= hashes {
							closeAt = m
							break
						}
					}
					if closeAt < 0 {
						closeAt = n - 1
					}
					blank(k+1, closeAt)
					i = min(closeAt+1+hashes, n)
					prev, prevWord = '"', ""
					continue
				}
			}
		}
		switch {
		case c == '"' || (c == '\'' && r.single):
			j := stringEnd(src, i, c, r.multiLine)
			blank(i+1, j)
			i = j + 1
			prev, prevWord = '"', ""
			continue
		case c == '\'' && !r.single: // Rust: a char literal or a lifetime
			if j, ok := rustChar(src, i); ok {
				blank(i+1, j)
				i = j + 1
				prev, prevWord = '"', ""
				continue
			}
			i++ // a lifetime: 'a
			continue
		case c == '`' && r.rawBacktick:
			j := i + 1
			for j < n && src[j] != '`' {
				j++
			}
			blank(i+1, j)
			i = j + 1
			prev, prevWord = '"', ""
			continue
		case c == '`' && r.template:
			inTemplate = true
			i++
			continue
		case c == '/' && r.regex && regexAllowed(prev, prevWord):
			if j, ok := regexEnd(src, i); ok {
				blank(i+1, j)
				i = j + 1
				prev, prevWord = '"', ""
				continue
			}
		case c == '{' && len(tmpl) > 0:
			tmpl[len(tmpl)-1]++
		case c == '}' && len(tmpl) > 0:
			if tmpl[len(tmpl)-1] == 0 {
				tmpl = tmpl[:len(tmpl)-1]
				blank(i, i+1)
				inTemplate = true
				i++
				continue
			}
			tmpl[len(tmpl)-1]--
		}
		switch {
		case isIdentByte(c):
			j := i
			for j < n && isIdentByte(src[j]) {
				j++
			}
			prevWord, prev = string(src[i:j]), 'a'
			i = j
			continue
		case c != ' ' && c != '\t' && c != '\r' && c != '\n':
			prev, prevWord = c, ""
		}
		i++
	}
	return out
}

func hasPrefixAt(src []byte, i int, ps []string) string {
	for _, p := range ps {
		if len(src)-i >= len(p) && string(src[i:i+len(p)]) == p {
			return p
		}
	}
	return ""
}

func countHashes(src []byte, i int) int {
	n := 0
	for i < len(src) && src[i] == '#' {
		n++
		i++
	}
	return n
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// stringEnd returns the index of the closing quote of the string opened at
// i, or of the line end (or the end of src) when it is not closed.
func stringEnd(src []byte, i int, q byte, multiLine bool) int {
	j := i + 1
	for j < len(src) {
		switch src[j] {
		case '\\':
			j += 2
			continue
		case q:
			return j
		case '\n':
			if !multiLine {
				return j
			}
		}
		j++
	}
	return len(src)
}

// rustChar tells a char literal ('x', '\n', 'é') from a lifetime ('a).
func rustChar(src []byte, i int) (int, bool) {
	if i+1 >= len(src) {
		return 0, false
	}
	if src[i+1] == '\\' {
		for j := i + 2; j < len(src) && j < i+12; j++ {
			if src[j] == '\'' {
				return j, true
			}
		}
		return 0, false
	}
	_, size := utf8.DecodeRune(src[i+1:])
	if j := i + 1 + size; j < len(src) && src[j] == '\'' {
		return j, true
	}
	return 0, false
}

// regexAllowed: a slash after these starts a regular expression literal,
// not a division.
func regexAllowed(prev byte, word string) bool {
	switch word {
	case "return", "typeof", "case", "do", "else", "in", "of", "new", "delete", "void", "throw", "yield", "await":
		return true
	case "":
	default:
		return false
	}
	switch prev {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^':
		return true
	}
	return false
}

// regexEnd finds the closing slash of a regex literal on the same line.
func regexEnd(src []byte, i int) (int, bool) {
	if i+1 < len(src) && (src[i+1] == '/' || src[i+1] == '*') {
		return 0, false
	}
	class := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '[':
			class = true
		case ']':
			class = false
		case '/':
			if !class {
				return j, true
			}
		case '\n':
			return 0, false
		}
	}
	return 0, false
}
