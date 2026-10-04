package deploy

import (
	"bufio"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnv reads a block of KEY=value lines, as pasted from a .env file.
// Blank lines and lines starting with # are skipped. A value may be wrapped
// in matching single or double quotes, which are removed. Later lines win
// when a key repeats.
//
// It returns an error naming the first line it cannot accept, because a
// silently dropped variable is found only when the app misbehaves.
func ParseEnv(text string) ([]db.EnvVar, error) {
	byKey := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKeyRE.MatchString(key) {
			return nil, fmt.Errorf("line %d: write each variable as NAME=value, where NAME uses letters, numbers and underscores", n)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		byKey[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("a line is longer than 1 MB")
	}
	vars := make([]db.EnvVar, 0, len(byKey))
	for k, v := range byKey {
		vars = append(vars, db.EnvVar{Key: k, Value: v})
	}
	sort.Slice(vars, func(i, j int) bool { return vars[i].Key < vars[j].Key })
	return vars, nil
}

// FormatEnv renders variables back into the editable KEY=value block.
func FormatEnv(vars []db.EnvVar) string {
	var b strings.Builder
	for _, v := range vars {
		b.WriteString(v.Key)
		b.WriteByte('=')
		b.WriteString(v.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// EnvFile renders variables in the format `docker run --env-file` reads:
// one KEY=value per line, the value taken literally to the end of the line.
// That format has no escaping, so a value cannot contain a line break.
func EnvFile(vars []db.EnvVar) (string, error) {
	var b strings.Builder
	for _, v := range vars {
		if !envKeyRE.MatchString(v.Key) {
			return "", fmt.Errorf("environment variable name %q is not valid", v.Key)
		}
		if strings.ContainsAny(v.Value, "\n\r") {
			return "", fmt.Errorf("the value of %s contains a line break, which a container env file cannot carry", v.Key)
		}
		b.WriteString(v.Key)
		b.WriteByte('=')
		b.WriteString(v.Value)
		b.WriteByte('\n')
	}
	return b.String(), nil
}
