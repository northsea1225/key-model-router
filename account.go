package main

import (
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

type cpaConfigFile struct {
	APIKeys []string `yaml:"api-keys"`
}

type accountInfo struct {
	Fingerprint string `json:"fingerprint"`
	Masked      string `json:"masked"`
	Alias       string `json:"alias"`
	TargetModel string `json:"target_model"`
	ValidTarget bool   `json:"valid_target"`
}

func readCPAConfig(path string) (cpaConfigFile, error) {
	var cfg cpaConfigFile
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return cfg, errRead
	}
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		return cpaConfigFile{}, errUnmarshal
	}
	return cfg, nil
}

func (r *runtime) Accounts() ([]accountInfo, error) {
	cfg := r.Config()
	cpaCfg, errRead := readCPAConfig(cfg.CPAConfigPath)
	if errRead != nil {
		return nil, errRead
	}
	state := r.State()
	models := r.Models()
	out := make([]accountInfo, 0, len(cpaCfg.APIKeys))
	seen := make(map[string]struct{}, len(cpaCfg.APIKeys))
	for _, key := range cpaCfg.APIKeys {
		key = normalizeTarget(key)
		if key == "" {
			continue
		}
		fingerprint := sha256Hex(key)
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		target := state.Target(fingerprint)
		validTarget := true
		if target != "" {
			known, present := models.Has(target)
			validTarget = !known || present
		}
		out = append(out, accountInfo{
			Fingerprint: fingerprint,
			Masked:      maskSecret(key),
			Alias:       state.Alias(fingerprint),
			TargetModel: target,
			ValidTarget: validTarget,
		})
	}
	return out, nil
}

// modelProbeKeys returns up to limit API keys from the CPA config. The values
// are kept in memory only and are never logged or returned to the browser.
func (r *runtime) modelProbeKeys(limit int) []string {
	cfg := r.Config()
	cpaCfg, errRead := readCPAConfig(cfg.CPAConfigPath)
	if errRead != nil {
		return nil
	}
	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, key := range cpaCfg.APIKeys {
		key = normalizeTarget(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (r *runtime) Fingerprints() map[string]struct{} {
	cfg := r.Config()
	cpaCfg, errRead := readCPAConfig(cfg.CPAConfigPath)
	if errRead != nil {
		return nil
	}
	out := make(map[string]struct{}, len(cpaCfg.APIKeys))
	for _, key := range cpaCfg.APIKeys {
		if key = normalizeTarget(key); key != "" {
			out[sha256Hex(key)] = struct{}{}
		}
	}
	return out
}

func sortedAccountFingerprints(accounts []accountInfo) []string {
	keys := make([]string, 0, len(accounts))
	for _, item := range accounts {
		keys = append(keys, item.Fingerprint)
	}
	sort.Strings(keys)
	return keys
}
