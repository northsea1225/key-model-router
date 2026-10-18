package main

import (
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// providerCandidate is one provider entry from CPA config.yaml that lists a model.
type providerCandidate struct {
	// Name is the bare entry name (what /v1/models reports as owned_by).
	Name string
	// Provider is the provider key CPA registers the entry under.
	Provider string
	// Priority is the entry priority; CPA prefers the highest value.
	Priority int
	// Order preserves the config file order for stable tie-breaking.
	Order int
}

// providerIndex maps a model name (or alias) to the config providers that list it,
// ordered the way CPA orders candidates (priority descending, then config order).
// The host only reports a single owner per model on /v1/models; this index is what
// lets the router pick the same provider CPA would have preferred.
type providerIndex struct {
	mu     sync.RWMutex
	byName map[string][]providerCandidate
	err    string
}

type providerConfigFile struct {
	OpenAICompatibility []struct {
		Name     string `yaml:"name"`
		Prefix   string `yaml:"prefix"`
		Disabled bool   `yaml:"disabled"`
		Priority int    `yaml:"priority"`
		APIKeys  []struct {
			APIKey string `yaml:"api-key"`
		} `yaml:"api-key-entries"`
		Models []struct {
			Name  string `yaml:"name"`
			Alias string `yaml:"alias"`
		} `yaml:"models"`
	} `yaml:"openai-compatibility"`
}

func newProviderIndex() *providerIndex {
	return &providerIndex{byName: make(map[string][]providerCandidate)}
}

func (p *providerIndex) Reload(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		p.setError(errRead.Error())
		return
	}
	var cfg providerConfigFile
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		p.setError(errUnmarshal.Error())
		return
	}
	next := make(map[string][]providerCandidate)
	for order, entry := range cfg.OpenAICompatibility {
		if entry.Disabled {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if name == "" {
			continue
		}
		hasKey := false
		for _, key := range entry.APIKeys {
			if strings.TrimSpace(key.APIKey) != "" {
				hasKey = true
				break
			}
		}
		if !hasKey {
			continue
		}
		candidate := providerCandidate{
			Name:     name,
			Provider: openAICompatProviderKey(name),
			Priority: entry.Priority,
			Order:    order,
		}
		for _, model := range entry.Models {
			for _, modelName := range []string{model.Name, model.Alias} {
				modelName = strings.TrimSpace(modelName)
				if modelName == "" {
					continue
				}
				if !hasCandidate(next[modelName], candidate.Provider) {
					next[modelName] = append(next[modelName], candidate)
				}
			}
		}
	}
	for model := range next {
		candidates := next[model]
		// CPA orders candidate credentials by priority descending; entries with the
		// same priority keep their config order.
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Priority != candidates[j].Priority {
				return candidates[i].Priority > candidates[j].Priority
			}
			return candidates[i].Order < candidates[j].Order
		})
		next[model] = candidates
	}
	p.mu.Lock()
	p.byName = next
	p.err = ""
	p.mu.Unlock()
}

func (p *providerIndex) setError(message string) {
	p.mu.Lock()
	p.err = message
	p.mu.Unlock()
}

func hasCandidate(candidates []providerCandidate, provider string) bool {
	for _, candidate := range candidates {
		if candidate.Provider == provider {
			return true
		}
	}
	return false
}

// CandidatesFor returns the configured providers serving the model, best first.
func (p *providerIndex) CandidatesFor(model string) []providerCandidate {
	p.mu.RLock()
	defer p.mu.RUnlock()
	values := p.byName[strings.TrimSpace(model)]
	if len(values) == 0 {
		return nil
	}
	out := make([]providerCandidate, len(values))
	copy(out, values)
	return out
}

func (p *providerIndex) LastError() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.err
}

func (p *providerIndex) Models() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.byName))
	for model := range p.byName {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

// openAICompatProviderKey mirrors the provider key CPA registers for an
// openai-compatibility entry.
func openAICompatProviderKey(name string) string {
	return "openai-compatible-" + strings.ToLower(strings.TrimSpace(name))
}
