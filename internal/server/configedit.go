package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
)

// modelConfigResponse is the payload of GET /api/models/config/{model}.
type modelConfigResponse struct {
	ModelID    string `json:"model_id"`
	SourceFile string `json:"source_file,omitempty"`
	Editable   bool   `json:"editable"`
	Reason     string `json:"reason,omitempty"`
	YAML       string `json:"yaml"`
}

type modelConfigRequest struct {
	YAML string `json:"yaml"`
	// Reload controls the save/apply split. Omitted (nil) keeps the legacy
	// all-in-one behavior: the write is followed by a hot reload. An explicit
	// false saves to the file only; the change activates on the next reload
	// (POST /api/reload) or restart.
	Reload *bool `json:"reload,omitempty"`
}

// modelNameRequest is the payload of PATCH /api/models/config/{model}.
type modelNameRequest struct {
	Name   string `json:"name"`
	Reload *bool  `json:"reload,omitempty"`
}

// shouldReload resolves the request reload flag: a missing flag keeps the
// legacy behavior (write + reload).
func shouldReload(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// handleAPIGetModelConfig returns the raw YAML block for a local model from
// the primary config file, plus editability metadata.
func (s *Server) handleAPIGetModelConfig(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model")

	mc, found := s.cfg.Models[modelID]
	if !found {
		swaputil.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q not found", modelID))
		return
	}

	resp := modelConfigResponse{ModelID: modelID, SourceFile: s.configFile}

	if s.configFile == "" {
		resp.Reason = "no primary config file configured; start llama-swap with --config to enable editing"
		resp.YAML = marshalModelConfigFallback(mc)
		swaputil.RespondJSON(w, r, http.StatusOK, resp)
		return
	}

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		resp.Editable = false
		resp.Reason = fmt.Sprintf("cannot read config file: %v", err)
		resp.YAML = marshalModelConfigFallback(mc)
		swaputil.RespondJSON(w, r, http.StatusOK, resp)
		return
	}

	block, err := config.ExtractModelYAML(data, modelID)
	if err != nil {
		if errors.Is(err, config.ErrModelNotInFile) {
			resp.SourceFile = ""
			resp.Reason = "this model is defined outside the primary config file (e.g. via -config-dir) and cannot be edited here"
			resp.YAML = marshalModelConfigFallback(mc)
			swaputil.RespondJSON(w, r, http.StatusOK, resp)
			return
		}
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("extract model block: %v", err))
		return
	}

	resp.Editable = true
	resp.YAML = block
	swaputil.RespondJSON(w, r, http.StatusOK, resp)
}

// handleAPIPutModelConfig patches a model's YAML block in the primary config
// file. By default a hot reload follows; {"reload": false} saves without a
// reload, activating the change on the next POST /api/reload (Apply).
func (s *Server) handleAPIPutModelConfig(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model")

	var req modelConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.YAML) == "" {
		swaputil.SendResponse(w, r, http.StatusBadRequest, `request body must be JSON: {"yaml": "<model settings>"}`)
		return
	}

	if _, ok := s.cfg.Models[modelID]; !ok {
		swaputil.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q not found", modelID))
		return
	}
	if s.configFile == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "config editing is not available: no primary config file")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	// A model defined only in a -config-dir file cannot be edited here:
	// appending its block to the primary file would not take effect (the dir
	// copy wins on merge) and would leave a duplicate behind, so 409 like the
	// other mutation endpoints.
	if _, err := config.ExtractModelYAML(data, modelID); errors.Is(err, config.ErrModelNotInFile) {
		swaputil.SendResponse(w, r, http.StatusConflict,
			fmt.Sprintf("model %q is defined outside the primary config file and cannot be edited here", modelID))
		return
	}

	patched, err := config.PatchModelYAML(data, modelID, req.YAML)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	newCfg, err := config.LoadConfigFromReader(bytes.NewReader(patched))
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("patched config is invalid: %v", err))
		return
	}
	if newMC, ok := newCfg.Models[modelID]; !ok {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "patched config lost the model being edited")
		return
	} else if strings.TrimSpace(newMC.Cmd) == "" {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "cmd cannot be empty")
		return
	}

	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	reload := shouldReload(req.Reload)
	s.logs.ProxyLogs.Infof("config for model %q updated via UI (reload=%t)", modelID, reload)
	s.finishConfigWrite(w, r, modelID, reload)
}

// handleAPIPatchModelConfig sets the display `name` of models.<modelID> in
// the primary config file. By default a hot reload follows; {"reload": false}
// saves the name without a reload. An empty name clears the field. Unlike the
// full-block PUT, this patch keeps every other setting and comment in the
// model block byte-identical.
func (s *Server) handleAPIPatchModelConfig(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model")

	var req modelNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, `request body must be JSON: {"name": "new display name"}`)
		return
	}
	name := strings.TrimSpace(req.Name)
	if len(name) > 200 {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "name is too long (max 200 characters)")
		return
	}

	if _, ok := s.cfg.Models[modelID]; !ok {
		swaputil.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q not found", modelID))
		return
	}
	if s.configFile == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "config editing is not available: no primary config file")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	patched, err := config.SetModelNameYAML(data, modelID, name)
	if errors.Is(err, config.ErrModelNotInFile) {
		swaputil.SendResponse(w, r, http.StatusConflict,
			fmt.Sprintf("model %q is defined outside the primary config file and cannot be edited here", modelID))
		return
	}
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	newCfg, err := config.LoadConfigFromReader(bytes.NewReader(patched))
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("patched config is invalid: %v", err))
		return
	}
	newMC, ok := newCfg.Models[modelID]
	if !ok {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "patched config lost the model being edited")
		return
	}
	if newMC.Name != name {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, "patched config does not contain the requested name")
		return
	}

	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	reload := shouldReload(req.Reload)
	s.logs.ProxyLogs.Infof("name for model %q updated to %q via UI (reload=%t)", modelID, name, reload)
	s.finishConfigWrite(w, r, modelID, reload, map[string]any{"name": name})
}

// modelRenameRequest is the payload of POST /api/models/rename/{model}.
type modelRenameRequest struct {
	NewID  string `json:"new_id"`
	Reload *bool  `json:"reload,omitempty"`
}

// handleAPIRenameModel renames the model's ID in the primary config file:
// the models: key plus every exact reference to the old ID (listeners,
// routing groups, selectors, profile pins, startup preloads, matrix vars,
// evict costs and set DSLs). Glob patterns, peer model lists and other
// models' aliases are untouched. The model's URL, its /v1/models id and the
// UI links all follow the new ID. By default a hot reload follows, so a
// running model stops and starts under the new ID on its next request;
// {"reload": false} saves the rename without a reload.
func (s *Server) handleAPIRenameModel(w http.ResponseWriter, r *http.Request) {
	oldID := r.PathValue("model")

	var req modelRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, `request body must be JSON: {"new_id": "new-model-id"}`)
		return
	}
	newID := strings.TrimSpace(req.NewID)

	if _, ok := s.cfg.Models[oldID]; !ok {
		swaputil.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q not found", oldID))
		return
	}
	if newID == "" {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "new_id is required")
		return
	}
	if newID == oldID {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "new_id is the same as the current model ID")
		return
	}
	if !validModelID(newID) {
		swaputil.SendResponse(w, r, http.StatusBadRequest,
			"invalid new ID: use letters, digits, '-', '_', '.' or ':' only (max 128 characters)")
		return
	}
	if _, ok := s.cfg.ResolveBaseModel(newID); ok {
		swaputil.SendResponse(w, r, http.StatusConflict,
			fmt.Sprintf("new ID %q is already in use as a model, alias or peer model", newID))
		return
	}
	if _, ok := s.cfg.Selectors[newID]; ok {
		swaputil.SendResponse(w, r, http.StatusConflict, fmt.Sprintf("new ID %q is already a selector", newID))
		return
	}
	if s.configFile == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "config editing is not available: no primary config file")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	patched, err := config.RenameModelYAML(data, oldID, newID)
	if errors.Is(err, config.ErrModelNotInFile) {
		swaputil.SendResponse(w, r, http.StatusConflict,
			fmt.Sprintf("model %q is defined outside the primary config file and cannot be renamed here", oldID))
		return
	}
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	newCfg, err := config.LoadConfigFromReader(bytes.NewReader(patched))
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("patched config is invalid: %v", err))
		return
	}
	if _, ok := newCfg.Models[newID]; !ok {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "patched config does not contain the renamed model")
		return
	}
	if _, ok := newCfg.Models[oldID]; ok {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "patched config still contains the old model ID")
		return
	}

	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	reload := shouldReload(req.Reload)
	s.logs.ProxyLogs.Infof("model %q renamed to %q via UI (reload=%t)", oldID, newID, reload)
	s.finishConfigWrite(w, r, oldID, reload, map[string]any{"new_id": newID})
}

// marshalModelConfigFallback renders an effective ModelConfig as YAML for
// read-only display when the raw block cannot be extracted.
func marshalModelConfigFallback(mc config.ModelConfig) string {
	out, err := yaml.Marshal(mc)
	if err != nil {
		return "# failed to render model configuration\n"
	}
	return string(out)
}

// validModelID restricts UI-created model IDs to characters that are safe in
// YAML mapping keys, URL path segments and API requests.
func validModelID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == ':':
		default:
			return false
		}
	}
	return true
}

// handleAPIAddModelConfig appends a new model block to the primary config
// file. By default a hot reload follows, which is what makes the new model
// servable; {"reload": false} saves it for a later POST /api/reload. The
// listeners block is intentionally left untouched: new models are served by
// the main listener only, and never get added to custom endpoint model lists
// automatically.
func (s *Server) handleAPIAddModelConfig(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model")
	if !validModelID(modelID) {
		swaputil.SendResponse(w, r, http.StatusBadRequest,
			"invalid model id: use letters, digits, '-', '_', '.' or ':' only")
		return
	}

	var req modelConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.YAML) == "" {
		swaputil.SendResponse(w, r, http.StatusBadRequest, `request body must be JSON: {"yaml": "<model settings>"}`)
		return
	}

	if _, ok := s.cfg.Models[modelID]; ok {
		swaputil.SendResponse(w, r, http.StatusConflict, fmt.Sprintf("model %q already exists", modelID))
		return
	}
	if s.configFile == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "config editing is not available: no primary config file")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	// The in-memory check above still misses a model added by a save-only
	// write that has not been applied yet; also reject when the block is
	// already present in the file.
	if existing, err := config.ExtractModelYAML(data, modelID); err == nil && existing != "" {
		swaputil.SendResponse(w, r, http.StatusConflict,
			fmt.Sprintf("model %q already exists in the config file", modelID))
		return
	}

	patched, err := config.PatchModelYAML(data, modelID, req.YAML)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	newCfg, err := config.LoadConfigFromReader(bytes.NewReader(patched))
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("patched config is invalid: %v", err))
		return
	}
	newMC, ok := newCfg.Models[modelID]
	if !ok {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, "patched config lost the new model")
		return
	}
	if strings.TrimSpace(newMC.Cmd) == "" {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "cmd cannot be empty")
		return
	}

	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	reload := shouldReload(req.Reload)
	s.logs.ProxyLogs.Infof("model %q added via UI (reload=%t)", modelID, reload)
	s.finishConfigWrite(w, r, modelID, reload)
}

// handleAPIRemoveModelConfig deletes a model's block from the primary config
// file. By default a hot reload follows; ?reload=false saves the removal
// without a reload. Exact-match references to the removed ID are also pruned
// from custom listeners' model lists so no dangling entries remain; glob
// patterns are preserved untouched.
func (s *Server) handleAPIRemoveModelConfig(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model")
	reload := true
	if v := r.URL.Query().Get("reload"); v == "false" || v == "0" {
		reload = false
	}

	if _, ok := s.cfg.Models[modelID]; !ok {
		swaputil.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q not found", modelID))
		return
	}
	if s.configFile == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "config editing is not available: no primary config file")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
		return
	}

	patched, err := config.RemoveModelYAML(data, modelID)
	if errors.Is(err, config.ErrModelNotInFile) {
		swaputil.SendResponse(w, r, http.StatusConflict,
			fmt.Sprintf("model %q is defined outside the primary config file and cannot be removed here", modelID))
		return
	}
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	pruned, listenersPruned, err := config.RemoveModelFromListeners(patched, modelID)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	newCfg, err := config.LoadConfigFromReader(bytes.NewReader(pruned))
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("patched config is invalid: %v", err))
		return
	}
	if _, exists := newCfg.Models[modelID]; exists {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "patched config still contains the removed model")
		return
	}

	if err := config.WriteFileAtomic(s.configFile, pruned); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	s.logs.ProxyLogs.Infof("model %q removed via UI (listeners pruned: %v, reload=%t)", modelID, listenersPruned, reload)
	s.finishConfigWrite(w, r, modelID, reload, map[string]any{"listeners_pruned": listenersPruned})
}
