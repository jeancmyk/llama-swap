package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mostlygeek/llama-swap/docs"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/docagent"
	"github.com/mostlygeek/llama-swap/internal/event"
	"github.com/mostlygeek/llama-swap/internal/hw"
	"github.com/mostlygeek/llama-swap/internal/logmon"
	"github.com/mostlygeek/llama-swap/internal/perf"
	"github.com/mostlygeek/llama-swap/internal/process"
	"github.com/mostlygeek/llama-swap/internal/server"
	"github.com/mostlygeek/llama-swap/internal/store/sqlite"
	"github.com/mostlygeek/llama-swap/internal/swaputil"
	"github.com/mostlygeek/llama-swap/internal/tailcat"
	"github.com/mostlygeek/llama-swap/internal/watcher"
)

var (
	version = "0"
	commit  = "abcd1234"
	date    = "unknown"
)

const shutdownTimeout = 30 * time.Second

// docsConfigSchema stays at the repository root because it is the public
// schema URL and is also used by deployment tooling.
//
//go:embed config-schema.json
var docsConfigSchema []byte

// logTimeFormats maps the cfg.LogTimeFormat value to a Go time layout. An
// unset or unrecognised value yields "" — no timestamp prefix.
var logTimeFormats = map[string]string{
	"ansic":       time.ANSIC,
	"unixdate":    time.UnixDate,
	"rubydate":    time.RubyDate,
	"rfc822":      time.RFC822,
	"rfc822z":     time.RFC822Z,
	"rfc850":      time.RFC850,
	"rfc1123":     time.RFC1123,
	"rfc1123z":    time.RFC1123Z,
	"rfc3339":     time.RFC3339,
	"rfc3339nano": time.RFC3339Nano,
	"kitchen":     time.Kitchen,
	"stamp":       time.Stamp,
	"stampmilli":  time.StampMilli,
	"stampmicro":  time.StampMicro,
	"stampnano":   time.StampNano,
}

func configStorePath(cfg config.Config) string {
	if cfg.Store == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Store.Path)
}

func configureTailcatListener(cfg *config.Config, keyPath string) error {
	enabled := keyPath != ""
	cfg.SetTailcatEnabled(enabled)
	if !enabled {
		return nil
	}
	if cfg.Tailcat == nil || len(cfg.Tailcat.Models) == 0 {
		return fmt.Errorf("-listen-tailcat requires tailcat.models to define at least one exposed model")
	}
	if len(cfg.Tailcat.Allow) == 0 {
		slog.Warn(`tailcat.allow is empty: every Tailcat client will get 403 Forbidden; add client node keys, or "*" to allow any client`)
	}
	return nil
}

func loadTailcatListenerKey(keyPath string, enabled bool) (*tailcat.PrivateKey, error) {
	if !enabled {
		return nil, nil
	}
	privateKey, err := tailcat.LoadPrivateKey(keyPath, true)
	if err != nil {
		return nil, fmt.Errorf("load -listen-tailcat key: %w", err)
	}
	return privateKey, nil
}

// runValidate loads the configuration from the given sources and prints a
// short human-readable result to out. It returns 0 when the config loads
// without error and 1 otherwise. It does not start the server, detect
// hardware, or open a listener.
func runValidate(configPath, configDir string, out io.Writer) int {
	cfg, err := config.LoadConfigSources(configPath, configDir)
	if err != nil {
		fmt.Fprintf(out, "config validation failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "config is valid: %d model(s), %d peer(s)\n", len(cfg.Models), len(cfg.Peers))
	return 0
}

// listenerPortOnly extracts the port from a host:port address, falling back to
// the input when it does not parse.
func listenerPortOnly(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}

func main() {
	flagConfig := flag.String("config", "", "path to config file")
	flagConfigDir := flag.String("config-dir", "", "directory of *.yml/*.yaml config files (additive to -config)")
	flagListen := flag.String("listen", "", "listen address (default :8080 or :8443 for TLS)")
	flagCertFile := flag.String("tls-cert-file", "", "TLS certificate file")
	flagKeyFile := flag.String("tls-key-file", "", "TLS key file")
	flagListenTailcat := flag.String("listen-tailcat", "", "path to Tailcat server PrivateKey JSON file")
	flagVersion := flag.Bool("version", false, "show version and exit")
	flagWatchConfig := flag.Bool("watch-config", false, "reload config on file change")
	flagValidate := flag.Bool("validate", false, "validate the config file and exit (without starting the server)")
	flag.Parse()

	if *flagVersion {
		fmt.Printf("version: %s (%s), built at %s\n", version, commit, date)
		os.Exit(0)
	}

	if *flagConfig == "" && *flagConfigDir == "" {
		slog.Error("at least one of -config or -config-dir must be provided")
		os.Exit(1)
	}

	if *flagValidate {
		code := runValidate(*flagConfig, *flagConfigDir, os.Stdout)
		os.Exit(code)
	}

	useTLS := *flagCertFile != "" || *flagKeyFile != ""
	if (*flagCertFile != "" && *flagKeyFile == "") || (*flagCertFile == "" && *flagKeyFile != "") {
		slog.Error("both -tls-cert-file and -tls-key-file must be provided for TLS")
		os.Exit(1)
	}

	listenAddr := *flagListen
	if listenAddr == "" {
		if useTLS {
			listenAddr = ":8443"
		} else {
			listenAddr = ":8080"
		}
	}

	cfg, err := config.LoadConfigSources(*flagConfig, *flagConfigDir)
	if err != nil {
		slog.Error("failed to load config", "config", *flagConfig, "config-dir", *flagConfigDir, "error", err)
		os.Exit(1)
	}
	if err := configureTailcatListener(&cfg, *flagListenTailcat); err != nil {
		slog.Error("invalid Tailcat listener configuration", "error", err)
		os.Exit(1)
	}
	tailcatPrivateKey, err := loadTailcatListenerKey(*flagListenTailcat, cfg.TailcatEnabled())
	if err != nil {
		slog.Error("failed to load -listen-tailcat key", "error", err)
		os.Exit(1)
	}

	// Loggers are wired per cfg.LogToStdout: the selected streams feed
	// logs.MuxLogs, which owns the combined history served by /logs. They
	// outlive config reloads, so a LogToStdout change requires a restart to
	// take effect.
	logs, err := server.NewLoggers(cfg.LogToStdout)
	if err != nil {
		slog.Error("failed to create loggers", "error", err)
		os.Exit(1)
	}
	proxyLog := logs.ProxyLogs

	applyLogSettings := func(cfg config.Config) {
		level := logmon.LevelInfo
		switch strings.ToLower(strings.TrimSpace(cfg.LogLevel)) {
		case "debug":
			level = logmon.LevelDebug
		case "warn":
			level = logmon.LevelWarn
		case "error":
			level = logmon.LevelError
		}
		timeFormat := logTimeFormats[strings.ToLower(strings.TrimSpace(cfg.LogTimeFormat))]
		logs.SetLogLevel(level)
		logs.SetLogTimeFormat(timeFormat)
	}

	applyLogSettings(cfg)
	proxyLog.Debugf("PID: %d", os.Getpid())

	// Hardware describes the inference host and remains stable for the life of
	// this process, including config reloads. Detection is best effort so an
	// unavailable platform probe never prevents llama-swap from starting.
	var hardwareSnapshot *hw.HardwareSnapshot
	detectCtx, detectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	detectedHardware, detectErr := hw.Detect(detectCtx, version)
	detectCancel()
	if detectErr != nil {
		proxyLog.Warnf("hardware detection unavailable: %v", detectErr)
	} else {
		hardwareSnapshot = &detectedHardware
	}

	// On Windows, bind the process tree to a Job Object so every upstream
	// process is reaped when llama-swap exits — even on a forced kill. No-op
	// elsewhere. Non-fatal: a failure just falls back to per-process teardown.
	if err := process.SetupTreeCleanup(); err != nil {
		proxyLog.Warnf("failed to set up process tree cleanup: %v", err)
	}

	// perfMon outlives config reloads; its config is updated in place.
	var perfMon *perf.Monitor
	if !cfg.Performance.Disabled {
		perfMon, err = perf.New(cfg.Performance, proxyLog)
		if err != nil {
			slog.Error("failed to create performance monitor", "error", err)
			os.Exit(1)
		}
		perfMon.Start()
	} else {
		proxyLog.Info("performance monitoring is disabled")
	}

	buildInfo := server.BuildInfo{Version: version, Commit: commit, Date: date}

	// Indexed once and shared by every Server instance, including the ones a
	// hot config reload creates: the documentation is immutable and does not
	// depend on cfg.
	referenceDocs := docagent.NewWithSchema(docs.Files, docsConfigSchema)

	initialStorePath := configStorePath(cfg)
	initialStore, err := sqlite.New(initialStorePath)
	if err != nil {
		slog.Error("failed to create store", "error", err)
		os.Exit(1)
	}

	initialSrv, err := server.New(cfg, logs, perfMon, initialStore, buildInfo, hardwareSnapshot, referenceDocs)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		initialStore.Close()
		os.Exit(1)
	}

	// activeSrv is swapped atomically during hot reload.
	var activeMu sync.RWMutex
	activeSrv := initialSrv
	activeStore := initialStore
	activeStorePath := initialStorePath

	tailcatHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeMu.RLock()
		srv := activeSrv
		activeMu.RUnlock()
		srv.ServeTailcatHTTP(w, r)
	})
	tailcatLogger := func(cfg config.Config) tailcat.Logger {
		if cfg.Tailcat != nil && cfg.Tailcat.Debug {
			return proxyLog
		}
		return nil
	}
	startTailcat := func(cfg config.Config) (*tailcat.Server, error) {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return tailcat.Start(ctx, tailcat.ServerOptions{
			PrivateKey: tailcatPrivateKey,
			Handler:    tailcatHandler,
			Logger:     tailcatLogger(cfg),
		})
	}

	var activeTailcat *tailcat.Server
	if cfg.TailcatEnabled() {
		activeTailcat, err = startTailcat(cfg)
		if err != nil {
			slog.Error("failed to start Tailcat server", "error", err)
			initialSrv.Shutdown(shutdownTimeout)
			initialStore.Close()
			os.Exit(1)
		}
	}
	if activeTailcat != nil {
		initialSrv.SetTailcatAddress(activeTailcat.Address())
		proxyLog.Infof("Tailcat listening on virtual TCP port 80: %s", activeTailcat.Address())
	}

	// ------------------------------------------------------------------
	// Multi-listener support: each entry in cfg.listeners gets its own
	// http.Server sharing the active llama-swap server, with a ListenerScope
	// restricting which models requests may reach. An occupied port skips
	// that listener with a CLI warning instead of failing startup.
	// ------------------------------------------------------------------
	type managedListener struct {
		addr  string
		srv   *http.Server
		scope atomic.Pointer[server.ListenerScope]
		// closing is set before an intentional Shutdown so the serve
		// goroutine can tell a reconcile-driven stop apart from a real
		// listener failure (only the latter is fatal).
		closing atomic.Bool
	}

	var (
		listenersMu sync.Mutex
		managed     []*managedListener
		bindFailed  = map[string]string{} // addr -> reason (reported via /api/listeners)
	)

	desiredScopes := func(cfg config.Config) []server.ListenerScope {
		// The -listen address is always the primary listener serving every
		// model; configured listeners add scoped ports on top.
		scopes := []server.ListenerScope{{Name: "default", Listen: listenAddr, Patterns: []string{"*"}}}
		for _, l := range cfg.Listeners {
			scopes = append(scopes, *server.NewListenerScope(l))
		}
		return scopes
	}

	listenerHandler := func(ml *managedListener) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(server.WithScope(r.Context(), ml.scope.Load()))
			activeMu.RLock()
			srv := activeSrv
			activeMu.RUnlock()
			srv.ServeHTTP(w, r)
		})
	}

	bindListener := func(sc server.ListenerScope) (*managedListener, error) {
		ln, err := net.Listen("tcp", sc.Listen)
		if err != nil {
			return nil, err
		}
		ml := &managedListener{addr: sc.Listen}
		ml.scope.Store(&sc)
		ml.srv = &http.Server{Handler: listenerHandler(ml)}
		go func() {
			var serveErr error
			if useTLS {
				proxyLog.Infof("listener %q serving with TLS on https://%s", sc.Name, ml.addr)
				serveErr = ml.srv.ServeTLS(ln, *flagCertFile, *flagKeyFile)
			} else {
				serveErr = ml.srv.Serve(ln)
			}
			// A listener that stopped because reconcileListeners closed it is
			// an expected part of a config reload, and Shutdown/Close can
			// surface as something other than http.ErrServerClosed (notably
			// net.ErrClosed when the listener is torn down mid-accept).
			// Treat only *unexpected* failures as fatal, otherwise a routine
			// config edit can exit the whole process and take every running
			// model and any unsaved UI state with it.
			if ml.closing.Load() {
				proxyLog.Debugf("listener %s (%s) stopped for reconcile", ml.addr, sc.Name)
				return
			}
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
				slog.Error("http listener error", "addr", ml.addr, "error", serveErr)
				os.Exit(1)
			}
		}()
		return ml, nil
	}

	shutdownManaged := func(ml *managedListener, timeout time.Duration) {
		ml.closing.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := ml.srv.Shutdown(ctx); err != nil {
			proxyLog.Warnf("listener %s shutdown error: %v", ml.addr, err)
		}
	}

	reconcileListeners := func(cfg config.Config) int {
		scopes := desiredScopes(cfg)
		desired := make(map[string]server.ListenerScope, len(scopes))
		for _, sc := range scopes {
			normalized, err := config.ParseListenAddr(sc.Listen)
			if err != nil {
				proxyLog.Warnf("listener %q: invalid address %q: %v", sc.Name, sc.Listen, err)
				continue
			}
			sc.Listen = normalized
			desired[normalized] = sc
		}

		listenersMu.Lock()
		defer listenersMu.Unlock()
		bindFailed = make(map[string]string)

		kept := make([]*managedListener, 0, len(desired))
		for _, ml := range managed {
			sc, wanted := desired[ml.addr]
			if wanted {
				ml.scope.Store(&sc)
				kept = append(kept, ml)
				continue
			}
			proxyLog.Infof("listener %s (%s) removed from config, closing", ml.addr, ml.scope.Load().Name)
			go shutdownManaged(ml, 5*time.Second)
		}

		existing := make(map[string]bool, len(kept))
		for _, ml := range kept {
			existing[ml.addr] = true
		}
		for addr, sc := range desired {
			if existing[addr] {
				continue
			}
			ml, err := bindListener(sc)
			if err != nil {
				bindFailed[addr] = err.Error()
				proxyLog.Warnf("listener %q (%s): port unavailable, skipping (%v)", sc.Name, addr, err)
				continue
			}
			kept = append(kept, ml)
			patternCount := len(sc.Patterns)
			proxyLog.Infof("llama-swap listening on http://%s (listener %q, %d model pattern(s))", addr, sc.Name, patternCount)
			if !swaputil.IsLoopbackAddr(addr) {
				proxyLog.Infof("listener %q is reachable by all hosts on the network, use localhost:%s to restrict to loopback only", sc.Name, listenerPortOnly(addr))
			}
		}
		managed = kept
		return len(managed)
	}

	listenerStatusProvider := func() []server.ListenerRuntimeStatus {
		listenersMu.Lock()
		defer listenersMu.Unlock()
		statuses := make([]server.ListenerRuntimeStatus, 0, len(managed)+len(bindFailed))
		for _, ml := range managed {
			sc := ml.scope.Load()
			statuses = append(statuses, server.ListenerRuntimeStatus{Name: sc.Name, Listen: ml.addr, Bound: true})
		}
		for addr, reason := range bindFailed {
			statuses = append(statuses, server.ListenerRuntimeStatus{Name: "port-" + listenerPortOnly(addr), Listen: addr, Bound: false, Error: reason})
		}
		return statuses
	}

	// reload guards against overlapping reloads triggered by concurrent signals
	// or file-watcher callbacks.
	var reloading bool
	var reloadMu sync.Mutex
	var reload func()

	wireServerHooks := func(srv *server.Server) {
		if *flagConfig != "" {
			srv.SetConfigFile(*flagConfig)
		}
		srv.SetReloadFunc(func() { go reload() })
		srv.SetListenerStatusFunc(listenerStatusProvider)
	}
	wireServerHooks(initialSrv)

	reload = func() {
		reloadMu.Lock()
		if reloading {
			reloadMu.Unlock()
			return
		}
		reloading = true
		reloadMu.Unlock()
		defer func() {
			reloadMu.Lock()
			reloading = false
			reloadMu.Unlock()
		}()

		proxyLog.Info("reloading configuration")

		newCfg, err := config.LoadConfigSources(*flagConfig, *flagConfigDir)
		if err != nil {
			proxyLog.Warnf("failed to reload config: %v", err)
			return
		}
		if err := configureTailcatListener(&newCfg, *flagListenTailcat); err != nil {
			proxyLog.Warnf("failed to reload config: %v", err)
			return
		}

		newStorePath := configStorePath(newCfg)
		activeMu.RLock()
		currentStore := activeStore
		currentStorePath := activeStorePath
		currentTailcat := activeTailcat
		activeMu.RUnlock()

		newStore := currentStore
		storeChanged := newStorePath != currentStorePath
		if storeChanged {
			newStore, err = sqlite.New(newStorePath)
			if err != nil {
				proxyLog.Warnf("failed to create new store during reload: %v", err)
				return
			}
		}

		newSrv, err := server.New(newCfg, logs, perfMon, newStore, buildInfo, hardwareSnapshot, referenceDocs)
		if err != nil {
			proxyLog.Warnf("failed to build new server during reload: %v", err)
			if storeChanged {
				newStore.Close()
			}
			return
		}
		wireServerHooks(newSrv)

		if currentTailcat != nil {
			newSrv.SetTailcatAddress(currentTailcat.Address())
			// The listener is never restarted: connected Tailcat clients do not
			// reconnect to a restarted server. tailcat.allow is enforced by
			// newSrv per request; only the debug logger needs updating here.
			currentTailcat.SetLogger(tailcatLogger(newCfg))
		}

		activeMu.Lock()
		old := activeSrv
		oldStore := activeStore
		activeSrv = newSrv
		activeStore = newStore
		activeStorePath = newStorePath
		activeMu.Unlock()

		reconcileListeners(newCfg)

		applyLogSettings(newCfg)
		if perfMon != nil {
			perfMon.UpdateConfig(newCfg.Performance)
		}

		if err := old.Shutdown(shutdownTimeout); err != nil {
			proxyLog.Warnf("error shutting down old server during reload: %v", err)
		}
		if storeChanged {
			if err := oldStore.Close(); err != nil {
				proxyLog.Warnf("error closing old store during reload: %v", err)
			}
		}

		// Notify UI after a short delay so it can refresh model state.
		time.AfterFunc(3*time.Second, func() {
			event.Emit(swaputil.ConfigFileChangedEvent{State: swaputil.ReloadingStateEnd})
		})

		proxyLog.Info("configuration reloaded")
	}

	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	defer watcherCancel()

	if *flagWatchConfig {
		proxyLog.Info("watching configuration for changes (poll-based, 2s interval)")

		if *flagConfig != "" {
			absConfigPath, err := filepath.Abs(*flagConfig)
			if err != nil {
				slog.Error("watch-config: failed to resolve config path", "error", err)
				os.Exit(1)
			}
			go func() {
				(&configwatcher.Watcher{
					Path:     absConfigPath,
					Interval: configwatcher.DefaultInterval,
					OnChange: func() {
						// llama-swap's own config writes (save-only UI edits,
						// capability write-back) must not reload: the change
						// is the one the user just saved, a reload would stop
						// running models, and the file state already matches
						// what the next explicit apply will read.
						activeMu.RLock()
						srv := activeSrv
						activeMu.RUnlock()
						if srv.IsSelfWrite(absConfigPath) {
							return
						}
						reload()
					},
				}).Run(watcherCtx)
			}()
		}

		if *flagConfigDir != "" {
			absConfigDir, err := filepath.Abs(*flagConfigDir)
			if err != nil {
				slog.Error("watch-config: failed to resolve config-dir path", "error", err)
				os.Exit(1)
			}
			go func() {
				(&configwatcher.DirWatcher{
					Path:     absConfigDir,
					Interval: configwatcher.DefaultInterval,
					OnChange: reload,
				}).Run(watcherCtx)
			}()
		}
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Bind all configured listeners. Startup fails only when nothing could be
	// bound at all.
	if len(cfg.Listeners) > 0 && useTLS {
		proxyLog.Warnf("TLS flags are ignored because listeners are configured in %s", *flagConfig)
	}
	if bound := reconcileListeners(cfg); bound == 0 {
		slog.Error("no listeners could be bound, exiting")
		os.Exit(1)
	}

	exitChan := make(chan struct{})

	go func() {
		for {
			sig := <-sigChan
			switch sig {
			case syscall.SIGHUP:
				proxyLog.Info("received SIGHUP, reloading config")
				go reload()
			case syscall.SIGINT, syscall.SIGTERM:
				proxyLog.Infof("received signal %v, shutting down", sig)
				watcherCancel()

				// Backstop against a stalled shutdown: force the process to
				// exit once the whole graceful sequence has had its full budget.
				// On Windows the Job Object reaps upstream processes on exit, so
				// a forced exit still cleans up rather than orphaning children.
				go func() {
					time.Sleep(shutdownTimeout + 5*time.Second)
					proxyLog.Warnf("graceful shutdown exceeded %v, forcing exit", shutdownTimeout)
					os.Exit(1)
				}()

				activeMu.RLock()
				srv := activeSrv
				st := activeStore
				tailcatRuntime := activeTailcat
				activeMu.RUnlock()

				// Close long-lived SSE streams first so listener Shutdown can
				// drain without blocking on them for the full timeout.
				srv.CloseStreams()

				// Both phases share a single deadline so total shutdown is
				// bounded by shutdownTimeout rather than 2x it.
				deadline := time.Now().Add(shutdownTimeout)
				shutdownCtx, cancel := context.WithDeadline(context.Background(), deadline)
				defer cancel()

				listenersMu.Lock()
				all := make([]*managedListener, len(managed))
				copy(all, managed)
				listenersMu.Unlock()
				var wg sync.WaitGroup
				for _, ml := range all {
					ml.closing.Store(true)
					wg.Add(1)
					go func(ml *managedListener) {
						defer wg.Done()
						if err := ml.srv.Shutdown(shutdownCtx); err != nil {
							proxyLog.Warnf("http server shutdown error (%s): %v", ml.addr, err)
						}
					}(ml)
				}
				if tailcatRuntime != nil {
					if err := tailcatRuntime.Close(shutdownCtx); err != nil {
						proxyLog.Warnf("Tailcat server shutdown error: %v", err)
					}
				}
				wg.Wait()

				// Clamp the remaining budget to a small positive value: a
				// non-positive timeout makes the router fall back to its own
				// healthCheckTimeout, which would defeat the shared deadline.
				remaining := time.Until(deadline)
				if remaining <= 0 {
					remaining = time.Millisecond
				}
				if err := srv.Shutdown(remaining); err != nil {
					proxyLog.Warnf("router shutdown error: %v", err)
				}

				if perfMon != nil {
					perfMon.Stop()
				}
				if err := st.Close(); err != nil {
					proxyLog.Warnf("store shutdown error: %v", err)
				}

				close(exitChan)
				return
			}
		}
	}()

	<-exitChan
	proxyLog.Info("shutdown complete")
}
