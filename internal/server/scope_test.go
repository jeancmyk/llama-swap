package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestScope_NilAllowsEverything(t *testing.T) {
	var sc *ListenerScope
	assert.True(t, sc.Allows("anything"))
}

func TestScope_GlobMatching(t *testing.T) {
	sc := NewListenerScope(config.ListenerConfig{
		Name:   "roleplay",
		Listen: "127.0.0.1:8091",
		Models: []string{"cydonia-*", "*safeword*", "code-*"},
	})

	assert.True(t, sc.Allows("cydonia-24b-v4.2.0-i1-c108032"))
	assert.True(t, sc.Allows("forgotten-safeword-24b-v4.0-i1-c32768"))
	assert.True(t, sc.Allows("code-qwen3.6-35b-a3b-mtp-c207872"))
	assert.False(t, sc.Allows("gemma-4-e4b-c131072"))

	// alias fallback: requested alias OR resolved ID both checked
	assert.True(t, sc.Allows("some-alias", "cydonia-24b"))
	assert.False(t, sc.Allows("some-alias", "gemma-4-e4b"))
}

func TestScope_EmptyPatternsDenyAll(t *testing.T) {
	sc := NewListenerScope(config.ListenerConfig{Name: "empty", Listen: ":1"})
	assert.False(t, sc.Allows("anything"))
}

func TestScope_ListingFilter(t *testing.T) {
	sc := NewListenerScope(config.ListenerConfig{Models: []string{"public*"}})
	assert.True(t, scopeAllowsRecord(sc, "public-model", nil))
	assert.True(t, scopeAllowsRecord(sc, "private-model", []string{"public-alias"}))
	assert.False(t, scopeAllowsRecord(sc, "private-model", []string{"other-alias"}))
	assert.True(t, scopeAllowsRecord(nil, "private-model", nil))
}

func TestScope_DispatchRejectedForForeignModels(t *testing.T) {
	cfg := config.Config{}
	cfg.Models = map[string]config.ModelConfig{
		"alpha": {Cmd: "echo ${PORT}"},
		"beta":  {Cmd: "echo ${PORT}"},
	}
	s := newTestServer(newStubRouter([]string{"alpha", "beta"}, "ok"), newStubRouter(nil, ""))
	s.cfg = cfg

	scope := NewListenerScope(config.ListenerConfig{Name: "scoped", Listen: ":1", Models: []string{"alpha"}})
	handler := func(model string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(WithScope(req.Context(), scope))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w
	}

	assert.Equal(t, http.StatusOK, handler("alpha").Code)

	rejected := handler("beta")
	assert.Equal(t, http.StatusNotFound, rejected.Code)
}

func TestScope_BareStarMatchesSlashIDs(t *testing.T) {
	// The default listener is attached with a bare "*": slash-containing
	// IDs (vendor/model style, or any alias containing "/") must still be
	// allowed on it, unlike path.Match's default "*" behavior.
	all := &ListenerScope{Name: "default", Listen: "127.0.0.1:8080", Patterns: []string{"*"}}
	assert.True(t, all.Allows("qwen3"))
	assert.True(t, all.Allows("vendor/model"))
	assert.True(t, all.Allows("peer1:qwen3"))
	assert.False(t, all.Allows(""))

	// Other globs keep path.Match semantics: a "*" inside a pattern never
	// crosses "/", so a whole vendor namespace needs "vendor/*".
	vendor := &ListenerScope{Name: "v", Listen: ":1", Patterns: []string{"vendor/*"}}
	assert.True(t, vendor.Allows("vendor/model"))
	assert.False(t, vendor.Allows("other/model"))

	flat := &ListenerScope{Name: "f", Listen: ":1", Patterns: []string{"qwen*"}}
	assert.True(t, flat.Allows("qwen3"))
	assert.False(t, flat.Allows("vendor/qwen3"))
}
