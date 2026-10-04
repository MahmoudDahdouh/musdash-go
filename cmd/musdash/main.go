// Command musdash is a self-hosted platform for deploying apps, databases
// and services with Docker. One binary provides the control plane (`server`)
// and the edge proxy (`proxy`).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/MahmoudDahdouh/musdash-go/internal/config"
	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/migrations"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

const usage = `musdash — deploy apps, databases and services on your own servers

Usage:
  musdash <command> [flags]

Commands:
  migrate          Apply database migrations and exit
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
	case "migrate":
		err = runMigrate(args)
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
