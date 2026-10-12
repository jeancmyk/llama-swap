package config

import (
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
)

// ListenerConfig declares one HTTP listener and which models it serves.
// Patterns are shell-style globs (path.Match) matched against local model
// IDs, aliases, selector IDs, and peer FQNs ("peer:model"). A bare "*" is
// special-cased to mean every model, including IDs containing "/"; other
// globs do not cross "/" (matching a namespace takes "vendor/*").
type ListenerConfig struct {
	Name   string   `yaml:"name" json:"name"`
	Listen string   `yaml:"listen" json:"listen"`
	Models []string `yaml:"models" json:"models"`
}

// NormalizeListenerDefaults fills in derived fields: an empty name becomes
// "port-<port>". Returns a copy; does not mutate the input slice entries that
// are already valid.
func NormalizeListenerDefaults(listeners []ListenerConfig) []ListenerConfig {
	if len(listeners) == 0 {
		return nil
	}
	out := make([]ListenerConfig, 0, len(listeners))
	for _, l := range listeners {
		if strings.TrimSpace(l.Name) == "" {
			l.Name = fmt.Sprintf("port-%s", listenerPort(l.Listen))
		}
		out = append(out, l)
	}
	return out
}

// ValidateListeners checks a listeners block: parseable addresses, unique
// listen addresses and names, and syntactically valid glob patterns.
func ValidateListeners(listeners []ListenerConfig) error {
	seenAddr := make(map[string]string)
	seenName := make(map[string]string)

	for i, l := range listeners {
		addr, err := ParseListenAddr(l.Listen)
		if err != nil {
			return fmt.Errorf("listeners[%d] (%s): %w", i, l.Name, err)
		}
		if prev, dup := seenAddr[addr]; dup {
			return fmt.Errorf("listeners[%d]: duplicate listen address %s (also used by %q)", i, addr, prev)
		}
		seenAddr[addr] = l.Name

		name := l.Name
		if name == "" {
			name = fmt.Sprintf("port-%s", listenerPort(addr))
		}
		if prev, dup := seenName[name]; dup {
			return fmt.Errorf("listeners[%d]: duplicate listener name %q (also used for %s)", i, name, prev)
		}
		seenName[name] = addr

		for j, pattern := range l.Models {
			if strings.TrimSpace(pattern) == "" {
				return fmt.Errorf("listeners[%d].models[%d]: empty pattern", i, j)
			}
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("listeners[%d].models[%d]: invalid pattern %q: %w", i, j, pattern, err)
			}
		}
	}
	return nil
}

// ParseListenAddr validates "host:port" and returns it in normalized
// "host:port" form. Port must be numeric in range 1-65535. An empty host
// stays empty (":port"), which binds all interfaces on every protocol
// family — the same semantics a bare -listen address has before listeners
// are configured.
func ParseListenAddr(listen string) (string, error) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", fmt.Errorf("listen address is required")
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("invalid listen address %q (want host:port): %w", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid port %q in listen address %q", portStr, listen)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func listenerPort(listen string) string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return listen
	}
	return port
}
