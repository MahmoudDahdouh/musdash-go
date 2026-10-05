package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/runner"
)

const sampledHost = `cpu  1000 0 500 8000 500 0 0 0 0 0
0.52
2
MemTotal:        2030000 kB
MemAvailable:    1218000 kB
/dev/vda1         25000000 10000000  14000000      42% /
cpu  1040 0 520 8030 510 0 0 0 0 0
`

// sampledServer answers the sampler's three questions for a server that
// runs the app's container, two containers of one service, and one
// container nobody here started.
func (e *env) sampledServer(service string) func(string, runner.Cmd) (string, error) {
	return func(line string, c runner.Cmd) (string, error) {
		switch {
		case c.Name == "sh" && strings.Contains(line, "/proc/stat"):
			return sampledHost, nil
		case strings.HasPrefix(line, "docker ps --all"):
			return appContainer + "\trunning\tapp\t" + e.app.ID + "\td1\tUp 2 minutes\n" +
				"svc-web\trunning\tservice\t" + service + "\t\tUp 1 minute\n" +
				"svc-db\trunning\tservice\t" + service + "\t\tUp 1 minute\n" +
				"svc-init\texited\tservice\t" + service + "\t\tExited (0) 1 minute ago\n" +
				"forged\trunning\tapp\t../../etc\t\tUp\n", nil
		case strings.HasPrefix(line, "docker stats"):
			return appContainer + "\t12.50%\t100MiB / 512MiB\t0B / 0B\t0B / 0B\t3\n" +
				"svc-web\t1.00%\t50MiB / 1GiB\t0B / 0B\t0B / 0B\t2\n" +
				"svc-db\t2.00%\t150MiB / 2GiB\t0B / 0B\t0B / 0B\t9\n" +
				"somebody-elses\t90.00%\t1GiB / 2GiB\t0B / 0B\t0B / 0B\t50\n" +
				"forged\t5.00%\t1MiB / 2MiB\t0B / 0B\t0B / 0B\t1\n", nil
		}
		return e.handle(line, c)
	}
}

func TestSamplingStoresAMinuteAndDropsADay(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const service = "svcaaaaaaaaa"
	e.fake.Handle = e.sampledServer(service)
	now := time.Date(2026, 10, 5, 12, 30, 20, 0, time.UTC)

	// Off unless switched on: a tick asks the server nothing.
	e.o.Tick(ctx, now)
	e.o.WaitSamples()
	for _, call := range e.fake.Calls() {
		if strings.HasPrefix(call, "docker stats") || strings.Contains(call, "/proc/stat") {
			t.Fatalf("a server that is not sampled was asked: %s", call)
		}
	}

	if err := e.db.SetServerSampled(ctx, e.team, e.server.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetServerSampled(ctx, "another-team", e.server.ID, false); err == nil {
		t.Fatal("another team switched the server's sampling off")
	}
	// A sample from a day and a minute ago is on its way out.
	old := now.Add(-SampleKeep - time.Minute).Unix()
	if err := e.db.AddSamples(ctx, e.server.ID, old, map[string]db.Sample{"": {CPU: 1}}); err != nil {
		t.Fatal(err)
	}
	e.o.Tick(ctx, now)
	e.o.WaitSamples()

	minute := now.Truncate(time.Minute).Unix()
	host, err := e.db.SampleHistory(ctx, e.server.ID, "", 0, 60)
	if err != nil || len(host) != 1 {
		t.Fatalf("the server's samples: %+v %v", host, err)
	}
	want := db.Sample{At: minute, CPU: 6000, Mem: (2030000 - 1218000) << 10, MemTotal: 2030000 << 10, Load: 52, DiskUsed: 10000000 << 10, DiskTotal: 25000000 << 10}
	if host[0] != want {
		t.Fatalf("got  %+v\nwant %+v", host[0], want)
	}
	app, _ := e.db.SampleHistory(ctx, e.server.ID, e.app.ID, 0, 60)
	if len(app) != 1 || app[0].CPU != 1250 || app[0].Mem != 100<<20 || app[0].MemTotal != 512<<20 {
		t.Fatalf("the app's samples: %+v", app)
	}
	// A service is the sum of its containers.
	svc, _ := e.db.SampleHistory(ctx, e.server.ID, service, 0, 60)
	if len(svc) != 1 || svc[0].CPU != 300 || svc[0].Mem != 200<<20 || svc[0].MemTotal != 2<<30 {
		t.Fatalf("the service's samples: %+v", svc)
	}
	// Nothing is stored for a container that is not musdash's, or under
	// an id that is not one.
	var rows int
	e.db.QueryRow(`SELECT count(*) FROM metric_samples`).Scan(&rows)
	if rows != 3 {
		t.Fatalf("%d rows stored, want the server, the app and the service", rows)
	}

	// The next minute adds a point; an hour of them is averaged.
	e.o.Tick(ctx, now.Add(time.Minute))
	e.o.WaitSamples()
	if h, _ := e.db.SampleHistory(ctx, e.server.ID, "", 0, 60); len(h) != 2 {
		t.Fatalf("%d samples after two minutes", len(h))
	}
	if h, _ := e.db.SampleHistory(ctx, e.server.ID, "", 0, 3600); len(h) != 1 || h[0].CPU != 6000 {
		t.Fatalf("averaged by the hour: %+v", h)
	}
	if h, _ := e.db.SampleHistory(ctx, e.server.ID, "", minute+60, 60); len(h) != 1 {
		t.Fatalf("since the second minute: %+v", h)
	}

	// Switching it off stops the questions and removes what was stored.
	if err := e.db.SetServerSampled(ctx, e.team, e.server.ID, false); err != nil {
		t.Fatal(err)
	}
	e.db.QueryRow(`SELECT count(*) FROM metric_samples`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("%d rows left after sampling was switched off", rows)
	}
	// A sample that was on its way when the switch was turned is dropped.
	e.db.AddSamples(ctx, e.server.ID, minute, map[string]db.Sample{"": {CPU: 1}})
	e.db.QueryRow(`SELECT count(*) FROM metric_samples`).Scan(&rows)
	if rows != 0 {
		t.Fatal("a sample was stored for a server that is not sampled")
	}
}

func TestAHangingServerDoesNotHoldTheTick(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.db.SetServerSampled(ctx, e.team, e.server.ID, true)
	e.fake.Handle = e.sampledServer("svcaaaaaaaaa")
	e.fake.Hang = func(line string) bool { return strings.Contains(line, "/proc/stat") }
	now := time.Date(2026, 10, 5, 12, 30, 0, 0, time.UTC)

	done := make(chan struct{})
	go func() {
		e.o.Tick(ctx, now)
		// The server is still being asked: its next minute is skipped
		// instead of a second question being put to it.
		e.o.Tick(ctx, now.Add(time.Minute))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a server that does not answer held up the tick")
	}
	asked := 0
	for _, call := range e.fake.Calls() {
		if strings.Contains(call, "/proc/stat") {
			asked++
		}
	}
	if asked != 1 {
		t.Fatalf("the hanging server was asked %d times", asked)
	}
	cancel()
	e.o.WaitSamples()
}
