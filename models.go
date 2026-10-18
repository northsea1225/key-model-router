package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type modelListResponse struct {
	Data []struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

type modelCache struct {
	mu        sync.RWMutex
	refreshMu sync.Mutex
	cfg       pluginConfig
	models    []string
	modelSet  map[string]struct{}
	providers map[string]string
	fetchedAt time.Time
	lastErr   string
	stop      chan struct{}
}

func newModelCache(cfg pluginConfig) *modelCache {
	c := &modelCache{cfg: cfg}
	c.start()
	return c
}

func (c *modelCache) reconfigure(cfg pluginConfig) {
	c.mu.Lock()
	c.cfg = cfg
	if c.stop != nil {
		close(c.stop)
		c.stop = nil
	}
	c.mu.Unlock()
	c.start()
}

func (c *modelCache) start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		return
	}
	stop := make(chan struct{})
	c.stop = stop
	interval := time.Duration(c.cfg.ModelRefreshSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		// Do not call back into CPA while plugin registration is still in
		// progress; the HTTP listener is not up until registration returns.
		delay := 3 * time.Second
		for {
			select {
			case <-stop:
				return
			case <-time.After(delay):
			}
			if errRefresh := c.Refresh(); errRefresh != nil {
				// CPA may still be starting or an api-key may be invalid;
				// retry quickly instead of waiting a full refresh interval.
				delay = 5 * time.Second
				continue
			}
			delay = interval
		}
	}()
}

func (c *modelCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		close(c.stop)
		c.stop = nil
	}
}

func (c *modelCache) Refresh() error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	keys := app.modelProbeKeys(3)
	if len(keys) == 0 {
		err := fmt.Errorf("no api-keys found in %s", cfg.CPAConfigPath)
		c.setError(err)
		return err
	}
	var lastErr error
	for _, key := range keys {
		models, errFetch := fetchModels(cfg.CPABaseURL, key)
		if errFetch != nil {
			lastErr = errFetch
			continue
		}
		c.mu.Lock()
		c.models = models.ids
		c.modelSet = make(map[string]struct{}, len(models.ids))
		for _, model := range models.ids {
			c.modelSet[model] = struct{}{}
		}
		c.providers = models.providers
		c.fetchedAt = time.Now()
		c.lastErr = ""
		c.mu.Unlock()
		if index := app.Providers(); index != nil {
			index.Reload(cfg.CPAConfigPath)
		}
		logDebug("key-model-router: model list refreshed", map[string]any{"count": len(models.ids)})
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("model list fetch failed")
	}
	c.setError(lastErr)
	return lastErr
}

func (c *modelCache) setError(err error) {
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
	logWarn("key-model-router: model list refresh failed", map[string]any{"error": err.Error()})
}

type modelSnapshot struct {
	ids       []string
	providers map[string]string
}

func fetchModels(baseURL, apiKey string) (modelSnapshot, error) {
	client := &http.Client{Timeout: 12 * time.Second}
	req, errReq := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if errReq != nil {
		return modelSnapshot{}, errReq
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "key-model-router/0.1")
	resp, errDo := client.Do(req)
	if errDo != nil {
		return modelSnapshot{}, fmt.Errorf("GET /v1/models: %w", errDo)
	}
	defer resp.Body.Close()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if errRead != nil {
		return modelSnapshot{}, fmt.Errorf("read /v1/models: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return modelSnapshot{}, fmt.Errorf("GET /v1/models: status %d", resp.StatusCode)
	}
	var parsed modelListResponse
	if errUnmarshal := json.Unmarshal(raw, &parsed); errUnmarshal != nil {
		return modelSnapshot{}, fmt.Errorf("decode /v1/models: %w", errUnmarshal)
	}
	seen := make(map[string]struct{}, len(parsed.Data))
	models := make([]string, 0, len(parsed.Data))
	providers := make(map[string]string, len(parsed.Data))
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if owner := normalizeProvider(item.OwnedBy); owner != "" {
			if _, exists := providers[id]; !exists {
				providers[id] = owner
			}
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.Strings(models)
	return modelSnapshot{ids: models, providers: providers}, nil
}

func (c *modelCache) List() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.models))
	copy(out, c.models)
	return out
}

func (c *modelCache) Has(model string) (known bool, present bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.modelSet) == 0 {
		return false, false
	}
	_, present = c.modelSet[model]
	return true, present
}

// Provider returns the provider key that /v1/models reported for the model.
// CPA sets owned_by to the provider that serves the model, so this is the key
// the model router needs for a provider-target decision.
func (c *modelCache) Provider(model string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	provider, ok := c.providers[strings.TrimSpace(model)]
	return provider, ok
}

// Providers returns a copy of the model -> provider map for the settings UI.
func (c *modelCache) Providers() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.providers))
	for model, provider := range c.providers {
		out[model] = provider
	}
	return out
}

func (c *modelCache) Status() (count int, fetchedAt time.Time, lastErr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.models), c.fetchedAt, c.lastErr
}
