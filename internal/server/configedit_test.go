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

func TestListenersAPI_Get_ReturnsAvailableModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfigListeners), 0o644))

	s := editTestServerRaw(t, editTestConfigListeners, path)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/listeners", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		AvailableModels []string `json:"available_models"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// every configured local model ID is offered, sorted
	assert.Equal(t, []string{"alpha", "beta"}, resp.AvailableModels)
}

// editTestConfigListeners seeds an explicit listeners block: roleplay pins
// beta exactly, port-8091 matches al* by glob.
const editTestConfigListeners = `# editable test config with listeners
models:
  alpha:
    cmd: echo alpha ${PORT}
    ttl: 300
  beta:
    cmd: echo beta ${PORT}
listeners:
  - name: roleplay
    listen: 127.0.0.1:8092
    models:
      - beta
      - al*
`

// editTestServerRaw is editTestServer with caller-provided config text.
func editTestServerRaw(t *testing.T, cfgText, cfgPath string) *Server {
	t.Helper()

	cfg, err := config.LoadConfigFromReader(strings.NewReader(cfgText))
	require.NoError(t, err)

	s := newTestServer(
		newStubRouter(mustModelIDs(t, cfg), "ok"),
		newStubRouter(nil, ""),
	)
	s.cfg = cfg
	if cfgPath != "" {
		s.SetConfigFile(cfgPath)
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

func mustModelIDs(t *testing.T, cfg config.Config) []string {
	t.Helper()
	ids := make([]string, 0, len(cfg.Models))
	for id := range cfg.Models {
		ids = append(ids, id)
	}
	return ids
}

func TestProxy_ConfigAPI_AddModel_LeavesListenersUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfigListeners), 0o644))

	s := editTestServerRaw(t, editTestConfigListeners, path)

	body := `{"yaml": "cmd: echo gamma ${PORT}\nttl: 60\n"}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/config/gamma", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "echo gamma ${PORT}")

	// reload picks up the new model
	require.Eventually(t, func() bool {
		_, ok := s.cfg.Models["gamma"]
		return ok
	}, 2*time.Second, 10*time.Millisecond)

	// the custom listener must NOT have gained gamma; exact and glob
	// patterns stay byte-identical
	require.Len(t, s.cfg.Listeners, 1)
	assert.Equal(t, []string{"beta", "al*"}, s.cfg.Listeners[0].Models)

	// duplicate add rejected
	req = httptest.NewRequest(http.MethodPost, "/api/models/config/alpha", strings.NewReader(body))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestProxy_ConfigAPI_PutModelOutsideFile_Conflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// The running config knows "alpha" (e.g. it came from -config-dir) but
	// the primary file does not contain it: PUT must 409 like the other
	// mutations instead of appending a block that the dir copy outvotes.
	fileText := "healthCheckTimeout: 120\n\nmodels:\n  beta:\n    cmd: echo beta ${PORT}\n"
	require.NoError(t, os.WriteFile(path, []byte(fileText), 0o644))

	s := editTestServerRaw(t, editTestConfig, path)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/models/config/alpha",
		strings.NewReader(`{"yaml": "cmd: echo alpha ${PORT}\n"}`))
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, fileText, string(onDisk), "failed PUT must not touch the file")
}

func TestProxy_ConfigAPI_AddModel_DuplicateInFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// "delta" exists only in the file (e.g. added by a save that has not
	// been applied yet): a second add must 409 instead of appending a
	// second block for the same ID.
	fileText := "models:\n  delta:\n    cmd: echo delta ${PORT}\n"
	require.NoError(t, os.WriteFile(path, []byte(fileText), 0o644))

	s := editTestServerRaw(t, editTestConfig, path)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/models/config/delta",
		strings.NewReader(`{"yaml": "cmd: echo delta2 ${PORT}\n"}`))
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(w, req)
	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
}

func TestProxy_ConfigAPI_RemoveModel_PrunesExactListenerEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfigListeners), 0o644))

	s := editTestServerRaw(t, editTestConfigListeners, path)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/models/config/beta", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(onDisk), "echo beta")
	assert.Contains(t, string(onDisk), "echo alpha")

	// exact entry pruned from the listener, glob pattern preserved
	require.Eventually(t, func() bool {
		_, ok := s.cfg.Models["beta"]
		return !ok
	}, 2*time.Second, 10*time.Millisecond)
	require.Len(t, s.cfg.Listeners, 1)
	assert.Equal(t, []string{"al*"}, s.cfg.Listeners[0].Models)

	// removing an unknown model 404s
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/models/config/beta", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestProxy_ConfigAPI_PatchModelName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfig), 0o644))

	s := editTestServer(t, path)

	req := httptest.NewRequest(http.MethodPatch, "/api/models/config/alpha", strings.NewReader(`{"name": "Alpha Display"}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// the rest of the model block survives the patch
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "name: Alpha Display")
	assert.Contains(t, string(onDisk), "echo alpha ${PORT}")
	assert.Contains(t, string(onDisk), "ttl: 300")
	assert.Contains(t, string(onDisk), "echo beta ${PORT}")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
	assert.Equal(t, "Alpha Display", resp["name"])

	// reload runs asynchronously; wait for the server config to pick it up
	require.Eventually(t, func() bool {
		return s.cfg.Models["alpha"].Name == "Alpha Display"
	}, 2*time.Second, 10*time.Millisecond)

	// rename again over an existing name replaces it
	req = httptest.NewRequest(http.MethodPatch, "/api/models/config/alpha", strings.NewReader(`{"name": "Renamed Again"}`))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	onDisk, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "name: Renamed Again")
	assert.NotContains(t, string(onDisk), "Alpha Display")
}

func TestProxy_ConfigAPI_PatchModelName_Clears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfgText := "models:\n  alpha:\n    cmd: echo alpha ${PORT}\n    name: Old\n"
	require.NoError(t, os.WriteFile(path, []byte(cfgText), 0o644))

	s := editTestServerRaw(t, cfgText, path)

	req := httptest.NewRequest(http.MethodPatch, "/api/models/config/alpha", strings.NewReader(`{"name": ""}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(onDisk), "name:")
	assert.Contains(t, string(onDisk), "echo alpha ${PORT}")
}

func TestProxy_ConfigAPI_PatchModelName_Errors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editTestConfig), 0o644))

	s := editTestServer(t, path)

	// unknown model
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/models/config/nope", strings.NewReader(`{"name": "x"}`)))
	assert.Equal(t, http.StatusNotFound, w.Code)

	// bad JSON body
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/models/config/alpha", strings.NewReader(`not json`)))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// no config file configured
	sNoFile := editTestServer(t, "")
	w = httptest.NewRecorder()
	sNoFile.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/models/config/alpha", strings.NewReader(`{"name": "x"}`)))
	assert.Equal(t, http.StatusConflict, w.Code)

	// on-disk file untouched by failures
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "echo alpha ${PORT}")
	assert.NotContains(t, string(onDisk), "name:")
}

const renameTestConfig = `# header comment
models:
  alpha:
    cmd: echo alpha ${PORT}
    ttl: 300
  beta:
    cmd: echo beta ${PORT}
listeners:
  - name: roleplay
    listen: 127.0.0.1:8092
    models:
      - alpha
      - be*
`

func TestProxy_ConfigAPI_RenameModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(renameTestConfig), 0o644))

	s := editTestServerRaw(t, renameTestConfig, path)

	req := httptest.NewRequest(http.MethodPost, "/api/models/rename/alpha", strings.NewReader(`{"new_id": "gamma"}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
	assert.Equal(t, "alpha", resp["model_id"])
	assert.Equal(t, "gamma", resp["new_id"])

	// the config file was rewritten: key and exact listener reference
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(onDisk)
	assert.Contains(t, out, "# header comment")
	assert.Contains(t, out, "gamma:")
	assert.NotContains(t, out, "alpha:")
	assert.Contains(t, out, "- gamma")
	assert.Contains(t, out, "- be*") // glob untouched
	assert.Contains(t, out, "echo beta ${PORT}")

	// the reload picks up the new ID and drops the old one
	require.Eventually(t, func() bool {
		_, okNew := s.cfg.Models["gamma"]
		_, okOld := s.cfg.Models["alpha"]
		return okNew && !okOld
	}, 2*time.Second, 10*time.Millisecond)

	// the old ID is gone from the API, the new one serves its block
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/models/config/alpha", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)

	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/models/config/gamma", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestProxy_ConfigAPI_RenameModel_Errors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(renameTestConfig), 0o644))

	s := editTestServerRaw(t, renameTestConfig, path)

	// unknown model
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/rename/nope", strings.NewReader(`{"new_id": "x"}`)))
	assert.Equal(t, http.StatusNotFound, w.Code)

	// empty new id
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/rename/alpha", strings.NewReader(`{"new_id": "  "}`)))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// no-op rename
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/rename/alpha", strings.NewReader(`{"new_id": "alpha"}`)))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// invalid characters (model IDs become URL path segments)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/rename/alpha", strings.NewReader(`{"new_id": "has space"}`)))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// collides with an existing model
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/rename/alpha", strings.NewReader(`{"new_id": "beta"}`)))
	assert.Equal(t, http.StatusConflict, w.Code)

	// bad JSON body
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/rename/alpha", strings.NewReader(`not json`)))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// no config file configured
	sNoFile := editTestServerRaw(t, renameTestConfig, "")
	w = httptest.NewRecorder()
	sNoFile.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/models/rename/alpha", strings.NewReader(`{"new_id": "gamma"}`)))
	assert.Equal(t, http.StatusConflict, w.Code)

	// on-disk file untouched by failures
	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(onDisk), "alpha:")
	assert.NotContains(t, string(onDisk), "gamma")
}
