package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mostlygeek/llama-swap/internal/capcompat"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const writebackFixture = `models:
  alpha:
    name: Alpha
    cmd: echo alpha ${PORT}
    # hand-tuned context
    capabilities:
      context: 8192
`

// writebackTestServer builds a server with one model whose config file lives
// at path, with capabilities write-back enabled or not.
func writebackTestServer(t *testing.T, path string, enabled bool) *Server {
	t.Helper()
	cfg := config.Config{
		CapabilitiesWriteback: enabled,
		Models: map[string]config.ModelConfig{
			"alpha": {
				Name:         "Alpha",
				Cmd:          "echo alpha",
				Capabilities: config.ModelCapConfig{Context: 8192},
			},
		},
	}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
	if path != "" {
		s.SetConfigFile(path)
	}
	// A zero boot time would make every file look dirty; tests that care set
	// it deliberately. Pin the file mtime well before bootTime so ConfigDirty
	// starts out false regardless of filesystem clock granularity.
	s.bootTime = time.Now()
	if path != "" {
		past := s.bootTime.Add(-time.Hour)
		require.NoError(t, os.Chtimes(path, past, past))
	}
	return s
}

func TestCapWriteback_PersistFillsAndMarks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(writebackFixture), 0o644))
	s := writebackTestServer(t, path, true)
	reloads := 0
	s.SetReloadFunc(func() { reloads++ })

	mc := s.cfg.Models["alpha"]
	key := capcompat.LocalKey("alpha", mc)
	seedCapabilities(t, s, "alpha", mc, config.ModelCapConfig{
		In:      []string{"text", "image"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 32768,
	})

	s.persistDiscoveredCapabilities("alpha", mc, key)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "llama-swap auto (")
	assert.Contains(t, text, "in: [text, image]")
	assert.Contains(t, text, "tools: true")
	assert.Contains(t, text, "context: 8192", "the hand-set context stays")
	assert.NotContains(t, text, "32768", "discovered context does not replace a manual one")
	assert.Equal(t, 0, reloads, "write-back must not reload")

	assert.ElementsMatch(t, []string{"in", "out", "tools"}, s.pendingFields("alpha"))

	statuses := s.modelStatus()
	require.Len(t, statuses, 1)
	assert.ElementsMatch(t, []string{"in", "out", "tools"}, statuses[0].ConfigUpdatePending)
}

func TestCapWriteback_PersistIsIdempotentAndRefreshes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(writebackFixture), 0o644))
	s := writebackTestServer(t, path, true)

	mc := s.cfg.Models["alpha"]
	key := capcompat.LocalKey("alpha", mc)
	seedCapabilities(t, s, "alpha", mc, config.ModelCapConfig{
		In:      []string{"text", "image"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 32768,
	})

	s.persistDiscoveredCapabilities("alpha", mc, key)
	before, err := os.Stat(path)
	require.NoError(t, err)

	// The same discovery again must not touch the file (no date churn).
	s.persistDiscoveredCapabilities("alpha", mc, key)
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, before.ModTime().Equal(after.ModTime()), "unchanged discovery rewrites nothing")

	// A changed discovery refreshes the auto fields.
	seedCapabilities(t, s, "alpha", mc, config.ModelCapConfig{
		In:      []string{"text"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 65536,
	})
	s.capcompat.Forget(key)
	s.persistDiscoveredCapabilities("alpha", mc, key)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "in: [text]")
	assert.NotContains(t, text, "image", "the auto field followed discovery")
	assert.Contains(t, text, "context: 8192", "the manual field is untouched")
}

func TestCapWriteback_PersistDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(writebackFixture), 0o644))
	s := writebackTestServer(t, path, false)

	mc := s.cfg.Models["alpha"]
	key := capcompat.LocalKey("alpha", mc)
	seedCapabilities(t, s, "alpha", mc, config.ModelCapConfig{In: []string{"text", "image"}})

	s.persistDiscoveredCapabilities("alpha", mc, key)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "llama-swap auto", "write-back off leaves the file alone")
	assert.Empty(t, s.pendingFields("alpha"))
}

func TestCapWriteback_Accept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(writebackFixture), 0o644))
	s := writebackTestServer(t, path, true)

	mc := s.cfg.Models["alpha"]
	key := capcompat.LocalKey("alpha", mc)
	seedCapabilities(t, s, "alpha", mc, config.ModelCapConfig{
		In:      []string{"text", "image"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 32768,
	})
	s.persistDiscoveredCapabilities("alpha", mc, key)
	require.NotEmpty(t, s.pendingFields("alpha"))

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/capabilities/alpha/accept", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["reloading"], "accepting is a save, not an apply")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	assert.NotContains(t, text, "llama-swap auto", "the marker is stripped")
	assert.Contains(t, text, "in: [text, image]", "the values are kept")
	assert.Contains(t, text, "context: 8192")
	assert.Empty(t, s.pendingFields("alpha"))
}

func TestCapWriteback_AcceptWithoutMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(writebackFixture), 0o644))
	s := writebackTestServer(t, path, true)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/capabilities/alpha/accept", nil))
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestCapWriteback_Discard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(writebackFixture), 0o644))
	s := writebackTestServer(t, path, true)

	mc := s.cfg.Models["alpha"]
	key := capcompat.LocalKey("alpha", mc)
	seedCapabilities(t, s, "alpha", mc, config.ModelCapConfig{
		In:      []string{"text", "image"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 32768,
	})
	s.persistDiscoveredCapabilities("alpha", mc, key)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/capabilities/alpha/discard", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	assert.NotContains(t, text, "llama-swap auto")
	assert.NotContains(t, text, "in:")
	assert.NotContains(t, text, "tools: true")
	assert.Contains(t, text, "context: 8192", "the manual field survives")
	assert.Empty(t, s.pendingFields("alpha"))

	declined, found := s.capcompat.Declined(context.Background(), key)
	require.True(t, found)
	assert.ElementsMatch(t, []string{"in", "out", "tools"}, declined)

	// The values stop being advertised at once.
	caps := s.resolveCapabilities(context.Background(), "alpha", mc)
	assert.Empty(t, caps.In)
	assert.False(t, caps.Tools)
	assert.Equal(t, 8192, caps.Context)
}

func TestCapWriteback_ReloadAndStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(writebackFixture), 0o644))
	s := writebackTestServer(t, path, false)
	reloads := 0
	s.SetReloadFunc(func() { reloads++ })

	// Nothing saved yet: not dirty.
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config/status", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"dirty":false`)

	// A save-only write leaves the file ahead of the running server.
	body := `{"yaml": "name: Alpha New\ncmd: echo alpha ${PORT}\n", "reload": false}`
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/models/config/alpha", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["reloading"])
	assert.Equal(t, 0, reloads)
	assert.True(t, s.ConfigDirty())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Alpha New")

	// The save announces itself with a ConfigSavedEvent.
	received := make(chan swaputil.ConfigSavedEvent, 1)
	cancel := event.On(func(e swaputil.ConfigSavedEvent) { received <- e })
	defer cancel()
	body = `{"yaml": "name: Alpha Again\ncmd: echo alpha ${PORT}\n", "reload": false}`
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/models/config/alpha", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code)
	select {
	case ev := <-received:
		assert.Equal(t, "alpha", ev.ModelID)
	case <-time.After(time.Second):
		t.Fatal("no ConfigSavedEvent after a save-only write")
	}

	// The status endpoint now reports dirty.
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config/status", nil))
	assert.Contains(t, w.Body.String(), `"dirty":true`)

	// Legacy callers omit reload and get the all-in-one behavior.
	body = `{"yaml": "name: Alpha Legacy\ncmd: echo alpha ${PORT}\n"}`
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/models/config/alpha", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 1, reloads)

	// Apply: an explicit reload.
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/reload", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 2, reloads)
}

func TestCapWriteback_SetConfigFileRescansPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	marked := `models:
  alpha:
    name: Alpha
    cmd: echo alpha
    # llama-swap auto (2026-02-14) in=[text,image]
    capabilities:
      in: [text, image]
      context: 8192
`
	require.NoError(t, os.WriteFile(path, []byte(marked), 0o644))

	cfg := config.Config{
		Models: map[string]config.ModelConfig{
			"alpha": {Name: "Alpha", Cmd: "echo alpha", Capabilities: config.ModelCapConfig{Context: 8192}},
		},
	}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
	s.SetConfigFile(path)

	assert.ElementsMatch(t, []string{"in"}, s.pendingFields("alpha"))
}
