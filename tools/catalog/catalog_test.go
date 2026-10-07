package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	file := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func wantAll(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("no %q in:\n%s", w, got)
		}
	}
}

func wantNone(t *testing.T, got string, not ...string) {
	t.Helper()
	for _, n := range not {
		if strings.Contains(got, n) {
			t.Errorf("%q is still in:\n%s", n, got)
		}
	}
}

func TestRewriteRefs(t *testing.T) {
	seen := []string{}
	got := rewriteRefs(`a $ONE ${TWO} ${THREE:-x} ${FOUR:?say} $$KEPT $${KEPT} $(date) ${process.env.PORT||1} 100$`, func(r ref) (string, bool) {
		seen = append(seen, r.Name+r.Op+r.Arg)
		return "<" + r.Name + ">", r.Name == "TWO"
	})
	if want := "ONE TWO THREE:-x FOUR:?say"; strings.Join(seen, " ") != want {
		t.Errorf("references %q, want %q", strings.Join(seen, " "), want)
	}
	// What Compose cannot read is escaped, so that it can read the file.
	if want := `a $ONE <TWO> ${THREE:-x} ${FOUR:?say} $$KEPT $${KEPT} $$(date) $${process.env.PORT||1} 100$$`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := escapeExceptMagic(`pay $5 to ${SERVICE_PASSWORD_DB} or $SERVICE_URL_APP, not ${OTHER}`); got != `pay $$5 to ${SERVICE_PASSWORD_DB} or $SERVICE_URL_APP, not $${OTHER}` {
		t.Errorf("a file's content: %s", got)
	}
}

const coolifySample = `# documentation: https://example.com/docs?utm_source=coolify.io&page=2
# slogan: "An app. It does a thing."
# category: analytics
# tags: tracking, privacy, postgres
# logo: svgs/app.svg
# port: 3000

services:
  app:
    image: example/app
    container_name: app
    ports:
      - "8080:3000"
    environment:
      - SERVICE_URL_APP
      - KEY=${SERVICE_BASE64_64_APP}
      - REAL=${SERVICE_REALBASE64_APP}
      - DB_URL=postgres://app:${SERVICE_PASSWORD_DB}@db/app
      - SMTP_HOST=${SMTP_HOST}
      - MODE=${MODE:-fast}
      - AGAIN=${MODE}
    volumes:
      - app-data:/data
      - ./uploads:/uploads
      - /etc/localtime:/etc/localtime:ro
      - /opt/app/cache:/cache
      - type: bind
        source: ./users.list
        target: /etc/users.list
        content: ""
      - type: bind
        source: ./app.conf
        target: /etc/app.conf
        content: |
          price = $5
          secret = ${SERVICE_PASSWORD_DB}
    labels:
      - traefik.enable=true
      - keep=me
    deploy:
      replicas: 2
      resources:
        limits:
          memory: 1G
        reservations:
          devices:
            - capabilities: [gpu]
    healthcheck:
      test: ["CMD-SHELL", "test $(id -u) = 0"]
  migrate:
    image: example/app:1.2
    exclude_from_hc: true
    restart: "no"
    command: migrate
  db:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: ${SERVICE_PASSWORD_DB}
`

func TestConvertCoolify(t *testing.T) {
	dir := t.TempDir()
	tpl, err := readCoolify(write(t, dir, "app-with-postgresql.yaml", coolifySample), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := tpl.convert(); err != nil {
		t.Fatal(err)
	}
	if err := tpl.describe(nameBook{"app": "The App"}); err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	wantAll(t, got,
		"# name: The App with PostgreSQL\n# about: An app. It does a thing.\n# docs: https://example.com/docs?page=2\n# categories: analytics\n# source: coolify\nservices:\n",
		"image: example/app:latest",
		// The address gets the port the header names.
		"- SERVICE_URL_APP\n", "- SERVICE_FQDN_APP_3000",
		// Coolify's BASE64 is letters and digits; its REALBASE64 is base64.
		"KEY=${SERVICE_PASSWORD_64_APP}", "REAL=${SERVICE_BASE64_APP}",
		// Optional there, optional here; a default given once holds everywhere.
		"SMTP_HOST=${SMTP_HOST:-}", "MODE=${MODE:-fast}", "AGAIN=${MODE:-fast}",
		// Directories become volumes of the stack, every one declared.
		"- app-uploads:/uploads", "- app-cache:/cache", "volumes:\n  app-cache:\n  app-data:\n  app-uploads:\n",
		// A file with content becomes a config; only magic variables stay
		// references in it.
		"configs:\n      - source: app-users-list\n        target: /etc/users.list\n      - source: app-app-conf\n        target: /etc/app.conf", "price = $$5\n", "secret = ${SERVICE_PASSWORD_DB}\n",
		// An empty file is one line end: Compose refuses a config with
		// no content at all.
		"  app-users-list:\n    content: \"\\n\"\n",
		"- keep=me", "memory: 1G",
		// A shell's own dollar sign is escaped for Compose.
		"test $$(id -u) = 0",
		// The job is waited for, so that starting the stack does not read
		// its end as a failure.
		"depends_on:\n      migrate:\n        condition: service_completed_successfully",
	)
	wantNone(t, got, "container_name", "ports:", "8080", "traefik", "exclude_from_hc", "localtime", "replicas", "devices", "utm_source", "content: |\n          price", "/opt/app")
}

func TestConvertRefuses(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct{ body, reason string }{
		"socket":   {"services:\n  a:\n    image: x:1\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n", "Docker socket"},
		"server":   {"services:\n  a:\n    image: x:1\n    volumes:\n      - /etc/passwd:/etc/passwd:ro\n", "directory of the server"},
		"build":    {"services:\n  a:\n    build: .\n", "builds an image"},
		"file":     {"services:\n  a:\n    image: x:1\n    volumes:\n      - ./nginx.conf:/etc/nginx/nginx.conf\n", "does not carry"},
		"anon":     {"services:\n  a:\n    image: x:1\n    environment:\n      - JWT=${SERVICE_SUPABASEANON_KEY}\n", "no equal"},
		"twoports": {"services:\n  a:\n    image: x:1\n    environment:\n      - SERVICE_URL_A_80\n      - SERVICE_FQDN_A_90\n", "two ports"},
		"noport":   {"services:\n  a:\n    image: x:1\n    environment:\n      - SERVICE_URL_A\n", "which port"},
	} {
		tpl, err := readCoolify(write(t, dir, name+".yaml", "# slogan: x\n# documentation: https://x\n"+c.body), dir)
		if err == nil {
			err = tpl.convert()
		}
		if err == nil || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s: %v, want a refusal that says %q", name, err, c.reason)
		}
	}
}

// An address Coolify gives to the service that names it with a port, and
// musdash to the service of that name.
func TestConvertPlacesAddresses(t *testing.T) {
	dir := t.TempDir()
	tpl, err := readCoolify(write(t, dir, "site.yaml", `# slogan: x
# documentation: https://x
services:
  web:
    image: x:1
    environment:
      - SERVICE_URL_SITE_8000
      - ALSO=${SERVICE_FQDN_SITE}
  worker:
    image: x:1
    environment:
      - PUBLIC=${SERVICE_URL_SITE}/api
      - OTHER=${SERVICE_URL_SITEMAP}
  sitemap:
    image: y:1
    expose:
      - "9000"
`), dir)
	if err == nil {
		err = tpl.convert()
	}
	if err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	wantAll(t, got, "- SERVICE_URL_WEB_8000", "ALSO=${SERVICE_FQDN_WEB}", "PUBLIC=${SERVICE_URL_WEB}/api", "OTHER=${SERVICE_URL_SITEMAP}", "SERVICE_FQDN_SITEMAP_9000")
	wantNone(t, got, "SERVICE_URL_SITE_", "SERVICE_URL_SITE}", "SERVICE_FQDN_SITE}")
}

// A template with no web address is reached from its environment, on the
// ports of the server it publishes, or not at all.
func TestTemplatesWithNoAddress(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		header, body string
		want         string // in the file, or in the refusal
		refused      bool
	}{
		// Coolify gives it a domain at the header's port without a variable.
		"cms": {"# category: cms\n# port: 8080\n", "services:\n  web:\n    image: x:1\n", "- SERVICE_FQDN_WEB_8080", false},
		// The first port it published is SSH; the header says where the web is.
		"git": {"# category: git\n# port: 3000\n", "services:\n  web:\n    image: x:1\n    ports:\n      - 2222:22\n      - 3000:3000\n    environment:\n      - SERVICE_URL_WEB\n", "- SERVICE_FQDN_WEB_3000", false},
		// A game is reached on its port, moved out of musdash's own range.
		"game": {"# category: games\n# port: 25565\n", "services:\n  mc:\n    image: x:1\n    ports:\n      - 25565:25565\n", `- "35565:25565"`, false},
		// DNS has no other port than 53, which a stack may not publish.
		"dns":    {"# category: networking\n", "services:\n  dns:\n    image: x:1\n    ports:\n      - 53:53/udp\n", "port of its own", true},
		"cache":  {"# category: database\n", "services:\n  kv:\n    image: x:1\n    ports:\n      - 6379:6379\n", "# connect: true", false},
		"tunnel": {"# category: networking\n", "services:\n  agent:\n    image: x:1\n", "# connect: true", false},
	} {
		tpl, err := readCoolify(write(t, dir, name+".yaml", "# slogan: x\n# documentation: https://x\n"+c.header+c.body), dir)
		if err == nil {
			err = tpl.convert()
		}
		if err == nil {
			err = tpl.describe(nameBook{})
		}
		if err == nil {
			err = tpl.reach()
		}
		got := ""
		if err != nil {
			got = err.Error()
		} else {
			got = string(tpl.render())
		}
		if (err != nil) != c.refused || !strings.Contains(got, c.want) {
			t.Errorf("%s: refused %v, want %v, and %q in:\n%s", name, err != nil, c.refused, c.want, got)
		}
		if name == "git" && strings.Contains(got, "_22") {
			t.Errorf("git: the address goes to SSH:\n%s", got)
		}
	}
}

// The ports a source published are kept where a stack may publish them.
func TestPublishedPorts(t *testing.T) {
	web := map[int]bool{3000: true}
	for in, want := range map[string]string{
		"2222:22":                    "2222:22",
		"22222:22":                   "32222:22",
		"27015:27015/udp":            "37015:27015/udp",
		"1883:1883/tcp":              "1883:1883",
		"5060-5063:5060-5063":        "5060-5063:5060-5063",
		"25565-25590:25565-25590":    "35565-35590:25565-25590",
		"${PORT}:25565":              "35565:25565",
		"$PORT:5672":                 "${PORT:-5672}:5672",
		"${DHT_PORT:-6881}:6881/udp": "${DHT_PORT:-6881}:6881/udp",
		"${GAME:-27015}:7777/udp":    "37015:7777/udp",
		// The proxy's, a port nobody named, the privileged ones, one
		// bound to an address, and what is not a port.
		"3000:3000":           "",
		"8080":                "",
		"7882/udp":            "",
		"25:25":               "",
		"53:53/udp":           "",
		"127.0.0.1:5432:5432": "",
		"19990-20010:1-21":    "",
		"60000-70000:1-10001": "",
		"2222:${SSH}":         "",
		"${PORTS}:5060-5063":  "",
		"1883:1883/sctp":      "",
	} {
		p, ok := readPort(str(in))
		got := ""
		if ok {
			got, _ = p.kept(web)
		}
		if got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	// UDP at the number of the web port is another port.
	if p, _ := readPort(str("3000:3000/udp")); true {
		if got, _ := p.kept(web); got != "3000:3000/udp" {
			t.Errorf("3000/udp: %q", got)
		}
	}

	dir := t.TempDir()
	tpl, err := readCoolify(write(t, dir, "git.yaml", `# slogan: x
# documentation: https://x
# category: git
# port: 3000
services:
  git:
    image: x:1
    environment:
      - SERVICE_URL_GIT_3000
      - DB=${SERVICE_URL_DB_5432}
    ports:
      - 22222:22
      - "3000:3000"
      - target: 9418
        published: "9418"
        protocol: tcp
  db:
    image: y:1
    ports:
      - 5432:5432
      - 22222:2222
  mail:
    image: z:1
    ports:
      - 25:25
      - 4190:4190
`), dir)
	if err == nil {
		err = tpl.convert()
	}
	if err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	// The database's port is where its address goes, and its second port
	// is one of the server that the first service has.
	wantAll(t, got, "    ports:\n      - \"32222:22\"\n      - \"9418:9418\"\n")
	// A mail server without port 25 is not one: it publishes nothing.
	wantNone(t, got, "3000:3000", "22222", "32222:2222", "5432:5432", "4190")
	if tpl.Ports != 2 {
		t.Errorf("%d ports kept, want 2", tpl.Ports)
	}
}

// Coolify gives an address to the service that lists it, whoever else
// reads it; a header can name two ports, and the first is the web's.
func TestAddressBelongsToWhoDeclaresIt(t *testing.T) {
	dir := t.TempDir()
	tpl, err := readCoolify(write(t, dir, "panel.yaml", `# slogan: x
# documentation: https://x
# port: 80, 2112
services:
  panel-web:
    image: x:1
    environment:
      - SERVICE_URL_PANEL
      - API=${SERVICE_URL_PAPI_4000}
  panel-api:
    image: x:1
    environment:
      - SERVICE_URL_PAPI_4000
      - WEB=${SERVICE_URL_PANEL}
`), dir)
	if err == nil {
		err = tpl.convert()
	}
	if err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	wantAll(t, got, "- SERVICE_URL_PANEL_WEB\n", "- SERVICE_FQDN_PANEL_WEB_80\n", "API=${SERVICE_URL_PANEL_API_4000}", "- SERVICE_URL_PANEL_API_4000\n", "WEB=${SERVICE_URL_PANEL_WEB}")
	wantNone(t, got, "PAPI", "SERVICE_URL_PANEL}", "2112")
}

// A link that gives a service a second name keeps the name; a job is waited
// for by the service people reach, not by the database it needs itself.
func TestConvertLinksAndJobs(t *testing.T) {
	dir := t.TempDir()
	tpl, err := readCoolify(write(t, dir, "stack.yaml", `# slogan: x
# documentation: https://x
services:
  db:
    image: postgres:16
  proxy:
    image: nginx:1
    links:
      - backend:api
      - db
    environment:
      - SERVICE_URL_PROXY_80
  backend:
    image: x:1
    networks:
      - inner
  migrate:
    image: x:1
    restart: "no"
    exclude_from_hc: true
networks:
  inner:
`), dir)
	if err == nil {
		err = tpl.convert()
	}
	if err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	wantAll(t, got,
		"  backend:\n    image: x:1\n    networks:\n      inner:\n        aliases:\n          - api\n",
		"  proxy:\n    image: nginx:1\n    environment:\n      - SERVICE_URL_PROXY_80\n    depends_on:\n      migrate:\n        condition: service_completed_successfully\n",
	)
	wantNone(t, got, "links", "  db:\n    image: postgres:16\n    depends_on")
}

func TestConvertDokploy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	write(t, dir, "meta.json", `{"id":"notes","name":"Notes","description":"Keeps the notes of a whole team in one place. And more, said at a length that no card of the catalogue could hold without growing to twice the height of the cards beside it in the row.","logo":"logo.png","links":{"github":"https://github.com/x/notes","website":"http://notes.example","docs":""},"tags":["notes","self-hosted","postgres"]}`)
	write(t, dir, "template.toml", `[variables]
main_domain = "${domain}"
db_pass = "${password:32}"
secret = "${base64:64}"
username = "${username}"
store_secret = "written-out"
root_admin_password = "Sup3rSecret!"
price = "$10 each"

[config]
mounts = []
env = [
  "APP_URL=https://${main_domain}",
  "DB_PASS=${db_pass}",
  "LOG_LEVEL=info",
  "ADMIN=${username}",
  "TOKEN=${hash:16}",
  "S3_KEY=${store_secret}",
  "FIRST_USER=${root_admin_password}",
  "S3_AGAIN=${store_secret}",
  "PRICE=${price}",
]

[[config.domains]]
serviceName = "web"
port = 3_000
host = "${main_domain}"

[[config.mounts]]
filePath = "conf/app.toml"
content = """
url = "https://${main_domain}"
key = "${secret}"
cost = $5
"""
`)
	write(t, dir, "docker-compose.yml", `version: "3.8"
services:
  web:
    image: example/notes:2
    restart: always
    ports:
      - 3000
    env_file: .env
    environment:
      DATABASE_URL: postgres://notes:${DB_PASS}@db:5432/notes
      LEVEL: ${LOG_LEVEL}
      REDIS_PASSWORD: sameeverywhere
      REDIS_URL: redis://:sameeverywhere@cache:6379
      UNSET: ${NOT_IN_ENV}
    volumes:
      - ../files/conf/app.toml:/app/app.toml:ro
      - ../files/data:/data
    networks:
      - dokploy-network
  db:
    image: postgres:16
    environment:
      POSTGRES_USER: notes
      POSTGRES_PASSWORD: ${DB_PASS}
  cache:
    image: redis:7
    command: redis-server --requirepass sameeverywhere
  search:
    image: example/search:1
    environment:
      MASTER_PASSWORD: onlyhere
      UI_ADMIN_PASSWORD: changeme
      PANEL_PASS: ${PANEL_PASS:-letmein}
      SMTP_PASSWORD: ${SMTP_PASSWORD:-yours}
      SMTP_PASSWORD_AGAIN_PASS: ${SMTP_PASSWORD:-yours}
      FIRST_USER: ${FIRST_USER}
      NOTE: costs $5
networks:
  dokploy-network:
    external: true
`)
	tpl, err := readDokploy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := tpl.convert(); err != nil {
		t.Fatal(err)
	}
	if err := tpl.describe(nameBook{}); err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	wantAll(t, got,
		"# name: Notes\n# about: Keeps the notes of a whole team in one place.\n# website: https://notes.example\n# categories: docs\n# source: dokploy\nservices:\n",
		// The domain is the service's endpoint, and written with its
		// scheme it follows the endpoint's.
		"SERVICE_FQDN_WEB_3000:", "APP_URL: ${SERVICE_URL_WEB}",
		// Dokploy's generated values are musdash's.
		"DATABASE_URL: postgres://notes:${SERVICE_PASSWORD_DB_PASS}@db:5432/notes", "POSTGRES_PASSWORD: ${SERVICE_PASSWORD_DB_PASS}",
		"ADMIN: ${SERVICE_USER_USERNAME}", "TOKEN: ${SERVICE_HEX_16_TOKEN}",
		// A fixed value of the .env file stays a variable a person can set.
		"LEVEL: ${LOG_LEVEL:-info}", "LOG_LEVEL: info", "UNSET: ${NOT_IN_ENV:-}",
		// The file Dokploy writes is a config, with its variables filled
		// in and the rest of it as written.
		"- source: web-conf-app-toml\n        target: /app/app.toml", `url = "${SERVICE_URL_WEB}"`, `key = "${SERVICE_BASE64_64_SECRET}"`, "cost = $$5",
		"- web-data:/data",
		// A password written out is generated where the file shows every
		// place it is used, and left alone where it shows one.
		"REDIS_PASSWORD: ${SERVICE_PASSWORD_REDIS}", "redis://:${SERVICE_PASSWORD_REDIS}@cache:6379", "--requirepass ${SERVICE_PASSWORD_REDIS}",
		"MASTER_PASSWORD: onlyhere",
		// The app's own sign-in would be the same on every install: it is
		// asked for. One the blueprint hands to both ends through a
		// variable of its own is generated.
		"UI_ADMIN_PASSWORD: ${UI_ADMIN_PASSWORD:?Set a password to sign in with}", "PANEL_PASS: ${PANEL_PASS:?Set a password to sign in with}",
		"FIRST_USER: ${ROOT_ADMIN_PASSWORD:?Set a password to sign in with}", "S3_KEY: ${SERVICE_PASSWORD_STORE_SECRET}", "S3_AGAIN: ${SERVICE_PASSWORD_STORE_SECRET}",
		// A variable with a default is the person's to set, twice or not.
		"SMTP_PASSWORD: ${SMTP_PASSWORD:-yours}", "SMTP_PASSWORD_AGAIN_PASS: ${SMTP_PASSWORD:-yours}",
		// A dollar sign that is a value's own is escaped for Compose.
		"NOTE: costs $$5", "PRICE: $$10 each",
		// Dokploy's proxy network is a network of the stack.
		"networks:\n  dokploy-network:\n",
	)
	wantNone(t, got, "version", "env_file", "ports:", "sameeverywhere", "changeme", "letmein", "Sup3rSecret", "written-out", "external", "../files", "${domain}", "${main_domain}")
}

// A blueprint that ships a password as a hash ships a sign-in everyone
// knows: there is no generated value to put in its place.
func TestDokployRefusesAShippedHash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "portal")
	write(t, dir, "meta.json", `{"id":"portal","name":"Portal","description":"A portal.","links":{"website":"https://portal.example"},"tags":["auth"]}`)
	write(t, dir, "template.toml", "[variables]\nadmin_hash = \"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA\"\n[config]\nenv = [\"HASH=${admin_hash}\"]\n")
	write(t, dir, "docker-compose.yml", "services:\n  web:\n    image: example/portal:1\n    environment:\n      HASH: ${HASH}\n")
	if _, err := readDokploy(dir); err == nil || !strings.Contains(err.Error(), "fixed password") {
		t.Fatalf("%v, want a refusal", err)
	}
}

// A password its makers wrote into the app's environment and into the
// script that makes the database's user has both its ends in the file: it
// is generated. One that also stands in the file as a word of another
// meaning is left, all of it.
func TestPasswordInACarriedFile(t *testing.T) {
	for name, c := range map[string]struct {
		script string
		want   []string
		not    []string
	}{
		"both ends": {`db.createUser({user: "app", pwd: "app_password"})`,
			[]string{"MONGO_PASS=${SERVICE_PASSWORD_MONGO}", `pwd: "${SERVICE_PASSWORD_MONGO}"`}, []string{"app_password"}},
		"another word": {`db.createUser({user: "app", pwd: "app_password"}); print("app_password is set")`,
			[]string{"MONGO_PASS=app_password", `pwd: "app_password"`}, []string{"SERVICE_PASSWORD"}},
	} {
		dir := t.TempDir()
		tpl, err := readCoolify(write(t, dir, name+".yaml", `# slogan: x
# documentation: https://x
services:
  app:
    image: x:1
    environment:
      - SERVICE_URL_APP_8080
      - MONGO_PASS=app_password
  db:
    image: mongo:7
    volumes:
      - type: bind
        source: ./init.js
        target: /docker-entrypoint-initdb.d/init.js
        content: '`+c.script+`'
`), dir)
		if err == nil {
			err = tpl.convert()
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := string(tpl.render())
		wantAll(t, got, c.want...)
		wantNone(t, got, c.not...)
	}
}

// A blueprint whose Compose line reads a variable that nothing sets, where
// its .env file sets the line's own name, means the value it made. Left as
// written the app would get an empty password, which is its default one.
func TestDokployEnvironmentLineWithAnUnsetVariable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pics")
	write(t, dir, "meta.json", `{"id":"pics","name":"Pics","description":"Pictures.","links":{"website":"https://pics.example"},"tags":["media"]}`)
	write(t, dir, "template.toml", `[variables]
main_domain = "${domain}"
admin_password = "${password:32}"
jwt_secret = "${jwt:jwt_secret}"

[config]
mounts = []
[[config.domains]]
serviceName = "pics"
port = 8080
host = "${main_domain}"

[config.env]
"PICS_ADMIN_PASSWORD" = "${admin_password}"
"PICS_JWT_SECRET" = "${jwt_secret}"
"PICS_MODE" = "fast"
`)
	// And a variable of the blueprint read in the Compose file directly.
	write(t, dir, "docker-compose.yml", `services:
  pics:
    image: example/pics:1
    environment:
      PICS_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      PICS_JWT_SECRET: "${JWT_SECRET}"
      PICS_MODE: ${MODE:-slow}
      PICS_OTHER: ${OTHER}
      SESSION_KEY: ${jwt_secret}
  worker:
    image: example/pics:1
    environment:
      - PICS_ADMIN_PASSWORD=$ADMIN_PASSWORD
`)
	tpl, err := readDokploy(dir)
	if err == nil {
		err = tpl.convert()
	}
	if err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	wantAll(t, got, "PICS_ADMIN_PASSWORD: ${SERVICE_PASSWORD_ADMIN_PASSWORD}", "PICS_JWT_SECRET: ${SERVICE_PASSWORD_64_JWT_SECRET}",
		"- PICS_ADMIN_PASSWORD=${SERVICE_PASSWORD_ADMIN_PASSWORD}",
		// A line with a default of its own, and one the .env file does
		// not name, are the Compose file's.
		"PICS_MODE: ${MODE:-slow}", "PICS_OTHER: ${OTHER:-}", "SESSION_KEY: ${SERVICE_PASSWORD_64_JWT_SECRET}")
	wantNone(t, got, "${ADMIN_PASSWORD", "${JWT_SECRET")
}

// A JWT secret is a key; and beside ports of the server, a host name that
// no service is routed to is the server's own.
func TestDokployHelpersOfAServiceWithPorts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "relay")
	write(t, dir, "meta.json", `{"id":"relay","name":"Relay","description":"Relays.","links":{"website":"https://relay.example"},"tags":["networking"]}`)
	write(t, dir, "template.toml", "[variables]\nserver_domain = \"${domain}\"\njwt_secret = \"${jwt:32}\"\n[config]\nenv = [\"RELAY=${server_domain}:21117\", \"JWT_SECRET=${jwt_secret}\"]\n")
	write(t, dir, "docker-compose.yml", "services:\n  relay:\n    image: example/relay:1\n    environment:\n      RELAY: ${RELAY}\n      JWT_SECRET: ${JWT_SECRET}\n    ports:\n      - \"21117:21117\"\n")
	tpl, err := readDokploy(dir)
	if err == nil {
		err = tpl.convert()
	}
	if err != nil {
		t.Fatal(err)
	}
	got := string(tpl.render())
	wantAll(t, got, "RELAY: ${SERVER_DOMAIN:?The name or address this server is reached at}:21117", "JWT_SECRET: ${SERVICE_PASSWORD_64_JWT_SECRET}", `- "31117:21117"`)

	// A token is not a secret, and without ports the name is nobody's.
	for name, toml := range map[string]string{
		"token": "[variables]\napi_token = \"${jwt}\"\n[config]\nenv = [\"TOKEN=${api_token}\"]\n",
		"name":  "[variables]\nother_domain = \"${domain}\"\n[config]\nenv = [\"TOKEN=${other_domain}\"]\n",
	} {
		dir := filepath.Join(t.TempDir(), name)
		write(t, dir, "meta.json", `{"id":"x","name":"X","description":"X.","links":{"website":"https://x.example"},"tags":[]}`)
		write(t, dir, "template.toml", toml)
		write(t, dir, "docker-compose.yml", "services:\n  web:\n    image: example/x:1\n    environment:\n      TOKEN: ${TOKEN}\n")
		if _, err := readDokploy(dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCleanLogo(t *testing.T) {
	dir := t.TempDir()
	good := cleanLogo(write(t, dir, "a.svg", `<?xml version="1.0"?>
<!-- made with a tool -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10">
  <title>App</title>
  <style>.a{fill:#f00;stroke:none}</style>
  <path class="a" d="M0 0h10v10z"/>
  <circle style="fill:#00f;opacity:.5" fill="#fff" r="2"/>
</svg>`))
	wantAll(t, good, `<path d="M0 0h10v10z" fill="#f00" stroke="none"/>`, `<circle r="2" fill="#00f" opacity=".5"/>`)
	wantNone(t, good, "style", "class", "title", "xlink", "<!--", "<?xml")
	for name, svg := range map[string]string{
		"link":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><use href="#a"/></svg>`,
		"image":  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><image width="1" height="1"/></svg>`,
		"script": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><script>alert(1)</script></svg>`,
		"css":    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><style>@media print{.a{fill:red}}</style><path class="a"/></svg>`,
		"prop":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><path style="background:url(x)"/></svg>`,
		"event":  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1" onload="alert(1)"><path d="M0 0"/></svg>`,
		"remote": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><path fill="url(//example.com/a.svg#b)" d="M0 0"/></svg>`,
		"big":    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><path d="` + strings.Repeat("M0 0h1v1z", 900) + `"/></svg>`,
		"nobox":  `<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`,
	} {
		if got := cleanLogo(write(t, dir, name+".svg", svg)); got != "" {
			t.Errorf("%s was taken: %s", name, got)
		}
	}
}
