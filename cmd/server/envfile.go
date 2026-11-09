package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// loadEnvFile applies the KEY=VALUE pairs of a .env-style file to the
// process environment. Variables that are already set in the real
// environment are never overwritten, so `BLEAK_NODE_ADDR=... make run`
// wins over the file. Lines may carry an optional `export ` prefix; values
// may be single- or double-quoted; whole-line and inline (` # ...`)
// comments are ignored. A missing file is not an error; a present but
// malformed line is reported through the optional warn callback.
func loadEnvFile(path string, warn func(line string)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || !validEnvKey(strings.TrimSpace(key)) {
			if warn != nil {
				warn(line)
			}
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, unquote(strings.TrimSpace(value))); err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return sc.Err()
}

// unquote strips surrounding quotes and inline comments from a raw value.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		inner := v[1 : len(v)-1]
		if v[0] == '"' {
			return unescape(inner)
		}
		return inner
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

// unescape expands the escape set of double-quoted shell strings.
func unescape(s string) string {
	return strings.NewReplacer(
		`\n`, "\n",
		`\t`, "\t",
		`\"`, `"`,
		`\\`, `\`,
	).Replace(s)
}

// validEnvKey reports whether s is a plausible environment variable name.
func validEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '.':
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
