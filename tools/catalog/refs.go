package main

import "strings"

// ref is one variable reference in a Compose string: $NAME, ${NAME} or
// ${NAME<op><arg>} with one of Compose's operators (":-", "-", ":?", "?",
// ":+", "+").
type ref struct {
	Name, Op, Arg string
}

func nameByte(c byte, first bool) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || !first && c >= '0' && c <= '9'
}

// rewriteRefs calls fn for each variable reference in s and puts what it
// returns in the reference's place; with false the reference stays. "$$"
// is Compose's escape for a dollar sign and is passed over, and a dollar
// sign that starts nothing Compose can read is escaped.
func rewriteRefs(s string, fn func(r ref) (string, bool)) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 == len(s) {
			b.WriteString("$$")
			break
		}
		if s[i+1] == '$' {
			b.WriteString("$$")
			i += 2
			continue
		}
		end := i + 1
		var r ref
		if s[i+1] == '{' {
			depth, j := 1, i+2
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '{':
					depth++
				case '}':
					depth--
				}
				j++
			}
			if depth != 0 {
				b.WriteString("$$")
				i++
				continue
			}
			inner := s[i+2 : j-1]
			k := 0
			for k < len(inner) && nameByte(inner[k], k == 0) {
				k++
			}
			r.Name = inner[:k]
			rest := inner[k:]
			for _, op := range []string{":-", ":?", ":+", "-", "?", "+"} {
				if strings.HasPrefix(rest, op) {
					r.Op, r.Arg = op, rest[len(op):]
					break
				}
			}
			if r.Name == "" || rest != "" && r.Op == "" {
				// Not a reference Compose can read (a shell's or a
				// script's own "${…}"): Compose refuses the file for it,
				// so it is written as the text it was meant as.
				b.WriteString("$$")
				i++
				continue
			}
			end = j
		} else {
			j := i + 1
			for j < len(s) && nameByte(s[j], j == i+1) {
				j++
			}
			if j == i+1 {
				// "$(" and "$1" are a shell's: see above.
				b.WriteString("$$")
				i++
				continue
			}
			r.Name = s[i+1 : j]
			end = j
		}
		if out, ok := fn(r); ok {
			b.WriteString(out)
		} else {
			b.WriteString(s[i:end])
		}
		i = end
	}
	return b.String()
}

// hasDefault reports whether a reference supplies its own value when the
// variable is not set.
func (r ref) hasDefault() bool { return r.Op == ":-" || r.Op == "-" }

// escapeDollars doubles every dollar sign of a text that Compose will fill
// variables into, so that it arrives as written.
func escapeDollars(s string) string { return strings.ReplaceAll(s, "$", "$$") }
