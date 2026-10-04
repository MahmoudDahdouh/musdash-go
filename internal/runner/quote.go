package runner

import "strings"

// Quote returns s as one POSIX shell word. The result is wrapped in single
// quotes, inside which the shell interprets nothing. An embedded single quote
// closes the quoted run, is written backslash-escaped, and reopens it. Every
// value that reaches a remote shell must pass through here.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if isPlain(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// QuoteJoin quotes each argument and joins them with spaces, producing a
// command line that a shell parses back into exactly these arguments.
func QuoteJoin(name string, args ...string) string {
	var b strings.Builder
	b.WriteString(Quote(name))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(Quote(a))
	}
	return b.String()
}

// isPlain reports whether s consists only of characters with no meaning to a
// shell, so it can be left unquoted for readable logs.
func isPlain(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.' || c == '/' || c == ':' || c == '@' || c == ',' || c == '+':
		default:
			return false
		}
	}
	// A leading '-' is still a plain word to the shell; callers that pass
	// user values as arguments guard against option injection with "--".
	return true
}
