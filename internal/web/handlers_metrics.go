package web

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/docker"
	"github.com/MahmoudDahdouh/musdash-go/internal/metrics"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/pages"
	"github.com/MahmoudDahdouh/musdash-go/internal/web/ui"
)

// What a server and its containers use is asked for when a page wants it
// and at no other time. A reading runs a command or two on the server and
// takes a few seconds; the page asks again while it stays open.

const (
	// maxReadings bounds the readings in progress across the dashboard.
	// One more is not queued: the page keeps what it shows and asks again.
	maxReadings = 4
	// readingTimeout is how long a server may take to answer.
	readingTimeout = 12 * time.Second
	// maxPolls is how many times in a row a page is answered before it is
	// asked whether anybody is still looking: about ten minutes.
	maxPolls = 120
)

// span is a stretch of time a chart can cover, and how wide a stretch one
// point of it stands for.
type span struct {
	key, label string
	length     time.Duration
	bucket     int64
}

var spans = []span{
	{"1h", "1 hour", time.Hour, 60},
	{"6h", "6 hours", 6 * time.Hour, 300},
	{"24h", "24 hours", 24 * time.Hour, 900},
}

func spanOf(r *http.Request) span {
	for _, sp := range spans {
		if sp.key == r.URL.Query().Get("range") {
			return sp
		}
	}
	return spans[0]
}

// resourceIDRE is the shape of an id. A container's label is the server's
// word; it becomes a link only when it looks like one.
var resourceIDRE = regexp.MustCompile(`^[a-z][a-z2-7]{11}$`)

// takeReading reserves one of the readings, or reports that all are in use.
func (s *Server) takeReading() (release func(), ok bool) {
	select {
	case s.readings <- struct{}{}:
		return func() { <-s.readings }, true
	default:
		return nil, false
	}
}

// nextPoll is where a usage fragment asks for the one after it, or "" when
// the page has asked often enough.
func nextPoll(r *http.Request, base string) string {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n < 0 || n >= maxPolls {
		return ""
	}
	return base + "?n=" + strconv.Itoa(n+1)
}

// history builds the charts of a server ("" as the resource) or of a
// resource on it.
func (s *Server) history(r *http.Request, serverID, resourceID, base string) (pages.History, error) {
	h := pages.History{ServerHref: "/servers/" + serverID + "/metrics"}
	sampled, err := s.DB.ServerSampled(r.Context(), serverID)
	if err != nil || !sampled {
		return h, err
	}
	sp := spanOf(r)
	h.Sampled, h.Range = true, sp.key
	for _, each := range spans {
		h.Ranges = append(h.Ranges, ui.Tab{Key: each.key, Label: each.label, Href: base + "?range=" + each.key + "#history"})
	}
	// Whole buckets, so that a point does not change as the minute passes.
	to := time.Now().Unix()/sp.bucket*sp.bucket + sp.bucket
	from := to - int64(sp.length/time.Second)
	samples, err := s.DB.SampleHistory(r.Context(), serverID, resourceID, from, sp.bucket)
	if err != nil {
		return h, err
	}
	series := func(of func(db.Sample) float64) []ui.ChartPoint {
		pts := make([]ui.ChartPoint, 0, len(samples))
		for _, m := range samples {
			pts = append(pts, ui.ChartPoint{At: m.At, V: of(m)})
		}
		return pts
	}
	chart := func(title string, format func(float64) string, top float64, note string, of func(db.Sample) float64) ui.ChartProps {
		return ui.ChartProps{Title: title, Points: series(of), From: from, To: to, Step: sp.bucket, Top: top, Format: format, Note: note}
	}
	mem := series(func(m db.Sample) float64 { return float64(m.Mem) })
	var memTotal, diskTotal int64
	for _, m := range samples {
		memTotal, diskTotal = max(memTotal, m.MemTotal), max(diskTotal, m.DiskTotal)
	}
	if resourceID == "" {
		h.Charts = []ui.ChartProps{
			chart("Processor", ui.Percent, 100, "of all cores", func(m db.Sample) float64 { return float64(m.CPU) / 100 }),
			{Title: "Memory", Points: mem, From: from, To: to, Step: sp.bucket, Top: float64(memTotal), Format: ui.Bytes, Note: "of " + ui.Bytes(float64(memTotal))},
			chart("Load", ui.Load, 0, "one-minute average", func(m db.Sample) float64 { return float64(m.Load) / 100 }),
			chart("Disk", ui.Bytes, float64(diskTotal), "of "+ui.Bytes(float64(diskTotal)), func(m db.Sample) float64 { return float64(m.DiskUsed) }),
		}
		return h, nil
	}
	note := ""
	if memTotal > 0 {
		note = "limit " + ui.Bytes(float64(memTotal))
	}
	h.Charts = []ui.ChartProps{
		chart("Processor", ui.Percent, 0, "100% is one core", func(m db.Sample) float64 { return float64(m.CPU) / 100 }),
		{Title: "Memory", Points: mem, From: from, To: to, Step: sp.bucket, Top: ui.BytesTop(mem), Format: ui.Bytes, Note: note},
	}
	return h, nil
}

// share is a part of a whole, or -1 when there is no whole.
func share(part, whole int64) float64 {
	if whole <= 0 {
		return -1
	}
	return float64(part) / float64(whole)
}

// containerMeters sums a resource's containers into what its page shows.
func containerMeters(list []metrics.Container) []ui.MeterProps {
	var sum metrics.Container
	for _, c := range list {
		sum.CPU += c.CPU
		sum.Mem += c.Mem
		sum.MemLimit = max(sum.MemLimit, c.MemLimit)
		sum.NetIn += c.NetIn
		sum.NetOut += c.NetOut
		sum.BlockRead += c.BlockRead
		sum.BlockWrite += c.BlockWrite
		sum.Processes += c.Processes
	}
	mem := ui.MeterProps{Label: "Memory", Value: ui.Bytes(float64(sum.Mem)), Share: -1}
	if len(list) == 1 && sum.MemLimit > 0 {
		// One container: its limit, or the server's memory when it has none.
		mem.Value += " of " + ui.Bytes(float64(sum.MemLimit))
		mem.Share = share(sum.Mem, sum.MemLimit)
	}
	return []ui.MeterProps{
		{Label: "Processor", Value: ui.Percent(float64(sum.CPU) / 100), Share: -1, Detail: "100% is one core in full use."},
		mem,
		{Label: "Network", Value: ui.Bytes(float64(sum.NetIn)) + " in · " + ui.Bytes(float64(sum.NetOut)) + " out", Share: -1, Detail: "Since the container started."},
		{Label: "Disk", Value: ui.Bytes(float64(sum.BlockRead)) + " read · " + ui.Bytes(float64(sum.BlockWrite)) + " written", Share: -1, Detail: strconv.Itoa(sum.Processes) + " processes."},
	}
}

// usageOf answers a page's question about a resource's containers.
func (s *Server) usageOf(w http.ResponseWriter, r *http.Request, serverID, base string, names func(ctx context.Context, dk docker.Client) ([]string, error)) {
	release, ok := s.takeReading()
	if !ok {
		// Nothing is swapped in: the page keeps what it shows and asks again.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer release()
	u := pages.Usage{Next: nextPoll(r, base+"/metrics/now"), Every: "5s", Resume: base + "/metrics"}
	ctx, cancel := context.WithTimeout(r.Context(), readingTimeout)
	defer cancel()
	list, err := func() ([]metrics.Container, error) {
		server, err := s.DB.ServerByID(ctx, serverID)
		if err != nil {
			return nil, err
		}
		dk, err := s.Pool.Docker(ctx, server)
		if err != nil {
			return nil, err
		}
		containers, err := names(ctx, dk)
		if err != nil || len(containers) == 0 {
			return nil, err
		}
		return metrics.ReadContainers(ctx, dk.R, containers...)
	}()
	switch {
	case r.Context().Err() != nil:
		return
	case err != nil:
		s.Log.Warn("read usage", "route", logRoute(r), "err", err)
		u.Problem, u.Every = "The server did not answer the question. It is asked again shortly.", "15s"
	case len(list) == 0:
		u.Problem, u.Every = "Nothing of this is running at the moment.", "15s"
	default:
		u.Meters = containerMeters(list)
		if len(list) > 1 {
			for _, c := range list {
				u.Rows = append(u.Rows, pages.UsageRow{Name: c.Name, CPU: ui.Percent(float64(c.CPU) / 100), Mem: ui.Bytes(float64(c.Mem))})
			}
		}
	}
	s.render(w, r, http.StatusOK, pages.UsageNow(u))
}

func one(name string) func(context.Context, docker.Client) ([]string, error) {
	return func(context.Context, docker.Client) ([]string, error) {
		if name == "" {
			return nil, nil
		}
		return []string{name}, nil
	}
}

func (s *Server) appMetrics(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadApp(w, r)
	if !ok {
		return
	}
	h, err := s.history(r, v.App.ServerID, v.App.ID, "/apps/"+v.App.ID+"/metrics")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.AppMetrics(s.appShell(w, r, v), v, h))
}

func (s *Server) appMetricsNow(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadApp(w, r); ok {
		s.usageOf(w, r, v.App.ServerID, "/apps/"+v.App.ID, one(v.App.Container))
	}
}

func (s *Server) databaseMetrics(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadDatabase(w, r)
	if !ok {
		return
	}
	h, err := s.history(r, v.DB.ServerID, v.DB.ID, "/databases/"+v.DB.ID+"/metrics")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.DatabaseMetrics(s.databaseShell(w, r, v), v, h))
}

func (s *Server) databaseMetricsNow(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadDatabase(w, r); ok {
		s.usageOf(w, r, v.DB.ServerID, "/databases/"+v.DB.ID, one(v.DB.Container))
	}
}

func (s *Server) serviceMetrics(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadService(w, r)
	if !ok {
		return
	}
	h, err := s.history(r, v.Service.ServerID, v.Service.ID, "/services/"+v.Service.ID+"/metrics")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, pages.ServiceMetrics(s.serviceShell(w, r, v), v, h))
}

// serviceContainers lists the running containers of a service: the ones
// that carry its id, by name.
func serviceContainers(serviceID string) func(context.Context, docker.Client) ([]string, error) {
	return func(ctx context.Context, dk docker.Client) ([]string, error) {
		listed, err := dk.List(ctx)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, c := range listed {
			if c.Kind == db.KindService && c.Resource == serviceID && c.State == "running" && docker.ValidName(c.Name) && len(names) < 50 {
				names = append(names, c.Name)
			}
		}
		return names, nil
	}
}

func (s *Server) serviceMetricsNow(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadService(w, r); ok {
		s.usageOf(w, r, v.Service.ServerID, "/services/"+v.Service.ID, serviceContainers(v.Service.ID))
	}
}

// loadServer fetches the server in the path for the signed-in team,
// answering 404 itself.
func (s *Server) loadServer(w http.ResponseWriter, r *http.Request) (db.Server, bool) {
	server, err := s.DB.Server(r.Context(), sessionFrom(r).TeamID, r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		s.notFound(w, r)
		return server, false
	}
	if err != nil {
		s.fail(w, r, err)
		return server, false
	}
	return server, true
}

func (s *Server) serverMetrics(w http.ResponseWriter, r *http.Request) {
	server, ok := s.loadServer(w, r)
	if !ok {
		return
	}
	h, err := s.history(r, server.ID, "", "/servers/"+server.ID+"/metrics")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h.ServerHref = ""
	shell := s.shell(w, r, server.Name, "servers", ui.Crumb{Label: "Servers", Href: "/servers"}, ui.Crumb{Label: server.Name})
	s.render(w, r, http.StatusOK, pages.ServerMetrics(shell, server, h))
}

// serverMetricsNow reads the server itself and every container on it.
func (s *Server) serverMetricsNow(w http.ResponseWriter, r *http.Request) {
	server, ok := s.loadServer(w, r)
	if !ok {
		return
	}
	release, ok := s.takeReading()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer release()
	base := "/servers/" + server.ID + "/metrics"
	u := pages.Usage{Next: nextPoll(r, base+"/now"), Every: "10s", Resume: base}
	ctx, cancel := context.WithTimeout(r.Context(), readingTimeout)
	defer cancel()
	run, err := s.Pool.Runner(ctx, server)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The server's own figures take a second and Docker's two: asked
	// together, the page waits for the slower one only.
	type hostAnswer struct {
		host metrics.Host
		err  error
	}
	hostDone := make(chan hostAnswer, 1)
	go func() {
		host, err := metrics.ReadHost(ctx, run)
		hostDone <- hostAnswer{host, err}
	}()
	dk := docker.Client{R: run}
	listed, listErr := dk.List(ctx)
	var readings []metrics.Container
	if listErr == nil {
		readings, listErr = metrics.ReadContainers(ctx, run)
	}
	host := <-hostDone
	if r.Context().Err() != nil {
		return
	}

	switch {
	case errors.Is(host.err, metrics.ErrUnsupported):
		u.Problem = "This machine does not report its own usage: it is not Linux. What its containers use is shown."
	case host.err != nil:
		s.Log.Warn("read a server's usage", "server", server.Name, "err", host.err)
		u.Problem, u.Every = "The server did not answer the question. It is asked again shortly.", "30s"
	default:
		h := host.host
		u.Meters = []ui.MeterProps{
			{Label: "Processor", Value: ui.Percent(float64(h.CPU) / 100), Share: float64(h.CPU) / 10000, Detail: "Of " + strconv.Itoa(h.Cores) + " cores, over one second."},
			{Label: "Memory", Value: ui.Bytes(float64(h.MemUsed)) + " of " + ui.Bytes(float64(h.MemTotal)), Share: share(h.MemUsed, h.MemTotal)},
			{Label: "Load", Value: ui.Load(float64(h.Load) / 100), Share: -1, Detail: "One-minute average. Above " + strconv.Itoa(h.Cores) + " means work is waiting for a core."},
			{Label: "Disk", Value: ui.Bytes(float64(h.DiskUsed)) + " of " + ui.Bytes(float64(h.DiskTotal)), Share: share(h.DiskUsed, h.DiskTotal), Detail: "The root filesystem."},
		}
	}
	if listErr != nil {
		if host.err == nil {
			s.Log.Warn("read a server's containers", "server", server.Name, "err", listErr)
			u.Problem = "Docker did not answer on this server."
		}
	} else {
		// Only what musdash started is listed, each with the page of what
		// it belongs to.
		owner := map[string]docker.Listed{}
		for _, c := range listed {
			owner[c.Name] = c
		}
		for _, c := range readings {
			o, managed := owner[c.Name]
			if !managed {
				continue
			}
			row := pages.UsageRow{Name: c.Name, CPU: ui.Percent(float64(c.CPU) / 100), Mem: ui.Bytes(float64(c.Mem))}
			if resourceIDRE.MatchString(o.Resource) {
				switch o.Kind {
				case db.KindApp:
					row.Href = "/apps/" + o.Resource + "/metrics"
				case db.KindDatabase:
					row.Href = "/databases/" + o.Resource + "/metrics"
				case db.KindService:
					row.Href = "/services/" + o.Resource + "/metrics"
				}
			}
			u.Rows = append(u.Rows, row)
		}
	}
	s.render(w, r, http.StatusOK, pages.UsageNow(u))
}

// serverMetricsSave switches a server's sampling on or off.
func (s *Server) serverMetricsSave(w http.ResponseWriter, r *http.Request) {
	server, ok := s.loadServer(w, r)
	if !ok {
		return
	}
	on := r.PostFormValue("sample") == "1"
	if err := s.DB.SetServerSampled(r.Context(), sessionFrom(r).TeamID, server.ID, on); err != nil {
		s.fail(w, r, err)
		return
	}
	if on {
		setFlash(w, r, ui.ToneOK, "Sampling is on. The first sample arrives within a minute.")
	} else {
		setFlash(w, r, ui.ToneOK, "Sampling is off, and what was kept is removed.")
	}
	redirect(w, r, "/servers/"+server.ID+"/metrics")
}
