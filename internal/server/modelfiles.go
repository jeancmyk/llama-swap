package server

import (
	"os"
	"sort"

	"github.com/mostlygeek/llama-swap/internal/config"
)

// scanModelFiles checks the existence of every local model's file arguments
// (main gguf, mmproj, draft models) parsed from its effective command. It
// returns model IDs mapped to the arguments whose files do not exist on disk.
// The scan is best-effort: models whose command cannot be parsed, or that
// pass files through flags outside modelFileFlags, are never flagged.
func scanModelFiles(models map[string]config.ModelConfig) map[string][]modelFileInfo {
	missing := make(map[string][]modelFileInfo)
	for id, mc := range models {
		_, files := extractCmdPaths(mc.Cmd)
		for _, f := range files {
			if _, err := os.Stat(f.Path); err != nil {
				missing[id] = append(missing[id], f)
			}
		}
	}
	return missing
}

// warnMissingFiles logs one warning per missing file argument in stable model
// ID order. Called once per Server construction, i.e. at startup and after
// every hot reload.
func (s *Server) warnMissingFiles() {
	ids := make([]string, 0, len(s.missingFiles))
	for id := range s.missingFiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, f := range s.missingFiles[id] {
			s.logs.ProxyLogs.Warnf("model %q: file argument %s %q not found on disk; the model is not served on the API until it exists", id, f.Flag, f.Path)
		}
	}
}
