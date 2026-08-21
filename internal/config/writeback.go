package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrModelNotInFile is returned when a model ID has no entry in the parsed
// YAML document (e.g. it comes from -config-dir or was never defined).
var ErrModelNotInFile = errors.New("model not found in config file")

// yamlNodeRoundTrip parses data into a document node. yaml.v3 keeps comments
// (head/line/foot) and scalar styles (literal blocks, quoting) on the node
// tree, so re-encoding preserves the parts of the file we do not touch.
func yamlNodeRoundTrip(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("config is not a valid YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root must be a mapping")
	}
	return &doc, nil
}

// findTopLevelKey returns the value node for key in a mapping node.
func findTopLevelKey(mapping *yaml.Node, key string) (*yaml.Node, int, bool) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], i, true
		}
	}
	return nil, -1, false
}

// findModelsMapping locates the top-level `models:` mapping node.
func findModelsMapping(doc *yaml.Node) (*yaml.Node, error) {
	root := doc.Content[0]
	val, _, ok := findTopLevelKey(root, "models")
	if !ok {
		return nil, fmt.Errorf("config file has no `models` section")
	}
	if val.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("`models` in config file is not a mapping")
	}
	return val, nil
}

func findModelEntry(models *yaml.Node, modelID string) (*yaml.Node, *yaml.Node, int, bool) {
	for i := 0; i+1 < len(models.Content); i += 2 {
		key := models.Content[i]
		if key.Value == modelID {
			return key, models.Content[i+1], i, true
		}
	}
	return nil, nil, -1, false
}

// encodeNodes renders nodes through an encoder with llama-swap's standard
// two-space indentation.
func encodeNode(node *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExtractModelYAML renders the raw YAML block of models.<modelID> from data.
// Comments inside the block are preserved. Returns ErrModelNotInFile when the
// model ID does not exist in this document.
func ExtractModelYAML(data []byte, modelID string) (string, error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return "", err
	}
	models, err := findModelsMapping(doc)
	if err != nil {
		return "", err
	}
	_, val, _, found := findModelEntry(models, modelID)
	if !found {
		return "", ErrModelNotInFile
	}
	out, err := encodeNode(val)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// PatchModelYAML replaces (or appends) the models.<modelID> entry with the
// replacement YAML text and returns the patched document bytes. Everything
// outside the replaced value node is preserved byte-for-byte where yaml.v3's
// round-trip allows it: comments, blank-line structure between keys and scalar
// styles all survive because only one subtree is swapped.
func PatchModelYAML(data []byte, modelID, replacement string) ([]byte, error) {
	replVal, err := parseReplacementMapping(replacement)
	if err != nil {
		return nil, err
	}

	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, err
	}
	models, err := findModelsMapping(doc)
	if err != nil {
		return nil, err
	}

	if _, oldVal, idx, found := findModelEntry(models, modelID); found {
		// Keep trailing comments that belonged to the end of the old block so
		// section separators after the model stay attached to something.
		replVal.FootComment = mergeFootComment(oldVal.FootComment, replVal.FootComment)
		models.Content[idx+1] = replVal
	} else {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: modelID}
		models.Content = append(models.Content, keyNode, replVal)
	}

	return encodeNode(doc)
}

// PatchTopLevelKey sets or replaces a top-level key with the given value node,
// appending it when missing. Used for persisting `listeners:` edits.
func PatchTopLevelKey(data []byte, key string, value *yaml.Node) ([]byte, error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]

	if _, idx, ok := findTopLevelKey(root, key); ok {
		old := root.Content[idx+1]
		value.FootComment = mergeFootComment(old.FootComment, value.FootComment)
		root.Content[idx+1] = value
	} else {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
		root.Content = append(root.Content, keyNode, value)
	}

	return encodeNode(doc)
}

// RemoveTopLevelKey deletes a top-level key when present; data otherwise comes
// back unchanged.
func RemoveTopLevelKey(data []byte, key string) ([]byte, bool, error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, false, err
	}
	root := doc.Content[0]
	if _, idx, ok := findTopLevelKey(root, key); ok {
		root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
		out, err := encodeNode(doc)
		return out, true, err
	}
	return data, false, nil
}

// ValueToNode marshals any value into a standalone node tree suitable for
// PatchTopLevelKey.
func ValueToNode(value any) (*yaml.Node, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("value did not produce a YAML document")
	}
	return doc.Content[0], nil
}

// parseReplacementMapping validates replacement text as a standalone mapping
// node.
func parseReplacementMapping(replacement string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(replacement), &doc); err != nil {
		return nil, fmt.Errorf("replacement YAML does not parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("replacement YAML must be a mapping of model settings")
	}
	return doc.Content[0], nil
}

func mergeFootComment(old, new string) string {
	switch {
	case old == "":
		return new
	case new == "":
		return old
	default:
		return new
	}
}

// WriteFileAtomic writes data to path via a temp-file rename. It first copies
// the previous contents to path+".bak" when the file exists. Mode 0644 matches
// typical config file permissions.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".llamaswap-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	info, statErr := os.Stat(path)
	if statErr == nil && info.Mode().IsRegular() {
		if err := os.WriteFile(path+".bak", mustRead(path), info.Mode()); err != nil {
			return fmt.Errorf("write backup file: %w", err)
		}
	}

	if statErr == nil {
		if err := os.Chmod(tmpName, info.Mode()); err != nil {
			return fmt.Errorf("preserve file mode: %w", err)
		}
	} else if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("set file mode: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	tmpName = ""
	return nil
}

func mustRead(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}
