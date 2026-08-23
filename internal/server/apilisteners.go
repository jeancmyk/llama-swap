package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
)

// handleAPIListeners returns the configured listeners joined with their
// runtime bind state.
func (s *Server) handleAPIListeners(w http.ResponseWriter, r *http.Request) {
	statusByAddr := make(map[string]ListenerRuntimeStatus)
	if s.listenerStatus != nil {
		for _, st := range s.listenerStatus() {
			statusByAddr[st.Listen] = st
		}
	}

	type listenerEntry struct {
		config.ListenerConfig
		Bound bool   `json:"bound"`
		Error string `json:"error,omitempty"`
	}

	out := make([]listenerEntry, 0, len(s.cfg.Listeners))
	for _, l := range s.cfg.Listeners {
		entry := listenerEntry{ListenerConfig: l}
		addr, err := config.ParseListenAddr(l.Listen)
		if err != nil {
			entry.Bound = false
			entry.Error = err.Error()
		} else if st, ok := statusByAddr[addr]; ok {
			entry.Bound = st.Bound
			entry.Error = st.Error
		}
		out = append(out, entry)
	}

	// Available models: the complete set of local model IDs the UI can assign
	// to a listener, sourced from the server so the checkbox list is populated
	// even before the client-side model store has loaded.
	available := make([]string, 0, len(s.cfg.Models))
	for id := range s.cfg.Models {
		available = append(available, id)
	}
	sort.Strings(available)

	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{
		"listeners":        out,
		"available_models": available,
	})
}

// handleAPIPutListeners replaces the listeners block in the primary config
// file and triggers a hot reload that reconciles the bound listeners.
func (s *Server) handleAPIPutListeners(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Listeners []config.ListenerConfig `json:"listeners"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, "request body must be JSON: {\"listeners\": [...]}")
		return
	}

	if err := config.ValidateListeners(req.Listeners); err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
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

	normalized := config.NormalizeListenerDefaults(req.Listeners)

	patched, changed, err := patchListenersKey(data, normalized)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Dry-run: the patched document must parse as a full valid config.
	if _, err := config.LoadConfigFromReader(bytes.NewReader(patched)); err != nil {
		swaputil.SendResponse(w, r, http.StatusBadRequest, fmt.Sprintf("patched config is invalid: %v", err))
		return
	}

	if changed {
		if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
			swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("write config: %v", err))
			return
		}
	}

	reloaded := false
	if s.reloadFn != nil {
		go s.reloadFn()
		reloaded = true
	}

	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{
		"status":    "ok",
		"reloading": reloaded,
	})
}

// patchListenersKey sets the top-level `listeners:` key to the given entries,
// or removes the key entirely when the list is empty. Returns whether the
// document changed.
func patchListenersKey(data []byte, listeners []config.ListenerConfig) ([]byte, bool, error) {
	if len(listeners) == 0 {
		return config.RemoveTopLevelKey(data, "listeners")
	}

	valueNode, err := config.ValueToNode(listeners)
	if err != nil {
		return nil, false, fmt.Errorf("encode listeners: %w", err)
	}

	patched, err := config.PatchTopLevelKey(data, "listeners", valueNode)
	if err != nil {
		return nil, false, err
	}
	return patched, true, nil
}
