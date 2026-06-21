package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mcuste/loom/pkg/store"
)

// TestCacheMissThenHit_ReplaysStoredOutput pins the core memoization contract:
// a key that was never saved reports a miss, and after SaveCache the same key
// loads back the stored output as a hit.
func TestCacheMissThenHit_ReplaysStoredOutput(t *testing.T) {
	root := t.TempDir()
	key := store.CacheKey("claude-code", "sonnet", "medium", "be brief", "hello")

	if _, hit, err := store.LoadCache(root, key); err != nil {
		t.Fatalf("LoadCache (miss): unexpected error: %v", err)
	} else if hit {
		t.Fatalf("LoadCache (miss): hit = true, want false for an unsaved key")
	}

	if err := store.SaveCache(root, key, store.CacheEntry{Output: "world"}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	got, hit, err := store.LoadCache(root, key)
	if err != nil {
		t.Fatalf("LoadCache (hit): %v", err)
	}
	if !hit {
		t.Fatalf("LoadCache (hit): hit = false, want true after SaveCache")
	}
	if got.Output != "world" {
		t.Errorf("LoadCache (hit): Output = %q, want %q", got.Output, "world")
	}
}

// TestSaveCache_WritesEntryUnderCacheDir pins the on-disk location: SaveCache
// must place the entry at <root>/cache/<hash>.json so a warm cache survives
// across processes.
func TestSaveCache_WritesEntryUnderCacheDir(t *testing.T) {
	root := t.TempDir()
	if err := store.SaveCache(root, "abc123", store.CacheEntry{Output: "x"}); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	path := filepath.Join(root, "cache", "abc123.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected cache file at %s: %v", path, err)
	}
}

// TestCacheKey_DiffersWhenAnyInputChanges pins hash sensitivity: changing the
// prompt — or any other hashed input — must produce a different key, so a
// modified task never replays a stale output.
func TestCacheKey_DiffersWhenAnyInputChanges(t *testing.T) {
	base := store.CacheKey("claude-code", "sonnet", "medium", "be brief", "hello")

	cases := []struct {
		name string
		key  string
	}{
		{"prompt", store.CacheKey("claude-code", "sonnet", "medium", "be brief", "goodbye")},
		{"runtime", store.CacheKey("codex", "sonnet", "medium", "be brief", "hello")},
		{"model", store.CacheKey("claude-code", "opus", "medium", "be brief", "hello")},
		{"effort", store.CacheKey("claude-code", "sonnet", "high", "be brief", "hello")},
		{"system_prompt", store.CacheKey("claude-code", "sonnet", "medium", "be terse", "hello")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.key == base {
				t.Errorf("CacheKey unchanged when %s changed: both = %q", tc.name, base)
			}
		})
	}
}
