package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const writebackTestConfig = `# top comment
healthCheckTimeout: 120

models:
  # section banner for alpha
  alpha:
    cmd: |
      /usr/bin/alpha-server --port ${PORT} -m "model file"
    ttl: 300

  beta: # inline beta comment
    cmd: echo beta ${PORT}
    metadata:
      llamaswap:
        note: "keep me"
    aliases: [b2]
`

func TestWriteback_ExtractModelYAML(t *testing.T) {
	block, err := ExtractModelYAML([]byte(writebackTestConfig), "alpha")
	require.NoError(t, err)
	assert.Contains(t, block, "alpha-server")
	assert.Contains(t, block, "ttl: 300")

	_, err = ExtractModelYAML([]byte(writebackTestConfig), "missing")
	assert.ErrorIs(t, err, ErrModelNotInFile)
}

func TestWriteback_PatchModelYAML_PreservesRestOfFile(t *testing.T) {
	replacement := `cmd: echo new-alpha ${PORT}
ttl: 999
metadata:
  llamaswap:
    note: patched
`
	patched, err := PatchModelYAML([]byte(writebackTestConfig), "alpha", replacement)
	require.NoError(t, err)
	out := string(patched)

	// untouched parts survive
	assert.Contains(t, out, "# top comment")
	assert.Contains(t, out, "# section banner for alpha")
	assert.Contains(t, out, `note: "keep me"`)
	assert.Contains(t, out, "aliases: [b2]")
	assert.NotContains(t, out, "alpha-server")

	// replaced block applied
	assert.Contains(t, out, "echo new-alpha")
	assert.Contains(t, out, "ttl: 999")

	// still a valid config
	cfg, err := LoadConfigFromReader(strings.NewReader(out))
	require.NoError(t, err)
	require.Contains(t, cfg.Models, "alpha")
	assert.Equal(t, 999, cfg.Models["alpha"].UnloadAfter)
	require.Equal(t, []string{"b2"}, cfg.Models["beta"].Aliases)
}

func TestWriteback_PatchModelYAML_LiteralBlockStyleKept(t *testing.T) {
	literal := "cmd: |\n  /usr/bin/new-server --port ${PORT}\n  --flag one\n"
	patched, err := PatchModelYAML([]byte(writebackTestConfig), "beta", literal)
	require.NoError(t, err)

	// the replacement round-trips as a literal block
	assert.Contains(t, string(patched), "cmd: |")

	block, err := ExtractModelYAML(patched, "beta")
	require.NoError(t, err)
	assert.Contains(t, block, "${PORT}")
}

func TestWriteback_PatchModelYAML_AppendNewModel(t *testing.T) {
	patched, err := PatchModelYAML([]byte(writebackTestConfig), "gamma", "cmd: echo gamma ${PORT}\nttl: 60\n")
	require.NoError(t, err)

	cfg, err := LoadConfigFromReader(strings.NewReader(string(patched)))
	require.NoError(t, err)
	require.Contains(t, cfg.Models, "gamma")
	assert.Equal(t, 60, cfg.Models["gamma"].UnloadAfter)
}

func TestWriteback_PatchModelYAML_RejectsBadReplacement(t *testing.T) {
	_, err := PatchModelYAML([]byte(writebackTestConfig), "alpha", "- just\n- a list")
	require.Error(t, err)

	_, err = PatchModelYAML([]byte(writebackTestConfig), "alpha", "cmd: [unclosed")
	require.Error(t, err)
}

func TestWriteback_SetModelNameYAML(t *testing.T) {
	patched, err := SetModelNameYAML([]byte(writebackTestConfig), "beta", "Beta Two")
	require.NoError(t, err)
	out := string(patched)

	// untouched parts survive
	assert.Contains(t, out, "# top comment")
	assert.Contains(t, out, "# section banner for alpha")
	assert.Contains(t, out, "alpha-server")
	assert.Contains(t, out, `note: "keep me"`)
	assert.Contains(t, out, `beta: # inline beta comment`)
	assert.Contains(t, out, "aliases: [b2]")

	// name applied
	assert.Contains(t, out, "name: Beta Two")

	cfg, err := LoadConfigFromReader(strings.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, "Beta Two", cfg.Models["beta"].Name)
	assert.Empty(t, cfg.Models["alpha"].Name)
	assert.Equal(t, 300, cfg.Models["alpha"].UnloadAfter)
}

func TestWriteback_SetModelNameYAML_ReplacesExisting(t *testing.T) {
	cfgText := `models:
  alpha:
    cmd: echo alpha ${PORT}
    name: Old Name
    ttl: 42
`
	patched, err := SetModelNameYAML([]byte(cfgText), "alpha", "New Name")
	require.NoError(t, err)
	out := string(patched)

	assert.Contains(t, out, "name: New Name")
	assert.NotContains(t, out, "Old Name")
	// sibling settings and ordering preserved
	assert.Contains(t, out, "ttl: 42")

	cfg, err := LoadConfigFromReader(strings.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, "New Name", cfg.Models["alpha"].Name)
	assert.Equal(t, 42, cfg.Models["alpha"].UnloadAfter)
}

func TestWriteback_SetModelNameYAML_ClearRemovesKey(t *testing.T) {
	cfgText := "models:\n  alpha:\n    cmd: echo alpha ${PORT}\n    name: Old Name\n    ttl: 42\n"
	patched, err := SetModelNameYAML([]byte(cfgText), "alpha", "")
	require.NoError(t, err)

	assert.NotContains(t, string(patched), "name:")

	cfg, err := LoadConfigFromReader(strings.NewReader(string(patched)))
	require.NoError(t, err)
	assert.Empty(t, cfg.Models["alpha"].Name)
	assert.Equal(t, 42, cfg.Models["alpha"].UnloadAfter)
}

func TestWriteback_SetModelNameYAML_Errors(t *testing.T) {
	_, err := SetModelNameYAML([]byte(writebackTestConfig), "missing", "x")
	assert.ErrorIs(t, err, ErrModelNotInFile)

	// non-mapping model block cannot carry a name key
	cfgText := "models:\n  alpha: just a scalar\n"
	_, err = SetModelNameYAML([]byte(cfgText), "alpha", "x")
	require.Error(t, err)
}

func TestWriteback_PatchTopLevelKey_SetAndRemove(t *testing.T) {
	listeners := []ListenerConfig{
		{Name: "roleplay", Listen: "127.0.0.1:8091", Models: []string{"beta", "g*"}},
	}
	node, err := ValueToNode(listeners)
	require.NoError(t, err)

	patched, err := PatchTopLevelKey([]byte(writebackTestConfig), "listeners", node)
	require.NoError(t, err)
	out := string(patched)
	assert.Contains(t, out, "listeners:")
	assert.Contains(t, out, "roleplay")
	assert.Contains(t, out, "# top comment")

	cfg, err := LoadConfigFromReader(strings.NewReader(out))
	require.NoError(t, err)
	require.Len(t, cfg.Listeners, 1)
	assert.Equal(t, "roleplay", cfg.Listeners[0].Name)

	removed, changed, err := RemoveTopLevelKey(patched, "listeners")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotContains(t, string(removed), "listeners:")

	_, changed, err = RemoveTopLevelKey(removed, "listeners")
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestWriteback_WriteFileAtomic_BackupAndContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("original: true\n"), 0o644))

	newData := []byte("original: false\n")
	require.NoError(t, WriteFileAtomic(path, newData))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, newData, got)

	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, "original: true\n", string(bak))
}
