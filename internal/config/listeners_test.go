package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListeners_ParseListenAddr(t *testing.T) {
	addr, err := ParseListenAddr("0.0.0.0:8090")
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0:8090", addr)

	addr, err = ParseListenAddr(":9000")
	require.NoError(t, err)
	assert.Equal(t, ":9000", addr)

	addr, err = ParseListenAddr("[::1]:8081")
	require.NoError(t, err)
	assert.Equal(t, "[::1]:8081", addr)

	_, err = ParseListenAddr("")
	assert.Error(t, err)
	_, err = ParseListenAddr("localhost")
	assert.Error(t, err)
	_, err = ParseListenAddr("localhost:99999")
	assert.Error(t, err)
	_, err = ParseListenAddr("localhost:abc")
	assert.Error(t, err)
}

func TestListeners_Validate_OK(t *testing.T) {
	valid := []ListenerConfig{
		{Listen: "127.0.0.1:8091", Models: []string{"a-*"}},
		{Name: "code", Listen: "127.0.0.1:8092", Models: []string{"code-*"}},
	}
	assert.NoError(t, ValidateListeners(valid))
	assert.NoError(t, ValidateListeners(nil))
}

func TestListeners_Validate_Errors(t *testing.T) {
	cases := map[string][]ListenerConfig{
		"missing address":   {{Models: []string{"*"}}},
		"bad address":       {{Listen: "no-port"}},
		"bad port":          {{Listen: "localhost:70000"}},
		"duplicate address": {{Name: "a", Listen: "127.0.0.1:9000"}, {Name: "b", Listen: "127.0.0.1:9000"}},
		"duplicate name":    {{Name: "x", Listen: "127.0.0.1:9001"}, {Name: "x", Listen: "127.0.0.1:9002"}},
		"empty pattern":     {{Listen: "127.0.0.1:9003", Models: []string{"  "}}},
		"bad glob":          {{Listen: "127.0.0.1:9004", Models: []string{"[bad"}}},
	}
	for name, listeners := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, ValidateListeners(listeners))
		})
	}
}

func TestListeners_NormalizeDefaults(t *testing.T) {
	got := NormalizeListenerDefaults([]ListenerConfig{{Listen: "127.0.0.1:8091"}})
	require.Len(t, got, 1)
	assert.Equal(t, "port-8091", got[0].Name)

	assert.Nil(t, NormalizeListenerDefaults(nil))

	named := NormalizeListenerDefaults([]ListenerConfig{{Name: "roleplay", Listen: ":1"}})
	require.Len(t, named, 1)
	assert.Equal(t, "roleplay", named[0].Name)
}

func TestListeners_LoadConfigFromReader(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
listeners:
  - listen: 127.0.0.1:8091
    models: ["cydonia-*"]
models:
  m1:
    cmd: echo one ${PORT}
`))
	require.NoError(t, err)
	require.Len(t, cfg.Listeners, 1)
	assert.Equal(t, "port-8091", cfg.Listeners[0].Name)
	assert.Equal(t, []string{"cydonia-*"}, cfg.Listeners[0].Models)
}
