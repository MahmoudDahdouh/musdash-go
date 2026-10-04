package sysmem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRSSIsPlausible(t *testing.T) {
	got := RSS()
	if got < 1<<20 || got > 1<<30 {
		t.Fatalf("RSS = %d bytes, want between 1 MB and 1 GB", got)
	}
}

func TestProcRSS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statm")
	os.WriteFile(path, []byte("12345 2000 300 4 0 500 0\n"), 0o600)
	if got, want := procRSS(path), int64(2000*os.Getpagesize()); got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
	os.WriteFile(path, []byte("garbage"), 0o600)
	if procRSS(path) != 0 || procRSS(path+".missing") != 0 {
		t.Fatal("malformed or missing statm must give 0")
	}
}

func TestMB(t *testing.T) {
	for in, want := range map[int64]int{0: 0, 1 << 20: 1, 21*1024*1024 + 600*1024: 22, 21*1024*1024 + 100*1024: 21} {
		if got := MB(in); got != want {
			t.Errorf("MB(%d) = %d, want %d", in, got, want)
		}
	}
}
