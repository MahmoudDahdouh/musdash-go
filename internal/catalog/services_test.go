package catalog

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

func TestParseMagic(t *testing.T) {
	cases := map[string]MagicVar{
		"SERVICE_FQDN_N8N":             {Kind: MagicFQDN, ID: "N8N"},
		"SERVICE_FQDN_N8N_5678":        {Kind: MagicFQDN, ID: "N8N", Port: 5678},
		"SERVICE_URL_UPTIME_KUMA_3001": {Kind: MagicURL, ID: "UPTIME_KUMA", Port: 3001},
		"SERVICE_URL_UPTIME_KUMA":      {Kind: MagicURL, ID: "UPTIME_KUMA"},
		"SERVICE_HTTPS_N8N":            {Kind: MagicHTTPS, ID: "N8N"},
		// Not a port: out of range, so it stays part of the name.
		"SERVICE_FQDN_APP_99999":         {Kind: MagicFQDN, ID: "APP_99999"},
		"SERVICE_FQDN_APP_0":             {Kind: MagicFQDN, ID: "APP_0"},
		"SERVICE_USER_WORDPRESS":         {Kind: MagicUser, ID: "WORDPRESS", Len: 16},
		"SERVICE_PASSWORD_ROOT":          {Kind: MagicPassword, ID: "ROOT", Len: 32},
		"SERVICE_PASSWORD_64_ENCRYPTION": {Kind: MagicPassword, ID: "ENCRYPTION", Len: 64},
		// "64" alone is the label, not a length with nothing after it.
		"SERVICE_PASSWORD_64":    {Kind: MagicPassword, ID: "64", Len: 32},
		"SERVICE_BASE64_KEY":     {Kind: MagicBase64, ID: "KEY", Len: 32},
		"SERVICE_BASE64_128_KEY": {Kind: MagicBase64, ID: "KEY", Len: 128},
		"SERVICE_HEX_SECRET":     {Kind: MagicHex, ID: "SECRET", Len: 32},
		"SERVICE_HEX_64_SECRET":  {Kind: MagicHex, ID: "SECRET", Len: 64},
	}
	for name, want := range cases {
		want.Name = name
		if got, ok := ParseMagic(name); !ok || got != want {
			t.Errorf("%s: %+v %v, want %+v", name, got, ok, want)
		}
	}
	for _, name := range []string{
		"", "SERVICE_", "SERVICE_FQDN", "SERVICE_FQDN_", "SERVICE_OTHER_X", "service_url_x", "SERVICE_URL_x",
		"MY_SERVICE_URL_X", "SERVICE_URL_X ", "SERVICE_PASSWORD_", "SERVICE_URL_X_", "POSTGRES_PASSWORD",
	} {
		if got, ok := ParseMagic(name); ok {
			t.Errorf("%q was read as a magic variable: %+v", name, got)
		}
	}
}

func TestScanMagic(t *testing.T) {
	compose := `
services:
  app:
    environment:
      - SERVICE_FQDN_APP_3000
      - PUBLIC_URL=${SERVICE_URL_APP}/api
      - DB=postgres://$SERVICE_USER_DB:${SERVICE_PASSWORD_DB}@db/app
      - AGAIN=${SERVICE_PASSWORD_DB}
      - MINE=${MY_SERVICE_URL_APP}
      - OTHER=$SERVICE_URL_APPx
      - LITERAL=$$SERVICE_HEX_KEPT
    labels:
      note: "SERVICE_BASE64_64_KEY is mentioned in text too"
`
	var names []string
	for _, v := range ScanMagic(compose) {
		names = append(names, v.Name)
	}
	// Sorted, each once. A name inside a longer name is not one; a mention
	// anywhere in the text is, including after Compose's "$$" escape, where
	// generating an unused value does no harm.
	want := "SERVICE_BASE64_64_KEY SERVICE_FQDN_APP_3000 SERVICE_HEX_KEPT SERVICE_PASSWORD_DB SERVICE_URL_APP SERVICE_USER_DB"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("found\n %s\nwant\n %s", got, want)
	}
}

func TestGenerate(t *testing.T) {
	shapes := map[string]string{
		"SERVICE_USER_X":        `^u[a-z0-9]{15}$`,
		"SERVICE_PASSWORD_X":    `^[A-Za-z0-9]{32}$`,
		"SERVICE_PASSWORD_64_X": `^[A-Za-z0-9]{64}$`,
		"SERVICE_HEX_X":         `^[0-9a-f]{32}$`,
		"SERVICE_HEX_16_X":      `^[0-9a-f]{16}$`,
		"SERVICE_HEX_64_X":      `^[0-9a-f]{64}$`,
	}
	for name, shape := range shapes {
		v, _ := ParseMagic(name)
		a, ok := Generate(v)
		b, _ := Generate(v)
		if !ok || !regexp.MustCompile(shape).MatchString(a) {
			t.Errorf("%s: %q does not match %s", name, a, shape)
		}
		if a == b {
			t.Errorf("%s: two generated values are equal", name)
		}
	}
	for name, bytes := range map[string]int{"SERVICE_BASE64_X": 32, "SERVICE_BASE64_64_X": 64, "SERVICE_BASE64_128_X": 128} {
		v, _ := ParseMagic(name)
		got, _ := Generate(v)
		if raw, err := base64.StdEncoding.DecodeString(got); err != nil || len(raw) != bytes {
			t.Errorf("%s: %d bytes (%v), want %d", name, len(raw), err, bytes)
		}
	}
	for _, name := range []string{"SERVICE_FQDN_X", "SERVICE_URL_X_80", "SERVICE_HTTPS_X"} {
		v, _ := ParseMagic(name)
		if got, ok := Generate(v); ok {
			t.Errorf("%s: generated %q, but its value comes from a domain", name, got)
		}
	}
}

func TestAddressValue(t *testing.T) {
	for name, want := range map[string][2]string{
		"SERVICE_FQDN_X_80": {"app.example.com", "app.example.com"},
		"SERVICE_URL_X":     {"https://app.example.com", "http://app.example.com"},
		"SERVICE_HTTPS_X":   {"true", "false"},
	} {
		v, _ := ParseMagic(name)
		if got := [2]string{AddressValue(v, "app.example.com", true), AddressValue(v, "app.example.com", false)}; got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

func TestScanVariables(t *testing.T) {
	compose := `
services:
  app:
    environment:
      - TOKEN=${TUNNEL_TOKEN:?Set the token}
      - TZ=${TZ:-UTC}
      - PLAIN=$PLAIN_ONE and ${BRACED}
      - BOTH=${TZ}
      - DASH=${WITH_DEFAULT-x}
      - ALT=${ALT:+yes}
      - PASS=${SERVICE_PASSWORD_DB}
    healthcheck:
      test: mysqladmin -p"$$MYSQL_ROOT_PASSWORD" ping
`
	got := map[string]bool{}
	for _, v := range ScanVariables(compose) {
		got[v.Name] = v.Required
	}
	want := map[string]bool{
		"TUNNEL_TOKEN": true, "PLAIN_ONE": true, "BRACED": true, "ALT": true,
		"TZ":           true, // one use has no default
		"WITH_DEFAULT": false,
	}
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for name, required := range want {
		if r, ok := got[name]; !ok || r != required {
			t.Errorf("%s: found=%v required=%v, want required=%v", name, ok, r, required)
		}
	}
}

func TestServiceCatalogue(t *testing.T) {
	want := []string{"cloudflared", "ghost", "minio", "n8n", "uptime-kuma", "wordpress"}
	got := map[string]ServiceTemplate{}
	for _, tpl := range Services() {
		got[tpl.Key] = tpl
	}
	if len(got) != len(want) {
		t.Fatalf("%d templates, want %d", len(got), len(want))
	}
	serviceRE := regexp.MustCompile(`(?m)^  ([a-z0-9-]+):\s*$`)
	for _, key := range want {
		tpl, ok := Service(key)
		if !ok || tpl.Name == "" || tpl.About == "" || !strings.HasPrefix(tpl.Docs, "https://") || !strings.HasPrefix(tpl.Website, "https://") {
			t.Errorf("%s: missing, or its header is incomplete: %+v", key, tpl)
			continue
		}
		if !strings.Contains(tpl.Compose, "\nservices:\n") {
			t.Errorf("%s: no services", key)
		}
		// Ports are named by variables, never published by the template:
		// two copies of one template must not collide on the server.
		if regexp.MustCompile(`(?m)^\s+ports:`).MatchString(tpl.Compose) {
			t.Errorf("%s: publishes a port itself", key)
		}
		// Every image carries a tag.
		for _, m := range regexp.MustCompile(`(?m)^\s+image:\s*(\S+)`).FindAllStringSubmatch(tpl.Compose, -1) {
			if !strings.Contains(m[1][strings.LastIndex(m[1], "/")+1:], ":") {
				t.Errorf("%s: image %s has no tag", key, m[1])
			}
		}
		// The top-level keys under "services:" are its services; every
		// service that should get a domain declares a port once.
		names := map[string]bool{}
		for _, m := range serviceRE.FindAllStringSubmatch(tpl.Compose, -1) {
			names[strings.ToUpper(strings.ReplaceAll(m[1], "-", "_"))] = true
		}
		withPort := map[string]bool{}
		for _, v := range ScanMagic(tpl.Compose) {
			if v.Address() && v.Port > 0 {
				withPort[v.ID] = true
			}
		}
		for _, v := range ScanMagic(tpl.Compose) {
			if v.Address() && !withPort[v.ID] {
				t.Errorf("%s: %s names an endpoint whose port is declared nowhere", key, v.Name)
			}
		}
		// A password in a template is always a generated one.
		for _, line := range strings.Split(tpl.Compose, "\n") {
			if strings.Contains(line, "PASSWORD=") && !strings.Contains(line, "${SERVICE_PASSWORD_") {
				t.Errorf("%s: a fixed password: %s", key, strings.TrimSpace(line))
			}
		}
		_ = names
	}
	if tpl, _ := Service("cloudflared"); !tpl.ConnectEnv {
		t.Error("cloudflared must join the environment network to reach apps")
	}
	if tpl, _ := Service("wordpress"); tpl.ConnectEnv {
		t.Error("wordpress should stay on its own network by default")
	}
	if _, ok := Service("../databases"); ok {
		t.Error("a path was accepted as a template key")
	}
}
