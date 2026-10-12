package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AutoWritebackMarkerPrefix heads the comment line written above a model's
// capabilities: key when llama-swap auto-filled fields into the config file.
// The line is the pending marker: while it is present, the listed fields are
// managed by discovery (refreshed on every successful probe), and the UI
// flags the model as "update pending". The user ends the pending state by
// keeping the values (the marker is stripped) or discarding them (the fields
// are removed).
//
// The marker records the value of every auto-managed field, so a later
// write-back can tell an untouched auto field (refresh it) from one the user
// edited by hand (leave it and stop managing it).
const AutoWritebackMarkerPrefix = "llama-swap auto "

// autoFieldOrder fixes the order fields appear in the marker line.
var autoFieldOrder = []string{"in", "out", "tools", "reranker", "context"}

// RenderAutoMarker renders the pending marker for values keyed by field name.
// Empty values produce no marker.
func RenderAutoMarker(date time.Time, values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, field := range autoFieldOrder {
		if value, ok := values[field]; ok {
			parts = append(parts, field+"="+value)
		}
	}
	return fmt.Sprintf("%s(%s) %s", AutoWritebackMarkerPrefix, date.Format("2006-01-02"), strings.Join(parts, " "))
}

// commentText strips the leading " # " a YAML head comment carries on each
// line, so marker lines can be recognised regardless of how the comment was
// stored.
func commentText(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#") {
		line = strings.TrimLeft(strings.TrimPrefix(line, "#"), " \t")
	}
	return line
}

// ParseAutoMarker extracts the marker line from a head comment, which may also
// carry the user's own comment lines. It returns the recorded date and the
// field values. ok is false when the comment carries no marker.
func ParseAutoMarker(comment string) (date string, values map[string]string, ok bool) {
	for _, line := range strings.Split(comment, "\n") {
		line = commentText(line)
		if !strings.HasPrefix(line, AutoWritebackMarkerPrefix) {
			continue
		}
		rest := line[len(AutoWritebackMarkerPrefix):]
		var tail string
		if strings.HasPrefix(rest, "(") {
			if end := strings.IndexByte(rest, ')'); end > 0 {
				date = rest[1:end]
				rest = rest[end+1:]
			}
		}
		tail = strings.TrimSpace(rest)
		values = make(map[string]string)
		for _, token := range strings.Fields(tail) {
			eq := strings.IndexByte(token, '=')
			if eq <= 0 {
				continue
			}
			values[token[:eq]] = token[eq+1:]
		}
		ok = len(values) > 0
		return date, values, ok
	}
	return "", nil, false
}

// StripAutoMarker removes the marker line from a head comment, keeping any
// user comment lines (with their original "# " prefix intact).
func StripAutoMarker(comment string) string {
	lines := strings.Split(comment, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(commentText(line), AutoWritebackMarkerPrefix) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimPrefix(strings.Join(kept, "\n"), "\n")
}

// AutoValuesEqual reports whether two marker value maps carry the same
// fields and values.
func AutoValuesEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// CapFieldValue renders one capability field the way the marker line does.
// It returns "" for the field's zero value.
func CapFieldValue(c ModelCapConfig, field string) string {
	switch field {
	case "in":
		if len(c.In) == 0 {
			return ""
		}
		return "[" + strings.Join(c.In, ",") + "]"
	case "out":
		if len(c.Out) == 0 {
			return ""
		}
		return "[" + strings.Join(c.Out, ",") + "]"
	case "tools":
		if !c.Tools {
			return ""
		}
		return "true"
	case "reranker":
		if !c.Reranker {
			return ""
		}
		return "true"
	case "context":
		if c.Context == 0 {
			return ""
		}
		return strconv.Itoa(c.Context)
	}
	return ""
}

// AutoCapValues renders every non-zero field of c in marker form.
func (c ModelCapConfig) AutoCapValues() map[string]string {
	values := make(map[string]string)
	for _, field := range autoFieldOrder {
		if v := CapFieldValue(c, field); v != "" {
			values[field] = v
		}
	}
	return values
}

// autoSetEqual reports whether two field sets contain the same fields.
func autoSetEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for field := range a {
		if !b[field] {
			return false
		}
	}
	return true
}

// MergeAuto computes the capabilities block to write into the config file:
// fields the file sets without a marker (manual) always win, fields the file
// sets under a marker whose value is still the recorded one are refreshed
// from discovered, and fields the file does not set are filled from
// discovered. recorded is the parsed marker (nil or empty when the file
// carries no marker). The second return lists the fields left under auto
// management in the result.
func (c ModelCapConfig) MergeAuto(discovered ModelCapConfig, recorded map[string]string) (ModelCapConfig, map[string]bool) {
	merged := c
	auto := make(map[string]bool)

	// in
	if len(c.In) == 0 {
		if len(discovered.In) > 0 {
			merged.In = append([]string(nil), discovered.In...)
			auto["in"] = true
		}
	} else if isAutoField(recorded, "in", "["+strings.Join(c.In, ",")+"]") {
		merged.In = append([]string(nil), discovered.In...)
		if len(merged.In) > 0 {
			auto["in"] = true
		}
	}
	// out
	if len(c.Out) == 0 {
		if len(discovered.Out) > 0 {
			merged.Out = append([]string(nil), discovered.Out...)
			auto["out"] = true
		}
	} else if isAutoField(recorded, "out", "["+strings.Join(c.Out, ",")+"]") {
		merged.Out = append([]string(nil), discovered.Out...)
		if len(merged.Out) > 0 {
			auto["out"] = true
		}
	}

	// tools
	if !c.Tools {
		if discovered.Tools {
			merged.Tools = true
			auto["tools"] = true
		}
	} else if isAutoField(recorded, "tools", "true") {
		merged.Tools = discovered.Tools
		if merged.Tools {
			auto["tools"] = true
		}
	}

	// reranker
	if !c.Reranker {
		if discovered.Reranker {
			merged.Reranker = true
			auto["reranker"] = true
		}
	} else if isAutoField(recorded, "reranker", "true") {
		merged.Reranker = discovered.Reranker
		if merged.Reranker {
			auto["reranker"] = true
		}
	}

	// context
	if c.Context == 0 {
		if discovered.Context > 0 {
			merged.Context = discovered.Context
			auto["context"] = true
		}
	} else if isAutoField(recorded, "context", strconv.Itoa(c.Context)) {
		merged.Context = discovered.Context
		if merged.Context > 0 {
			auto["context"] = true
		}
	}

	return merged, auto
}

func isAutoField(recorded map[string]string, field, fileValue string) bool {
	if len(recorded) == 0 {
		return false
	}
	v, ok := recorded[field]
	return ok && v == fileValue
}

// findMappingKey returns the key and value nodes for key in a mapping node.
func findMappingKey(m *yaml.Node, key string) (keyNode, valNode *yaml.Node, idx int, ok bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil, -1, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1], i, true
		}
	}
	return nil, nil, -1, false
}

// ReadModelCaps reads models.<modelID>.capabilities from data, along with the
// pending marker line above it (markerLine in head-comment form, without the
// leading "#") and the values it records. found is false when the model has
// no capabilities entry; err wraps ErrModelNotInFile for a missing model.
func ReadModelCaps(data []byte, modelID string) (caps ModelCapConfig, markerLine string, recorded map[string]string, err error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return ModelCapConfig{}, "", nil, err
	}
	models, err := findModelsMapping(doc)
	if err != nil {
		return ModelCapConfig{}, "", nil, err
	}
	_, val, _, found := findModelEntry(models, modelID)
	if !found {
		return ModelCapConfig{}, "", nil, ErrModelNotInFile
	}
	if val.Kind != yaml.MappingNode {
		return ModelCapConfig{}, "", nil, fmt.Errorf("model %q is not a mapping of settings", modelID)
	}

	capsKey, capsVal, _, foundKey := findMappingKey(val, "capabilities")
	if !foundKey {
		return ModelCapConfig{}, "", nil, nil
	}
	if err := capsVal.Decode(&caps); err != nil {
		return ModelCapConfig{}, "", nil, fmt.Errorf("decoding capabilities of model %q: %w", modelID, err)
	}
	if capsKey.HeadComment != "" {
		for _, line := range strings.Split(capsKey.HeadComment, "\n") {
			if strings.HasPrefix(commentText(line), AutoWritebackMarkerPrefix) {
				markerLine = commentText(line)
				break
			}
		}
		if markerLine != "" {
			_, recorded, _ = ParseAutoMarker(markerLine)
		}
	}
	return caps, markerLine, recorded, nil
}

// flowifySequences switches sequence nodes inside v to flow style, so
// capability lists render as [text, image] instead of a block list.
func flowifySequences(v *yaml.Node) {
	if v == nil {
		return
	}
	for _, c := range v.Content {
		if c.Kind == yaml.SequenceNode {
			c.Style = yaml.FlowStyle
		} else if c.Kind == yaml.MappingNode {
			flowifySequences(c)
		}
	}
}

// SetModelCapabilitiesYAML replaces (or appends) the capabilities entry of
// models.<modelID> with caps and sets the pending marker line above the key;
// an empty marker clears it. User comment lines already above the key are
// kept, ahead of the marker. Everything else in the document — comments,
// ordering and scalar styles — is preserved where yaml.v3's round-trip
// allows it, because only this one subtree is swapped.
func SetModelCapabilitiesYAML(data []byte, modelID string, caps *yaml.Node, marker string) ([]byte, error) {
	if caps == nil || caps.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("caps must be a YAML mapping node")
	}
	flowifySequences(caps)
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

	capsKey, _, idx, foundKey := findMappingKey(val, "capabilities")
	userHead := ""
	if foundKey {
		userHead = StripAutoMarker(capsKey.HeadComment)
	}
	head := userHead
	if marker != "" {
		if head == "" {
			head = marker
		} else {
			head = head + "\n" + marker
		}
	}

	if foundKey {
		capsKey.HeadComment = head
		val.Content[idx+1] = caps
	} else {
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "capabilities"}
		keyNode.HeadComment = head
		val.Content = append(val.Content, keyNode, caps)
	}

	return encodeNode(doc)
}

// RemoveModelCapabilitiesYAML deletes the capabilities entry of
// models.<modelID> (with its marker line) when present. The second return
// reports whether anything was removed.
func RemoveModelCapabilitiesYAML(data []byte, modelID string) ([]byte, bool, error) {
	doc, err := yamlNodeRoundTrip(data)
	if err != nil {
		return nil, false, err
	}
	models, err := findModelsMapping(doc)
	if err != nil {
		return nil, false, err
	}
	_, val, _, found := findModelEntry(models, modelID)
	if !found {
		return nil, false, ErrModelNotInFile
	}
	if val.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("model %q is not a mapping of settings", modelID)
	}
	if capsKey, _, idx, ok := findMappingKey(val, "capabilities"); ok {
		// The user's own comment lines described this block; the marker
		// itself goes with it. Keep the user lines by attaching them to the
		// key that now stands in its place.
		userHead := ""
		if capsKey.HeadComment != "" {
			userHead = StripAutoMarker(capsKey.HeadComment)
		}
		val.Content = append(val.Content[:idx], val.Content[idx+2:]...)
		if userHead != "" && idx < len(val.Content) {
			nextKey := val.Content[idx]
			if nextKey.HeadComment == "" {
				nextKey.HeadComment = userHead
			} else {
				nextKey.HeadComment = userHead + "\n" + nextKey.HeadComment
			}
		}
		out, err := encodeNode(doc)
		return out, true, err
	}
	return data, false, nil
}

// AutoSetEqual reports whether two field sets contain the same fields.
func AutoSetEqual(a, b map[string]bool) bool {
	return autoSetEqual(a, b)
}

// SortedAutoFields returns the auto field set in marker order, for stable
// rendering and comparisons.
func SortedAutoFields(set map[string]bool) []string {
	fields := make([]string, 0, len(set))
	for _, field := range autoFieldOrder {
		if set[field] {
			fields = append(fields, field)
		}
	}
	// Unknown fields (defensively) keep sorted order after the known ones.
	extra := make([]string, 0)
	for field := range set {
		known := false
		for _, f := range autoFieldOrder {
			if field == f {
				known = true
				break
			}
		}
		if !known {
			extra = append(extra, field)
		}
	}
	sort.Strings(extra)
	return append(fields, extra...)
}
