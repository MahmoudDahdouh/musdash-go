package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
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
</svg>`), smallLogo)
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
		"big":    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><path d="` + strings.Repeat("M0 0h1v1z", maxLogo/9+1) + `"/></svg>`,
		"nobox":  `<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`,
	} {
		if got := cleanLogo(write(t, dir, name+".svg", svg), maxLogo); got != "" {
			t.Errorf("%s was taken: %s", name, got)
		}
	}
}

// What a logo says for a dark page is left out, and the rest of its style
// is still written as attributes.
func TestCleanLogoWithADarkModeRule(t *testing.T) {
	got := cleanLogo(write(t, t.TempDir(), "a.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">
  <style>.m { fill: #231e1e; } @media (prefers-color-scheme: dark) { .m { fill: #d7d72e; } }</style>
  <rect class="m" width="10" height="10"/>
</svg>`), smallLogo)
	wantAll(t, got, `<rect width="10" height="10" fill="#231e1e"/>`)
	wantNone(t, got, "style", "d7d72e")
}

// Two rules for one class are one attribute, the later rule's.
func TestCleanLogoWithARuleSaidTwice(t *testing.T) {
	got := cleanLogo(write(t, t.TempDir(), "a.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">
  <style>.a{fill:#ead961;stroke:#000}.a{fill:#ecda61}.b{stroke:#111}</style>
  <path class="a b" style="opacity:.5" d="M0 0h10v10z"/>
</svg>`), smallLogo)
	wantAll(t, got, `<path d="M0 0h10v10z" fill="#ecda61" stroke="#111" opacity=".5"/>`)
}

// A logo that would be an empty square on the page is not taken: one drawn
// in white for a dark page, and one a browser cannot read.
func TestCleanLogoRefusesWhatWouldNotBeSeen(t *testing.T) {
	dir := t.TempDir()
	svg := func(inside string) string {
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"` + inside + `</svg>`
	}
	for name, body := range map[string]string{
		"white":      svg(`><path fill="#fff" d="M0 0h10v10z"/><path fill="white" d="M0 0h1v1z"/>`),
		"inherited":  svg(` fill="none"><g fill="#FFFFFF"><path d="M0 0h10v10z"/></g>`),
		"nearly":     svg(`><path fill="rgb(250, 250, 250)" stroke="none" d="M0 0h10v10z"/>`),
		"no hash":    svg(` fill="ffffff"><path d="M0 0h10v10z"/>`),
		"only kept":  svg(`><defs><path id="a" d="M0 0h10v10z"/></defs><path fill="#fff" d="M0 0h1v1z"/>`),
		"no prefix":  svg(`><path its:own="cc" fill="#000" d="M0 0h10v10z"/>`),
		"not closed": svg(`><g><path d="M0 0h10v10z"/>`),
		"twice":      svg(`><path fill="#000" d="M0 0h10v10z" fill="#111"/>`),
		"entity":     svg(`><path style="font-family:&quot;Open Sans&quot;;fill:#000" d="M0 0h10v10z"/>`),
	} {
		if got := cleanLogo(write(t, dir, name+".svg", body), maxLogo); got != "" {
			t.Errorf("%s was taken: %s", name, got)
		}
	}
	for name, body := range map[string]string{
		"black by default": svg(`><path d="M0 0h10v10z"/><path fill="#fff" d="M2 2h1v1z"/>`),
		"a white hole":     svg(`><defs><mask id="m"><rect width="10" height="10" fill="white"/></mask></defs><rect width="10" height="10" fill="#231e1e" mask="url(#m)"/>`),
		"a stroke":         svg(` fill="none"><path stroke="#0969da" d="M0 0h10v10z"/>`),
		"a gradient":       svg(`><path fill="url(#g)" d="M0 0h10v10z"/>`),
		"a short colour":   svg(`><path fill="#08c" d="M0 0h10v10z"/>`),
	} {
		if got := cleanLogo(write(t, dir, name+".svg", body), smallLogo); got == "" {
			t.Errorf("%s was refused", name)
		}
	}
}

// A drawing too large as it stands is written with fewer decimals, and
// only where a number is a coordinate.
func TestSmallerLogo(t *testing.T) {
	for in, want := range map[string]string{
		"M1.23456 2.5.123456":      "M1.23 2.5.12",
		"M1.5.0004 2":              "M1.5 0 2",
		"M10.0001.5":               "M10 .5",
		"M-3.999999-.0004":         "M-3.99-0",
		"M0 0a1 1 0 011.99999.25":  "M0 0a1 1 0 011.99.25",
		"M0 0a1 1 0 011.00001.756": "M0 0a1 1 0 011 .76",
		"M1.126 1.5e-7":            "M1.13 1.5e-7",
		"M4 5.5z":                  "M4 5.5z",
	} {
		if got := shrink(`<path d="`+in+`"/>`, 2); got != `<path d="`+want+`"/>` {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	if got := shrink(`<g transform="scale(0.123456)"><stop offset="0.123456"/><polygon points="0.123456,1"/></g>`, 2); got != `<g transform="scale(0.123456)"><stop offset="0.123456"/><polygon points="0.12,1"/></g>` {
		t.Errorf("more than coordinates were changed: %s", got)
	}
	for svg, want := range map[string]int{
		`<svg viewBox="0 0 24 24">`:                                        3,
		`<svg viewBox="0 0 512 512">`:                                      2,
		`<svg width="2000" height="900">`:                                  1,
		`<svg viewBox="0 0 512 512"><g transform="matrix(10 0 0 10 3 4)">`: 3,
		`<svg viewBox="0 0 512 512"><g transform="scale(.5)">`:             2,
		`<svg>`: 6,
	} {
		if got := places(svg); got != want {
			t.Errorf("%s: %d places, want %d", svg, got, want)
		}
	}

	// 37 bytes a step as written and 21 rounded: over the size as it
	// stands, under it with fewer decimals.
	dir := t.TempDir()
	steps := func(n int) string {
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><path d="` + strings.Repeat("M1.123456 2.123456h1.123456v1.123456z", n) + `"/></svg>`
	}
	for _, limit := range []int{smallLogo, maxLogo} {
		file := write(t, dir, "long.svg", steps(limit/30))
		if got := cleanLogo(file, limit); got != "" {
			t.Fatalf("a drawing of %d bytes was taken as it is under %d", len(got), limit)
		}
		got := smallerLogo(file, limit)
		wantAll(t, got, `M1.12 2.12h1.12v1.12z`)
		if len(got) > limit {
			t.Errorf("%d bytes under %d", len(got), limit)
		}
		if got := smallerLogo(write(t, dir, "huge.svg", steps(limit/20)), limit); got != "" {
			t.Errorf("a drawing that is too large with any decimals was taken under %d: %d bytes", limit, len(got))
		}
	}
}

// Where a logo is looked for, and in which order.
func TestLogoPlaces(t *testing.T) {
	c := collections{selfhst: "/i", coolify: "/c", dashboard: "/d", svgl: "/s", simple: "/none"}
	own := &tmpl{Key: "gitea-sqlite", Name: "Gitea (SQLite)", Source: "dokploy", Logos: []string{"/k/blueprints/gitea-sqlite/logo.svg"}}
	twin := &tmpl{Key: "gitea_sqlite", Source: "coolify", Logos: []string{"/c/public/svgs/gitea-lite.svg"}}
	parent := &tmpl{Key: "gitea", Source: "dokploy", Logos: []string{"/k/blueprints/gitea/logo.svg"}}
	var from []string
	for _, p := range logoPlaces(own, []*tmpl{twin, parent}, c) {
		from = append(from, p.from)
	}
	got := strings.Join(from, " ")
	order := []string{
		"selfhst:gitea-sqlite.svg", "dokploy:logo.svg", "coolify:gitea-lite.svg", "coolify:gitea-sqlite.svg",
		"dashboard:gitea-sqlite.svg", "selfhst:gitea-sqlite-dark.svg", "dashboard:gitea-sqlite-dark.svg",
		"selfhst:gitea.svg", "dokploy:gitea/logo.svg", "coolify:gitea.svg", "dashboard:gitea.svg", "selfhst:gitea-dark.svg",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(got[at:], want)
		if i < 0 {
			t.Fatalf("%s is not after what should come before it in:\n%s", want, strings.ReplaceAll(got, " ", "\n"))
		}
		at += i + len(want)
	}
	if strings.Count(got, "dokploy:logo.svg") != 1 || strings.Contains(got, "svgl:") || strings.Contains(got, "simple:") {
		t.Errorf("places: %s", got)
	}

	// A collection of every kind of brand is asked only for what was
	// picked from it, and Simple Icons only where its colour would show.
	dir := t.TempDir()
	write(t, dir, "data/simple-icons.json", `[{"title":"Foundry Virtual Tabletop","hex":"FE6A1F"},{"title":"Wiki.js","hex":"1976D2"},{"title":"Pale & Co","hex":"FFFFAA"},{"title":"Other","hex":"000000","slug":"another"}]`)
	c.simple = dir
	last := func(key string) logoFile {
		places := logoPlaces(&tmpl{Key: key, Name: key, Source: "coolify"}, nil, c)
		return places[len(places)-1]
	}
	if p := last("foundryvtt"); p.from != "simple:foundryvirtualtabletop.svg" || p.fill != "#FE6A1F" || p.file != filepath.Join(dir, "icons/foundryvirtualtabletop.svg") {
		t.Errorf("foundryvtt: %+v", p)
	}
	if p := last("typesense"); p.from != "svgl:typesense.svg" || p.fill != "" || p.file != "/s/static/library/typesense.svg" {
		t.Errorf("typesense: %+v", p)
	}
	if p := last("something"); strings.HasPrefix(p.from, "simple:") || strings.HasPrefix(p.from, "svgl:") {
		t.Errorf("something: %+v", p)
	}
	for name, want := range map[string]string{"wikidotjs": "#1976D2", "paleandco": "", "another": "#000000", "other": "", "nobody": ""} {
		if got := simpleColour(dir, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// A logo drawn in white is shown on a dark square, as the drawing it is.
func TestOnDark(t *testing.T) {
	dir := t.TempDir()
	white := write(t, dir, "white.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="215" height="32" viewBox="0 0 215 32" fill="none"><path d="M0 0h215v32z" fill="white"/></svg>`)
	got := onDark(white, false, smallLogo)
	wantAll(t, got,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="10" fill="#1f2328"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 215 32" fill="none" x="7" y="7" width="34" height="34"><path d="M0 0h215v32z" fill="white"/></svg></svg>`)
	if why := unseen(strings.TrimSpace(got)); why != "" {
		t.Errorf("the square itself is not seen: %s", why)
	}
	// One that has a size and no viewBox is given the box of its size, or
	// it would not be made to fit.
	sized := write(t, dir, "sized.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="45" height="31"><path d="M0 0h45v31z" fill="#fff"/></svg>`)
	wantAll(t, onDark(sized, false, smallLogo), `viewBox="0 0 45 31" x="7" y="7" width="34" height="34">`)

	// A logo with a colour in it is one for a dark page only where
	// somebody said so, and a file the dashboard cannot show stays one.
	colour := write(t, dir, "colour.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M0 0h10v10z" fill="#fff"/><path d="M0 9h3v1z" fill="#ff6d2d"/></svg>`)
	if got := onDark(colour, false, smallLogo); got != "" {
		t.Errorf("a logo that shows was put on a dark square: %s", got)
	}
	if got := onDark(colour, true, smallLogo); !strings.Contains(got, darkPage) {
		t.Errorf("a logo named as one for a dark page was not: %q", got)
	}
	text := write(t, dir, "text.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><text fill="#fff">a</text></svg>`)
	if got := onDark(text, true, smallLogo); got != "" {
		t.Errorf("a file with text in it was taken: %s", got)
	}
	if got := onDark(white, false, 100); got != "" {
		t.Errorf("larger than it may be: %s", got)
	}
}

// webpSize reads the size of a WebP picture from its first chunk.
func webpSize(t *testing.T, b []byte) (int, int) {
	t.Helper()
	if len(b) < 30 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		t.Fatalf("no WebP file: % x", b[:min(len(b), 16)])
	}
	switch string(b[12:16]) {
	case "VP8X":
		return 1 + int(b[24]) | int(b[25])<<8 | int(b[26])<<16, 1 + int(b[27]) | int(b[28])<<8 | int(b[29])<<16
	case "VP8 ":
		return int(b[26]) | int(b[27]&0x3f)<<8, int(b[28]) | int(b[29]&0x3f)<<8
	case "VP8L":
		bits := uint32(b[21]) | uint32(b[22])<<8 | uint32(b[23])<<16 | uint32(b[24])<<24
		return int(bits&0x3fff) + 1, int(bits>>14&0x3fff) + 1
	}
	t.Fatalf("a WebP file of an unknown kind: %q", b[12:16])
	return 0, 0
}

// The browser makes a picture of the first file that is a logo. It needs
// Chrome or Chromium, as a run of the converter does.
func TestPictures(t *testing.T) {
	browser, err := findBrowser(os.Getenv("CATALOG_CHROME"))
	if err != nil {
		t.Skip(err)
	}
	if testing.Short() {
		t.Skip("starts a browser")
	}
	dir := t.TempDir()
	// A square of one colour in the middle of a transparent picture.
	drawn := func(name string, size int, c color.Color) string {
		img := image.NewNRGBA(image.Rect(0, 0, size, size))
		for y := size / 3; y < 2*size/3; y++ {
			for x := size / 3; x < 2*size/3; x++ {
				img.Set(x, y, c)
			}
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		return write(t, dir, name, b.String())
	}
	blue := drawn("blue.png", 600, color.NRGBA{9, 105, 218, 255})
	white := drawn("white.png", 300, color.NRGBA{255, 255, 255, 255})
	small := drawn("small.png", 150, color.NRGBA{9, 105, 218, 255})
	tiny := drawn("tiny.png", 16, color.NRGBA{9, 105, 218, 255})
	text := write(t, dir, "text.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 50"><rect width="100" height="50" fill="#0969da"/><text x="10" y="35" fill="#fff" font-size="30">Hi</text><script>fetch("/done", {method: "POST", body: "[]"})</script></svg>`)
	pale := drawn("pale.png", 300, color.NRGBA{120, 220, 160, 255})
	// The same square on white paper, and on a ground of its own.
	ground := func(name string, c color.Color) string {
		img := image.NewNRGBA(image.Rect(0, 0, 400, 200))
		for y := 0; y < 200; y++ {
			for x := 0; x < 400; x++ {
				img.Set(x, y, c)
				if x >= 150 && x < 250 && y >= 50 && y < 150 {
					img.Set(x, y, color.NRGBA{9, 105, 218, 255})
				}
			}
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		return write(t, dir, name, b.String())
	}
	paper := ground("paper.png", color.NRGBA{255, 255, 255, 255})
	icon := ground("icon.png", color.NRGBA{20, 20, 20, 255})
	blank := ground("blank.png", color.NRGBA{9, 105, 218, 255})
	logoForDark["test:pale.png"] = true
	defer delete(logoForDark, "test:pale.png")

	made, err := pictures(browser, []wanted{
		{Key: "first", files: []logoFile{{from: "a:gone.png", file: filepath.Join(dir, "gone.png")}, {from: "a:tiny.png", file: tiny}, {from: "a:white.png", file: white}, {from: "a:blue.png", file: blue}, {from: "a:small.png", file: small}}},
		{Key: "white", files: []logoFile{{from: "a:white.png", file: white}, {from: "a:tiny.png", file: tiny}}},
		{Key: "small", files: []logoFile{{from: "a:small.png", file: small}}},
		{Key: "text", files: []logoFile{{from: "a:text.svg", file: text}}},
		{Key: "pale", files: []logoFile{{from: "test:pale.png", file: pale}, {from: "a:blue.png", file: blue}}},
		{Key: "paleonly", files: []logoFile{{from: "test:pale.png", file: pale}}},
		{Key: "paper", files: []logoFile{{from: "a:blank.png", file: blank}, {from: "a:paper.png", file: paper}}},
		{Key: "icon", files: []logoFile{{from: "a:icon.png", file: icon}}},
		{Key: "none", files: []logoFile{{from: "a:tiny.png", file: tiny}, {from: "a:readme.txt", file: write(t, dir, "readme.txt", "x")}}},
		{Key: "nothing", files: []logoFile{{from: "a:gone.png", file: filepath.Join(dir, "gone.png")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]struct {
		from string
		dark bool
		w, h int
	}{
		"first":    {"a:blue.png", false, 96, 96},   // not the one before it that is too small, nor the white one
		"white":    {"a:white.png", true, 96, 96},   // on its dark square
		"small":    {"a:small.png", false, 50, 50},  // cut to what is drawn, and not made larger
		"text":     {"a:text.svg", false, 96, 48},   // a drawing is made the size it is needed in
		"pale":     {"a:blue.png", false, 96, 96},   // a logo named as one for a dark page waits for a better one
		"paleonly": {"test:pale.png", true, 96, 96}, // and is on a dark square where there is none
		"paper":    {"a:paper.png", false, 96, 96},  // white paper is cut off, and a picture of one colour is none
		"icon":     {"a:icon.png", false, 96, 48},   // a ground of its own is the logo's
	} {
		p := made[key]
		if p.From != want.from || p.Dark != want.dark {
			t.Errorf("%s: from %q, dark %v; want %q, %v (%v)", key, p.From, p.Dark, want.from, want.dark, p.Notes)
			continue
		}
		if w, h := webpSize(t, p.WebP); w != want.w || h != want.h {
			t.Errorf("%s: %dx%d, want %dx%d", key, w, h, want.w, want.h)
		}
		if len(p.WebP) > smallLogo {
			t.Errorf("%s: %d bytes", key, len(p.WebP))
		}
	}
	if p := made["first"]; len(p.Notes) != 2 {
		t.Errorf("first: the notes do not say why two files were passed over: %q", p.Notes)
	}
	if p := made["none"]; p.From != "" || len(p.WebP) != 0 || len(p.Notes) != 1 {
		t.Errorf("none: %+v", p)
	}
	if _, ok := made["nothing"]; ok {
		t.Error("a service with no file was sent to the browser")
	}
}

// A card's links come from links.txt where it has the template: Coolify
// names no website, and "-" keeps the one address the source has right.
func TestLinksFromTheList(t *testing.T) {
	dir := t.TempDir()
	list := write(t, dir, "links.txt", "# a comment\n\napp-with-postgresql\thttps://example.com/\t-\nother\t-\thttps://other.example/install\n")
	var err error
	if links, err = readList(list, 3); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { links = nil })

	tpl, err := readCoolify(write(t, dir, "app-with-postgresql.yaml", coolifySample), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := tpl.convert(); err != nil {
		t.Fatal(err)
	}
	if err := tpl.describe(nameBook{}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, string(tpl.render()), "# docs: https://example.com/docs?page=2\n# website: https://example.com/\n")

	// A list is read whole or not at all: a line with a field missing would
	// be a card that kept an address somebody meant to change.
	for name, text := range map[string]string{
		"a field too few": "app\thttps://example.com/\n",
		"an empty field":  "app\t\thttps://example.com/docs\n",
		"a key twice":     "app\t-\thttps://example.com/a\napp\t-\thttps://example.com/b\n",
	} {
		if _, err := readList(write(t, dir, "bad.txt", text), 3); err == nil {
			t.Errorf("%s: the list was read", name)
		}
	}
	if _, err := readList(filepath.Join(dir, "none.txt"), 3); err == nil {
		t.Error("a list that is not there was read as an empty one")
	}
}

// A template written for musdash is read with what its header says and
// then held to what the two catalogues' templates are: its volumes are
// declared, its image gets a tag, and what a stack may not do is refused
// with the reason. Its header is whole or the file is not read.
func TestATemplateWrittenForMusdash(t *testing.T) {
	dir := t.TempDir()
	const header = "# name: The App\n# about: Does a thing\n# docs: https://example.com/docs/docker\n# website: https://example.com/\n# categories: storage, media\n"
	const body = "services:\n  app:\n    image: example/app\n    container_name: app\n    environment:\n      - SERVICE_FQDN_APP_8080\n      - SECRET=${SERVICE_PASSWORD_APP}\n      - TZ=${TZ}\n    volumes:\n      - data:/data\n"
	tpl, err := readWritten(write(t, dir, "the-app.yaml", header+body))
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{tpl.convert, func() error { return tpl.describe(nameBook{"theapp": "Another Name"}) }, tpl.reach} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	got := string(tpl.render())
	wantAll(t, got,
		"# name: The App\n# about: Does a thing.\n# docs: https://example.com/docs/docker\n# website: https://example.com/\n# categories: storage, media\n# source: musdash\n",
		"image: example/app:latest", "- SERVICE_FQDN_APP_8080", "${SERVICE_PASSWORD_APP}", "${TZ:-}", "\nvolumes:\n  data:")
	if strings.Contains(got, "container_name") || tpl.Connect {
		t.Errorf("what only another platform reads is kept, or the template joins its environment:\n%s", got)
	}

	socket, err := readWritten(write(t, dir, "socket.yaml", header+"services:\n  app:\n    image: example/app\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := socket.convert(); err == nil || !strings.Contains(err.Error(), "Docker socket") {
		t.Errorf("a template that mounts the Docker socket: %v", err)
	}

	for name, text := range map[string]string{
		"no website":          strings.Replace(header, "# website: https://example.com/\n", "", 1) + body,
		"an unknown category": strings.Replace(header, "storage, media", "storage, files", 1) + body,
		"a category twice":    strings.Replace(header, "storage, media", "media, media", 1) + body,
		"four categories":     strings.Replace(header, "storage, media", "storage, media, ai, home", 1) + body,
		"another line":        header + "# port: 8080\n" + body,
		"no Compose file":     header,
	} {
		if _, err := readWritten(write(t, dir, "bad.yaml", text)); err == nil {
			t.Errorf("%s: the template was read", name)
		}
	}
}
