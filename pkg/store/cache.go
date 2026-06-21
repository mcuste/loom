package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mcuste/loom/pkg/runtime"
)

// CacheEntry is the memoized result of one LLM task, persisted to
// <root>/cache/<hash>.json. Output is replayed verbatim on a cache hit.
type CacheEntry struct {
	Output string `json:"output"`
}

// CacheKey hashes the inputs that fully determine an LLM task's output —
// runtime, model, effort, system prompt, and the substituted prompt — into the
// stable filename stem used by LoadCache and SaveCache. Two calls with
// identical inputs return the same key; changing any input changes the key.
func CacheKey(rt runtime.Name, model runtime.Model, effort runtime.Effort, systemPrompt, prompt string) string {
	h := sha256.New()
	// Length-prefix each field so adjacent fields cannot be confused: without
	// it ("a", "bc") and ("ab", "c") would hash identically.
	for _, part := range []string{string(rt), string(model), string(effort), systemPrompt, prompt} {
		h.Write(fmt.Appendf(nil, "%d:", len(part)))
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// LoadCache reads the memoized entry at <root>/cache/<hash>.json. It returns
// (entry, true, nil) on a hit and (zero, false, nil) when the file is absent.
// root defaults to ".loom" when empty, matching [Config.Root].
func LoadCache(root, hash string) (CacheEntry, bool, error) {
	if root == "" {
		root = ".loom"
	}
	path := cachePath(root, hash)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CacheEntry{}, false, nil
		}
		return CacheEntry{}, false, fmt.Errorf("store: read cache %s: %w", path, err)
	}
	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return CacheEntry{}, false, fmt.Errorf("store: parse cache %s: %w", path, err)
	}
	return entry, true, nil
}

// SaveCache atomically writes entry to <root>/cache/<hash>.json. The bytes go
// to a .tmp sibling and are renamed into place, so a crash mid-write never
// leaves a half-written file. root defaults to ".loom" when empty.
func SaveCache(root, hash string, entry CacheEntry) error {
	if root == "" {
		root = ".loom"
	}
	dir := filepath.Join(root, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal cache: %w", err)
	}
	data = append(data, '\n')
	path := cachePath(root, hash)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("store: write cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("store: rename cache: %w", err)
	}
	return nil
}

// cachePath is the on-disk location of a memoized task entry.
func cachePath(root, hash string) string {
	return filepath.Join(root, "cache", hash+".json")
}
