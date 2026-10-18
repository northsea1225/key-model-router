package main

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// providerHealth is the aggregated state of one provider's credentials, as
// reported by the host. It lets the router skip a provider that has no usable
// credential right now instead of pinning a request to it.
type providerHealth struct {
	Credentials int
	Usable      int
	Priority    int
}

type authSnapshot struct {
	mu         sync.RWMutex
	byProvider map[string]providerHealth
	fetchedAt  time.Time
	lastErr    string

	refreshMu sync.Mutex
	startOnce sync.Once
	stop      chan struct{}
}

func newAuthSnapshot() *authSnapshot {
	return &authSnapshot{byProvider: make(map[string]providerHealth)}
}

type hostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

// Refresh asks the host for the credential list and aggregates it per provider.
// The credential payload is never logged or exposed.
func (a *authSnapshot) Refresh() error {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	raw, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		a.mu.Lock()
		a.lastErr = errCall.Error()
		a.mu.Unlock()
		return errCall
	}
	var resp hostAuthListResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		a.mu.Lock()
		a.lastErr = errUnmarshal.Error()
		a.mu.Unlock()
		return errUnmarshal
	}
	next := make(map[string]providerHealth)
	now := time.Now()
	for _, entry := range resp.Files {
		provider := normalizeProvider(entry.Provider)
		if provider == "" {
			continue
		}
		state := next[provider]
		state.Credentials++
		if credentialUsable(entry, now) {
			state.Usable++
			if entry.Priority > state.Priority {
				state.Priority = entry.Priority
			}
		}
		next[provider] = state
	}
	a.mu.Lock()
	a.byProvider = next
	a.fetchedAt = now
	a.lastErr = ""
	a.mu.Unlock()
	return nil
}

func credentialUsable(entry pluginapi.HostAuthFileEntry, now time.Time) bool {
	if entry.Disabled || entry.Unavailable {
		return false
	}
	if !entry.NextRetryAfter.IsZero() && entry.NextRetryAfter.After(now) {
		return false
	}
	// Unknown status values are treated as usable: skipping a healthy provider
	// would silently drop the route, which is worse than a single failed call.
	switch normalizeProvider(entry.Status) {
	case "disabled", "unavailable", "error", "invalid", "expired", "failed":
		return false
	default:
		return true
	}
}

func (a *authSnapshot) Health(provider string) (providerHealth, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	state, ok := a.byProvider[normalizeProvider(provider)]
	return state, ok
}

func (a *authSnapshot) Status() (fetchedAt time.Time, lastErr string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fetchedAt, a.lastErr
}

// Start refreshes the snapshot periodically. A failed call keeps the previous
// snapshot so routing does not degrade when the host is briefly busy.
func (a *authSnapshot) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 20 * time.Second
	}
	a.startOnce.Do(func() {
		a.stop = make(chan struct{})
		go func() {
			delay := 5 * time.Second
			for {
				select {
				case <-a.stop:
					return
				case <-time.After(delay):
				}
				_ = a.Refresh()
				delay = interval
			}
		}()
	})
}

func (a *authSnapshot) Close() {
	a.mu.Lock()
	stop := a.stop
	a.stop = nil
	a.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}
