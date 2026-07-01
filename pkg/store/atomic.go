package store

import (
	"encoding/json"
	"os"
)

// writeJSONAtomic serializes v as indented JSON and atomically writes it to
// path: the bytes go to path+".tmp" then os.Rename into path, so a crash mid-
// write never leaves a half-written file under the canonical path. Errors are
// returned unwrapped so callers can add context.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
