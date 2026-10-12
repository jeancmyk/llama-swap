package capcompat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/logmon"
	"github.com/mostlygeek/llama-swap/internal/store"
)

const (
	// cacheTTL bounds how long a discovered value survives without being
	// rewritten. Every model start refreshes its entry, so this only sweeps
	// up models that were removed from the config.
	cacheTTL = 30 * 24 * time.Hour

	// probeTimeout caps one Refresh, retries included.
	probeTimeout = 10 * time.Second
)

// probeAttempts is how many times a refresh tries before giving up. A model
// configured with `checkEndpoint: none` is reported ready before it is
// listening, so the first attempt can legitimately be refused.
//
// probeRetryDelay separates those attempts. Both are variables so tests can
// run the retry paths without spending real seconds on them.
var (
	probeAttempts   = 3
	probeRetryDelay = time.Second
)

// Service reads and writes discovered capabilities, backed by a store cache
// and fronted by an in-memory memo.
//
// The memo is not an optimisation detail. Clients poll /v1/models, and every
// model in the listing is a lookup, so without it each listing would be a
// burst of SQLite reads. Misses are memoized too: a model whose upstream is
// not supported must not re-query the database on every request.
//
// All methods are safe on a nil *Service, which reports no capabilities and
// stores nothing. That keeps the /v1/models path working for callers that
// construct a Server without one.
type Service struct {
	cache  store.CacheRepository
	logger *logmon.Monitor

	mu   sync.RWMutex
	memo map[string]memoEntry

	// declinedMemo remembers which fields the user discarded for a key. Like
	// memo, misses are memoized too, so the listing path does not re-query the
	// store on every request.
	declinedMemo map[string]memoDeclined

	// inflight guards against stacking probes for the same key when a model
	// flaps between ready and stopped.
	inflightMu sync.Mutex
	inflight   map[string]struct{}
}

// memoDeclined is a remembered Declined answer; found is false for a
// remembered miss.
type memoDeclined struct {
	fields []string
	found  bool
}

// memoEntry is a remembered lookup. found is false for a remembered miss.
type memoEntry struct {
	caps  config.ModelCapConfig
	found bool
}

// New returns a Service backed by cache. A nil cache disables persistence;
// lookups then only ever see what this process discovered.
func New(cache store.CacheRepository, logger *logmon.Monitor) *Service {
	return &Service{
		cache:        cache,
		logger:       logger,
		memo:         make(map[string]memoEntry),
		declinedMemo: make(map[string]memoDeclined),
		inflight:     make(map[string]struct{}),
	}
}

// LocalKey returns the cache key for a local model.
//
// The hash covers the fields that decide what the upstream will report, so
// editing a model's command invalidates its entry instead of serving values
// discovered from the old one.
func LocalKey(modelID string, mc config.ModelConfig) string {
	sum := sha256.Sum256([]byte(mc.Cmd + "\x00" + mc.Proxy + "\x00" + mc.UseModelName))
	return "capcompat:v1:local:" + modelID + ":" + hex.EncodeToString(sum[:4])
}

// UpstreamModelName returns the name the upstream knows this model by.
func UpstreamModelName(modelID string, mc config.ModelConfig) string {
	if mc.UseModelName != "" {
		return mc.UseModelName
	}
	return modelID
}

// Lookup returns the capabilities cached under key. The second return value
// is false when nothing has been discovered for it.
//
// The store is read at most once per key; later calls are answered from
// memory until Refresh replaces the entry.
func (s *Service) Lookup(ctx context.Context, key string) (config.ModelCapConfig, bool) {
	if s == nil {
		return config.ModelCapConfig{}, false
	}

	s.mu.RLock()
	entry, memoized := s.memo[key]
	s.mu.RUnlock()
	if memoized {
		return entry.caps, entry.found
	}

	info, found, err := s.read(ctx, key)
	if err != nil {
		// A failed read is not memoized: the next request should try again.
		s.logf("capcompat: reading cache for %s: %v", key, err)
		return config.ModelCapConfig{}, false
	}

	s.remember(key, info.Capabilities, found)
	return info.Capabilities, found
}

// Refresh probes the upstream behind c and stores what it reports under key.
//
// It is the write half of the cache and is meant to be called when a model
// becomes ready. An unsupported upstream is recorded as a miss so the listing
// path stops asking the store about it.
func (s *Service) Refresh(ctx context.Context, key string, c *Client, modelName string) error {
	if s == nil {
		return nil
	}
	if !s.claim(key) {
		return nil
	}
	defer s.release(key)

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	info, err := s.detect(ctx, c, modelName)
	if err != nil {
		if errors.Is(err, ErrUnsupportedUpstream) {
			// Not every upstream has capabilities to report. Drop anything
			// stored under this key first: a server can be replaced by one
			// llama-swap does not recognise without cmd, proxy or
			// useModelName changing, and the key would still be the same.
			// The memo would hide the old row for the life of the process,
			// but a restart starts with an empty memo and would serve it.
			if s.cache != nil {
				if err := s.cache.Delete(ctx, key); err != nil {
					return fmt.Errorf("capcompat: dropping stale %s: %w", key, err)
				}
			}
			// Remember the miss so /v1/models stops hitting the store.
			s.remember(key, config.ModelCapConfig{}, false)
			s.logf("capcompat: %s: %v", key, err)
			return nil
		}
		return err
	}

	blob, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("capcompat: encoding %s: %w", key, err)
	}

	// Remember before persisting. The probe succeeded either way, so a store
	// that cannot be written should cost durability across a restart, not the
	// value this process just learned.
	s.remember(key, info.Capabilities, true)
	s.logf("capcompat: %s reported by %s: %s", key, info.Upstream, describe(info.Capabilities))

	if s.cache != nil {
		if err := s.cache.Set(ctx, store.CacheEntry{
			Key:       key,
			Data:      blob,
			TTL:       cacheTTL,
			Timestamp: info.DetectedAt,
		}); err != nil {
			return fmt.Errorf("capcompat: caching %s: %w", key, err)
		}
	}

	return nil
}

// detect runs Detect, retrying a connection that is not answering yet. An
// upstream that answers and is simply not supported is returned immediately;
// retrying would not change the answer.
func (s *Service) detect(ctx context.Context, c *Client, modelName string) (Info, error) {
	var lastErr error
	for attempt := 0; attempt < probeAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Info{}, ctx.Err()
			case <-time.After(probeRetryDelay):
			}
		}

		info, err := Detect(ctx, c, modelName)
		if err == nil {
			return info, nil
		}
		if errors.Is(err, ErrUnsupportedUpstream) || ctx.Err() != nil {
			return Info{}, err
		}
		lastErr = err
	}
	return Info{}, lastErr
}

// read loads and decodes one cache entry.
func (s *Service) read(ctx context.Context, key string) (Info, bool, error) {
	if s.cache == nil {
		return Info{}, false, nil
	}
	entry, found, err := s.cache.Get(ctx, key)
	if err != nil || !found {
		return Info{}, false, err
	}

	var info Info
	if err := json.Unmarshal(entry.Data, &info); err != nil {
		// A row we cannot read is worse than no row: drop it so the next
		// model start can write a good one.
		if delErr := s.cache.Delete(ctx, key); delErr != nil {
			s.logf("capcompat: dropping unreadable cache entry %s: %v", key, delErr)
		}
		return Info{}, false, fmt.Errorf("capcompat: decoding cached %s: %w", key, err)
	}
	if info.Capabilities.Empty() {
		return Info{}, false, nil
	}
	return info, true, nil
}

// Forget drops the memoized answers for key (capabilities and declined
// fields), so the next Lookup reads the store again. Used when the
// configuration behind a key may have changed.
func (s *Service) Forget(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.memo, key)
	delete(s.declinedMemo, key)
	s.mu.Unlock()
}

// declinedKey is the cache key for the discarded-fields record of key.
func declinedKey(key string) string {
	return "capcompat:v1:declined:" + key
}

// StoreDeclined records that the user discarded auto-filling the listed
// fields for a model, so write-back and /v1/models stop offering them. The
// record lives as long as discovered entries (cacheTTL). A nil or empty list
// clears any existing record (the user accepted the fields in the meantime).
func (s *Service) StoreDeclined(ctx context.Context, key string, fields []string) error {
	if s == nil {
		return nil
	}
	if len(fields) == 0 {
		if s.cache != nil {
			if err := s.cache.Delete(ctx, declinedKey(key)); err != nil {
				return fmt.Errorf("capcompat: clearing declined fields for %s: %w", key, err)
			}
		}
		s.mu.Lock()
		s.declinedMemo[key] = memoDeclined{found: false}
		s.mu.Unlock()
		return nil
	}
	blob, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("capcompat: encoding declined fields for %s: %w", key, err)
	}
	if s.cache != nil {
		if err := s.cache.Set(ctx, store.CacheEntry{Key: declinedKey(key), Data: blob, TTL: cacheTTL}); err != nil {
			return fmt.Errorf("capcompat: storing declined fields for %s: %w", key, err)
		}
	}
	s.mu.Lock()
	s.declinedMemo[key] = memoDeclined{fields: fields, found: true}
	s.mu.Unlock()
	return nil
}

// Declined returns the field names the user discarded for key. found is false
// when no record exists.
func (s *Service) Declined(ctx context.Context, key string) ([]string, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	entry, memoized := s.declinedMemo[key]
	s.mu.RUnlock()
	if memoized {
		return entry.fields, entry.found
	}
	if s.cache == nil {
		return nil, false
	}
	row, found, err := s.cache.Get(ctx, declinedKey(key))
	if err != nil || !found {
		s.mu.Lock()
		s.declinedMemo[key] = memoDeclined{found: false}
		s.mu.Unlock()
		return nil, false
	}
	var fields []string
	if err := json.Unmarshal(row.Data, &fields); err != nil {
		return nil, false
	}
	s.mu.Lock()
	s.declinedMemo[key] = memoDeclined{fields: fields, found: true}
	s.mu.Unlock()
	return fields, true
}

// Drop removes the cached capabilities and the declined-fields record under
// key, so the next probe starts from a clean slate.
func (s *Service) Drop(ctx context.Context, key string) error {
	if s == nil {
		return nil
	}
	s.Forget(key)
	if s.cache != nil {
		if err := s.cache.Delete(ctx, key); err != nil {
			return fmt.Errorf("capcompat: dropping %s: %w", key, err)
		}
		if err := s.cache.Delete(ctx, declinedKey(key)); err != nil {
			return fmt.Errorf("capcompat: dropping declined record for %s: %w", key, err)
		}
	}
	return nil
}

func (s *Service) remember(key string, caps config.ModelCapConfig, found bool) {
	s.mu.Lock()
	s.memo[key] = memoEntry{caps: caps, found: found}
	s.mu.Unlock()
}

// claim reserves key for a probe, reporting false when one is already running.
func (s *Service) claim(key string) bool {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if _, busy := s.inflight[key]; busy {
		return false
	}
	s.inflight[key] = struct{}{}
	return true
}

func (s *Service) release(key string) {
	s.inflightMu.Lock()
	delete(s.inflight, key)
	s.inflightMu.Unlock()
}

func (s *Service) logf(format string, args ...any) {
	if s == nil || s.logger == nil {
		return
	}
	s.logger.Debugf(format, args...)
}

// describe renders capabilities for a log line.
func describe(caps config.ModelCapConfig) string {
	return fmt.Sprintf("in=%v out=%v tools=%t context=%d", caps.In, caps.Out, caps.Tools, caps.Context)
}
