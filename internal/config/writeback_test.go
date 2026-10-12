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

// A brand-new config being built from the UI starts as an empty (or
// comment-only) file. Writing the first model into it must work and must
// produce a valid config, rather than "config is not a valid YAML document".
func TestWriteback_PatchModelYAML_EmptyAndCommentOnlyFiles(t *testing.T) {
	replacement := "cmd: |\n  /usr/bin/new-server --jinja --port ${PORT} -m /models/x.gguf\n" +
		"ttl: 600\nname: first model\n"

	// A leading tab is not valid YAML whitespace by spec, so the blank-file
	// cases use spaces only.
	for name, input := range map[string]string{
		"empty":          "",
		"newline":        "\n",
		"spaces":         "   \n  \n",
		"comment only":   "# my llama-swap config\n",
		"multi comment":  "# llama-swap\n# built in the UI\n\n",
		"crlf empty":     "\r\n",
		"comment + CRLF": "# note\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			patched, err := PatchModelYAML([]byte(input), "first-model", replacement)
			require.NoError(t, err)

			out := string(patched)
			assert.Contains(t, out, "models:")
			assert.Contains(t, out, "first-model")

			cfg, err := LoadConfigFromReader(strings.NewReader(out))
			require.NoError(t, err)
			require.Contains(t, cfg.Models, "first-model")
			assert.Equal(t, 600, cfg.Models["first-model"].UnloadAfter)
			assert.Equal(t, "first model", cfg.Models["first-model"].Name)

			// the synthesized document is re-readable by the write-back path
			block, err := ExtractModelYAML(patched, "first-model")
			require.NoError(t, err)
			assert.Contains(t, block, "${PORT}")
		})
	}
}

// Comments in an otherwise empty file must not be swallowed when the first
// model is appended after them.
func TestWriteback_PatchModelYAML_EmptyFileKeepsLeadingComment(t *testing.T) {
	patched, err := PatchModelYAML([]byte("# my llama-swap config\n"), "alpha", "cmd: echo a ${PORT}\n")
	require.NoError(t, err)
	assert.Contains(t, string(patched), "# my llama-swap config")
	assert.Contains(t, string(patched), "models:")
}

// Reads and renames must still refuse a file with no models section rather
// than inventing one: only the append path creates the section.
func TestWriteback_MissingModelsSectionIsStillAnErrorOutsideAppend(t *testing.T) {
	empty := []byte("healthCheckTimeout: 120\n")

	_, err := ExtractModelYAML(empty, "alpha")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrModelNotInFile)

	_, err = SetModelNameYAML(empty, "alpha", "A")
	require.Error(t, err)

	_, err = RenameModelYAML(empty, "alpha", "beta")
	require.Error(t, err)
}

// A non-mapping root (e.g. a stray list) is a real error, not a blank config.
func TestWriteback_NonMappingRootStillRejected(t *testing.T) {
	_, err := PatchModelYAML([]byte("- one\n- two\n"), "alpha", "cmd: echo a ${PORT}\n")
	require.Error(t, err)

	_, err = PatchModelYAML([]byte("just a scalar"), "alpha", "cmd: echo a ${PORT}\n")
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

// renameTestConfig exercises every reference site RenameModelYAML must
// rewrite, plus every site it must leave alone (globs, comments, cmd
// strings, peer model lists, other models' aliases).
const renameTestConfig = `# top comment
healthCheckTimeout: 120

models:
  # section banner for old-name
  old-name:
    cmd: |
      /usr/bin/old-name-server --port ${PORT} -m "old-name.gguf"
    ttl: 300
    aliases: [old-alias]
  beta:
    cmd: echo beta ${PORT}
    aliases: [old-name, keep-me]

listeners:
  - name: scoped
    listen: 127.0.0.1:8091
    models: [old-name, keep*, other]

selectors:
  sel1:
    strategy: warm
    targets: [old-name]

profiles:
  p1:
    pins:
      alpha: old-name
      old-name: beta

hooks:
  on_startup:
    preload: [old-name]

routing:
  router:
    use: matrix
    settings:
      matrix:
        vars:
          v1: old-name
        evict_costs:
          old-name: 5
          v1: 2
        sets:
          main: "old-name | beta"
  scheduler:
    settings:
      fifo:
        priority:
          old-name: 10
          beta: 5

peers:
  remote:
    proxy: http://10.0.0.2:8080
    models: [old-name]
`

func TestWriteback_RenameModelYAML_RenamesKeyAndReferences(t *testing.T) {
	patched, err := RenameModelYAML([]byte(renameTestConfig), "old-name", "new-name")
	require.NoError(t, err)
	out := string(patched)

	// still loads and the ID moved
	cfg, err := LoadConfigFromReader(strings.NewReader(out))
	require.NoError(t, err)
	require.Contains(t, cfg.Models, "new-name")
	require.NotContains(t, cfg.Models, "old-name")
	assert.Equal(t, []string{"old-alias"}, cfg.Models["new-name"].Aliases)
	assert.Equal(t, 300, cfg.Models["new-name"].UnloadAfter)

	// references rewritten
	assert.Equal(t, []string{"new-name", "keep*", "other"}, cfg.Listeners[0].Models)
	assert.Equal(t, []string{"new-name"}, cfg.Selectors["sel1"].Targets)
	assert.Equal(t, "new-name", cfg.Profiles["p1"].Pins["alpha"])
	assert.Equal(t, "beta", cfg.Profiles["p1"].Pins["new-name"])
	assert.Equal(t, []string{"new-name"}, cfg.Hooks.OnStartup.Preload)
	matrix := cfg.Routing.Router.Settings.Matrix
	require.NotNil(t, matrix)
	assert.Equal(t, "new-name", matrix.Var["v1"])
	assert.Equal(t, "new-name | beta", matrix.Sets[0].DSL)

	// scheduler fifo priority keys follow the rename
	priority := cfg.Routing.Scheduler.Settings.Fifo.Priority
	assert.Equal(t, 10, priority["new-name"])
	assert.Equal(t, 5, priority["beta"])
	assert.NotContains(t, priority, "old-name")

	// untouched: comments, cmd strings, globs, peer models, other aliases
	assert.Contains(t, out, "# top comment")
	assert.Contains(t, out, "# section banner for old-name")
	assert.Contains(t, out, "/usr/bin/old-name-server")
	assert.Contains(t, out, `"old-name.gguf"`)
	assert.Equal(t, []string{"old-name", "keep-me"}, cfg.Models["beta"].Aliases)
	assert.Equal(t, []string{"old-name"}, cfg.Peers["remote"].Models)
	assert.Contains(t, out, "new-name: 5")
}

func TestWriteback_RenameModelYAML_LegacyTopLevelGroups(t *testing.T) {
	cfgText := "models:\n" +
		"  old-name:\n    cmd: echo old-name ${PORT}\n" +
		"  beta:\n    cmd: echo beta ${PORT}\n\n" +
		"groups:\n  g1:\n    members: [old-name, beta]\n"

	patched, err := RenameModelYAML([]byte(cfgText), "old-name", "new-name")
	require.NoError(t, err)

	cfg, err := LoadConfigFromReader(strings.NewReader(string(patched)))
	require.NoError(t, err)
	require.Contains(t, cfg.Models, "new-name")
	assert.Equal(t, []string{"new-name", "beta"}, cfg.Groups["g1"].Members)
}

func TestWriteback_RenameModelYAML_Errors(t *testing.T) {
	_, err := RenameModelYAML([]byte(writebackTestConfig), "missing", "x")
	assert.ErrorIs(t, err, ErrModelNotInFile)

	_, err = RenameModelYAML([]byte(writebackTestConfig), "alpha", "alpha")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same as the current model ID")
}

func TestWriteback_replaceDSLIdentifier(t *testing.T) {
	assert.Equal(t, "new | b", replaceDSLIdentifier("a | b", "a", "new"))
	assert.Equal(t, "new | new", replaceDSLIdentifier("a | a", "a", "new"))
	// whole tokens only: "ab" and "a-b" must survive a rename of "a"
	assert.Equal(t, "ab | a-b", replaceDSLIdentifier("ab | a-b", "a", "new"))
	assert.Equal(t, "b | c", replaceDSLIdentifier("a | c", "a", "b"))
	// namespaced refs are a different token and never match
	assert.Equal(t, "a & +auto:orphans", replaceDSLIdentifier("a & +auto:orphans", "b", "new"))
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
