package main

import (
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// providerIndex maps a model name (or alias) to the provider keys that list it
// in CPA config.yaml. The host only reports one owner per model on /v1/models,
// and that owner may have no usable credential; this index lets the router pick
// a provider that is actually available.
type providerIndex struct {
	mu     sync.RWMutex
	byName map[string][]string
	err    string
}

type providerConfigFile struct {
	OpenAICompatibility []struct {
		Name     string `yaml:"name"`
		Prefix   string `yaml:"prefix"`
		Disabled bool   `yaml:"disabled"`
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
	return &providerIndex{byName: make(map[string][]string)}
}

func (p *providerIndex) Reload(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		p.mu.Lock()
		p.err = errRead.Error()
		p.mu.Unlock()
		return
	}
	var cfg providerConfigFile
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		p.mu.Lock()
		p.err = errUnmarshal.Error()
		p.mu.Unlock()
		return
	}
	next := make(map[string][]string)
	for _, entry := range cfg.OpenAICompatibility {
		if entry.Disabled {
			continue
		}
		provider := normalizeProvider(entry.Name)
		if provider == "" {
			continue
		}
		// CPA registers openai-compatibility entries under a prefixed provider
		// key; /v1/models reports only the bare entry name as owned_by.
		provider = "openai-compatible-" + provider
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
		for _, model := range entry.Models {
			for _, name := range []string{model.Name, model.Alias} {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				if !containsFold(next[name], provider) {
					next[name] = append(next[name], provider)
				}
			}
		}
	}
	p.mu.Lock()
	p.byName = next
	p.err = ""
	p.mu.Unlock()
}

// ProvidersFor returns the configured provider keys that serve the model.
func (p *providerIndex) ProvidersFor(model string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := p.byName[strings.TrimSpace(model)]
	if len(out) == 0 {
		return nil
	}
	cp := make([]string, len(out))
	copy(cp, out)
	return cp
}

func (p *providerIndex) LastError() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.err
}
