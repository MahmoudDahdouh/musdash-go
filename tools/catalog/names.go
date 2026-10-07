package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Coolify's templates have no name of their own: its catalogue shows the
// file's name. A name is looked up where the service's makers' own spelling
// is likely to be (the icon collection's index, Dokploy's catalogue) before
// the file's name is put in capitals.

var norm = func(s string) string { return regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(strings.ToLower(s), "") }

// same is norm for telling whether the two catalogues have one service
// under two file names: "gitea-with-postgresql" and "gitea-postgres".
func same(key string) string {
	key = strings.ReplaceAll(strings.ToLower(key), "_", "-")
	key = strings.ReplaceAll(key, "-with-", "-")
	parts := strings.Split(key, "-")
	for i, p := range parts {
		if p == "postgres" || p == "pg" {
			parts[i] = "postgresql"
		}
	}
	return norm(strings.Join(parts, ""))
}

type nameBook map[string]string

func readNames(icons string, dokployNames map[string]string) nameBook {
	book := nameBook{}
	var index []struct {
		Name      string `json:"Name"`
		Reference string `json:"Reference"`
	}
	if raw, err := os.ReadFile(filepath.Join(icons, "index.json")); err == nil && json.Unmarshal(raw, &index) == nil {
		for _, e := range index {
			book[norm(e.Reference)] = e.Name
		}
	}
	for k, v := range dokployNames {
		if _, ok := book[k]; !ok {
			book[k] = v
		}
	}
	return book
}

// with are the companions a Coolify file's name ends in: "gitea-with-mysql".
var with = map[string]string{
	"postgresql": "PostgreSQL", "postgres": "PostgreSQL", "mysql": "MySQL", "mariadb": "MariaDB", "mongodb": "MongoDB",
	"sqlite": "SQLite", "redis": "Redis", "clickhouse": "ClickHouse", "elasticsearch": "Elasticsearch",
	"meilisearch": "Meilisearch", "typesense": "Typesense", "minio": "MinIO", "s3": "S3", "ollama": "Ollama",
	"openai": "OpenAI", "worker": "a worker", "workers": "workers", "postgis": "PostGIS", "tika": "Tika",
	"browserless": "Browserless", "chrome": "Chrome", "libreoffice": "LibreOffice", "onlyoffice": "ONLYOFFICE",
	"collabora": "Collabora", "keydb": "KeyDB", "dragonfly": "Dragonfly", "valkey": "Valkey", "timescaledb": "TimescaleDB",
	"pgvector": "pgvector", "litellm": "LiteLLM", "qdrant": "Qdrant", "weaviate": "Weaviate", "docker": "Docker",
	"proxy": "a proxy", "tailscale": "Tailscale", "cloudflared": "Cloudflare Tunnel", "wireguard": "WireGuard",
	"gluetun": "Gluetun", "vpn": "a VPN", "ssl": "SSL", "gpu": "a GPU", "cpu": "CPU only", "legacy": "legacy",
	"external": "an external", "database": "database", "db": "database", "and": "and", "runner": "a runner",
	"plugins": "plugins", "local": "local", "storage": "storage", "auth": "sign-in", "smtp": "SMTP",
}

func titleWords(s string) string {
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(s))
	for i, w := range words {
		if len(w) <= 3 && regexp.MustCompile(`^[a-z]+[0-9]*$`).MatchString(w) && i > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// coolifyName makes a display name from a Coolify file's name.
func (b nameBook) coolifyName(key string) string {
	if n, ok := nameOverrides[key]; ok {
		return n
	}
	if base, ok := strings.CutSuffix(key, "-without-database"); ok {
		return b.coolifyName(base) + " without a database"
	}
	base, variant, hasVariant := strings.Cut(key, "-with-")
	name := b[norm(base)]
	if name == "" {
		name = titleWords(base)
	}
	if !hasVariant {
		return name
	}
	parts := strings.Split(variant, "-")
	for i, p := range parts {
		if w, ok := with[p]; ok {
			parts[i] = w
		} else if n := b[norm(p)]; n != "" {
			parts[i] = n
		} else {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return name + " with " + strings.Join(parts, " ")
}

// about makes the catalogue's line from a source's description: one or two
// sentences, on one line, of a length a card can hold.
func about(s string) string {
	s = strings.Join(strings.Fields(strings.Trim(strings.TrimSpace(s), `"'`)), " ")
	const most = 150
	if len(s) > most {
		// The first sentence, when there is one that ends early enough.
		if m := regexp.MustCompile(`^(.{30,` + "150" + `}?[.!?])(\s|$)`).FindStringSubmatch(s); m != nil {
			s = m[1]
		} else {
			cut := strings.LastIndexByte(s[:most], ' ')
			if cut < 0 {
				cut = most
			}
			s = strings.TrimRight(strings.ToValidUTF8(s[:cut], ""), ",;:-–—") + "…"
		}
	}
	if s != "" && !strings.ContainsAny(s[len(s)-1:], ".!?…") && !strings.HasSuffix(s, "…") {
		s += "."
	}
	return s
}
