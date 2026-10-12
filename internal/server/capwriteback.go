package server

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mostlygeek/llama-swap/internal/capcompat"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
)

// capsBlock is the subset of a model's capabilities that lives in the config
// file. DisableAuto is a switch, not a capability, and never written.
type capsBlock struct {
	In       []string `yaml:"in,omitempty"`
	Out      []string `yaml:"out,omitempty"`
	Tools    bool     `yaml:"tools,omitempty"`
	Reranker bool     `yaml:"reranker,omitempty"`
	Context  int      `yaml:"context,omitempty"`
}

func capsBlockOf(c config.ModelCapConfig) capsBlock {
	return capsBlock{In: c.In, Out: c.Out, Tools: c.Tools, Reranker: c.Reranker, Context: c.Context}
}

// ---- pending state (auto-filled fields in the config file) ----

// pendingFields returns the fields marked auto-filled for modelID in the
// config file, or nil when the model has no pending capabilities.
func (s *Server) pendingFields(modelID string) []string {
	s.pendingMu.RLock()
	defer s.pendingMu.RUnlock()
	return s.pendingCaps[modelID]
}

// setPending records (clears with an empty list) the pending fields for a
// model.
func (s *Server) setPending(modelID string, fields []string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.pendingCaps == nil {
		s.pendingCaps = make(map[string][]string)
	}
	if len(fields) == 0 {
		delete(s.pendingCaps, modelID)
		return
	}
	s.pendingCaps[modelID] = fields
}

// rescanPending re-reads the primary config file and rebuilds the pending map
// from the marker lines it finds. It runs when a config file becomes attached
// and after every hot reload (a fresh Server instance is created for each).
func (s *Server) rescanPending() {
	if s.configFile == "" {
		return
	}
	data, err := os.ReadFile(s.configFile)
	if err != nil {
		return
	}
	for modelID := range s.cfg.Models {
		_, markerLine, recorded, err := config.ReadModelCaps(data, modelID)
		if err != nil || markerLine == "" {
			continue
		}
		fields := make([]string, 0, len(recorded))
		for field := range recorded {
			fields = append(fields, field)
		}
		s.setPending(modelID, fields)
	}
}

// ---- self-write tracking (dirty detection, watcher suppression) ----

// ConfigDirty reports whether the config file changed after this server
// instance was built. That is exactly the state "saved, not yet applied":
// the running server still serves the pre-save config until a reload.
func (s *Server) ConfigDirty() bool {
	if s.configFile == "" {
		return false
	}
	fi, err := os.Stat(s.configFile)
	if err != nil {
		return false
	}
	return fi.ModTime().After(s.bootTime)
}

// recordSelfWrite remembers the file state right after one of llama-swap's
// own writes, so the poll-based config watcher can tell its own change from a
// real external edit and not reload (which would stop running models).
func (s *Server) recordSelfWrite() {
	fi, err := os.Stat(s.configFile)
	if err != nil {
		return
	}
	s.selfWriteMu.Lock()
	s.selfWriteMod = fi.ModTime()
	s.selfWriteSize = fi.Size()
	s.selfWriteMu.Unlock()
}

// IsSelfWrite reports whether the file's current state matches llama-swap's
// last own write.
func (s *Server) IsSelfWrite(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	s.selfWriteMu.Lock()
	mod, size, ok := s.selfWriteMod, s.selfWriteSize, !s.selfWriteMod.IsZero()
	s.selfWriteMu.Unlock()
	if !ok {
		return false
	}
	return fi.ModTime().Equal(mod) && fi.Size() == size
}

// finishConfigWrite centralizes what every config-file write does after the
// atomic write succeeds: remember the self-write state, then either reload
// (the legacy all-in-one behavior) or report the change as saved-not-applied.
// Extra response fields (e.g. a new name) are merged into the response.
func (s *Server) finishConfigWrite(w http.ResponseWriter, r *http.Request, modelID string, reload bool, extra ...map[string]any) {
	s.recordSelfWrite()
	reloaded := false
	if reload {
		if s.reloadFn != nil {
			go s.reloadFn()
			reloaded = true
		}
	} else {
		event.Emit(swaputil.ConfigSavedEvent{ModelID: modelID})
	}
	resp := map[string]any{
		"status":    "ok",
		"model_id":  modelID,
		"reloading": reloaded,
	}
	for _, e := range extra {
		for k, v := range e {
			resp[k] = v
		}
	}
	swaputil.RespondJSON(w, r, http.StatusOK, resp)
}

// ---- write-back of discovered capabilities ----

// persistDiscoveredCapabilities writes the capabilities the config file does
// not set into the model's capabilities block under the pending marker, when
// write-back is enabled. It never touches fields the file sets manually, and
// it refreshes fields it wrote itself (still matching the marker) on every
// successful probe, so a changed --ctx-size or --mmproj picks up on the
// model's next load.
//
// The write is deliberately NOT followed by a reload: the values are already
// served through the discovery cache, the file write is pure durability, and
// a reload would stop running models over metadata that does not change
// runtime behavior.
func (s *Server) persistDiscoveredCapabilities(modelID string, mc config.ModelConfig, key string) {
	if !s.cfg.CapabilitiesWriteback || s.configFile == "" || mc.Capabilities.DisableAuto {
		return
	}
	auto, found := s.capcompat.Lookup(s.shutdownCtx, key)
	if !found || auto.Empty() {
		return
	}
	if declined, ok := s.capcompat.Declined(s.shutdownCtx, key); ok && len(declined) > 0 {
		auto = auto.Except(declined)
		if auto.Empty() {
			return
		}
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		s.logs.ProxyLogs.Debugf("capcompat: %s writeback: reading config: %v", modelID, err)
		return
	}
	fileCaps, markerLine, recorded, err := config.ReadModelCaps(data, modelID)
	if err != nil {
		if !errors.Is(err, config.ErrModelNotInFile) {
			s.logs.ProxyLogs.Debugf("capcompat: %s writeback: %v", modelID, err)
		}
		return
	}

	merged, autoSet := fileCaps.MergeAuto(auto, recorded)
	values := make(map[string]string, len(autoSet))
	for field := range autoSet {
		if v := config.CapFieldValue(merged, field); v != "" {
			values[field] = v
		}
	}
	if markerLine != "" && config.AutoValuesEqual(recorded, values) {
		// The auto-managed fields did not move. The file already carries the
		// values the marker records, so there is nothing to write — and
		// rendering a fresh date would only churn the file.
		return
	}

	capsNode, err := config.ValueToNode(capsBlockOf(merged))
	if err != nil {
		s.logs.ProxyLogs.Debugf("capcompat: %s writeback: %v", modelID, err)
		return
	}
	marker := config.RenderAutoMarker(time.Now(), values)
	patched, err := config.SetModelCapabilitiesYAML(data, modelID, capsNode, marker)
	if err != nil {
		s.logs.ProxyLogs.Debugf("capcompat: %s writeback: %v", modelID, err)
		return
	}
	if bytes.Equal(patched, data) {
		return
	}
	if _, err := config.LoadConfigFromReader(bytes.NewReader(patched)); err != nil {
		s.logs.ProxyLogs.Warnf("capcompat: %s writeback: patched config invalid, not writing: %v", modelID, err)
		return
	}
	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		s.logs.ProxyLogs.Warnf("capcompat: %s writeback: %v", modelID, err)
		return
	}
	s.recordSelfWrite()
	s.setPending(modelID, config.SortedAutoFields(autoSet))
	s.logs.ProxyLogs.Infof("capcompat: %s capabilities written to config (pending: %v)", modelID, config.SortedAutoFields(autoSet))
	event.Emit(swaputil.ConfigSavedEvent{ModelID: modelID})
}

// ---- accept / discard endpoints ----

// handleAPICapabilitiesAction accepts or discards a model's pending
// capabilities. The {model...} wildcard captures the model id and, as its
// last segment, the action, because a ServeMux wildcard must end the pattern
// and model ids may themselves contain "/".
func (s *Server) handleAPICapabilitiesAction(w http.ResponseWriter, r *http.Request) {
	segments := strings.Split(r.PathValue("model"), "/")
	if len(segments) < 2 {
		swaputil.SendResponse(w, r, http.StatusBadRequest,
			"expected /api/models/capabilities/{model}/accept or /discard")
		return
	}
	action := segments[len(segments)-1]
	modelID := strings.Join(segments[:len(segments)-1], "/")
	switch action {
	case "accept":
		s.capabilitiesAccept(w, r, modelID)
	case "discard":
		s.capabilitiesDiscard(w, r, modelID)
	default:
		swaputil.SendResponse(w, r, http.StatusNotFound,
			fmt.Sprintf("unknown capabilities action %q", action))
	}
}

// capabilitiesAccept strips the pending marker from a model's capabilities,
// keeping the auto-filled values: the user adopted them, and they are now
// plain config.
func (s *Server) capabilitiesAccept(w http.ResponseWriter, r *http.Request, modelID string) {
	if _, ok := s.cfg.Models[modelID]; !ok {
		swaputil.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q not found", modelID))
		return
	}
	if s.configFile == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "no primary config file; capabilities cannot be accepted")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("cannot read config file: %v", err))
		return
	}
	caps, markerLine, recorded, err := config.ReadModelCaps(data, modelID)
	if err != nil {
		if errors.Is(err, config.ErrModelNotInFile) {
			swaputil.SendResponse(w, r, http.StatusConflict, "model is not defined in the primary config file")
			return
		}
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("cannot read config file: %v", err))
		return
	}
	if markerLine == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "model has no pending capabilities to accept")
		return
	}

	capsNode, err := config.ValueToNode(capsBlockOf(caps))
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("building capabilities node: %v", err))
		return
	}
	patched, err := config.SetModelCapabilitiesYAML(data, modelID, capsNode, "")
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("patching config: %v", err))
		return
	}
	if _, err := config.LoadConfigFromReader(bytes.NewReader(patched)); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("patched config invalid: %v", err))
		return
	}
	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("writing config: %v", err))
		return
	}
	s.recordSelfWrite()
	s.setPending(modelID, nil)
	// The user kept these values: a decline record for them is stale.
	if declined, ok := s.capcompat.Declined(s.shutdownCtx, capcompat.LocalKey(modelID, s.cfg.Models[modelID])); ok {
		kept := make([]string, 0, len(declined))
		for _, field := range declined {
			if _, pending := recorded[field]; !pending {
				kept = append(kept, field)
			}
		}
		if err := s.capcompat.StoreDeclined(s.shutdownCtx, capcompat.LocalKey(modelID, s.cfg.Models[modelID]), kept); err != nil {
			s.logs.ProxyLogs.Debugf("capcompat: %s accept: %v", modelID, err)
		}
	}
	s.logs.ProxyLogs.Infof("capabilities of %s accepted (pending marker stripped)", modelID)
	s.finishConfigWrite(w, r, modelID, false)
}

// capabilitiesDiscard removes the auto-filled fields from a model's
// capabilities block, clears the discovery cache for the model (so the values
// stop being advertised at once), and records the discarded fields so the
// next probe does not simply re-add them.
func (s *Server) capabilitiesDiscard(w http.ResponseWriter, r *http.Request, modelID string) {
	mc, ok := s.cfg.Models[modelID]
	if !ok {
		swaputil.SendResponse(w, r, http.StatusNotFound, fmt.Sprintf("model %q not found", modelID))
		return
	}
	if s.configFile == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "no primary config file; capabilities cannot be discarded")
		return
	}

	s.editMu.Lock()
	defer s.editMu.Unlock()

	data, err := os.ReadFile(s.configFile)
	if err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("cannot read config file: %v", err))
		return
	}
	caps, markerLine, recorded, err := config.ReadModelCaps(data, modelID)
	if err != nil {
		if errors.Is(err, config.ErrModelNotInFile) {
			swaputil.SendResponse(w, r, http.StatusConflict, "model is not defined in the primary config file")
			return
		}
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("cannot read config file: %v", err))
		return
	}
	if markerLine == "" {
		swaputil.SendResponse(w, r, http.StatusConflict, "model has no pending capabilities to discard")
		return
	}
	discarded := config.SortedAutoFields(setFromKeys(recorded))

	var patched []byte
	if remaining := caps.Except(discarded); remaining.Empty() {
		// Nothing manual left: drop the whole block.
		var removed bool
		patched, removed, err = config.RemoveModelCapabilitiesYAML(data, modelID)
		if err != nil {
			swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("patching config: %v", err))
			return
		}
		if !removed {
			s.logs.ProxyLogs.Debugf("capcompat: %s discard: capabilities block already gone", modelID)
		}
	} else {
		capsNode, nodeErr := config.ValueToNode(capsBlockOf(remaining))
		if nodeErr != nil {
			swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("building capabilities node: %v", nodeErr))
			return
		}
		patched, err = config.SetModelCapabilitiesYAML(data, modelID, capsNode, "")
		if err != nil {
			swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("patching config: %v", err))
			return
		}
	}
	if _, err := config.LoadConfigFromReader(bytes.NewReader(patched)); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("patched config invalid: %v", err))
		return
	}
	if err := config.WriteFileAtomic(s.configFile, patched); err != nil {
		swaputil.SendResponse(w, r, http.StatusInternalServerError, fmt.Sprintf("writing config: %v", err))
		return
	}
	s.recordSelfWrite()
	s.setPending(modelID, nil)

	key := capcompat.LocalKey(modelID, mc)
	// Stop advertising the discarded values immediately. Drop first: it also
	// clears any stale declined record, which StoreDeclined then rewrites.
	if err := s.capcompat.Drop(s.shutdownCtx, key); err != nil {
		s.logs.ProxyLogs.Debugf("capcompat: %s discard: dropping cache: %v", modelID, err)
	}
	if err := s.capcompat.StoreDeclined(s.shutdownCtx, key, discarded); err != nil {
		s.logs.ProxyLogs.Debugf("capcompat: %s discard: %v", modelID, err)
	}
	s.logs.ProxyLogs.Infof("capabilities of %s discarded (removed: %v)", modelID, discarded)
	event.Emit(swaputil.ModelCapabilitiesChangedEvent{ModelID: modelID})
	s.finishConfigWrite(w, r, modelID, false)
}

func setFromKeys(m map[string]string) map[string]bool {
	set := make(map[string]bool, len(m))
	for k := range m {
		set[k] = true
	}
	return set
}

// ---- reload (apply) and config status endpoints ----

// handleAPIReload triggers a hot reload: the explicit "Apply" step of the
// save/apply split.
func (s *Server) handleAPIReload(w http.ResponseWriter, r *http.Request) {
	if s.reloadFn == nil {
		swaputil.SendResponse(w, r, http.StatusNotImplemented, "reload is not available for this server")
		return
	}
	s.reloadFn()
	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{"status": "ok", "reloading": true})
}

// handleAPIConfigStatus reports whether the config file is ahead of the
// running server (saved changes pending an apply).
func (s *Server) handleAPIConfigStatus(w http.ResponseWriter, r *http.Request) {
	swaputil.RespondJSON(w, r, http.StatusOK, map[string]any{"dirty": s.ConfigDirty()})
}
