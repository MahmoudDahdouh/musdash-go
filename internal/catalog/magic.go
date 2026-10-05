package catalog

import (
	"encoding/base64"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// Magic variables are names a Compose file uses to ask musdash for a value:
// a generated password, or the address a service is reached at. They follow
// the convention of Coolify's service templates, so those can be pasted.
//
//	SERVICE_FQDN_<NAME>[_<PORT>]   the host name routed to service NAME
//	SERVICE_URL_<NAME>[_<PORT>]    the same with its scheme: https://host
//	SERVICE_HTTPS_<NAME>           "true" or "false": whether that address is HTTPS
//	SERVICE_USER_<ID>              a generated user name
//	SERVICE_PASSWORD_[64_]<ID>     a generated password, 32 or 64 characters
//	SERVICE_BASE64_[64_|128_]<ID>  32, 64 or 128 random bytes in base64
//	SERVICE_HEX_[16_|64_]<ID>      32, 16 or 64 hexadecimal characters
//
// A <PORT> names the port the service listens on inside its container. It
// tells musdash where to send requests and is not part of the value.
// SERVICE_HTTPS is musdash's own addition.

// Kinds of magic variable.
const (
	MagicFQDN     = "FQDN"
	MagicURL      = "URL"
	MagicHTTPS    = "HTTPS"
	MagicUser     = "USER"
	MagicPassword = "PASSWORD"
	MagicBase64   = "BASE64"
	MagicHex      = "HEX"
)

// MagicVar is one magic variable found in a Compose file.
type MagicVar struct {
	Name string // the whole name, as written
	Kind string
	// ID is what the variable is about: for the address kinds the name of
	// the endpoint (usually a Compose service), otherwise a label that
	// makes the generated value distinct.
	ID   string
	Port int // address kinds only; 0 when the name carries none
	Len  int // generated kinds only
}

// Address reports whether the variable's value comes from a domain rather
// than being generated.
func (v MagicVar) Address() bool {
	return v.Kind == MagicFQDN || v.Kind == MagicURL || v.Kind == MagicHTTPS
}

var magicRE = regexp.MustCompile(`SERVICE_(FQDN|URL|HTTPS|USER|PASSWORD|BASE64|HEX)_[A-Z0-9_]*[A-Z0-9]`)

func isNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// ParseMagic reads a variable name. It reports false for a name that is
// not a magic variable.
func ParseMagic(name string) (MagicVar, bool) {
	if magicRE.FindString(name) != name {
		return MagicVar{}, false
	}
	kind, rest, _ := strings.Cut(strings.TrimPrefix(name, "SERVICE_"), "_")
	v := MagicVar{Name: name, Kind: kind, ID: rest}
	// takeLen reads an optional leading "<n>_" as the length to generate.
	takeLen := func(def int, allowed ...int) {
		v.Len = def
		for _, n := range allowed {
			if after, ok := strings.CutPrefix(rest, strconv.Itoa(n)+"_"); ok && after != "" {
				v.Len, v.ID = n, after
				return
			}
		}
	}
	switch kind {
	case MagicFQDN, MagicURL, MagicHTTPS:
		if i := strings.LastIndexByte(rest, '_'); i > 0 {
			if port, err := strconv.Atoi(rest[i+1:]); err == nil && port >= 1 && port <= 65535 {
				v.ID, v.Port = rest[:i], port
			}
		}
	case MagicUser:
		v.Len = 16
	case MagicPassword:
		takeLen(32, 64)
	case MagicBase64:
		takeLen(32, 64, 128)
	case MagicHex:
		takeLen(32, 16, 64)
	}
	return v, v.ID != ""
}

// ScanMagic finds the magic variables a Compose file mentions, in any
// position: "${NAME}", "$NAME", or a bare "NAME" in an environment list.
// The result is sorted by name without repeats.
func ScanMagic(compose string) []MagicVar {
	seen := map[string]bool{}
	var out []MagicVar
	for _, loc := range magicRE.FindAllStringIndex(compose, -1) {
		// A longer name that merely contains one (MY_SERVICE_URL_X, or
		// SERVICE_URL_Xy) is somebody's own variable.
		if loc[0] > 0 && isNameByte(compose[loc[0]-1]) || loc[1] < len(compose) && isNameByte(compose[loc[1]]) {
			continue
		}
		name := compose[loc[0]:loc[1]]
		if v, ok := ParseMagic(name); ok && !seen[name] {
			seen[name] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Generate makes a value for a generated kind. It reports false for the
// address kinds, whose value comes from the service's domain.
func Generate(v MagicVar) (string, bool) {
	switch v.Kind {
	case MagicUser:
		// Starts with a letter: several databases refuse a name that does
		// not.
		return "u" + strings.ToLower(secret.RandomAlnum(v.Len-1)), true
	case MagicPassword:
		return secret.RandomAlnum(v.Len), true
	case MagicBase64:
		return base64.StdEncoding.EncodeToString(secret.RandomBytes(v.Len)), true
	case MagicHex:
		return secret.RandomHex((v.Len + 1) / 2)[:v.Len], true
	}
	return "", false
}

// AddressValue is the value of an address variable for a host name.
func AddressValue(v MagicVar, host string, tls bool) string {
	switch v.Kind {
	case MagicFQDN:
		return host
	case MagicURL:
		if tls {
			return "https://" + host
		}
		return "http://" + host
	case MagicHTTPS:
		return strconv.FormatBool(tls)
	}
	return ""
}

// VarRef is a variable a Compose file reads that musdash does not fill in:
// the person supplies it.
type VarRef struct {
	Name string
	// Required is set when the file gives no default for it.
	Required bool
}

var varRefRE = regexp.MustCompile(`\$(\$|\{([A-Za-z_][A-Za-z0-9_]*)([^}]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)

// ScanVariables lists the variables a Compose file interpolates, other than
// magic ones, sorted by name. "$$" is Compose's escape for a literal dollar
// sign and is skipped.
func ScanVariables(compose string) []VarRef {
	required := map[string]bool{}
	for _, m := range varRefRE.FindAllStringSubmatch(compose, -1) {
		name, modifier := m[2], m[3]
		if name == "" {
			name = m[4]
		}
		if name == "" {
			continue // "$$"
		}
		if _, magic := ParseMagic(name); magic {
			continue
		}
		// ${X:-default} and ${X-default} supply a value; everything else
		// (plain, ${X:?message}, ${X:+alternative}) needs one to be useful.
		hasDefault := strings.HasPrefix(modifier, ":-") || strings.HasPrefix(modifier, "-")
		if prev, seen := required[name]; !seen {
			required[name] = !hasDefault
		} else {
			required[name] = prev || !hasDefault
		}
	}
	out := make([]VarRef, 0, len(required))
	for name, req := range required {
		out = append(out, VarRef{Name: name, Required: req})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
