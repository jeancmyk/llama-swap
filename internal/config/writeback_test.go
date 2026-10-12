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
