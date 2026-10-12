package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelFiles_ScanDetectsMissing(t *testing.T) {
	dir := t.TempDir()
	exists := filepath.Join(dir, "model.gguf")
	require.NoError(t, os.WriteFile(exists, []byte("x"), 0o644))

	models := map[string]config.ModelConfig{
		"ok":         {Cmd: `echo -m "` + exists + `"`},
		"broken":     {Cmd: `echo -m "` + filepath.Join(dir, "missing.gguf") + `"`},
		"noproxy":    {Cmd: `echo hello`},
		"unparsable": {Cmd: `"unclosed`},
	}

	missing := scanModelFiles(models)
	require.Len(t, missing, 1)
	require.Contains(t, missing, "broken")
	require.Len(t, missing["broken"], 1)
	assert.Equal(t, "-m", missing["broken"][0].Flag)
	assert.Equal(t, filepath.Join(dir, "missing.gguf"), missing["broken"][0].Path)
}

func TestModelFiles_ListingExcludesMissing(t *testing.T) {
	dir := t.TempDir()
	exists := filepath.Join(dir, "model.gguf")
	require.NoError(t, os.WriteFile(exists, []byte("x"), 0o644))

	s := newTestServer(newStubRouter([]string{"good", "bad"}, "ok"), newStubRouter(nil, ""))
	s.cfg = config.Config{
		Models: map[string]config.ModelConfig{
			"good": {Cmd: `echo -m "` + exists + `"`},
			"bad":  {Cmd: `echo -m "` + filepath.Join(dir, "nope.gguf") + `"`},
		},
	}
	s.missingFiles = scanModelFiles(s.cfg.Models)
	s.routes()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	ids := map[string]bool{}
	for _, m := range resp.Data {
		ids[m.ID] = true
	}
	assert.True(t, ids["good"])
	assert.False(t, ids["bad"], "model with missing files must not be listed")
}

func TestModelFiles_StatusFlagsMissing(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "nope.gguf")

	s := newTestServer(newStubRouter([]string{"broken"}, "ok"), newStubRouter(nil, ""))
	s.cfg = config.Config{
		Models: map[string]config.ModelConfig{
			"broken": {Cmd: `echo --model "` + missingPath + `"`},
		},
	}
	s.missingFiles = scanModelFiles(s.cfg.Models)

	status := s.modelStatus()
	require.Len(t, status, 1)
	require.Len(t, status[0].MissingFiles, 1)
	assert.Equal(t, "--model", status[0].MissingFiles[0].Flag)
	assert.Equal(t, missingPath, status[0].MissingFiles[0].Path)
}
