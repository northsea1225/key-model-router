package main

import (
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type cpaConfigFile struct {
	APIKeys []string `yaml:"api-keys"`
}

type accountInfo struct {
	Fingerprint    string           `json:"fingerprint"`
	Masked         string           `json:"masked"`
	Alias          string           `json:"alias"`
	TargetModel    string           `json:"target_model"`
	TargetProvider string           `json:"target_provider,omitempty"`
	AutoProvider   string           `json:"auto_provider,omitempty"`
	ProviderList   []providerOption `json:"provider_list,omitempty"`
	ValidTarget    bool             `json:"valid_target"`
}

// providerOption is one selectable provider in the settings UI.
type providerOption struct {
	Name        string `json:"name"`
	Priority    int    `json:"priority"`
	Usable      bool   `json:"usable"`
	Source      string `json:"source,omitempty"`
	Credentials int    `json:"credentials,omitempty"`
	Available   int    `json:"available,omitempty"`
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
		target, provider := state.Route(fingerprint)
		validTarget := true
		if target != "" {
			known, present := models.Has(target)
			validTarget = !known || present
		}
		autoProvider := ""
		var options []providerOption
		if target != "" {
			autoProvider = r.AutoProvider(target)
			if index := r.Providers(); index != nil {
				for _, candidate := range index.CandidatesFor(target) {
					usable, _ := r.ProviderUsable(candidate.Name)
					usableCount, total := r.ProviderCounts(candidate.Name)
					if candidate.Count > 0 {
						total = candidate.Count
					}
					options = append(options, providerOption{
						Name:        candidate.Name,
						Priority:    candidate.Priority,
						Usable:      usable,
						Source:      candidate.Source,
						Credentials: total,
						Available:   usableCount,
					})
				}
			}
			if provider != "" && !hasOption(options, provider) {
				usable, _ := r.ProviderUsable(provider)
				options = append(options, providerOption{Name: provider, Usable: usable})
			}
		}
		out = append(out, accountInfo{
			Fingerprint:    fingerprint,
			Masked:         maskSecret(key),
			Alias:          state.Alias(fingerprint),
			TargetModel:    target,
			TargetProvider: provider,
			AutoProvider:   autoProvider,
			ProviderList:   options,
			ValidTarget:    validTarget,
		})
	}
	return out, nil
}

func hasOption(options []providerOption, name string) bool {
	for _, option := range options {
		if strings.EqualFold(option.Name, name) {
			return true
		}
	}
	return false
}

// providerListsModel reports whether the provider is known to serve the model,
// either from config.yaml or from the /v1/models owner field.
func providerListsModel(r *runtime, model, provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	if index := r.Providers(); index != nil {
		for _, candidate := range index.CandidatesFor(model) {
			if strings.EqualFold(candidate.Name, provider) || candidate.Provider == provider {
				return true
			}
		}
	}
	if owner, okOwner := r.Models().Provider(model); okOwner {
		return strings.EqualFold(owner, provider)
	}
	return false
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
