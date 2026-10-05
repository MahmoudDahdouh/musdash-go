package web

import (
	"unicode"
	"unicode/utf8"
)

// plainText reports whether a text that a person typed, and that is only
// ever shown, can be shown as it is: a project's name, a person's, a
// channel's. Such a text is escaped wherever a page shows it, but it also
// goes into notifications, log lines and the API's answers, where a line
// break or a NUL byte in a name is somebody else's problem.
//
// Refused are bytes that are not text at all, control characters (which
// include tab and line breaks), the line and paragraph separators, and the
// characters that reorder what follows them, with which a name can be made
// to read as another. Other invisible characters are left alone: joiners
// and direction marks are part of how names are written in Arabic and
// Persian. But a text of nothing else is refused, since it would be a name
// that shows as nothing.
//
// The characters are written by their numbers here and in the tests: a
// reordering character in a source file reorders the line it is in.
//
// Not for texts where a line break belongs (a command, variables, a file)
// or that become part of an address or a command: those have rules of
// their own.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	seen := s == ""
	for _, r := range s {
		switch {
		case unicode.IsControl(r),
			// Line and paragraph separator.
			r == 0x2028, r == 0x2029,
			// Embeddings and overrides, isolates, and the deprecated
			// formatting characters next to them.
			r >= 0x202A && r <= 0x202E,
			r >= 0x2066 && r <= 0x206F,
			// Tags: invisible copies of ASCII.
			r >= 0xE0000 && r <= 0xE007F:
			return false
		}
		if !unicode.Is(unicode.Cf, r) && !unicode.Is(unicode.Mn, r) && !unicode.IsSpace(r) {
			seen = true
		}
	}
	return seen
}

// notShown is what a form says about a text plainText refuses.
const notShown = "This has a character in it that cannot be shown, such as a line break. Type it again."

// labelProblem is the message for a text that failed its check: about the
// character when that is what is wrong, otherwise the field's own.
func labelProblem(text, otherwise string) string {
	if !plainText(text) {
		return notShown
	}
	return otherwise
}
