// Package catalog holds what musdash knows how to run out of the box: the
// database engines and the one-click services.
package catalog

import (
	"net/url"
	"strconv"
	"strings"
)

// DBTemplate describes how to run one database engine.
type DBTemplate struct {
	Engine string // "postgres"
	Label  string // "PostgreSQL"
	About  string // one line for the engine picker
	Image  string // default image; a person may choose another tag
	Port   int    // the port clients connect to
	// Env is the container's environment. Values may use {{.User}},
	// {{.Pass}} and {{.DB}}.
	Env map[string]string
	// Command replaces the image's command. Engines without a password
	// variable read it from their own environment here.
	Command []string
	// VolumePath is where the engine keeps its data inside the container.
	VolumePath string
	// OtherVolumePaths are where other versions of the image keep it. The
	// path an image itself declares as a volume is the one that is used.
	OtherVolumePaths []string
	// HealthCmd runs inside the container and exits 0 once the engine
	// accepts connections from other containers. Empty means the image's
	// own health check is used.
	//
	// Most images first run a private server to set up users and databases
	// and then restart it. A health command must not pass against that
	// first server, so each one asks over the network interface the first
	// server does not listen on, not over a socket or the loopback address.
	HealthCmd []string
	// URLFormat is the connection string, with {{.Host}} and {{.Port}} as
	// well as the credentials.
	URLFormat string
	// DumpCmd writes a backup to standard output; RestoreCmd reads one from
	// standard input. Both run inside the container through its shell.
	DumpCmd    string
	RestoreCmd string

	DefaultUser string
	DefaultDB   string // "" for engines without named databases
}

// Creds are a database's generated credentials.
type Creds struct {
	User string
	Pass string
	DB   string
}

func (c Creds) replacer(extra ...string) *strings.Replacer {
	pairs := append([]string{"{{.User}}", c.User, "{{.Pass}}", c.Pass, "{{.DB}}", c.DB}, extra...)
	return strings.NewReplacer(pairs...)
}

// RenderEnv fills the credentials into the template's environment.
func (t DBTemplate) RenderEnv(c Creds) map[string]string {
	r := c.replacer()
	out := make(map[string]string, len(t.Env))
	for k, v := range t.Env {
		out[k] = r.Replace(v)
	}
	return out
}

// RenderHealth fills the credentials into the health command.
func (t DBTemplate) RenderHealth(c Creds) []string {
	r := c.replacer()
	out := make([]string, len(t.HealthCmd))
	for i, arg := range t.HealthCmd {
		out[i] = r.Replace(arg)
	}
	return out
}

// URL builds a connection string. The user and password are escaped for use
// in a URL; generated credentials need none, but a person may one day be
// able to choose their own.
func (t DBTemplate) URL(c Creds, host string, port int) string {
	return strings.NewReplacer(
		// Escaped by the rules for the user-information part of a URL, where
		// "@", ":" and "/" must not appear raw.
		"{{.User}}", url.User(c.User).String(),
		"{{.Pass}}", strings.TrimPrefix(url.UserPassword("", c.Pass).String(), ":"),
		"{{.DB}}", url.PathEscape(c.DB),
		"{{.Host}}", host,
		"{{.Port}}", strconv.Itoa(port),
	).Replace(t.URLFormat)
}

// The Redis-compatible engines take their password as a server setting, not
// from a variable. The container's own shell writes it into a private
// configuration file, so it is in no argument list: not docker's, and not
// the server's own inside the container. The image's entry point is then
// run as usual, which drops from root to the engine's own user. The clients
// read the password from REDISCLI_AUTH.
const (
	redisCommand = `umask 077 && printf 'requirepass %s\nappendonly yes\n' "$REDIS_PASSWORD" > /tmp/musdash.conf && chown redis /tmp/musdash.conf && exec docker-entrypoint.sh redis-server /tmp/musdash.conf`
	keydbCommand = `umask 077 && printf 'requirepass %s\nappendonly yes\n' "$REDIS_PASSWORD" > /tmp/musdash.conf && chown keydb /tmp/musdash.conf && exec docker-entrypoint.sh keydb-server /tmp/musdash.conf`
	redisURL     = "redis://default:{{.Pass}}@{{.Host}}:{{.Port}}/0"
	// mongoWithPassword writes the root password to a file only the
	// container's user can read and leaves its path in $f. printf is the
	// shell's own, so the password is in no process's arguments.
	mongoWithPassword = `umask 077; f=$(mktemp) || exit 1; printf 'password: "%s"\n' "$MONGO_INITDB_ROOT_PASSWORD" > "$f"; `
)

var databases = []DBTemplate{
	{
		Engine: "postgres", Label: "PostgreSQL", About: "The general-purpose relational database.",
		Image: "postgres:17-alpine", Port: 5432,
		Env:        map[string]string{"POSTGRES_USER": "{{.User}}", "POSTGRES_PASSWORD": "{{.Pass}}", "POSTGRES_DB": "{{.DB}}"},
		VolumePath: "/var/lib/postgresql/data",
		// From version 18 the image keeps each major version's data in its
		// own directory below this one.
		OtherVolumePaths: []string{"/var/lib/postgresql"},
		HealthCmd:        []string{"pg_isready", "-h", "127.0.0.1", "-U", "{{.User}}", "-d", "{{.DB}}"},
		URLFormat:        "postgres://{{.User}}:{{.Pass}}@{{.Host}}:{{.Port}}/{{.DB}}",
		DumpCmd:          `pg_dump -U "$POSTGRES_USER" -Fc "$POSTGRES_DB"`,
		RestoreCmd:       `pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --no-owner`,
		DefaultUser:      "postgres", DefaultDB: "postgres",
	},
	{
		Engine: "mysql", Label: "MySQL", About: "The widely used relational database.",
		Image: "mysql:8.4", Port: 3306,
		// The administrator shares the generated password but can only log
		// in from inside the container, which is how backups run. What an
		// app is given is the ordinary user of the one database.
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "{{.Pass}}", "MYSQL_ROOT_HOST": "localhost",
			"MYSQL_USER": "{{.User}}", "MYSQL_PASSWORD": "{{.Pass}}", "MYSQL_DATABASE": "{{.DB}}",
		},
		VolumePath: "/var/lib/mysql",
		// Answers 0 as soon as the server replies at all, also with "access
		// denied", so it needs no password.
		HealthCmd:   []string{"mysqladmin", "ping", "-h", "127.0.0.1", "--silent"},
		URLFormat:   "mysql://{{.User}}:{{.Pass}}@{{.Host}}:{{.Port}}/{{.DB}}",
		DumpCmd:     `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -u root --single-transaction --routines --databases "$MYSQL_DATABASE"`,
		RestoreCmd:  `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -u root`,
		DefaultUser: "app", DefaultDB: "app",
	},
	{
		Engine: "mariadb", Label: "MariaDB", About: "A community-developed fork of MySQL.",
		Image: "mariadb:11", Port: 3306,
		Env: map[string]string{
			"MARIADB_ROOT_PASSWORD": "{{.Pass}}", "MARIADB_ROOT_HOST": "localhost",
			"MARIADB_USER": "{{.User}}", "MARIADB_PASSWORD": "{{.Pass}}", "MARIADB_DATABASE": "{{.DB}}",
		},
		VolumePath:  "/var/lib/mysql",
		HealthCmd:   []string{"healthcheck.sh", "--connect", "--innodb_initialized"},
		URLFormat:   "mysql://{{.User}}:{{.Pass}}@{{.Host}}:{{.Port}}/{{.DB}}",
		DumpCmd:     `MYSQL_PWD="$MARIADB_ROOT_PASSWORD" mariadb-dump -u root --single-transaction --routines --databases "$MARIADB_DATABASE"`,
		RestoreCmd:  `MYSQL_PWD="$MARIADB_ROOT_PASSWORD" mariadb -u root`,
		DefaultUser: "app", DefaultDB: "app",
	},
	{
		Engine: "mongodb", Label: "MongoDB", About: "A document database.",
		Image: "mongo:8", Port: 27017,
		Env:        map[string]string{"MONGO_INITDB_ROOT_USERNAME": "{{.User}}", "MONGO_INITDB_ROOT_PASSWORD": "{{.Pass}}"},
		VolumePath: "/data/db",
		HealthCmd:  []string{"sh", "-c", `mongosh --quiet --host "$(hostname)" --eval 'db.adminCommand({ping:1}).ok' | grep -q 1`},
		URLFormat:  "mongodb://{{.User}}:{{.Pass}}@{{.Host}}:{{.Port}}/?authSource=admin",
		// The password goes through a private file: an argument would be
		// readable in the container's process list.
		DumpCmd:     mongoWithPassword + `mongodump --config "$f" --username "$MONGO_INITDB_ROOT_USERNAME" --authenticationDatabase admin --archive; s=$?; rm -f "$f"; exit $s`,
		RestoreCmd:  mongoWithPassword + `mongorestore --config "$f" --username "$MONGO_INITDB_ROOT_USERNAME" --authenticationDatabase admin --archive --drop; s=$?; rm -f "$f"; exit $s`,
		DefaultUser: "root",
	},
	{
		Engine: "redis", Label: "Redis", About: "An in-memory key-value store, saved to disk.",
		Image: "redis:7-alpine", Port: 6379,
		Env:         map[string]string{"REDIS_PASSWORD": "{{.Pass}}", "REDISCLI_AUTH": "{{.Pass}}"},
		Command:     []string{"sh", "-c", redisCommand},
		VolumePath:  "/data",
		HealthCmd:   []string{"sh", "-c", `redis-cli -h "$(hostname)" ping | grep -q PONG`},
		URLFormat:   redisURL,
		DumpCmd:     `redis-cli --rdb /tmp/musdash.rdb >/dev/null && cat /tmp/musdash.rdb && rm -f /tmp/musdash.rdb`,
		DefaultUser: "default",
	},
	{
		Engine: "keydb", Label: "KeyDB", About: "A multithreaded, Redis-compatible store.",
		Image: "eqalpha/keydb:latest", Port: 6379,
		Env:         map[string]string{"REDIS_PASSWORD": "{{.Pass}}", "REDISCLI_AUTH": "{{.Pass}}"},
		Command:     []string{"sh", "-c", keydbCommand},
		VolumePath:  "/data",
		HealthCmd:   []string{"sh", "-c", `keydb-cli -h "$(hostname)" ping | grep -q PONG`},
		URLFormat:   redisURL,
		DefaultUser: "default",
	},
	{
		Engine: "dragonfly", Label: "Dragonfly", About: "A fast, Redis-compatible in-memory store.",
		Image: "docker.dragonflydb.io/dragonflydb/dragonfly:latest", Port: 6379,
		// Dragonfly reads any flag from an environment variable named
		// DFLY_<flag>.
		Env:         map[string]string{"DFLY_requirepass": "{{.Pass}}", "DFLY_dir": "/data"},
		VolumePath:  "/data",
		URLFormat:   redisURL,
		DefaultUser: "default",
	},
	{
		Engine: "clickhouse", Label: "ClickHouse", About: "A column store for analytics.",
		Image: "clickhouse/clickhouse-server:latest-alpine", Port: 9000,
		Env: map[string]string{
			"CLICKHOUSE_USER": "{{.User}}", "CLICKHOUSE_PASSWORD": "{{.Pass}}", "CLICKHOUSE_DB": "{{.DB}}",
			"CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1",
		},
		VolumePath:  "/var/lib/clickhouse",
		HealthCmd:   []string{"sh", "-c", `wget -q -O- "http://$(hostname):8123/ping" | grep -q Ok`},
		URLFormat:   "clickhouse://{{.User}}:{{.Pass}}@{{.Host}}:{{.Port}}/{{.DB}}",
		DefaultUser: "app", DefaultDB: "app",
	},
}

// Databases returns the engine templates in display order.
func Databases() []DBTemplate { return databases }

// Database finds a template by engine name.
func Database(engine string) (DBTemplate, bool) {
	for _, t := range databases {
		if t.Engine == engine {
			return t, true
		}
	}
	return DBTemplate{}, false
}
