// Package metrics asks a server what it and its containers are using.
//
// Nothing here runs unless somebody asks: a page that is open, or the
// sampler of a server that sampling was switched on for. Each question is
// one short command through the server's Runner.
//
// What comes back is the server's word, and a remote server is not trusted
// with the dashboard. Every value is parsed into a number or dropped, and
// nothing of an answer is kept as text except a container's name, which
// must look like one.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

// Host is one reading of a server.
type Host struct {
	// CPU is how busy the processors were over one second, in hundredths
	// of a percent of the whole machine: 10000 is every core fully used.
	CPU int
	// Load is the one-minute load average times a hundred.
	Load  int
	Cores int
	// Memory and the root filesystem, in bytes. MemUsed is what is not
	// available to a program that asks for more.
	MemTotal, MemUsed   int64
	DiskTotal, DiskUsed int64
}

// ErrUnsupported is returned for a server that has no /proc to read: one
// that is not Linux, such as a developer's own machine.
var ErrUnsupported = errors.New("this server does not report its usage: it is not a Linux machine")

// hostScript prints what Host reads, one thing a line. The processor
// counters are read twice, a second apart: they only ever count up, and
// how busy the machine is, is the difference.
const hostScript = `[ -r /proc/stat ] || exit 3
head -n 1 /proc/stat
cut -d ' ' -f 1 /proc/loadavg
grep -c '^cpu[0-9]' /proc/stat
grep '^MemTotal:' /proc/meminfo
grep '^MemAvailable:' /proc/meminfo
df -Pk / | tail -n 1
sleep 1
head -n 1 /proc/stat`

// ReadHost takes one reading of a server. It takes a second.
func ReadHost(ctx context.Context, r runner.Runner) (Host, error) {
	out, err := r.Output(ctx, runner.Cmd{Name: "sh", Args: []string{"-c", hostScript}})
	if err != nil {
		var exit *runner.ExitError
		if errors.As(err, &exit) && exit.Code == 3 {
			return Host{}, ErrUnsupported
		}
		return Host{}, fmt.Errorf("read the server's usage: %w", err)
	}
	return parseHost(string(out))
}

var errHostAnswer = errors.New("the server's answer about its usage cannot be read")

func parseHost(out string) (Host, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 7 {
		return Host{}, errHostAnswer
	}
	var h Host
	busy1, total1, ok1 := cpuCounters(lines[0])
	busy2, total2, ok2 := cpuCounters(lines[6])
	load, err := strconv.ParseFloat(strings.TrimSpace(lines[1]), 64)
	cores, cerr := strconv.Atoi(strings.TrimSpace(lines[2]))
	memTotal, ok3 := meminfoKB(lines[3], "MemTotal:")
	memAvail, ok4 := meminfoKB(lines[4], "MemAvailable:")
	df := strings.Fields(lines[5])
	if !ok1 || !ok2 || err != nil || cerr != nil || !ok3 || !ok4 || len(df) < 4 ||
		load < 0 || load > 1e6 || math.IsNaN(load) || cores < 1 || cores > 4096 || memAvail > memTotal {
		return Host{}, errHostAnswer
	}
	// `df -P`: filesystem, size, used, available, in units of 1024 bytes.
	diskTotal, derr1 := strconv.ParseInt(df[1], 10, 64)
	diskUsed, derr2 := strconv.ParseInt(df[2], 10, 64)
	if derr1 != nil || derr2 != nil || diskTotal < 0 || diskUsed < 0 || diskUsed > diskTotal || diskTotal > maxBytes/1024 {
		return Host{}, errHostAnswer
	}
	if total2 > total1 && busy2 >= busy1 && busy2-busy1 <= total2-total1 {
		h.CPU = int((busy2 - busy1) * 10000 / (total2 - total1))
	}
	h.Load, h.Cores = int(math.Round(load*100)), cores
	h.MemTotal, h.MemUsed = memTotal*1024, (memTotal-memAvail)*1024
	h.DiskTotal, h.DiskUsed = diskTotal*1024, diskUsed*1024
	return h, nil
}

// cpuCounters reads the first line of /proc/stat: the time all processors
// together spent in each state since the machine started. Idle and waiting
// for a disk are not work.
func cpuCounters(line string) (busy, total uint64, ok bool) {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, false
	}
	// The last two fields count time given to guests, which the first two
	// already include.
	if len(f) > 9 {
		f = f[:9]
	}
	for i, field := range f[1:] {
		n, err := strconv.ParseUint(field, 10, 64)
		if err != nil || n > 1<<56 {
			return 0, 0, false
		}
		total += n
		if i != 3 && i != 4 {
			busy += n
		}
	}
	return busy, total, true
}

// maxBytes is the largest amount believed: a petabyte. A server has less
// of anything, and the readings of every container of one answer can then
// be added up without the sum running over.
const maxBytes = 1 << 50

func meminfoKB(line, key string) (int64, bool) {
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != key || f[2] != "kB" {
		return 0, false
	}
	n, err := strconv.ParseInt(f[1], 10, 64)
	return n, err == nil && n >= 0 && n <= maxBytes/1024
}

// Container is one reading of a container.
type Container struct {
	Name string
	// CPU is in hundredths of a percent of one core: 15000 is a core and
	// a half.
	CPU int
	// Mem is what the container holds; MemLimit its limit, or the
	// server's memory when it has none.
	Mem, MemLimit         int64
	NetIn, NetOut         int64 // bytes since the container started
	BlockRead, BlockWrite int64
	Processes             int
}

// maxContainers is how many rows of an answer are read.
const maxContainers = 500

const statsFormat = "{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}\t{{.BlockIO}}\t{{.PIDs}}"

// ReadContainers takes one reading of the named containers, or of every
// running container when none is named. It takes about two seconds:
// Docker measures processor use over an interval. A container that is not
// running is left out.
func ReadContainers(ctx context.Context, r runner.Runner, names ...string) ([]Container, error) {
	args := []string{"stats", "--no-stream", "--no-trunc", "--format", statsFormat}
	if len(names) > 0 {
		for _, name := range names {
			if !docker.ValidName(name) {
				return nil, fmt.Errorf("bad container name %q", name)
			}
		}
		// "--" ends the options: a name is never read as one.
		args = append(append(args, "--"), names...)
	}
	out, err := r.Output(ctx, runner.Cmd{Name: "docker", Args: args})
	if err != nil {
		return nil, fmt.Errorf("read the containers' usage: %w", err)
	}
	return parseContainers(string(out)), nil
}

func parseContainers(out string) []Container {
	var list []Container
	for _, line := range strings.Split(out, "\n") {
		if len(list) >= maxContainers {
			break
		}
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) != 6 || !docker.ValidName(f[0]) || len(f[0]) > 128 {
			continue
		}
		c := Container{Name: f[0]}
		cpu, ok := strings.CutSuffix(f[1], "%")
		pct, err := strconv.ParseFloat(cpu, 64)
		if !ok || err != nil || math.IsNaN(pct) || pct < 0 || pct > 1e6 {
			// "--" is what Docker prints for a container that is not
			// running; anything else unreadable is treated the same.
			continue
		}
		c.CPU = int(math.Round(pct * 100))
		var ok1, ok2, ok3 bool
		c.Mem, c.MemLimit, ok1 = pair(f[2])
		c.NetIn, c.NetOut, ok2 = pair(f[3])
		c.BlockRead, c.BlockWrite, ok3 = pair(f[4])
		pids, err := strconv.Atoi(f[5])
		if !ok1 || !ok2 || !ok3 || err != nil || pids < 0 || pids > 1<<22 {
			continue
		}
		c.Processes = pids
		list = append(list, c)
	}
	return list
}

// pair reads "12.3MiB / 1.944GiB".
func pair(s string) (a, b int64, ok bool) {
	left, right, found := strings.Cut(s, " / ")
	if !found {
		return 0, 0, false
	}
	a, ok1 := size(left)
	b, ok2 := size(right)
	return a, b, ok1 && ok2
}

// units are the ones Docker writes: powers of 1024 for memory, of 1000 for
// what went over a network or a disk.
var units = []struct {
	suffix string
	factor float64
}{
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40}, {"PiB", 1 << 50},
	{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"PB", 1e15},
	{"B", 1},
}

// size reads an amount as Docker prints it, such as "1.944GiB" or "0B".
func size(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for _, u := range units {
		number, ok := strings.CutSuffix(s, u.suffix)
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(number, 64)
		if err != nil || math.IsNaN(n) || n < 0 || n*u.factor > maxBytes {
			return 0, false
		}
		return int64(math.Round(n * u.factor)), true
	}
	return 0, false
}
