package main

import (
	"strings"
)

// musdash's categories, as internal/catalog lists them.
var categoryKeys = map[string]bool{
	"ai": true, "analytics": true, "auth": true, "automation": true, "backend": true, "business": true, "cms": true,
	"communication": true, "database": true, "devtools": true, "docs": true, "ecommerce": true, "email": true,
	"finance": true, "games": true, "git": true, "home": true, "media": true, "monitoring": true, "networking": true,
	"productivity": true, "rss": true, "search": true, "security": true, "storage": true, "support": true,
}

// words maps what the two catalogues call things (Coolify's category and
// tags, Dokploy's tags) to musdash's categories. A word that says how a
// service is built or licensed ("postgres", "self-hosted", "docker") says
// nothing about what it is for and is not here.
var words = map[string]string{
	// AI
	"ai": "ai", "llm": "ai", "llms": "ai", "chatbot": "ai", "mcp": "ai", "machine-learning": "ai", "ml": "ai",
	"openai": "ai", "gpt": "ai", "chatgpt": "ai", "rag": "ai", "agents": "ai", "ai-agents": "ai", "agent": "ai",
	"genai": "ai", "generative-ai": "ai", "ollama": "ai", "inference": "ai", "vector": "ai", "vector-database": "ai",
	"embeddings": "ai", "stable-diffusion": "ai", "image-generation": "ai", "speech-to-text": "ai", "tts": "ai",
	"text-to-speech": "ai", "transcription": "ai", "langchain": "ai", "ai-assistant": "ai", "assistant": "ai",
	"artificial-intelligence": "ai", "copilot": "ai", "deep-learning": "ai", "nlp": "ai", "voice": "ai",
	// Analytics
	"analytics": "analytics", "web-analytics": "analytics", "tracking": "analytics", "statistics": "analytics",
	"bi": "analytics", "business-intelligence": "analytics", "data-visualization": "analytics",
	"visualization": "analytics", "product-analytics": "analytics", "reporting": "analytics", "data": "analytics",
	"data-analytics": "analytics", "session-replay": "analytics", "heatmaps": "analytics", "charts": "analytics",
	// Authentication
	"auth": "auth", "authentication": "auth", "sso": "auth", "oauth": "auth", "oidc": "auth", "identity": "auth",
	"iam": "auth", "ldap": "auth", "saml": "auth", "2fa": "auth", "mfa": "auth", "identity-provider": "auth",
	"single-sign-on": "auth", "authorization": "auth", "oauth2": "auth", "idp": "auth", "login": "auth",
	// Automation
	"automation": "automation", "workflow": "automation", "workflows": "automation", "scheduler": "automation",
	"scheduling": "automation", "cron": "automation", "orchestration": "automation", "etl": "automation",
	"integration": "automation", "integrations": "automation", "zapier": "automation", "rpa": "automation",
	"workflow-automation": "automation", "data-pipeline": "automation", "pipelines": "automation", "jobs": "automation",
	"scraping": "automation", "web-scraping": "automation", "crawler": "automation", "scraper": "automation",
	"browser-automation": "automation", "data-integration": "automation", "queue": "automation", "task-queue": "automation",
	// Backend
	"backend": "backend", "baas": "backend", "low-code": "backend", "no-code": "backend", "nocode": "backend",
	"lowcode": "backend", "firebase": "backend", "backend-as-a-service": "backend", "realtime": "backend",
	"graphql": "backend", "internal-tools": "backend", "app-builder": "backend", "serverless": "backend",
	"functions": "backend", "headless": "backend", "api-gateway": "backend", "message-broker": "backend",
	"message-queue": "backend", "mqtt": "backend", "pubsub": "backend", "event-streaming": "backend", "kafka": "backend",
	"feature-flags": "backend", "paas": "backend", "websocket": "backend", "websockets": "backend", "airtable": "backend",
	// Business
	"business": "business", "crm": "business", "erp": "business", "marketing": "business", "hr": "business",
	"sales": "business", "inventory": "business", "survey": "business", "surveys": "business", "forms": "business",
	"form-builder": "business", "booking": "business", "appointments": "business", "newsletter": "business",
	"email-marketing": "business", "seo": "business", "link-shortener": "business", "url-shortener": "business",
	"shortener": "business", "links": "business", "social-media": "business", "recruitment": "business",
	"hrm": "business", "legal": "business", "contracts": "business", "signature": "business", "e-signature": "business",
	"document-signing": "business", "asset-management": "business", "it-asset-management": "business",
	"event-management": "business", "events": "business", "ticketing-system": "business", "pos": "business",
	"real-estate": "business", "manufacturing": "business", "logistics": "business", "fleet": "business",
	"link-in-bio": "business", "landing-page": "business", "feedback": "business", "waitlist": "business",
	// CMS
	"cms": "cms", "blog": "cms", "blogging": "cms", "content-management": "cms", "headless-cms": "cms",
	"website-builder": "cms", "website": "cms", "publishing": "cms", "wiki-cms": "cms", "page-builder": "cms",
	"static-site": "cms", "site-builder": "cms", "wordpress": "cms", "content": "cms", "portfolio": "cms",
	// Communication
	"communication": "communication", "chat": "communication", "messaging": "communication", "forum": "communication",
	"community": "communication", "discussion": "communication", "video-conferencing": "communication",
	"whatsapp": "communication", "telegram": "communication", "discord": "communication", "slack": "communication",
	"matrix": "communication", "irc": "communication", "voip": "communication", "social": "communication",
	"social-network": "communication", "fediverse": "communication", "mastodon": "communication",
	"activitypub": "communication", "notifications": "communication", "push-notifications": "communication",
	"notification": "communication", "team-chat": "communication", "meetings": "communication", "video-chat": "communication",
	"comments": "communication", "conferencing": "communication", "sms": "communication", "webrtc": "communication",
	"live-chat": "communication", "social-networking": "communication", "microblogging": "communication",
	"instant-messaging": "communication", "collaboration-chat": "communication", "xmpp": "communication",
	// Databases
	"database": "database", "databases": "database", "sql": "database", "nosql": "database", "db": "database",
	"database-management": "database", "database-admin": "database", "data-warehouse": "database",
	"time-series": "database", "timeseries": "database", "graph-database": "database", "key-value": "database",
	"olap": "database", "cache": "database", "caching": "database", "db-admin": "database", "database-tools": "database",
	"database-gui": "database", "spreadsheet-database": "database", "vector-db": "database", "dbms": "database",
	// Developer tools
	"devtools": "devtools", "development": "devtools", "developer-tools": "devtools", "ide": "devtools",
	"code": "devtools", "coding": "devtools", "code-editor": "devtools", "api": "devtools", "testing": "devtools",
	"api-testing": "devtools", "debugging": "devtools", "registry": "devtools", "docker-registry": "devtools",
	"package-registry": "devtools", "container-registry": "devtools", "error-tracking": "devtools",
	"webhook": "devtools", "webhooks": "devtools", "devops": "devtools", "developer": "devtools", "sdk": "devtools",
	"pastebin": "devtools", "paste": "devtools", "snippets": "devtools", "code-quality": "devtools",
	"static-analysis": "devtools", "api-management": "devtools", "api-documentation": "devtools", "mock": "devtools",
	"localization": "devtools", "translation": "devtools", "i18n": "devtools", "tools": "devtools", "utilities": "devtools",
	"utility": "devtools", "terminal": "devtools", "ssh": "devtools", "infrastructure": "devtools", "containers": "devtools",
	"container-management": "devtools", "kubernetes": "devtools", "jupyter": "devtools", "notebook": "devtools",
	"notebooks": "devtools", "data-science": "devtools", "diagrams": "devtools", "diagram": "devtools",
	"programming": "devtools", "developer-platform": "devtools", "feature-flag": "devtools", "browser": "devtools",
	"remote-desktop": "devtools", "vnc": "devtools", "desktop": "devtools", "virtualization": "devtools",
	"windows": "devtools", "macos": "devtools", "linux": "devtools", "os": "devtools", "emulator": "devtools",
	"converter": "devtools", "pdf": "devtools", "image-processing": "devtools", "ocr": "devtools", "qr": "devtools",
	// Documentation and notes
	"documentation": "docs", "docs": "docs", "wiki": "docs", "knowledge-base": "docs", "notes": "docs",
	"note-taking": "docs", "knowledge-management": "docs", "markdown": "docs", "notebook-notes": "docs",
	"knowledge": "docs", "notion": "docs", "editor": "docs", "writing": "docs", "second-brain": "docs",
	"document-management": "docs", "documents": "docs", "office": "docs", "whiteboard": "docs", "drawing": "docs",
	"note": "docs", "journal": "docs", "collaborative-editing": "docs", "text-editor": "docs",
	// E-commerce
	"ecommerce": "ecommerce", "e-commerce": "ecommerce", "shop": "ecommerce", "store": "ecommerce",
	"shopping": "ecommerce", "commerce": "ecommerce", "marketplace": "ecommerce", "online-store": "ecommerce",
	"payments": "ecommerce", "payment": "ecommerce", "checkout": "ecommerce", "cart": "ecommerce",
	// Email
	"email": "email", "mail": "email", "smtp": "email", "webmail": "email", "imap": "email", "mail-server": "email",
	"email-server": "email", "mailserver": "email", "mailing-list": "email", "transactional-email": "email",
	"email-testing": "email", "inbox": "email",
	// Finance
	"finance": "finance", "accounting": "finance", "invoicing": "finance", "invoice": "finance", "budget": "finance",
	"budgeting": "finance", "money": "finance", "personal-finance": "finance", "billing": "finance",
	"expenses": "finance", "expense-tracking": "finance", "crypto": "finance", "bitcoin": "finance",
	"cryptocurrency": "finance", "blockchain": "finance", "trading": "finance", "stocks": "finance",
	"investment": "finance", "bookkeeping": "finance", "subscriptions": "finance", "banking": "finance",
	"invoices": "finance", "time-tracking": "finance", "web3": "finance", "ethereum": "finance",
	// Games
	"games": "games", "gaming": "games", "game": "games", "game-server": "games", "minecraft": "games",
	"game-servers": "games", "emulation": "games", "retro": "games", "roms": "games",
	// Git and CI
	"git": "git", "ci": "git", "ci/cd": "git", "cicd": "git", "ci-cd": "git", "version-control": "git",
	"vcs": "git", "code-hosting": "git", "continuous-integration": "git", "github": "git", "gitlab": "git",
	"build": "git", "deployment": "git", "continuous-deployment": "git", "runner": "git", "code-review": "git",
	"source-control": "git", "pipeline": "git",
	// Home
	"home": "home", "home-automation": "home", "smart-home": "home", "iot": "home", "family": "home",
	"health": "home", "fitness": "home", "recipes": "home", "recipe": "home", "food": "home", "cooking": "home",
	"homelab": "home", "household": "home", "weather": "home", "travel": "home", "maps": "home", "location": "home",
	"gps": "home", "habits": "home", "habit-tracker": "home", "meal-planning": "home", "grocery": "home",
	"lifestyle": "home", "personal": "home", "genealogy": "home", "pets": "home", "garden": "home", "energy": "home",
	"homepage": "home", "startpage": "home", "start-page": "home", "dashboard": "home", "personal-dashboard": "home",
	"medical": "home", "healthcare": "home", "workout": "home", "kids": "home", "education": "home",
	"learning": "home", "lms": "home", "e-learning": "home", "school": "home", "quiz": "home", "flashcards": "home",
	"language-learning": "home", "sports": "home", "car": "home", "vehicle": "home", "chores": "home", "tracker": "home",
	// Media
	"media": "media", "video": "media", "music": "media", "streaming": "media", "photos": "media", "photo": "media",
	"audio": "media", "podcast": "media", "podcasts": "media", "movies": "media", "tv": "media", "gallery": "media",
	"images": "media", "image": "media", "ebooks": "media", "books": "media", "ebook": "media", "comics": "media",
	"downloader": "media", "torrent": "media", "torrents": "media", "youtube": "media", "media-server": "media",
	"audiobooks": "media", "library": "media", "manga": "media", "anime": "media", "iptv": "media", "radio": "media",
	"downloads": "media", "download-manager": "media", "usenet": "media", "media-management": "media",
	"transcoding": "media", "photography": "media", "image-hosting": "media", "video-streaming": "media",
	"music-streaming": "media", "live-streaming": "media", "video-editing": "media", "screen-recording": "media",
	"subtitles": "media", "plex": "media", "jellyfin": "media", "arr": "media", "media-player": "media",
	"design": "media", "graphics": "media", "art": "media", "reading": "media", "audiobook": "media",
	// Monitoring
	"monitoring": "monitoring", "observability": "monitoring", "metrics": "monitoring", "logging": "monitoring",
	"logs": "monitoring", "uptime": "monitoring", "status-page": "monitoring", "status": "monitoring",
	"alerting": "monitoring", "apm": "monitoring", "tracing": "monitoring", "grafana": "monitoring",
	"prometheus": "monitoring", "log-management": "monitoring", "uptime-monitoring": "monitoring",
	"performance": "monitoring", "speedtest": "monitoring", "benchmark": "monitoring", "healthcheck": "monitoring",
	"incident-management": "monitoring", "on-call": "monitoring", "alerts": "monitoring", "network-monitoring": "monitoring",
	"server-monitoring": "monitoring", "telemetry": "monitoring", "opentelemetry": "monitoring", "profiling": "monitoring",
	"system-monitoring": "monitoring", "docker-monitoring": "monitoring", "status-monitoring": "monitoring",
	// Networking
	"networking": "networking", "network": "networking", "proxy": "networking", "vpn": "networking",
	"dns": "networking", "reverse-proxy": "networking", "tunnel": "networking", "wireguard": "networking",
	"load-balancer": "networking", "firewall": "networking", "ad-blocker": "networking", "adblock": "networking",
	"ad-blocking": "networking", "cdn": "networking", "gateway": "networking", "tunneling": "networking",
	"ddns": "networking", "dynamic-dns": "networking", "web-server": "networking", "webserver": "networking",
	"nginx": "networking", "mesh": "networking", "p2p": "networking", "tor": "networking", "router": "networking",
	"ip": "networking", "ipam": "networking", "zero-trust": "networking", "remote-access": "networking",
	"domain": "networking", "domains": "networking", "ssl": "networking", "certificates": "networking",
	// Productivity
	"productivity": "productivity", "project-management": "productivity", "tasks": "productivity",
	"todo": "productivity", "kanban": "productivity", "collaboration": "productivity", "calendar": "productivity",
	"bookmarks": "productivity", "bookmark": "productivity", "planning": "productivity", "task-management": "productivity",
	"to-do": "productivity", "agile": "productivity", "scrum": "productivity", "issue-tracker": "productivity",
	"issue-tracking": "productivity", "spreadsheet": "productivity", "spreadsheets": "productivity",
	"teamwork": "productivity", "team": "productivity", "organization": "productivity", "bookmark-manager": "productivity",
	"time-management": "productivity", "pomodoro": "productivity", "contacts": "productivity", "groupware": "productivity",
	"office-suite": "productivity", "presentations": "productivity", "mind-map": "productivity", "roadmap": "productivity",
	"project": "productivity", "tasks-management": "productivity", "read-later": "productivity", "clipboard": "productivity",
	"todo-list": "productivity", "task": "productivity", "workspace": "productivity", "resume": "productivity",
	"polls": "productivity", "poll": "productivity", "scheduling-polls": "productivity", "timer": "productivity",
	// RSS and reading
	"rss": "rss", "feed": "rss", "feeds": "rss", "news": "rss", "feed-reader": "rss", "rss-reader": "rss",
	"news-reader": "rss", "aggregator": "rss", "news-aggregator": "rss", "atom": "rss",
	// Search
	"search": "search", "search-engine": "search", "full-text-search": "search", "elasticsearch": "search",
	"metasearch": "search", "indexing": "search", "indexer": "search",
	// Security
	"security": "security", "password-manager": "security", "passwords": "security", "secrets": "security",
	"secrets-management": "security", "encryption": "security", "vulnerability": "security", "password": "security",
	"waf": "security", "vault": "security", "secret-management": "security",
	"siem": "security", "pentest": "security", "scanner": "security", "antivirus": "security", "captcha": "security",
	"compliance": "security", "audit": "security", "vulnerability-scanner": "security", "threat-intelligence": "security",
	"intrusion-detection": "security", "pki": "security", "cybersecurity": "security", "secret": "security",
	"anonymity": "security", "bot-protection": "security", "access-control": "security",
	// Storage
	"storage": "storage", "s3": "storage", "file-sharing": "storage", "files": "storage", "backup": "storage",
	"object-storage": "storage", "file-manager": "storage", "sync": "storage", "cloud-storage": "storage",
	"file-sync": "storage", "file-transfer": "storage", "ftp": "storage", "sftp": "storage", "nas": "storage",
	"file-hosting": "storage", "file-storage": "storage", "backups": "storage", "file": "storage", "webdav": "storage",
	"file-upload": "storage", "sharing": "storage", "cloud": "storage", "file-browser": "storage", "drive": "storage",
	"archive": "storage", "archiving": "storage", "web-archive": "storage", "upload": "storage", "share": "storage",
	"file-management": "storage", "snapshot": "storage", "minio": "storage", "synchronization": "storage",
	// Support
	"support": "support", "helpdesk": "support", "help-desk": "support", "customer-support": "support",
	"ticketing": "support", "tickets": "support", "customer-service": "support", "customer-engagement": "support",
	"service-desk": "support", "itsm": "support", "customer-success": "support", "live-support": "support",
}

// categorise gives a template its categories: its source's own first, then
// what its tags say, three at most.
func categorise(t *tmpl) {
	seen := map[string]bool{}
	add := func(word string) {
		word = strings.ToLower(strings.TrimSpace(word))
		c := words[word]
		if c == "" {
			c = words[strings.ReplaceAll(word, " ", "-")]
		}
		if c != "" && !seen[c] && len(t.Categories) < 3 {
			seen[c] = true
			t.Categories = append(t.Categories, c)
		}
	}
	if own, ok := categoryOverrides[t.Key]; ok {
		for _, c := range strings.Split(own, ",") {
			c = strings.TrimSpace(c)
			if categoryKeys[c] && !seen[c] {
				seen[c] = true
				t.Categories = append(t.Categories, c)
			}
		}
		return
	}
	for _, c := range strings.Split(t.SrcCategory, ",") {
		add(c)
	}
	for _, tag := range t.Tags {
		add(tag)
	}
}
