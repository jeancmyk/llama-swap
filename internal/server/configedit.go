package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
)

// backendEntry describes one unique backend executable and which configured
// models use it.
type backendEntry struct {
	Path   string   `json:"path"`
	Models []string `json:"models"`
}

// modelConfigResponse is the payload of GET /api/models/config/{model}.
type modelConfigResponse struct {
	ModelID    string `json:"model_id"`
	SourceFile string `json:"source_file,omitempty"`
	Editable   bool   `json:"editable"`
	Reason     string `json:"reason,omitempty"`
	YAML       string `json:"yaml"`
	// BackendPath is the executable the model's cmd runs, and ModelFiles are
	// the file arguments (main gguf, mmproj, drafts) parsed from the
	// effective (macro-expanded) command. Both power the read-only Paths
	// panel in the UI.
	BackendPath string          `json:"backend_path,omitempty"`
	ModelFiles  []modelFileInfo `json:"model_files,omitempty"`
}

type modelFileInfo struct {
	Flag string `json:"flag"`
	Path string `json:"path"`
}

// modelFileFlags are the command-line flags whose values are model files.
var modelFileFlags = map[string]bool{
	"-m":                 true,
	"--model":            true,
	"--mmproj":           true,
	"--model-draft":      true,
	"-md":                true,
	"--spec-draft-model": true,
}

// extractCmdPaths splits an effective (macro-expanded) cmd into the backend
// executable and its file arguments. Best effort: unparseable commands return
// whatever was understood.
func extractCmdPaths(cmd string) (string, []modelFileInfo) {
	args, err := config.SanitizeCommand(cmd)
	if err != nil || len(args) == 0 {
		return "", nil
	}
	var files []modelFileInfo
	for i := 1; i < len(args); i++ {
		if !modelFileFlags[args[i]] || i+1 >= len(args) {
			continue
		}
		files = append(files, modelFileInfo{Flag: args[i], Path: args[i+1]})
		i++
	}
	return args[0], files
}

type modelConfigRequest struct {
	YAML string `json:"yaml"`
}

// modelNameRequest is the payload of PATCH /api/models/config/{model}.
type modelNameRequest struct {
	Name string `json:"name"`
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
	resp.BackendPath, resp.ModelFiles = extractCmdPaths(mc.Cmd)

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
// file and triggers a hot reload.
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
	if !strings.ContainsAny(modelID, "/") && strings.Contains(modelID, "/") { // unreachable guard kept simple
		swaputil.SendResponse(w, r, http.StatusBadRequest, "invalid model id")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("read config: %v", err))
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
	} else if strings.TrimSpace(newMC.Cmd) == "" {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "cmd cannot be empty")
		return
	}

	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
		return
	}

	s.logs.ProxyLogs.Infof("config for model %q updated via UI, triggering reload", modelID)

	reloaded := false
	if s.reloadFn != nil {
		go s.reloadFn()
		reloaded = true
	}

	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{
		"status":    "ok",
		"model_id":  modelID,
		"reloading": reloaded,
	})
}

// handleAPIPatchModelConfig sets the display `name` of models.<modelID> in
// the primary config file and triggers a hot reload. An empty name clears the
// field. Unlike the full-block PUT, this patch keeps every other setting and
// comment in the model block byte-identical.
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

	s.logs.ProxyLogs.Infof("name for model %q updated to %q via UI, triggering reload", modelID, name)

	reloaded := false
	if s.reloadFn != nil {
		go s.reloadFn()
		reloaded = true
	}

	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{
		"status":    "ok",
		"model_id":  modelID,
		"name":      name,
		"reloading": reloaded,
	})
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
// file and triggers a hot reload. The listeners block is intentionally left
// untouched: new models are served by the main listener only, and never get
// added to custom endpoint model lists automatically.
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

	s.logs.ProxyLogs.Infof("model %q added via UI, triggering reload", modelID)

	reloaded := false
	if s.reloadFn != nil {
		go s.reloadFn()
		reloaded = true
	}

	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{
		"status":    "ok",
		"model_id":  modelID,
		"reloading": reloaded,
	})
}

// handleAPIRemoveModelConfig deletes a model's block from the primary config
// file and triggers a hot reload. Exact-match references to the removed ID
// are also pruned from custom listeners' model lists so no dangling entries
// remain; glob patterns are preserved untouched.
func (s *Server) handleAPIRemoveModelConfig(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model")

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

	s.logs.ProxyLogs.Infof("model %q removed via UI (listeners pruned: %v), triggering reload", modelID, listenersPruned)

	reloaded := false
	if s.reloadFn != nil {
		go s.reloadFn()
		reloaded = true
	}

	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{
		"status":           "ok",
		"model_id":         modelID,
		"reloading":        reloaded,
		"listeners_pruned": listenersPruned,
	})
}

// handleAPIBackends lists unique backend executables across configured local
// models together with the model IDs that use them. This powers the Add
// Model dialog's backend picker.
func (s *Server) handleAPIBackends(w http.ResponseWriter, r *http.Request) {
	ids := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	byPath := make(map[string]*backendEntry)
	order := []string{}
	for _, id := range ids {
		path, _ := extractCmdPaths(s.cfg.Models[id].Cmd)
		if path == "" {
			continue
		}
		entry, ok := byPath[path]
		if !ok {
			entry = &backendEntry{Path: path}
			byPath[path] = entry
			order = append(order, path)
		}
		entry.Models = append(entry.Models, id)
	}

	out := make([]backendEntry, 0, len(order))
	for _, path := range order {
		out = append(out, *byPath[path])
	}
	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{"backends": out})
}
