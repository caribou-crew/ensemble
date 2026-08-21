package config

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// envPattern matches "$$" (an escaped literal "$", so a value that must
// contain a real "$" isn't mistaken for a reference) and "${VAR}" /
// "${VAR:-default}" (bash's ":-" operator: default applies when VAR is
// unset OR set-but-empty, not only when it's absent).
var envPattern = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// expandEnvVars substitutes "${VAR}"/"${VAR:-default}" references in data
// (ensemble.yaml's raw bytes, before YAML parsing — the same approach
// docker-compose uses, since the substitution is plain text and doesn't
// need to understand YAML structure) using lookup to resolve each VAR.
// "${VAR}" with no default and no value from lookup is an error, not a
// silent empty string: config that goes on to fail at a much less obvious
// point (an empty port, an empty image tag) is a worse experience than
// failing here, at the one place that knows which variable was supposed to
// fill it in.
func expandEnvVars(data []byte, lookup func(string) (string, bool)) ([]byte, error) {
	var firstErr error

	result := envPattern.ReplaceAllStringFunc(string(data), func(match string) string {
		if firstErr != nil {
			return match
		}
		if match == "$$" {
			return "$"
		}
		sub := envPattern.FindStringSubmatch(match)
		name, hasDefault, def := sub[1], sub[2] != "", sub[3]
		if v, ok := lookup(name); ok && v != "" {
			return v
		}
		if hasDefault {
			return def
		}
		firstErr = fmt.Errorf("env var %q is not set and has no default (use ${%s:-default} to allow one)", name, name)
		return match
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return []byte(result), nil
}

// loadDotEnv parses a simple .env file (KEY=VALUE per line; blank lines and
// lines starting with "#" are skipped; a value may be wrapped in matching
// single or double quotes, which are stripped). A missing file is not an
// error — .env is entirely optional, so Load can call this unconditionally.
func loadDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	vars := map[string]string{}
	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, lineNo, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		vars[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return vars, nil
}

// envLookup returns a lookup func for expandEnvVars that checks the real
// process environment first — an explicit `FOO=bar ensemble up` (or
// whatever launched the process) always wins over a checked-in .env file,
// the same precedence every other dotenv-loading tool uses.
func envLookup(dotenv map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if v, ok := os.LookupEnv(key); ok {
			return v, true
		}
		v, ok := dotenv[key]
		return v, ok
	}
}
