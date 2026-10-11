package deploy

import (
	"strings"
	"testing"
)

func TestUnrepeated(t *testing.T) {
	for name, c := range map[string]struct {
		writes []string
		want   string
	}{
		"a line that repeats is written once": {
			[]string{" a Downloading 1MB\n a Downloading 1MB\n a Downloading 1MB\n a Pull complete\n"},
			" a Downloading 1MB\n a Pull complete\n",
		},
		"lines that differ are all kept, also when one comes back later": {
			[]string{"one\ntwo\none\n"}, "one\ntwo\none\n",
		},
		"a line split over writes is one line": {
			[]string{"Network x Cre", "ating\nNetwork x ", "Creating\nNetwork x Created\n"},
			"Network x Creating\nNetwork x Created\n",
		},
		"a last line with no end is passed on by Flush": {
			[]string{"done\n", "and more"}, "done\nand more",
		},
		"empty lines repeat too": {
			[]string{"a\n\n\n\nb\n"}, "a\n\nb\n",
		},
		"nothing": {nil, ""},
	} {
		var out strings.Builder
		u := &unrepeated{w: &out}
		for _, w := range c.writes {
			if n, err := u.Write([]byte(w)); n != len(w) || err != nil {
				t.Fatalf("%s: Write = %d, %v", name, n, err)
			}
		}
		u.Flush()
		if out.String() != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, out.String(), c.want)
		}
	}

	// Output with no line ends is passed on in pieces, not kept.
	var out strings.Builder
	u := &unrepeated{w: &out}
	long := strings.Repeat("x", 3*unrepeatedLimit)
	u.Write([]byte(long))
	if out.Len() == 0 || len(u.held) > unrepeatedLimit {
		t.Errorf("%d bytes held, %d passed on", len(u.held), out.Len())
	}
	u.Flush()
	if out.String() != long {
		t.Errorf("%d of %d bytes came through", out.Len(), len(long))
	}
}
