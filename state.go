package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type stateEntry struct {
	Alias       string `json:"alias"`
	TargetModel string `json:"target_model"`
}

type stateFile struct {
	Entries map[string]stateEntry
}

type stateStore struct {
	mu       sync.RWMutex
	path     string
	entries  map[string]stateEntry
	seeds    map[string]string
	lastErr  string
	loadedAt time.Time
	modTime  time.Time
	size     int64
	lastStat time.Time
}

func newStateStore(path string) *stateStore {
	return &stateStore{
		path:    path,
		entries: make(map[string]stateEntry),
		seeds:   make(map[string]string),
	}
}

func (s *stateStore) setPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == path {
		return
	}
	s.path = path
	s.entries = make(map[string]stateEntry)
	s.lastErr = ""
	s.modTime = time.Time{}
	s.size = -1
}

func (s *stateStore) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *stateStore) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

func (s *stateStore) reloadLocked() error {
	info, errStat := os.Stat(s.path)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			s.entries = make(map[string]stateEntry)
			s.lastErr = ""
			s.modTime = time.Time{}
			s.size = -1
			s.lastStat = time.Now()
			return nil
		}
		s.lastErr = errStat.Error()
		return errStat
	}
	raw, errRead := os.ReadFile(s.path)
	if errRead != nil {
		s.lastErr = errRead.Error()
		return errRead
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		logWarn("key-model-router: state file permissions are too broad path="+s.path+
			" mode="+fmt.Sprintf("%04o", mode), nil)
	}
	entries, errParse := parseStateFile(raw)
	if errParse != nil {
		// Corrupt state must never take routing down: behave as "no routes".
		s.entries = make(map[string]stateEntry)
		s.lastErr = errParse.Error()
		s.modTime = info.ModTime()
		s.size = info.Size()
		s.lastStat = time.Now()
		logError("key-model-router: state file is invalid; treating all keys as unrouted path="+s.path+
			" error="+errParse.Error(), nil)
		return errParse
	}
	s.entries = entries
	s.lastErr = ""
	s.modTime = info.ModTime()
	s.size = info.Size()
	s.loadedAt = time.Now()
	s.lastStat = time.Now()
	return nil
}

func parseStateFile(raw []byte) (map[string]stateEntry, error) {
	var decoded map[string]stateEntry
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		return nil, fmt.Errorf("invalid state JSON: %w", errUnmarshal)
	}
	if decoded == nil {
		return nil, fmt.Errorf("invalid state JSON: expected an object")
	}
	out := make(map[string]stateEntry, len(decoded))
	for fingerprint, entry := range decoded {
		if !validFingerprint(fingerprint) {
			return nil, fmt.Errorf("invalid fingerprint key %q", fingerprint)
		}
		entry.Alias = normalizeAlias(entry.Alias)
		entry.TargetModel = normalizeTarget(entry.TargetModel)
		out[fingerprint] = entry
	}
	return out, nil
}

func (s *stateStore) maybeReloadLocked() {
	now := time.Now()
	if !s.lastStat.IsZero() && now.Sub(s.lastStat) < 500*time.Millisecond {
		return
	}
	info, errStat := os.Stat(s.path)
	if errStat != nil {
		if os.IsNotExist(errStat) {
			if len(s.entries) != 0 || s.lastErr != "" {
				_ = s.reloadLocked()
			}
			s.lastStat = now
			return
		}
		s.lastStat = now
		return
	}
	if info.ModTime().Equal(s.modTime) && info.Size() == s.size {
		s.lastStat = now
		return
	}
	_ = s.reloadLocked()
}

func (s *stateStore) Snapshot() (map[string]stateEntry, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeReloadLocked()
	out := make(map[string]stateEntry, len(s.entries))
	for key, value := range s.entries {
		out[key] = value
	}
	return out, s.lastErr
}

func (s *stateStore) Target(fingerprint string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeReloadLocked()
	return normalizeTarget(s.entries[fingerprint].TargetModel)
}

func (s *stateStore) Alias(fingerprint string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maybeReloadLocked()
	if alias := normalizeAlias(s.entries[fingerprint].Alias); alias != "" {
		return alias
	}
	return normalizeAlias(s.seeds[fingerprint])
}

func (s *stateStore) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

func (s *stateStore) LoadedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadedAt
}

func (s *stateStore) SeedAliases(path string) {
	if path == "" {
		return
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return
	}
	var seeds map[string]string
	if errUnmarshal := json.Unmarshal(raw, &seeds); errUnmarshal != nil {
		logWarn("key-model-router: alias seed is invalid", map[string]any{"path": path})
		return
	}
	clean := make(map[string]string, len(seeds))
	for fingerprint, alias := range seeds {
		if !validFingerprint(fingerprint) {
			continue
		}
		if alias = normalizeAlias(alias); alias != "" {
			clean[fingerprint] = alias
		}
	}
	s.mu.Lock()
	s.seeds = clean
	s.mu.Unlock()
}

func (s *stateStore) SaveRows(rows []stateRow) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make(map[string]stateEntry, len(rows))
	for _, row := range rows {
		if !validFingerprint(row.Fingerprint) {
			return time.Time{}, fmt.Errorf("invalid fingerprint")
		}
		next[row.Fingerprint] = stateEntry{
			Alias:       normalizeAlias(row.Alias),
			TargetModel: normalizeTarget(row.TargetModel),
		}
	}
	raw, errMarshal := json.MarshalIndent(next, "", "  ")
	if errMarshal != nil {
		return time.Time{}, errMarshal
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.path)
	tmp, errCreate := os.CreateTemp(dir, ".key-model-router-*.tmp")
	if errCreate != nil {
		return time.Time{}, errCreate
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if errChmod := tmp.Chmod(0o600); errChmod != nil {
		_ = tmp.Close()
		return time.Time{}, errChmod
	}
	if _, errWrite := tmp.Write(raw); errWrite != nil {
		_ = tmp.Close()
		return time.Time{}, errWrite
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		return time.Time{}, errSync
	}
	if errClose := tmp.Close(); errClose != nil {
		return time.Time{}, errClose
	}
	if errRename := os.Rename(tmpName, s.path); errRename != nil {
		return time.Time{}, errRename
	}
	if dirFile, errOpenDir := os.Open(dir); errOpenDir == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	if errChmod := os.Chmod(s.path, 0o600); errChmod != nil {
		return time.Time{}, errChmod
	}

	s.entries = next
	s.lastErr = ""
	s.modTime = time.Now()
	if info, errStat := os.Stat(s.path); errStat == nil {
		s.modTime = info.ModTime()
		s.size = info.Size()
	}
	s.lastStat = time.Now()
	return s.modTime, nil
}

type stateRow struct {
	Fingerprint string `json:"fingerprint"`
	Alias       string `json:"alias"`
	TargetModel string `json:"target_model"`
}

func sortedEntries(entries map[string]stateEntry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
