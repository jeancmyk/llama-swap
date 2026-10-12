package server

import (
	"context"
	"net/url"
	"reflect"

	"github.com/mostlygeek/llama-swap/internal/capcompat"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/process"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
)

// onProcessStateChange refreshes a model's discovered capabilities when its
// process becomes ready, which is the only moment the upstream can be asked.
//
// This runs on the event dispatcher's consumer goroutine, and a consumer that
// falls behind eventually blocks the publisher, which here is the process
// state machine itself. So it must not do any work inline: the probe goes to
// its own goroutine and this returns immediately.
func (s *Server) onProcessStateChange(e swaputil.ProcessStateChangeEvent) {
	if e.NewState != string(process.StateReady) {
		return
	}
	mc, ok := s.cfg.Models[e.ProcessName]
	if !ok || mc.Capabilities.DisableAuto {
		return
	}
	go s.refreshCapabilities(e.ProcessName, mc)
}

// refreshCapabilities probes one ready model and caches what it reports.
//
// The probe dials the model's own proxy address rather than going through the
// local router. Routing a synthetic request through the router would reset the
// model's TTL idle window and occupy a concurrency slot, and could start a
// model that had already stopped again by the time this ran.
func (s *Server) refreshCapabilities(modelID string, mc config.ModelConfig) {
	base, err := url.Parse(mc.Proxy)
	if err != nil {
		s.logs.ProxyLogs.Debugf("capcompat: <%s> unusable proxy URL %q: %v", modelID, mc.Proxy, err)
		return
	}

	client := capcompat.NewClient(base, mc.Timeouts)
	key := capcompat.LocalKey(modelID, mc)
	name := capcompat.UpstreamModelName(modelID, mc)

	// What the model advertised before the probe, so a refresh that learns
	// nothing new does not push a redundant model list to every UI client.
	before := s.resolveCapabilities(s.shutdownCtx, modelID, mc)

	if err := s.capcompat.Refresh(s.shutdownCtx, key, client, name); err != nil {
		// Debug, not warn. llama-swap also fronts image, speech and
		// transcription servers with no capability surface, and a model that
		// stopped again mid-probe is normal. Neither is worth a warning on
		// every model start.
		s.logs.ProxyLogs.Debugf("capcompat: <%s> discovery failed: %v", modelID, err)
		return
	}

	// Optional durability: mirror what the cache learned into the config
	// file, under the pending marker, when write-back is enabled.
	s.persistDiscoveredCapabilities(modelID, mc, key)

	// The listing pushed when this model went ready was built before the
	// probe finished, so anything new has to announce itself.
	if after := s.resolveCapabilities(s.shutdownCtx, modelID, mc); !reflect.DeepEqual(before, after) {
		event.Emit(swaputil.ModelCapabilitiesChangedEvent{ModelID: modelID})
	}
}

// resolveCapabilities returns the capabilities to advertise for a local model:
// what the config sets, with anything it leaves unset filled in from the last
// successful probe.
//
// Configured values win field by field, so a model that hand-sets one field
// still benefits from discovery for the rest. A model with
// capabilities.disableAuto gets exactly what its config says. Fields the
// user discarded from auto-filling are suppressed from the discovery fill
// only — a value the user sets in the config still advertises.
func (s *Server) resolveCapabilities(ctx context.Context, modelID string, mc config.ModelConfig) config.ModelCapConfig {
	caps := mc.Capabilities
	if caps.DisableAuto {
		return caps
	}
	key := capcompat.LocalKey(modelID, mc)
	auto, found := s.capcompat.Lookup(ctx, key)
	if !found {
		return caps
	}
	if declined, ok := s.capcompat.Declined(ctx, key); ok && len(declined) > 0 {
		auto = auto.Except(declined)
	}
	return caps.Merge(auto)
}
