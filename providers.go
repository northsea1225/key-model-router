package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// providerCandidate is one provider CPA can use for a model.
type providerCandidate struct {
	// Name is what the settings UI shows.
	Name string
	// Provider is the provider key CPA registers the credential under.
	Provider string
	// Priority is the routing priority; CPA prefers the highest value.
	Priority int
	// Order preserves config order for stable tie-breaking.
	Order int
	// Source is "config" when the model is listed for the provider in
	// config.yaml, and "oauth" when the provider only matches by model family.
	Source string
	// Count is how many credentials back this provider (config entries plus
	// OAuth accounts of the same type).
	Count int
}

const (
	providerSourceConfig = "config"
	providerSourceOAuth  = "oauth"
)

// providerIndex maps a model to the providers that can serve it, ordered the way
// CPA orders candidates (priority descending, exact config matches first).
//
// Sources:
//   - openai-compatibility, codex/xai/claude/gemini/interactions/vertex API key
//     sections carry explicit model lists, so those mappings are exact.
//   - OAuth accounts live under auth-dir and carry no model list; they are
//     matched by model family and marked as inferred.
type providerIndex struct {
	mu             sync.RWMutex
	byName         map[string][]providerCandidate
	oauthProviders []providerCandidate
	err            string
}

type providerModel struct {
	Name  string `yaml:"name"`
	Alias string `yaml:"alias"`
}

type providerKeyEntry struct {
	Disabled bool            `yaml:"disabled"`
	Priority int             `yaml:"priority"`
	Models   []providerModel `yaml:"models"`
}

type providerConfigFile struct {
	AuthDir string `yaml:"auth-dir"`

	OpenAICompatibility []struct {
		Name     string `yaml:"name"`
		Prefix   string `yaml:"prefix"`
		Disabled bool   `yaml:"disabled"`
		Priority int    `yaml:"priority"`
		APIKeys  []struct {
			APIKey string `yaml:"api-key"`
		} `yaml:"api-key-entries"`
		Models []providerModel `yaml:"models"`
	} `yaml:"openai-compatibility"`

	CodexKey        []providerKeyEntry `yaml:"codex-api-key"`
	XAIKey          []providerKeyEntry `yaml:"xai-api-key"`
	ClaudeKey       []providerKeyEntry `yaml:"claude-api-key"`
	GeminiKey       []providerKeyEntry `yaml:"gemini-api-key"`
	InteractionsKey []providerKeyEntry `yaml:"interactions-api-key"`
	VertexKey       []providerKeyEntry `yaml:"vertex-api-key"`
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

	byName := make(map[string][]providerCandidate)
	add := func(model string, candidate providerCandidate) {
		model = strings.TrimSpace(model)
		if model == "" || candidate.Provider == "" {
			return
		}
		for i, existing := range byName[model] {
			if existing.Provider != candidate.Provider {
				continue
			}
			if candidate.Priority > existing.Priority {
				byName[model][i].Priority = candidate.Priority
			}
			byName[model][i].Count++
			return
		}
		if candidate.Count == 0 {
			candidate.Count = 1
		}
		byName[model] = append(byName[model], candidate)
	}
	addModels := func(provider, display string, priority, order int, models []providerModel) {
		for _, model := range models {
			for _, name := range []string{model.Name, model.Alias} {
				add(name, providerCandidate{
					Name:     display,
					Provider: provider,
					Priority: priority,
					Order:    order,
					Source:   providerSourceConfig,
				})
			}
		}
	}

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
		addModels(openAICompatProviderKey(name), name, entry.Priority, order, entry.Models)
	}

	keySections := []struct {
		provider string
		entries  []providerKeyEntry
	}{
		{"codex", cfg.CodexKey},
		{"xai", cfg.XAIKey},
		{"claude", cfg.ClaudeKey},
		{"gemini", cfg.GeminiKey},
		{"interactions", cfg.InteractionsKey},
		{"vertex", cfg.VertexKey},
	}
	order := len(cfg.OpenAICompatibility)
	for _, section := range keySections {
		for _, entry := range section.entries {
			if entry.Disabled {
				continue
			}
			addModels(section.provider, section.provider, entry.Priority, order, entry.Models)
			order++
		}
	}

	for model := range byName {
		sortCandidates(byName[model])
	}

	oauthProviders := scanOAuthProviders(cfg.AuthDir, filepath.Dir(path))

	p.mu.Lock()
	p.byName = byName
	p.oauthProviders = oauthProviders
	p.err = ""
	p.mu.Unlock()
}

func sortCandidates(candidates []providerCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Source != candidates[j].Source {
			return candidates[i].Source == providerSourceConfig
		}
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		return candidates[i].Order < candidates[j].Order
	})
}

// scanOAuthProviders reads auth-dir token files and returns one entry per
// provider type. OAuth files carry no model list, so the type is later matched
// by model family.
func scanOAuthProviders(authDir, configDir string) []providerCandidate {
	dir := strings.TrimSpace(authDir)
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(configDir, dir)
	}
	des, errRead := os.ReadDir(dir)
	if errRead != nil {
		return nil
	}
	type state struct {
		priority int
		count    int
	}
	found := make(map[string]state)
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(strings.ToLower(de.Name()), ".json") {
			continue
		}
		raw, errFile := os.ReadFile(filepath.Join(dir, de.Name()))
		if errFile != nil {
			continue
		}
		var meta struct {
			Type     string `json:"type"`
			Priority int    `json:"priority"`
			Disabled bool   `json:"disabled"`
		}
		if errUnmarshal := json.Unmarshal(raw, &meta); errUnmarshal != nil {
			continue
		}
		provider := normalizeProvider(meta.Type)
		if provider == "" || meta.Disabled {
			continue
		}
		entry := found[provider]
		entry.count++
		if meta.Priority > entry.priority {
			entry.priority = meta.Priority
		}
		found[provider] = entry
	}
	out := make([]providerCandidate, 0, len(found))
	for provider, entry := range found {
		out = append(out, providerCandidate{
			Name:     provider,
			Provider: provider,
			Priority: entry.priority,
			Source:   providerSourceOAuth,
			Count:    entry.count,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
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

// exactCandidatesFor returns providers whose config entry lists the model.
func (p *providerIndex) exactCandidatesFor(model string) []providerCandidate {
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

// CandidatesFor returns exact config matches followed by OAuth providers that
// match the model family. The UI uses the full list; routing prefers exact ones.
func (p *providerIndex) CandidatesFor(model string) []providerCandidate {
	out := p.exactCandidatesFor(model)
	p.mu.RLock()
	oauth := p.oauthProviders
	p.mu.RUnlock()
	for _, candidate := range oauth {
		if idx := candidateIndex(out, candidate.Provider); idx >= 0 {
			// Same provider key: fold the OAuth accounts into the credential count.
			out[idx].Count += candidate.Count
			continue
		}
		if !providerServesModelFamily(candidate.Provider, model) {
			continue
		}
		out = append(out, candidate)
	}
	sortCandidates(out)
	return out
}

func candidateIndex(candidates []providerCandidate, provider string) int {
	for i, candidate := range candidates {
		if candidate.Provider == provider {
			return i
		}
	}
	return -1
}

// providerServesModelFamily is the heuristic used for OAuth accounts, which do
// not publish a model list.
func providerServesModelFamily(provider, model string) bool {
	base := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(base, "/"); idx >= 0 && idx < len(base)-1 {
		base = base[idx+1:]
	}
	switch normalizeProvider(provider) {
	case "xai":
		return strings.HasPrefix(base, "grok")
	case "codex", "openai":
		return strings.HasPrefix(base, "gpt-") ||
			strings.HasPrefix(base, "o1") ||
			strings.HasPrefix(base, "o3") ||
			strings.HasPrefix(base, "o4") ||
			strings.Contains(base, "codex")
	case "claude":
		return strings.HasPrefix(base, "claude")
	case "gemini", "gemini-cli", "antigravity":
		if strings.HasPrefix(base, "gemini") {
			return true
		}
		return normalizeProvider(provider) == "antigravity" && strings.HasPrefix(base, "claude")
	}
	return false
}

func (p *providerIndex) LastError() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.err
}

// Models returns every model that has an exact config mapping.
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
