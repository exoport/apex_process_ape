package sandbox

import (
	"fmt"
	"os"
	"strings"
)

// ResolveSecret reads the value behind a `env:NAME` or `file:PATH` source
// reference (the shape used by api_key_source / token_source). The value
// is trimmed of surrounding whitespace — a trailing newline in a token
// file is the usual gotcha. An empty resolved value is an error: a
// present-but-empty env var or file almost always means a misconfigured
// job, and failing loudly beats a guest that silently can't authenticate.
func ResolveSecret(src string) (string, error) {
	scheme, rest, ok := strings.Cut(src, ":")
	if !ok || rest == "" {
		return "", fmt.Errorf("secret source must be env:NAME or file:PATH, got %q", src)
	}
	switch scheme {
	case "env":
		v, present := os.LookupEnv(rest)
		if !present {
			return "", fmt.Errorf("secret source %s: env var %s is not set", src, rest)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return "", fmt.Errorf("secret source %s: env var %s is empty", src, rest)
		}
		return v, nil
	case "file":
		data, err := os.ReadFile(rest)
		if err != nil {
			return "", fmt.Errorf("secret source %s: %w", src, err)
		}
		v := strings.TrimSpace(string(data))
		if v == "" {
			return "", fmt.Errorf("secret source %s: file %s is empty", src, rest)
		}
		return v, nil
	default:
		return "", fmt.Errorf("secret source %s: unsupported scheme %q (want env: or file:)", src, scheme)
	}
}
