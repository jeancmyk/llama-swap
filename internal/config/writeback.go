package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// ErrModelNotInFile is returned when a model ID has no entry in the parsed
// YAML document (e.g. it comes from -config-dir or was never defined).
var ErrModelNotInFile = errors.New("model not found in config file")

// yamlNodeRoundTrip parses data into a document node. yaml.v3 keeps comments
// (head/line/foot) and scalar styles (literal blocks, quoting) on the node
// tree, so re-encoding preserves the parts of the file we do not touch.
//
// An empty file, a whitespace-only file and a comment-only file all parse to
// a null document (Kind 0, no content). That is a legitimate starting point
// for a config being built from scratch — the UI's "new model" flow writes
// the first model into exactly such a file — so those become an empty
// mapping here instead of an error. Leading comments are salvaged from the
// raw bytes by leadingComments; see the note there for why they cannot come
// from the parsed node tree.
func yamlNodeRoundTrip(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if doc.Kind == 0 {
		// null document: empty or comments only. Start from an empty mapping.
		// yaml.v3 drops comments attached to a null document (there is no
		// node for them to belong to), so leading comment lines are salvaged
		// from the raw text and re-attached as the root's head comment.
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: leadingComments(data)}
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{root},
		}
		return &doc, nil
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

// leadingComments collects the comment lines at the top of a document that
// yaml.v3 discarded, joined with newlines. Blank lines between comments are
// dropped: the emitter re-inserts comment lines one per line and keeps no
// position information, so blank spacing cannot be reproduced faithfully.
func leadingComments(data []byte) string {
	var lines []string
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			// Real content: this is not a null document after all, and the
			// parser would have kept it. Stop rather than guess.
			return ""
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
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

// ensureModelsMapping is findModelsMapping for the append path: when the file
// has no `models:` section yet (an empty or brand-new config being built from
// the UI) the section is created rather than reported as an error. Callers
// that must not mutate the document — reads, renames, removals — keep using
// findModelsMapping so a missing section stays a real error there.
func ensureModelsMapping(doc *yaml.Node) (*yaml.Node, error) {
	root := doc.Content[0]
	val, _, ok := findTopLevelKey(root, "models")
	if !ok {
		val = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "models"}
		root.Content = append(root.Content, key, val)
		return val, nil
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
	models, err := ensureModelsMapping(doc)
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

// SetModelNameYAML sets (or clears with an empty string) the `name` field
// of models.<modelID> in data and returns the patched document bytes.
// Everything else in the document — comments, ordering, scalar styles — is
// preserved because only the single name value node is swapped in place.
// Returns ErrModelNotInFile when the model ID does not exist in this
// document.
func SetModelNameYAML(data []byte, modelID, name string) ([]byte, error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, err
	}
	models, err := findModelsMapping(doc)
	if err != nil {
		return nil, err
	}
	_, val, _, found := findModelEntry(models, modelID)
	if !found {
		return nil, ErrModelNotInFile
	}
	if val.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("model %q is not a mapping of settings", modelID)
	}

	if name == "" {
		// clearing: drop the name key entirely so no stale value remains
		for i := 0; i+1 < len(val.Content); i += 2 {
			if val.Content[i].Value == "name" {
				val.Content = append(val.Content[:i], val.Content[i+2:]...)
				break
			}
		}
	} else {
		nameNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
		replaced := false
		for i := 0; i+1 < len(val.Content); i += 2 {
			if val.Content[i].Value == "name" {
				// keep the key node (with its comments); swap only the value
				val.Content[i+1] = nameNode
				replaced = true
				break
			}
		}
		if !replaced {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"}
			val.Content = append(val.Content, keyNode, nameNode)
		}
	}

	return encodeNode(doc)
}

// RenameModelYAML renames the models.<oldID> key to models.<newID> in data
// and rewrites every exact reference to oldID that the config loader
// resolves to a model:
//
//   - listeners[].models (glob patterns are never touched)
//   - routing.router.settings.groups[].members and the legacy top-level
//     groups[].members
//   - routing.scheduler.settings.fifo.priority keys
//   - selectors[].targets
//   - profiles[].pins keys and values
//   - hooks.on_startup.preload
//   - matrix vars values, evict_costs keys that are model IDs (var names are
//     never touched), and whole identifier tokens in sets DSL expressions
//
// Everything else in the document is left byte-identical where yaml.v3's
// round-trip allows it: comments, key ordering and scalar styles survive
// because only the key node and the referenced scalar nodes are swapped in
// place. Strings outside the reference lists (cmd, proxy, peer model lists,
// comments, and aliases of other models) are never rewritten: an alias that
// happens to equal the old ID is an independent name owned by that model,
// and rewriting it would hand the renamed model's new ID to a different
// model. Returns ErrModelNotInFile when oldID does not exist in this
// document.
func RenameModelYAML(data []byte, oldID, newID string) ([]byte, error) {
	if oldID == newID {
		return nil, fmt.Errorf("new ID is the same as the current model ID")
	}

	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, err
	}
	models, err := findModelsMapping(doc)
	if err != nil {
		return nil, err
	}
	key, _, _, found := findModelEntry(models, oldID)
	if !found {
		return nil, ErrModelNotInFile
	}
	key.Value = newID

	root := doc.Content[0]

	if listeners, _, ok := findTopLevelKey(root, "listeners"); ok && listeners.Kind == yaml.SequenceNode {
		for _, item := range listeners.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			if mv, _, ok := findTopLevelKey(item, "models"); ok {
				rewriteSequenceScalars(mv, oldID, newID)
			}
		}
	}
	rewriteGroupMapping(root, "groups", oldID, newID)
	rewriteSelectorTargets(root, oldID, newID)
	rewriteProfilePins(root, oldID, newID)
	if routing, _, ok := findTopLevelKey(root, "routing"); ok && routing.Kind == yaml.MappingNode {
		if router, _, ok := findTopLevelKey(routing, "router"); ok && router.Kind == yaml.MappingNode {
			if settings, _, ok := findTopLevelKey(router, "settings"); ok && settings.Kind == yaml.MappingNode {
				rewriteGroupMapping(settings, "groups", oldID, newID)
				rewriteMatrixBlock(settings, "matrix", oldID, newID)
			}
		}
		if scheduler, _, ok := findTopLevelKey(routing, "scheduler"); ok && scheduler.Kind == yaml.MappingNode {
			rewriteFifoPriority(scheduler, oldID, newID)
		}
	}
	if hooks, _, ok := findTopLevelKey(root, "hooks"); ok && hooks.Kind == yaml.MappingNode {
		if onStartup, _, ok := findTopLevelKey(hooks, "on_startup"); ok && onStartup.Kind == yaml.MappingNode {
			if pre, _, ok := findTopLevelKey(onStartup, "preload"); ok {
				rewriteSequenceScalars(pre, oldID, newID)
			}
		}
	}
	rewriteMatrixBlock(root, "matrix", oldID, newID)

	return encodeNode(doc)
}

// rewriteFifoPriority renames an exact oldID key inside
// scheduler.settings.fifo.priority (the values are integers, so only keys
// can reference a model).
func rewriteFifoPriority(scheduler *yaml.Node, oldID, newID string) {
	settings, _, ok := findTopLevelKey(scheduler, "settings")
	if !ok || settings.Kind != yaml.MappingNode {
		return
	}
	fifo, _, ok := findTopLevelKey(settings, "fifo")
	if !ok || fifo.Kind != yaml.MappingNode {
		return
	}
	priority, _, ok := findTopLevelKey(fifo, "priority")
	if !ok || priority.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(priority.Content); i += 2 {
		if priority.Content[i].Value == oldID {
			priority.Content[i].Value = newID
		}
	}
}

// rewriteSequenceScalars replaces scalar elements of a sequence that
// exactly equal oldID with newID. Glob patterns can never equal a model ID,
// so they are left alone by the exact match.
func rewriteSequenceScalars(seq *yaml.Node, oldID, newID string) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range seq.Content {
		if item.Kind == yaml.ScalarNode && item.Value == oldID {
			item.Value = newID
		}
	}
}

// rewriteGroupMapping rewrites exact oldID members in every group of the
// container's groups: mapping.
func rewriteGroupMapping(container *yaml.Node, groupsKey, oldID, newID string) {
	groups, _, ok := findTopLevelKey(container, groupsKey)
	if !ok || groups.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(groups.Content); i += 2 {
		group := groups.Content[i+1]
		if group.Kind != yaml.MappingNode {
			continue
		}
		if members, _, ok := findTopLevelKey(group, "members"); ok {
			rewriteSequenceScalars(members, oldID, newID)
		}
	}
}

// rewriteSelectorTargets rewrites exact oldID entries in every selector's
// targets list.
func rewriteSelectorTargets(root *yaml.Node, oldID, newID string) {
	selectors, _, ok := findTopLevelKey(root, "selectors")
	if !ok || selectors.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(selectors.Content); i += 2 {
		selector := selectors.Content[i+1]
		if selector.Kind != yaml.MappingNode {
			continue
		}
		if targets, _, ok := findTopLevelKey(selector, "targets"); ok {
			rewriteSequenceScalars(targets, oldID, newID)
		}
	}
}

// rewriteProfilePins rewrites oldID as both pin keys (the rewritten model)
// and pin values (the target model) in every profile.
func rewriteProfilePins(root *yaml.Node, oldID, newID string) {
	profiles, _, ok := findTopLevelKey(root, "profiles")
	if !ok || profiles.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(profiles.Content); i += 2 {
		profile := profiles.Content[i+1]
		if profile.Kind != yaml.MappingNode {
			continue
		}
		pins, _, ok := findTopLevelKey(profile, "pins")
		if !ok || pins.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(pins.Content); j += 2 {
			if pins.Content[j].Kind == yaml.ScalarNode && pins.Content[j].Value == oldID {
				pins.Content[j].Value = newID
			}
			if pins.Content[j+1].Kind == yaml.ScalarNode && pins.Content[j+1].Value == oldID {
				pins.Content[j+1].Value = newID
			}
		}
	}
}

// rewriteMatrixBlock rewrites model ID references inside a matrix block
// nested under container's matrixKey: vars values, evict_costs keys that
// name models (var names are never touched), and whole identifier tokens in
// set DSL expressions.
func rewriteMatrixBlock(container *yaml.Node, matrixKey, oldID, newID string) {
	matrix, _, ok := findTopLevelKey(container, matrixKey)
	if !ok || matrix.Kind != yaml.MappingNode {
		return
	}

	vars, _, varsOK := findTopLevelKey(matrix, "vars")
	if varsOK && vars.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(vars.Content); i += 2 {
			if vars.Content[i+1].Kind == yaml.ScalarNode && vars.Content[i+1].Value == oldID {
				vars.Content[i+1].Value = newID
			}
		}
	}
	if evict, _, ok := findTopLevelKey(matrix, "evict_costs"); ok && evict.Kind == yaml.MappingNode {
		varNames := make(map[string]bool)
		if varsOK && vars.Kind == yaml.MappingNode {
			for i := 0; i < len(vars.Content); i += 2 {
				if vars.Content[i].Kind == yaml.ScalarNode {
					varNames[vars.Content[i].Value] = true
				}
			}
		}
		for i := 0; i+1 < len(evict.Content); i += 2 {
			if evict.Content[i].Kind == yaml.ScalarNode && evict.Content[i].Value == oldID && !varNames[oldID] {
				evict.Content[i].Value = newID
			}
		}
	}
	if sets, _, ok := findTopLevelKey(matrix, "sets"); ok && sets.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(sets.Content); i += 2 {
			if sets.Content[i+1].Kind == yaml.ScalarNode && sets.Content[i+1].Value != "" {
				sets.Content[i+1].Value = replaceDSLIdentifier(sets.Content[i+1].Value, oldID, newID)
			}
		}
	}
}

// isDSLIdentChar mirrors the matrix DSL lexer's identifier grammar.
func isDSLIdentChar(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '-' || ch == '.'
}

// replaceDSLIdentifier replaces every whole identifier token in a matrix DSL
// expression that equals oldID with newID. Only maximal identifier runs are
// considered, so renaming "a" never rewrites "ab" or "a-v2".
func replaceDSLIdentifier(dsl, oldID, newID string) string {
	runes := []rune(dsl)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); {
		if !isDSLIdentChar(runes[i]) {
			out = append(out, runes[i])
			i++
			continue
		}
		start := i
		for i < len(runes) && isDSLIdentChar(runes[i]) {
			i++
		}
		token := string(runes[start:i])
		if token == oldID {
			out = append(out, []rune(newID)...)
		} else {
			out = append(out, runes[start:i]...)
		}
	}
	return string(out)
}

// RemoveModelYAML deletes the models.<modelID> entry from data and returns
// the patched document bytes. Returns ErrModelNotInFile when the model ID
// does not exist in this document.
func RemoveModelYAML(data []byte, modelID string) ([]byte, error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, err
	}
	models, err := findModelsMapping(doc)
	if err != nil {
		return nil, err
	}
	_, _, idx, found := findModelEntry(models, modelID)
	if !found {
		return nil, ErrModelNotInFile
	}
	models.Content = append(models.Content[:idx], models.Content[idx+2:]...)
	return encodeNode(doc)
}

// RemoveModelFromListeners drops exact-match entries of modelID from every
// listener's models list in data. Glob patterns are never touched. The
// second return reports whether anything changed.
func RemoveModelFromListeners(data []byte, modelID string) ([]byte, bool, error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, false, err
	}
	root := doc.Content[0]
	listeners, _, ok := findTopLevelKey(root, "listeners")
	if !ok || listeners.Kind != yaml.SequenceNode {
		return data, false, nil
	}

	changed := false
	for _, item := range listeners.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		modelsVal, _, ok := findTopLevelKey(item, "models")
		if !ok || modelsVal.Kind != yaml.SequenceNode {
			continue
		}
		kept := modelsVal.Content[:0]
		for _, pat := range modelsVal.Content {
			if pat.Kind == yaml.ScalarNode && pat.Value == modelID {
				changed = true
				continue
			}
			kept = append(kept, pat)
		}
		modelsVal.Content = kept
	}
	if !changed {
		return data, false, nil
	}
	out, err := encodeNode(doc)
	return out, true, err
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
