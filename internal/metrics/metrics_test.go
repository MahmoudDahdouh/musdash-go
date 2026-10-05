package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner/runnertest"
)

// What a small Linux server prints for the host script.
const hostAnswer = `cpu  1000 0 500 8000 500 0 0 0 0 0
0.52
2
MemTotal:        2030000 kB
MemAvailable:    1218000 kB
/dev/vda1         25000000 10000000  14000000      42% /
cpu  1040 0 520 8030 510 0 0 0 0 0
`

func TestParseHost(t *testing.T) {
	h, err := parseHost(hostAnswer)
	if err != nil {
		t.Fatal(err)
	}
	// Between the two readings 100 ticks passed, 60 of them at work.
	want := Host{CPU: 6000, Load: 52, Cores: 2, MemTotal: 2030000 << 10, MemUsed: (2030000 - 1218000) << 10,
		DiskTotal: 25000000 << 10, DiskUsed: 10000000 << 10}
	if h != want {
		t.Fatalf("got  %+v\nwant %+v", h, want)
	}

	lines := strings.Split(strings.TrimSpace(hostAnswer), "\n")
	replace := func(i int, with string) string {
		l := append([]string(nil), lines...)
		l[i] = with
		return strings.Join(l, "\n")
	}
	for name, answer := range map[string]string{
		"nothing":                     "",
		"a line too few":              strings.Join(lines[:6], "\n"),
		"a line too many":             hostAnswer + "extra\n",
		"counters that are text":      replace(0, "cpu  a b c d e"),
		"a negative counter":          replace(6, "cpu  -1 0 500 8000 500 0 0 0"),
		"a load that is text":         replace(1, "<script>"),
		"a load that is not a number": replace(1, "NaN"),
		"no cores":                    replace(2, "0"),
		"memory in another unit":      replace(3, "MemTotal: 2030000 MB"),
		"memory under another name":   replace(3, "MemFree: 2030000 kB"),
		"more available than there":   replace(4, "MemAvailable: 9930000 kB"),
		"more disk used than there":   replace(5, "/dev/vda1 100 200 0 200% /"),
		"a disk size that is text":    replace(5, "/dev/vda1 big small 0 1% /"),
		"an absurd amount of memory":  replace(3, "MemTotal: 99999999999999999 kB"),
	} {
		if h, err := parseHost(answer); err == nil {
			t.Errorf("%s: read as %+v", name, h)
		}
	}
	// Counters that went backwards (a machine that restarted between the
	// readings) give no figure rather than a wrong one.
	if h, err := parseHost(replace(6, lines[0])); err != nil || h.CPU != 0 {
		t.Fatalf("counters that did not move: %+v %v", h, err)
	}
}

func TestReadHost(t *testing.T) {
	fake := &runnertest.Fake{Handle: func(line string, c runner.Cmd) (string, error) {
		if c.Name != "sh" || len(c.Args) != 2 || c.Args[0] != "-c" {
			t.Errorf("the host is asked with %q: nothing of a caller belongs in it", line)
		}
		return hostAnswer, nil
	}}
	if h, err := ReadHost(context.Background(), fake); err != nil || h.Cores != 2 {
		t.Fatalf("%+v %v", h, err)
	}
	fake.Handle = func(string, runner.Cmd) (string, error) { return "", runnertest.Exit("sh", 3, "") }
	if _, err := ReadHost(context.Background(), fake); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a machine without /proc: %v", err)
	}
}

func TestParseContainers(t *testing.T) {
	out := "musdash-web-abc\t0.15%\t12.3MiB / 1.944GiB\t1.2kB / 0B\t4.1MB / 8.19kB\t7\n" +
		"musdash-db-xyz\t150.00%\t512MiB / 512MiB\t3.5GB / 1.2GB\t0B / 0B\t42\r\n" +
		"stopped\t--\t-- / --\t--\t--\t--\n" +
		"\n"
	got := parseContainers(out)
	if len(got) != 2 {
		t.Fatalf("%d rows: %+v", len(got), got)
	}
	want := Container{Name: "musdash-web-abc", CPU: 15, Mem: 12897485, MemLimit: 2087354106,
		NetIn: 1200, NetOut: 0, BlockRead: 4100000, BlockWrite: 8190, Processes: 7}
	if got[0] != want {
		t.Fatalf("got  %+v\nwant %+v", got[0], want)
	}
	if got[1].CPU != 15000 || got[1].Mem != 512<<20 || got[1].NetIn != 3500000000 || got[1].Processes != 42 {
		t.Fatalf("%+v", got[1])
	}
	// A server that answers something else gets nothing shown of it.
	for name, row := range map[string]string{
		"a name that is markup":     "<img src=x>\t1%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"a name with a space":       "a b\t1%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"a very long name":          strings.Repeat("a", 200) + "\t1%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"a share that is text":      "web\tlots\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"a negative share":          "web\t-5%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"a share that is no number": "web\tNaN%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"an unknown unit":           "web\t1%\t1XiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"an absurd amount":          "web\t1%\t1e30GiB / 1MiB\t0B / 0B\t0B / 0B\t1",
		"processes that are text":   "web\t1%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\tmany",
		"too few fields":            "web\t1%\t1MiB / 1MiB",
		"too many fields":           "web\t1%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1\textra",
	} {
		if got := parseContainers(row + "\n"); len(got) != 0 {
			t.Errorf("%s: read as %+v", name, got)
		}
	}
	// And only so many rows are read.
	if got := parseContainers(strings.Repeat("web\t1%\t1MiB / 1MiB\t0B / 0B\t0B / 0B\t1\n", 2000)); len(got) != maxContainers {
		t.Fatalf("%d rows kept", len(got))
	}
}

func TestReadContainers(t *testing.T) {
	var asked string
	fake := &runnertest.Fake{Handle: func(line string, _ runner.Cmd) (string, error) {
		asked = line
		return "web\t1.00%\t1MiB / 2MiB\t0B / 0B\t0B / 0B\t1\n", nil
	}}
	got, err := ReadContainers(context.Background(), fake, "web", "db")
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	if !strings.HasPrefix(asked, "docker stats --no-stream --no-trunc --format ") || !strings.HasSuffix(asked, " -- web db") {
		t.Fatalf("asked %q", asked)
	}
	for _, name := range []string{"--all", "a b", "", "x;rm"} {
		if _, err := ReadContainers(context.Background(), fake, name); err == nil {
			t.Errorf("the name %q was accepted", name)
		}
	}
	if _, err := ReadContainers(context.Background(), fake); err != nil || strings.Contains(asked, " -- ") {
		t.Fatalf("every container: %q %v", asked, err)
	}
}
