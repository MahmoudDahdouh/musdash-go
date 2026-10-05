package ops

import (
	"context"
	"regexp"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/metrics"
)

const (
	// SampleKeep is how long a sample is kept.
	SampleKeep = 24 * time.Hour
	// sampleTimeout is how long one server may take to be sampled. It is
	// under a minute, so a slow server is done before its next turn.
	sampleTimeout = 45 * time.Second
	// maxSampled is how many resources of one server are stored a minute.
	maxSampled = 300
)

// resourceIDRE is the shape of a resource's id. The label it is read from
// is the server's word, and it becomes part of a row.
var resourceIDRE = regexp.MustCompile(`^[a-z][a-z2-7]{11}$`)

// sampleServers takes this minute's sample of every server that sampling
// is switched on for, and drops the samples that are a day old.
//
// Each server is asked in a goroutine of its own with a time limit: one
// that hangs holds up neither the others nor the schedules. A server that
// is still being asked when its next minute comes is skipped.
func (o *Ops) sampleServers(ctx context.Context, now time.Time) {
	if err := o.DB.PruneSamples(ctx, now.Add(-SampleKeep).Unix()); err != nil {
		o.Log.Error("drop old samples", "err", err)
	}
	servers, err := o.DB.SampledServers(ctx)
	if err != nil {
		o.Log.Error("list servers to sample", "err", err)
		return
	}
	for _, s := range servers {
		o.samplingMu.Lock()
		busy := o.sampling[s.ID]
		if !busy {
			if o.sampling == nil {
				o.sampling = map[string]bool{}
			}
			o.sampling[s.ID] = true
		}
		o.samplingMu.Unlock()
		if busy {
			continue
		}
		o.samples.Add(1)
		go func(s db.Server) {
			defer o.samples.Done()
			defer func() {
				o.samplingMu.Lock()
				delete(o.sampling, s.ID)
				o.samplingMu.Unlock()
			}()
			ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
			defer cancel()
			if err := o.sampleServer(ctx, s, now); err != nil && ctx.Err() == nil {
				o.Log.Warn("sample a server", "server", s.Name, "err", err)
			}
		}(s)
	}
}

// sampleServer stores one reading of a server and of the resources on it.
// A resource's reading is the sum over its containers: a deployment
// replaces the container, and a service has several.
func (o *Ops) sampleServer(ctx context.Context, s db.Server, now time.Time) error {
	r, err := o.Runners.Runner(ctx, s)
	if err != nil {
		return err
	}
	rows := map[string]db.Sample{}
	// A server that does not report its own usage (it is not Linux) can
	// still say what its containers use.
	host, hostErr := metrics.ReadHost(ctx, r)
	if hostErr == nil {
		rows[""] = db.Sample{CPU: host.CPU, Mem: host.MemUsed, MemTotal: host.MemTotal, Load: host.Load, DiskUsed: host.DiskUsed, DiskTotal: host.DiskTotal}
	}
	listed, err := docker.Client{R: r}.List(ctx)
	if err != nil {
		return err
	}
	owner := make(map[string]string, len(listed))
	for _, c := range listed {
		if c.State == "running" && resourceIDRE.MatchString(c.Resource) {
			owner[c.Name] = c.Resource
		}
	}
	if len(owner) > 0 {
		readings, err := metrics.ReadContainers(ctx, r)
		if err != nil {
			return err
		}
		for _, c := range readings {
			id, managed := owner[c.Name]
			if !managed {
				continue
			}
			row, seen := rows[id]
			if !seen && len(rows) > maxSampled {
				continue
			}
			row.CPU += c.CPU
			row.Mem += c.Mem
			// The limits of a service's containers do not add up to
			// anything; the largest is the scale its memory is drawn on.
			if c.MemLimit > row.MemTotal {
				row.MemTotal = c.MemLimit
			}
			rows[id] = row
		}
	}
	if len(rows) == 0 {
		return hostErr
	}
	// The minute the sample is of, so that samples line up on the charts.
	return o.DB.AddSamples(ctx, s.ID, now.Truncate(time.Minute).Unix(), rows)
}

// WaitSamples waits for the samples that are being taken. Shutdown and
// tests use it.
func (o *Ops) WaitSamples() { o.samples.Wait() }
