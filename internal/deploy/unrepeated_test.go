package deploy

import (
	"strings"
	"testing"
	"time"
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

	// The progress of a layer: a line every few seconds, of each layer and
	// each step, however many Compose prints. What is no progress report
	// is never held back for the time.
	var pulled strings.Builder
	clock := time.Unix(1000, 0)
	p := &unrepeated{w: &pulled, now: func() time.Time { return clock }}
	for _, step := range []struct {
		after time.Duration
		line  string
	}{
		{0, " aaa Downloading 1MB\n"},
		{time.Second, " bbb Downloading 1MB\n"},
		{time.Second, " aaa Downloading 2MB\n"},
		{time.Second, " bbb Downloading 2MB\n"},
		{time.Second, " aaa Downloading 3MB\n"},
		{2 * time.Second, " aaa Downloading 9MB\n"},
		{0, " aaa Download complete\n"},
		{0, " aaa Extracting 1MB\n"},
		{time.Second, " aaa Extracting 2MB\n"},
		{0, " aaa Pull complete\n"},
		{0, " Container x-worker-1 Creating\n"},
		{0, " Container x-worker-2 Creating\n"},
	} {
		clock = clock.Add(step.after)
		p.Write([]byte(step.line))
	}
	want := " aaa Downloading 1MB\n bbb Downloading 1MB\n aaa Downloading 9MB\n aaa Download complete\n aaa Extracting 1MB\n aaa Pull complete\n Container x-worker-1 Creating\n Container x-worker-2 Creating\n"
	if pulled.String() != want {
		t.Errorf("a pull's progress:\n got %q\nwant %q", pulled.String(), want)
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
	// Also when the pieces are alike: a long line written in equal parts
	// is not a line said twice.
	out.Reset()
	u = &unrepeated{w: &out}
	part := strings.Repeat("y", unrepeatedLimit+1)
	u.Write([]byte(part))
	u.Write([]byte(part))
	u.Flush()
	if out.String() != part+part {
		t.Errorf("%d of %d bytes of a line in equal parts came through", out.Len(), 2*len(part))
	}
}
