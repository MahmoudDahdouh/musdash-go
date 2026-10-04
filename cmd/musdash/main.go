// Command musdash is a self-hosted platform for deploying apps, databases
// and services with Docker. One binary provides the control plane (`server`)
// and the edge proxy (`proxy`).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/proxy"
	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
	"github.com/MahmoudDahdouh/musdash-go/internal/web"
	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const usage = `musdash — deploy apps, databases and services on your own servers

Usage:
  musdash <command> [flags]

Commands:
  server           Run the control plane: UI, API, webhooks and jobs
  proxy            Run the edge proxy for deployed apps
  migrate          Apply database migrations and exit
  reset-password   Print a one-time link to reset an account's password
  version          Print the version

Run "musdash <command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "server":
		err = runServer(args)
	case "proxy":
		err = runProxy(args)
	case "migrate":
		err = runMigrate(args)
	case "reset-password":
		err = runResetPassword(args)
	case "version", "-v", "--version":
		fmt.Println("musdash", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "musdash: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "musdash:", err)
		os.Exit(1)
	}
}

// commonFlags registers the flags every subcommand shares.
func commonFlags(fs *flag.FlagSet) *config.Config {
	cfg := &config.Config{}
	fs.StringVar(&cfg.DataDir, "data", config.EnvOr("MUSDASH_DATA", config.DefaultDataDir), "data directory (env MUSDASH_DATA)")
	fs.BoolVar(&cfg.Dev, "dev", os.Getenv("MUSDASH_DEV") == "1", "development mode (env MUSDASH_DEV=1)")
	return cfg
}

// openDB prepares the data directory and returns a migrated database.
func openDB(ctx context.Context, cfg *config.Config) (*db.DB, error) {
	if err := cfg.EnsureDirs(); err != nil {
		return nil, err
	}
	d, err := db.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	cfg := commonFlags(fs)
	fs.Parse(args)
	d, err := openDB(context.Background(), cfg)
	if err != nil {
		return err
	}
	fmt.Println("database is up to date:", cfg.DBPath())
	return d.Close()
}

// tuneMemory applies the soft memory limit and GC pace the RAM budget is
// built on, unless the operator set GOMEMLIMIT or GOGC themselves.
func tuneMemory(limitMiB int64) {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(limitMiB << 20)
	}
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(50)
	}
}

// newLogger writes structured logs to stderr; systemd's journal collects them.
func newLogger(dev bool) *slog.Logger {
	level := slog.LevelInfo
	if dev {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// signalContext is cancelled on SIGINT or SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	cfg := commonFlags(fs)
	listen := fs.String("listen", config.EnvOr("MUSDASH_LISTEN", ":8000"), "address the UI listens on (env MUSDASH_LISTEN)")
	workers := fs.Int("workers", 4, "background jobs that may run at once")
	pprof := fs.Bool("pprof", false, "serve /debug/pprof to loopback clients")
	fs.Parse(args)
	tuneMemory(48)

	log := newLogger(cfg.Dev)
	ctx, stop := signalContext()
	defer stop()

	d, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer d.Close()
	key, err := cfg.MasterKey()
	if err != nil {
		return err
	}
	// The key is in memory now; it must not stay in the environment, where
	// child processes and /proc/<pid>/environ would expose it.
	os.Unsetenv("MUSDASH_MASTER_KEY")
	box, err := secret.New(key)
	if err != nil {
		return err
	}

	queue := jobs.New(d.DB, log, *workers)
	if err := queue.Start(ctx); err != nil {
		return err
	}

	app := &web.Server{Cfg: cfg, DB: d, Box: box, Queue: queue, Log: log, Pprof: *pprof}
	srv := &http.Server{
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		// No WriteTimeout: log and event streams stay open.
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	log.Info("musdash server listening", "addr", ln.Addr().String(), "data", cfg.DataDir, "version", version)

	go housekeeping(ctx, d, log)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shut, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shut); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Error("http shutdown", "err", err)
	}
	queue.Stop(shut)
	return nil
}

// housekeeping removes expired sessions and reset tokens once an hour.
func housekeeping(ctx context.Context, d *db.DB, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := d.DeleteExpired(ctx); err != nil && ctx.Err() == nil {
			log.Error("delete expired sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func runProxy(args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	cfg := commonFlags(fs)
	httpAddr := fs.String("http", config.EnvOr("MUSDASH_PROXY_HTTP", ":80"), "HTTP listen address (env MUSDASH_PROXY_HTTP)")
	httpsAddr := fs.String("https", config.EnvOr("MUSDASH_PROXY_HTTPS", ":443"), "HTTPS listen address; empty turns HTTPS off (env MUSDASH_PROXY_HTTPS)")
	fs.Parse(args)
	tuneMemory(32)

	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return proxy.Run(ctx, proxy.Options{
		HTTPAddr:   *httpAddr,
		HTTPSAddr:  *httpsAddr,
		RoutesPath: cfg.RoutesPath(),
		PIDPath:    cfg.ProxyPIDPath(),
		CertDir:    cfg.CertDir(),
		Log:        newLogger(cfg.Dev),
	})
}

func runResetPassword(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	cfg := commonFlags(fs)
	baseURL := fs.String("url", config.EnvOr("MUSDASH_URL", "http://localhost:8000"), "address the UI is reached at (env MUSDASH_URL)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: musdash reset-password [flags] <email>")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	d, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer d.Close()
	user, err := d.UserByEmail(ctx, strings.ToLower(strings.TrimSpace(fs.Arg(0))))
	if errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("no account uses %s", fs.Arg(0))
	}
	if err != nil {
		return err
	}
	token := secret.RandomToken(32)
	if err := d.CreatePasswordReset(ctx, secret.HashToken(token), user.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		return err
	}
	fmt.Printf("Open this link within one hour to choose a new password for %s:\n\n  %s/reset/%s\n", user.Email, strings.TrimRight(*baseURL, "/"), token)
	return nil
}
