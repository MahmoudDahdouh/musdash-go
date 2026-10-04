package secret

import (
	"bytes"
	"regexp"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()
	b, err := New(RandomBytes(KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := testBox(t)
	for _, plain := range [][]byte{nil, []byte("x"), []byte("pässwörd with \x00 bytes"), bytes.Repeat([]byte("k"), 70000)} {
		sealed, err := b.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		got, err := b.Open(sealed)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("round trip changed value of length %d", len(plain))
		}
	}
}

func TestSealIsRandomised(t *testing.T) {
	b := testBox(t)
	a, _ := b.SealString("same")
	c, _ := b.SealString("same")
	if a == c {
		t.Fatal("two seals of one value must differ")
	}
}

func TestOpenRejectsTamperedAndForeign(t *testing.T) {
	b := testBox(t)
	sealed, _ := b.SealString("value")

	tampered := []byte(sealed)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}
	for name, in := range map[string]string{
		"tampered":   string(tampered),
		"truncated":  sealed[:8],
		"not base64": "!!!",
		"empty":      "",
	} {
		if _, err := b.Open(in); err != ErrOpen {
			t.Errorf("%s: want ErrOpen, got %v", name, err)
		}
	}
	if _, err := testBox(t).Open(sealed); err != ErrOpen {
		t.Errorf("other key: want ErrOpen, got %v", err)
	}
}

func TestOpenStringEmpty(t *testing.T) {
	got, err := testBox(t).OpenString("")
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestNewRejectsShortKey(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("want error for 16-byte key")
	}
}

func TestRandomID(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z][a-z2-7]{11}$`)
	seen := make(map[string]bool, 10000)
	for range 10000 {
		id := RandomID()
		if !shape.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestRandomAlnum(t *testing.T) {
	shape := regexp.MustCompile(`^[a-zA-Z0-9]{32}$`)
	for range 200 {
		if s := RandomAlnum(32); !shape.MatchString(s) {
			t.Fatalf("bad value %q", s)
		}
	}
	if RandomAlnum(0) != "" {
		t.Fatal("zero length must be empty")
	}
}

func TestHashToken(t *testing.T) {
	if HashToken("a") == HashToken("b") || len(HashToken("a")) != 64 {
		t.Fatal("unexpected hash")
	}
}
