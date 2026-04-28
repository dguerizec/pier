package headscale

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// Record matches one entry in headscale's extra_records_path JSON file.
// Headscale supports A and AAAA today (the protocol limit), but we keep
// Type generic so future record types fall in cleanly.
type Record struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Add ensures (name, A, ip) is present in the records file. Idempotent: a
// re-add with the same triplet is a no-op (returns added=false).
//
// Returns ErrConflict when an entry with the same name exists but its
// value differs — the caller is mutating someone else's record and that
// would be silent destruction.
func Add(path, name, ip string) (added bool, err error) {
	rec := Record{Name: name, Type: "A", Value: ip}
	return mutate(path, func(records []Record) ([]Record, bool, error) {
		for _, r := range records {
			if r.Name != name {
				continue
			}
			if r.Type == rec.Type && r.Value == rec.Value {
				return records, false, nil
			}
			return nil, false, fmt.Errorf("%w: %s already maps to %s/%s",
				ErrConflict, name, r.Type, r.Value)
		}
		records = append(records, rec)
		sortRecords(records)
		return records, true, nil
	})
}

// Remove drops the entry whose name matches. Idempotent: removing a
// missing name returns removed=false.
func Remove(path, name string) (removed bool, err error) {
	return mutate(path, func(records []Record) ([]Record, bool, error) {
		out := records[:0]
		dropped := false
		for _, r := range records {
			if r.Name == name {
				dropped = true
				continue
			}
			out = append(out, r)
		}
		if !dropped {
			return records, false, nil
		}
		return out, true, nil
	})
}

// Has reports whether a record exists for name.
func Has(path, name string) (bool, error) {
	records, err := List(path)
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if r.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// List returns the current contents of the records file. An empty or
// missing file is treated as the empty list.
func List(path string) ([]Record, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	var records []Record
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("records: parse %s: %w", path, err)
	}
	return records, nil
}

// ErrConflict is returned by Add when name exists with a different value.
var ErrConflict = errors.New("records: name already mapped to a different value")

// mutate is the read-modify-write loop with flock + atomic rename. The
// callback decides what the new state should be and reports whether
// anything actually changed (so we can skip the rewrite when it's a no-op).
func mutate(path string, fn func([]Record) (newRecords []Record, changed bool, err error)) (bool, error) {
	if path == "" {
		return false, errors.New("records: path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	// Lock a sentinel file alongside the records JSON. Locking the JSON
	// itself isn't enough: each writer renames a new file over it, which
	// changes the inode and breaks flock semantics for waiters that
	// opened the previous inode. The .lock file is never renamed, so its
	// inode is stable across concurrent writers.
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false, fmt.Errorf("records: open lock %s: %w", lockPath, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return false, fmt.Errorf("records: flock %s: %w", lockPath, err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	// Re-read the records file AFTER acquiring the lock so we observe
	// any update committed by the previous holder.
	body, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var records []Record
	if len(body) > 0 {
		if err := json.Unmarshal(body, &records); err != nil {
			return false, fmt.Errorf("records: parse %s: %w", path, err)
		}
	}

	updated, changed, err := fn(records)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}

	out, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		return false, err
	}
	out = append(out, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".records-*.json")
	if err != nil {
		return false, fmt.Errorf("records: create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return false, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return false, err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return false, err
	}
	// Atomic on the same filesystem; headscale's fsnotify watcher sees a
	// single rename event and re-reads the new file rather than catching
	// us mid-write.
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return false, fmt.Errorf("records: rename: %w", err)
	}
	return true, nil
}

// sortRecords keeps the file deterministic for diffs and human inspection.
func sortRecords(records []Record) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].Type < records[j].Type
	})
}
