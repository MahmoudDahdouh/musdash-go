package main

// nameOverrides are the names the lookups get wrong, by Coolify file name.
var nameOverrides = map[string]string{
	"audiobookshelf": "Audiobookshelf", "changedetection": "changedetection.io", "cloudbeaver": "CloudBeaver",
	"codimd": "CodiMD", "deno-kv": "Deno KV", "drizzle-gateway": "Drizzle Gateway", "electricsql": "ElectricSQL",
	"embystat": "EmbyStat", "emqx-enterprise": "EMQX Enterprise", "foundryvtt": "Foundry VTT", "getoutline": "Outline",
	"hermes-agent-with-webui": "Hermes Agent with a web UI", "labelstudio": "Label Studio", "mediawiki": "MediaWiki",
	"metamcp": "MetaMCP", "mindsdb": "MindsDB", "netbird-client": "NetBird Client", "newt-pangolin": "Newt (Pangolin)",
	"nexus-arm": "Nexus (ARM)", "ollama-with-open-webui": "Ollama with Open WebUI", "once-campfire": "Campfire",
	"orangehrm": "OrangeHRM", "paperless": "Paperless-ngx", "supertokens-with-mysql": "SuperTokens with MySQL",
	"supertokens-with-postgresql": "SuperTokens with PostgreSQL", "superset-with-postgresql": "Superset with PostgreSQL",
	"uptime-kuma-with-mariadb": "Uptime Kuma with MariaDB", "uptime-kuma-with-mysql": "Uptime Kuma with MySQL",
	"wireguard-easy": "WireGuard Easy", "flowise-with-databases": "Flowise with databases",
	"classicpress-without-database": "ClassicPress without a database",
	"wordpress-without-database":    "WordPress without a database",
}

// categoryOverrides are the categories of the templates whose source's
// words map to none, or to the wrong ones, by key.
var categoryOverrides = map[string]string{
	"answer": "communication, support", "blender": "media", "dumbassets": "business", "dumbpad": "docs",
	"geoserver": "devtools", "hi-events": "business", "kaneo": "productivity", "ontime": "productivity",
	"pterodactyl": "games", "pyrodactyl": "games", "reef-dev-cluster": "finance, devtools", "reef-keygen": "finance",
	"trmnl-larapaper": "home", "verdaccio": "devtools",
	// Tagged by what they are built with, or by a word that means
	// something else here.
	"calibre": "media", "drawnix": "docs", "tooljet": "backend, devtools", "libredesk": "support, communication",
	"wanderer": "home", "frappe-hr": "business", "superset": "analytics, database", "chiefonboarding": "business",
	"gitingest": "ai, devtools", "lavalink": "media", "quant-ux": "devtools", "booklore": "media",
	"imgproxy": "media, devtools", "pastefy": "devtools", "navi-music": "media", "cookie-cloud": "devtools",
	"postgresus": "database, storage", "capso": "media", "hortusfox": "home", "neko": "devtools",
	"easyappointments": "business, productivity", "cockpit": "cms, backend", "cryptgeon": "security",
	"ownCloud": "storage", "owncloud": "storage, productivity", "opnform": "business, productivity",
	"mixpost": "business", "statusnook": "monitoring",
}
