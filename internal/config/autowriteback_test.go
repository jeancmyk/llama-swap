package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAutoWriteback_RenderMarker(t *testing.T) {
	assert.Equal(t, "", RenderAutoMarker(time.Now(), nil))

	// Fields render in canonical order, not input order.
	marker := RenderAutoMarker(time.Date(2026, 2, 14, 12, 0, 0, 0, time.UTC), map[string]string{
		"context": "262144",
		"in":      "[text,image]",
		"out":     "[text]",
		"tools":   "true",
	})
	want := "llama-swap auto (2026-02-14) in=[text,image] out=[text] tools=true context=262144"
	assert.Equal(t, want, marker)
}

func TestAutoWriteback_ParseMarker(t *testing.T) {
	// The marker may sit among the user's own comment lines.
	date, values, ok := ParseAutoMarker("my own comment\n" +
		"llama-swap auto (2026-02-14) in=[text,image] tools=true")
	require.True(t, ok)
	assert.Equal(t, "2026-02-14", date)
	assert.Equal(t, map[string]string{"in": "[text,image]", "tools": "true"}, values)

	// A marker without a recorded date still parses (tolerant of hand edits).
	_, values, ok = ParseAutoMarker("llama-swap auto in=[text] out=[text]")
	require.True(t, ok)
	assert.Equal(t, "[text]", values["in"])
	assert.Equal(t, "[text]", values["out"])

	_, _, ok = ParseAutoMarker("a user note")
	assert.False(t, ok)

	// The prefix alone is not a marker.
	_, _, ok = ParseAutoMarker("llama-swap auto")
	assert.False(t, ok)
}

func TestAutoWriteback_StripMarker(t *testing.T) {
	assert.Equal(t, "my own comment",
		StripAutoMarker("my own comment\nllama-swap auto (2026-02-14) in=[text]"))
	assert.Equal(t, "",
		StripAutoMarker("llama-swap auto (2026-02-14) in=[text]"))
	assert.Equal(t, "kept", StripAutoMarker("kept"))
}

func TestAutoWriteback_CapFieldValue(t *testing.T) {
	c := ModelCapConfig{
		In:      []string{"text", "image"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 1000,
	}
	assert.Equal(t, "[text,image]", CapFieldValue(c, "in"))
	assert.Equal(t, "[text]", CapFieldValue(c, "out"))
	assert.Equal(t, "true", CapFieldValue(c, "tools"))
	assert.Equal(t, "true", CapFieldValue(ModelCapConfig{Reranker: true}, "reranker"))
	assert.Equal(t, "1000", CapFieldValue(c, "context"))
	assert.Equal(t, "", CapFieldValue(c, "reranker"))
	assert.Equal(t, "", CapFieldValue(ModelCapConfig{}, "in"))
	assert.Equal(t, "", CapFieldValue(c, "nope"))
}

func TestAutoWriteback_AutoCapValues(t *testing.T) {
	values := ModelCapConfig{
		In:      []string{"text", "image"},
		Tools:   true,
		Context: 4096,
	}.AutoCapValues()
	assert.Equal(t, map[string]string{
		"in":      "[text,image]",
		"tools":   "true",
		"context": "4096",
	}, values)
}

func TestAutoWriteback_MergeAuto(t *testing.T) {
	discovered := ModelCapConfig{
		In:      []string{"text", "image"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 32768,
	}

	t.Run("fills fields absent from the file", func(t *testing.T) {
		merged, auto := ModelCapConfig{Context: 8192}.MergeAuto(discovered, nil)
		assert.Equal(t, []string{"text", "image"}, merged.In)
		assert.Equal(t, []string{"text"}, merged.Out)
		assert.True(t, merged.Tools)
		assert.Equal(t, 8192, merged.Context, "manual context wins")
		assert.Equal(t, map[string]bool{"in": true, "out": true, "tools": true}, auto)
	})

	t.Run("refreshes marker fields the user did not edit", func(t *testing.T) {
		file := ModelCapConfig{In: []string{"text"}, Context: 8192}
		recorded := map[string]string{"in": "[text]", "context": "8192"}
		merged, auto := file.MergeAuto(discovered, recorded)
		assert.Equal(t, []string{"text", "image"}, merged.In, "marker field follows discovery")
		assert.Equal(t, 32768, merged.Context, "marker context follows discovery")
		assert.True(t, auto["in"])
		assert.True(t, auto["context"])
	})

	t.Run("marker fields the user edited become manual", func(t *testing.T) {
		// The recorded marker values differ from the file values: the user
		// edited both fields by hand.
		file := ModelCapConfig{In: []string{"text"}, Context: 4096}
		recorded := map[string]string{"in": "[text,image]", "context": "8192"}
		merged, auto := file.MergeAuto(discovered, recorded)
		assert.Equal(t, []string{"text"}, merged.In, "user value wins")
		assert.Equal(t, 4096, merged.Context, "user value wins")
		assert.False(t, auto["in"])
		assert.False(t, auto["context"])
	})

	t.Run("discovery can clear a stale marker field", func(t *testing.T) {
		// The discovery no longer reports tools.
		noTools := ModelCapConfig{In: []string{"text"}, Out: []string{"text"}}
		file := ModelCapConfig{Tools: true, Out: []string{"text"}}
		recorded := map[string]string{"tools": "true"}
		merged, auto := file.MergeAuto(noTools, recorded)
		assert.False(t, merged.Tools, "discovery no longer reports tools")
		assert.False(t, auto["tools"])
		// Out was manual all along and is untouched.
		assert.Equal(t, []string{"text"}, merged.Out)
		assert.False(t, auto["out"])
	})

	t.Run("no marker, manual fields only", func(t *testing.T) {
		file := ModelCapConfig{Tools: true, Context: 8192}
		merged, auto := file.MergeAuto(discovered, nil)
		assert.True(t, merged.Tools)
		assert.Equal(t, 8192, merged.Context)
		assert.False(t, auto["tools"], "unmarked fields never become auto")
		assert.False(t, auto["context"])
	})
}

const autoWritebackFixture = `# file level note
models:
  alpha:
    name: Alpha
    cmd: llama-server --model a.gguf
    proxy: http://127.0.0.1:5000
    # hand-tuned context
    capabilities:
      context: 8192
  beta:
    name: Beta
    cmd: llama-server --model b.gguf
    proxy: http://127.0.0.1:5001
`

func TestAutoWriteback_ReadModelCaps(t *testing.T) {
	caps, marker, recorded, err := ReadModelCaps([]byte(autoWritebackFixture), "alpha")
	require.NoError(t, err)
	assert.Equal(t, 8192, caps.Context)
	assert.Empty(t, marker)
	assert.Empty(t, recorded)

	// A model without a capabilities entry yields zero caps, no error.
	caps, marker, recorded, err = ReadModelCaps([]byte(autoWritebackFixture), "beta")
	require.NoError(t, err)
	assert.True(t, caps.Empty())
	assert.Empty(t, marker)
	assert.Empty(t, recorded)

	// A model that is not in the file at all.
	_, _, _, err = ReadModelCaps([]byte(autoWritebackFixture), "nope")
	assert.True(t, errors.Is(err, ErrModelNotInFile))

	// With a marker, both the line and its values come back.
	marked := `models:
  alpha:
    name: Alpha
    cmd: llama-server --model a.gguf
    # hand-tuned context
    # llama-swap auto (2026-02-14) in=[text] out=[text]
    capabilities:
      in: [text]
      out: [text]
      context: 8192
`
	caps, marker, recorded, err = ReadModelCaps([]byte(marked), "alpha")
	require.NoError(t, err)
	assert.Equal(t, "llama-swap auto (2026-02-14) in=[text] out=[text]", marker)
	assert.Equal(t, map[string]string{"in": "[text]", "out": "[text]"}, recorded)
	assert.Equal(t, []string{"text"}, caps.In)
	assert.Equal(t, 8192, caps.Context)
}

func autoWritebackCapsNode(t *testing.T) *yaml.Node {
	t.Helper()
	node, err := ValueToNode(map[string]any{
		"in":      []string{"text", "image"},
		"out":     []string{"text"},
		"tools":   true,
		"context": 8192,
	})
	require.NoError(t, err)
	return node
}

func TestAutoWriteback_SetModelCapabilitiesYAML(t *testing.T) {
	marker := "llama-swap auto (2026-02-14) in=[text,image] out=[text] tools=true context=8192"

	t.Run("replaces an existing block, keeps user comments", func(t *testing.T) {
		patched, err := SetModelCapabilitiesYAML([]byte(autoWritebackFixture), "alpha", autoWritebackCapsNode(t), marker)
		require.NoError(t, err)
		out := string(patched)
		assert.Contains(t, out, "# hand-tuned context")
		assert.Contains(t, out, "# "+marker, "marker becomes a comment line")
		assert.Contains(t, out, "in: [text, image]")
		assert.Contains(t, out, "context: 8192")
		assert.Contains(t, out, "# file level note")
		// The user comment stays above the marker.
		assert.Less(t, strings.Index(out, "hand-tuned context"), strings.Index(out, marker))

		caps, m, recorded, err := ReadModelCaps(patched, "alpha")
		require.NoError(t, err)
		assert.Equal(t, "llama-swap auto (2026-02-14) in=[text,image] out=[text] tools=true context=8192", m)
		assert.Equal(t, map[string]string{
			"in": "[text,image]", "out": "[text]", "tools": "true", "context": "8192",
		}, recorded)
		assert.Equal(t, []string{"text", "image"}, caps.In)
		assert.Equal(t, 8192, caps.Context)
	})

	t.Run("appends a block to a model without one", func(t *testing.T) {
		patched, err := SetModelCapabilitiesYAML([]byte(autoWritebackFixture), "beta", autoWritebackCapsNode(t), marker)
		require.NoError(t, err)
		out := string(patched)
		assert.Contains(t, out, "beta:")
		assert.Contains(t, out, "capabilities:")
		assert.Contains(t, out, marker)
		assert.Contains(t, out, "in: [text, image]")
		// alpha's block is untouched.
		assert.Contains(t, out, "context: 8192")
	})

	t.Run("clears the marker but keeps user comments and values", func(t *testing.T) {
		marked := `models:
  alpha:
    name: Alpha
    cmd: llama-server --model a.gguf
    # hand-tuned context
    # llama-swap auto (2026-02-14) in=[text]
    capabilities:
      in: [text]
`
		patched, err := SetModelCapabilitiesYAML([]byte(marked), "alpha", autoWritebackCapsNode(t), "")
		require.NoError(t, err)
		out := string(patched)
		assert.Contains(t, out, "# hand-tuned context")
		assert.NotContains(t, out, "llama-swap auto")
		caps, m, _, err := ReadModelCaps(patched, "alpha")
		require.NoError(t, err)
		assert.Empty(t, m)
		assert.Equal(t, []string{"text", "image"}, caps.In)
	})

	t.Run("repeated identical writes are byte-identical", func(t *testing.T) {
		once, err := SetModelCapabilitiesYAML([]byte(autoWritebackFixture), "alpha", autoWritebackCapsNode(t), marker)
		require.NoError(t, err)
		again, err := SetModelCapabilitiesYAML(once, "alpha", autoWritebackCapsNode(t), marker)
		require.NoError(t, err)
		assert.Equal(t, string(once), string(again))
	})

	t.Run("missing model", func(t *testing.T) {
		_, err := SetModelCapabilitiesYAML([]byte(autoWritebackFixture), "nope", autoWritebackCapsNode(t), marker)
		assert.True(t, errors.Is(err, ErrModelNotInFile))
	})
}

func TestAutoWriteback_RemoveModelCapabilitiesYAML(t *testing.T) {
	marked := `models:
  alpha:
    name: Alpha
    cmd: llama-server --model a.gguf
    # hand-tuned context
    # llama-swap auto (2026-02-14) in=[text]
    capabilities:
      in: [text]
      context: 8192
    ttl: 300
`
	patched, removed, err := RemoveModelCapabilitiesYAML([]byte(marked), "alpha")
	require.NoError(t, err)
	assert.True(t, removed)
	out := string(patched)
	assert.NotContains(t, out, "capabilities")
	assert.NotContains(t, out, "llama-swap auto")
	assert.Contains(t, out, "# hand-tuned context", "the user comment survives")

	// Without a capabilities key nothing is removed.
	base, removed, err := RemoveModelCapabilitiesYAML([]byte(autoWritebackFixture), "beta")
	require.NoError(t, err)
	assert.False(t, removed)
	assert.Equal(t, autoWritebackFixture, string(base))

	_, _, err = RemoveModelCapabilitiesYAML([]byte(autoWritebackFixture), "nope")
	assert.True(t, errors.Is(err, ErrModelNotInFile))
}

func TestAutoWriteback_MarkerRoundTripPreservesFile(t *testing.T) {
	// The full pipeline a server write-back performs: read, merge, write,
	// re-read — the result must be valid config and keep the user's values.
	data := []byte(autoWritebackFixture)
	fileCaps, _, recorded, err := ReadModelCaps(data, "alpha")
	require.NoError(t, err)

	discovered := ModelCapConfig{
		In:      []string{"text", "image"},
		Out:     []string{"text"},
		Tools:   true,
		Context: 32768,
	}
	merged, auto := fileCaps.MergeAuto(discovered, recorded)
	assert.Equal(t, 8192, merged.Context, "manual context survives the round trip")

	node, err := ValueToNode(map[string]any{
		"in": merged.In, "out": merged.Out, "tools": merged.Tools, "context": merged.Context,
	})
	require.NoError(t, err)
	values := map[string]string{}
	for field, present := range auto {
		if present {
			if v := CapFieldValue(merged, field); v != "" {
				values[field] = v
			}
		}
	}
	patched, err := SetModelCapabilitiesYAML(data, "alpha", node, RenderAutoMarker(time.Date(2026, 2, 14, 0, 0, 0, 0, time.UTC), values))
	require.NoError(t, err)

	// The patched document still loads as a config with the model intact.
	cfg, err := LoadConfigFromReader(strings.NewReader(string(patched)))
	require.NoError(t, err)
	assert.Equal(t, 8192, cfg.Models["alpha"].Capabilities.Context)
	assert.Equal(t, []string{"text", "image"}, cfg.Models["alpha"].Capabilities.In)
	assert.True(t, cfg.Models["alpha"].Capabilities.Tools)
}
