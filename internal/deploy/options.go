package deploy

import (
	"fmt"
	"regexp"
	"strings"
)

// A person may add options to an app's `docker run`. They are parsed here
// and checked against an allow-list, because many Docker options hand the
// container the host: --privileged, --network host, --pid host, --device,
// -v, --cap-add SYS_ADMIN. Only options that tune the container without
// widening its reach are allowed.
//
// Options that loosen the limits musdash itself sets are left out too:
// --memory-swap would lift the app's memory limit, and --cpu-shares or the
// SYS_NICE capability would let one app starve the proxy and the dashboard.

// optionRule validates the value of one allowed flag. A nil rule means the
// flag takes no value.
type optionRule func(value string) bool

var (
	sizeRE     = regexp.MustCompile(`^[0-9]+[bkmgBKMG]?$`)
	ulimitRE   = regexp.MustCompile(`^[a-z]+=-?[0-9]+(:-?[0-9]+)?$`)
	addHostRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*:([0-9a-fA-F.:]+|host-gateway)$`)
	hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,61}[A-Za-z0-9])?$`)
	userRE     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(:[A-Za-z0-9_][A-Za-z0-9_.-]*)?$`)
	intRE      = regexp.MustCompile(`^-?[0-9]+$`)
	signalRE   = regexp.MustCompile(`^(SIG)?[A-Z0-9+]{1,12}$`)
	ipRE       = regexp.MustCompile(`^[0-9a-fA-F.:]+$`)
	tmpfsRE    = regexp.MustCompile(`^/[A-Za-z0-9_./-]*(:[A-Za-z0-9=,]+)?$`)
	absPathRE  = regexp.MustCompile(`^/[A-Za-z0-9_./-]*$`)
	// Sysctls are limited to the network namespace, which is the
	// container's own. Kernel-wide ones are not namespaced.
	sysctlRE = regexp.MustCompile(`^net\.[a-z0-9_.]+=[A-Za-z0-9 ._-]+$`)
)

func matches(re *regexp.Regexp) optionRule { return re.MatchString }

// safeCaps are capabilities that do not let a container act on the host.
var safeCaps = map[string]bool{
	"NET_BIND_SERVICE": true, "CHOWN": true, "SETUID": true, "SETGID": true,
	"DAC_OVERRIDE": true, "FOWNER": true, "KILL": true, "IPC_LOCK": true,
}

var allowedOptions = map[string]optionRule{
	"--init":               nil,
	"--read-only":          nil,
	"--shm-size":           matches(sizeRE),
	"--ulimit":             matches(ulimitRE),
	"--add-host":           matches(addHostRE),
	"--hostname":           matches(hostnameRE),
	"--user":               matches(userRE),
	"--workdir":            matches(absPathRE),
	"--stop-timeout":       matches(intRE),
	"--stop-signal":        matches(signalRE),
	"--dns":                matches(ipRE),
	"--tmpfs":              matches(tmpfsRE),
	"--pids-limit":         matches(intRE),
	"--memory-reservation": matches(sizeRE),
	"--sysctl":             matches(sysctlRE),
	"--cap-drop":           func(v string) bool { return regexp.MustCompile(`^[A-Z_]{2,24}$`).MatchString(v) },
	"--cap-add":            func(v string) bool { return safeCaps[strings.TrimPrefix(v, "CAP_")] },
	// Only the hardening direction of --security-opt.
	"--security-opt": func(v string) bool { return v == "no-new-privileges" || v == "no-new-privileges:true" },
}

// ParseRunOptions turns the options a person typed into an argument vector,
// refusing anything outside the allow-list. Each flag and its value come out
// as separate arguments ("--shm-size", "256m"), so a value can never be read
// as a second flag.
func ParseRunOptions(s string) ([]string, error) {
	words, err := splitWords(s)
	if err != nil {
		return nil, err
	}
	var args []string
	for i := 0; i < len(words); i++ {
		word := words[i]
		flag, value, glued := strings.Cut(word, "=")
		rule, known := allowedOptions[flag]
		if !known {
			if !strings.HasPrefix(flag, "-") {
				return nil, fmt.Errorf("%q is not an option; each option starts with --", word)
			}
			return nil, fmt.Errorf("the option %s is not allowed", flag)
		}
		if rule == nil {
			if glued {
				return nil, fmt.Errorf("%s takes no value", flag)
			}
			args = append(args, flag)
			continue
		}
		if !glued {
			if i+1 >= len(words) {
				return nil, fmt.Errorf("%s needs a value", flag)
			}
			i++
			value = words[i]
		}
		if value == "" || strings.HasPrefix(value, "-") && !intRE.MatchString(value) || !rule(value) {
			return nil, fmt.Errorf("%q is not an allowed value for %s", value, flag)
		}
		args = append(args, flag, value)
	}
	return args, nil
}

// splitWords splits on whitespace, honouring single and double quotes.
func splitWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, c := range s {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("a quote is not closed")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
