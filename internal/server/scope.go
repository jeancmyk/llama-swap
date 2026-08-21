package server

import (
	"context"
	"path"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/config"
)

// ListenerScope restricts which models a request may reach. A nil *ListenerScope
// means "unrestricted" (legacy single-port behaviour). A non-nil scope with no
// patterns serves nothing until patterns are configured.
type ListenerScope struct {
	Name     string   `json:"name"`
	Listen   string   `json:"listen"`
	Patterns []string `json:"models"`
}

// Allows reports whether any of the candidate names (requested name, resolved
// ID, aliases) matches one of the scope's glob patterns.
func (sc *ListenerScope) Allows(names ...string) bool {
	if sc == nil {
		return true
	}
	for _, pattern := range sc.Patterns {
		for _, name := range names {
			if name == "" {
				continue
			}
			if ok, err := path.Match(pattern, name); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// NewListenerScope builds a scope from a config entry, expanding an empty
// pattern list into nothing (deny-all).
func NewListenerScope(l config.ListenerConfig) *ListenerScope {
	patterns := make([]string, 0, len(l.Models))
	for _, p := range l.Models {
		p = strings.TrimSpace(p)
		if p != "" {
			patterns = append(patterns, p)
		}
	}
	return &ListenerScope{Name: l.Name, Listen: l.Listen, Patterns: patterns}
}

type scopeContextKey struct{}

// WithScope attaches a listener scope to the request context.
func WithScope(ctx context.Context, sc *ListenerScope) context.Context {
	return context.WithValue(ctx, scopeContextKey{}, sc)
}

// ScopeFromContext returns the attached scope or nil when unrestricted.
func ScopeFromContext(ctx context.Context) *ListenerScope {
	sc, _ := ctx.Value(scopeContextKey{}).(*ListenerScope)
	return sc
}

// scopeAllowsRecord decides whether a /v1/models record is visible through a
// scope: the record ID, its aliases, and (for selectors) its targets are all
// considered so alias- or target-based globs surface the right entries.
func scopeAllowsRecord(sc *ListenerScope, id string, extraNames []string) bool {
	if sc == nil {
		return true
	}
	names := append([]string{id}, extraNames...)
	return sc.Allows(names...)
}
