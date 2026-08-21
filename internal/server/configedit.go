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

	s.proxylog.Infof("config for model %q updated via UI, triggering reload", modelID)

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

// marshalModelConfigFallback renders an effective ModelConfig as YAML for
// read-only display when the raw block cannot be extracted.
func marshalModelConfigFallback(mc config.ModelConfig) string {
	out, err := yaml.Marshal(mc)
	if err != nil {
		return "# failed to render model configuration\n"
	}
	return string(out)
}
