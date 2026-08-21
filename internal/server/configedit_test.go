package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const editTestConfig = `# editable test config
models:
  alpha:
    cmd: echo alpha ${PORT}
    ttl: 300
  beta:
    cmd: echo beta ${PORT}
`

func editTestServer(t *testing.T, cfgPath string) *Server {
	t.Helper()

	cfg, err := config.LoadConfigFromReader(strings.NewReader(editTestConfig))
	require.NoError(t, err)
	alpha := cfg.Models["alpha"]
	alpha.UnloadAfter = 300
	cfg.Models["alpha"] = alpha

	s := newTestServer(newStubRouter([]string{"alpha", "beta"}, "ok"), newStubRouter(nil, ""))
	s.cfg = cfg
	if cfgPath != "" {
		s.SetConfigFile(cfgPath)
		// mimic main()'s hot-reload: re-read config from disk into the server
		s.SetReloadFunc(func() {
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				return
			}
			if newCfg, err := config.LoadConfigFromReader(strings.NewReader(string(data))); err == nil {
				s.cfg = newCfg
			}
		})
	}
	return s
}

func TestConfigEdit_Get_ReturnsRawBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("# editable test config\n"+editTestConfig), 0o644))

	s := editTestServer(t, path)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/models/config/alpha", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		ModelID  string `json:"model_id"`
		Editable bool   `json:"editable"`
		YAML     string `json:"yaml"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Editable)
	assert.Equal(t, "alpha", resp.ModelID)
	assert.Contains(t, resp.YAML, "echo alpha ${PORT}")
}

func TestConfigEdit_Get_NotEditableWithoutFile(t *testing.T) {
	s := editTestServer(t, "")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/models/config/alpha", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["editable"])
	assert.NotEmpty(t, resp["reason"])
}

func TestConfigEdit_Put_PatchesAndTriggersReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "# header comment\n" + editTestConfig
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))

	s := editTestServer(t, path)

	body := `{"yaml": "cmd: echo patched ${PORT}\nttl: 999\nmetadata:\n  llamaswap:\n    note: hi\n"}`
	req := httptest.NewRequest(http.MethodPut, "/api/models/config/alpha", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "# header comment")
	assert.Contains(t, string(onDisk), "echo patched ${PORT}")
	assert.NotContains(t, string(onDisk), "ttl: 300\n")

	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Contains(t, string(bak), "echo alpha ${PORT}")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestConfigEdit_Put_RejectsInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfig), 0o644))

	s := editTestServer(t, path)

	cases := []string{
		`{"yaml": "- not a mapping"}`,
	}
	for i, body := range cases {
		req := httptest.NewRequest(http.MethodPut, "/api/models/config/beta", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		assert.NotEqual(t, http.StatusOK, w.Code, "case %d should fail", i)
	}

	// empty cmd rejected
	req := httptest.NewRequest(http.MethodPut, "/api/models/config/alpha", strings.NewReader(`{"yaml": "ttl: 5\n"}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// on-disk file untouched by failures
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "echo beta ${PORT}")
}

func TestListenersAPI_GetAndPut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfig), 0o644))

	s := editTestServer(t, path)
	s.SetListenerStatusFunc(func() []ListenerRuntimeStatus {
		return []ListenerRuntimeStatus{
			{Name: "port-8091", Listen: "127.0.0.1:8091", Bound: true},
			{Name: "roleplay", Listen: "127.0.0.1:8092", Bound: false, Error: "bind: address already in use"},
		}
	})

	// PUT two listeners
	body := `{"listeners": [
	  {"name": "roleplay", "listen": "127.0.0.1:8092", "models": ["beta"]},
	  {"listen": "127.0.0.1:8091", "models": ["al*"]}
	]}`
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/listeners", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "listeners:")
	assert.Contains(t, string(onDisk), "roleplay")

	// reload runs asynchronously; wait for the server config to pick it up
	require.Eventually(t, func() bool {
		return len(s.cfg.Listeners) == 2
	}, 2*time.Second, 10*time.Millisecond)

	// GET merges config with runtime status
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/listeners", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Listeners []struct {
			Name   string `json:"name"`
			Listen string `json:"listen"`
			Bound  bool   `json:"bound"`
			Error  string `json:"error,omitempty"`
		} `json:"listeners"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Listeners, 2)

	byAddr := map[string]struct {
		Bound bool
		Error string
	}{}
	for _, l := range resp.Listeners {
		byAddr[l.Listen] = struct {
			Bound bool
			Error string
		}{l.Bound, l.Error}
		assert.NotEmpty(t, l.Name) // normalized default names applied
	}
	assert.True(t, byAddr["127.0.0.1:8091"].Bound)
	assert.False(t, byAddr["127.0.0.1:8092"].Bound)
	assert.Contains(t, byAddr["127.0.0.1:8092"].Error, "address already in use")
}

func TestListenersAPI_PutRejectsDuplicates(t *testing.T) {
	s := editTestServer(t, "")
	body := `{"listeners": [
	  {"listen": "127.0.0.1:9000"},
	  {"listen": "localhost:9000"}
	]}`
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/listeners", strings.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
