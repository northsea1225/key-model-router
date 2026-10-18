package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type pluginConfig struct {
	Enabled             bool   `yaml:"enabled"`
	RouteMode           string `yaml:"route_mode"`
	Listen              string `yaml:"listen"`
	CPABaseURL          string `yaml:"cpa_base_url"`
	CPAConfigPath       string `yaml:"cpa_config_path"`
	StateFile           string `yaml:"state_file"`
	PasswordFile        string `yaml:"password_file"`
	AliasSeedFile       string `yaml:"alias_seed_file"`
	ModelRefreshSeconds int    `yaml:"model_refresh_seconds"`
	SessionTTLSeconds   int    `yaml:"session_ttl_seconds"`
	PluginMemoryLimitMB int    `yaml:"plugin_memory_limit_mb"`
	MaxExecutorBodyMB   int    `yaml:"max_executor_body_mb"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:             true,
		RouteMode:           routeModeProvider,
		Listen:              "127.0.0.1:8320",
		CPABaseURL:          "http://127.0.0.1:8318",
		CPAConfigPath:       "/opt/cpa/config.yaml",
		StateFile:           "/opt/cpa/key-model-router.json",
		PasswordFile:        "/opt/cpa/key-model-router.pass",
		AliasSeedFile:       "",
		ModelRefreshSeconds: 300,
		SessionTTLSeconds:   8 * 60 * 60,
		PluginMemoryLimitMB: 96,
		MaxExecutorBodyMB:   2,
	}
}

const (
	// routeModeProvider lets CPA run the request on its native provider path with
	// a rewritten target model. The plugin never touches the body or the stream.
	routeModeProvider = "provider"
	// routeModeExecutor keeps the legacy behaviour: the plugin takes the request
	// over and calls back into the host, which copies the body across the ABI.
	routeModeExecutor = "executor"
)

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

func decodePluginConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return pluginConfig{}, errUnmarshal
		}
	}
	cfg.Listen = strings.TrimSpace(cfg.Listen)
	cfg.RouteMode = strings.ToLower(strings.TrimSpace(cfg.RouteMode))
	if cfg.RouteMode == "" {
		cfg.RouteMode = routeModeProvider
	}
	cfg.CPABaseURL = strings.TrimRight(strings.TrimSpace(cfg.CPABaseURL), "/")
	cfg.CPAConfigPath = strings.TrimSpace(cfg.CPAConfigPath)
	cfg.StateFile = strings.TrimSpace(cfg.StateFile)
	cfg.PasswordFile = strings.TrimSpace(cfg.PasswordFile)
	cfg.AliasSeedFile = strings.TrimSpace(cfg.AliasSeedFile)
	if cfg.ModelRefreshSeconds < 30 {
		cfg.ModelRefreshSeconds = 30
	}
	if cfg.SessionTTLSeconds < 300 {
		cfg.SessionTTLSeconds = 300
	}
	if cfg.PluginMemoryLimitMB < 32 {
		cfg.PluginMemoryLimitMB = 32
	}
	if cfg.MaxExecutorBodyMB < 1 {
		cfg.MaxExecutorBodyMB = 1
	}
	if errValidate := validatePluginConfig(cfg); errValidate != nil {
		return pluginConfig{}, errValidate
	}
	return cfg, nil
}

func validatePluginConfig(cfg pluginConfig) error {
	if cfg.Listen == "" {
		return fmt.Errorf("listen is required")
	}
	switch cfg.RouteMode {
	case routeModeProvider, routeModeExecutor:
	default:
		return fmt.Errorf("route_mode must be %q or %q, got %q", routeModeProvider, routeModeExecutor, cfg.RouteMode)
	}
	host, port, errSplit := net.SplitHostPort(cfg.Listen)
	if errSplit != nil {
		return fmt.Errorf("listen must be host:port: %w", errSplit)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf("listen port is required")
	}
	switch strings.TrimSpace(host) {
	case "127.0.0.1", "localhost", "::1":
	default:
		return fmt.Errorf("listen must bind loopback only, got %q", host)
	}
	parsedURL, errURL := url.Parse(cfg.CPABaseURL)
	if errURL != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return fmt.Errorf("cpa_base_url must be an absolute URL")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("cpa_base_url scheme must be http or https")
	}
	if !isLoopbackHost(parsedURL.Hostname()) {
		return fmt.Errorf("cpa_base_url must point at loopback, got %q", parsedURL.Hostname())
	}
	for name, path := range map[string]string{
		"cpa_config_path": cfg.CPAConfigPath,
		"state_file":      cfg.StateFile,
		"password_file":   cfg.PasswordFile,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if cfg.AliasSeedFile != "" && !filepath.IsAbs(cfg.AliasSeedFile) {
		return fmt.Errorf("alias_seed_file must be an absolute path")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type runtime struct {
	mu        sync.RWMutex
	cfg       pluginConfig
	state     *stateStore
	models    *modelCache
	providers *providerIndex
	auths     *authSnapshot
	sessions  *sessionStore
	ui        *uiServer
}

var app = &runtime{cfg: defaultPluginConfig()}

func configureRuntime(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg, errDecode := decodePluginConfig(req.ConfigYAML)
	if errDecode != nil {
		return errDecode
	}
	return app.Configure(cfg)
}

func shutdownRuntime() {
	app.Close()
}

func (r *runtime) Configure(cfg pluginConfig) error {
	// The plugin runs inside the CPA process but has its own Go runtime. Without
	// an explicit cap it inherits GOMEMLIMIT and both runtimes target the same
	// ceiling, which overshoots the cgroup limit under concurrent large requests.
	debug.SetMemoryLimit(int64(cfg.PluginMemoryLimitMB) << 20)
	r.mu.Lock()
	r.cfg = cfg
	if r.sessions == nil {
		r.sessions = newSessionStore(time.Duration(cfg.SessionTTLSeconds) * time.Second)
	} else {
		r.sessions.setTTL(time.Duration(cfg.SessionTTLSeconds) * time.Second)
	}
	if r.state == nil {
		r.state = newStateStore(cfg.StateFile)
	} else {
		r.state.setPath(cfg.StateFile)
	}
	if r.models == nil {
		r.models = newModelCache(cfg)
	} else {
		r.models.reconfigure(cfg)
	}
	if r.providers == nil {
		r.providers = newProviderIndex()
	}
	r.providers.Reload(cfg.CPAConfigPath)
	if r.auths == nil {
		r.auths = newAuthSnapshot()
	}
	r.auths.Start(20 * time.Second)
	// Load state and aliases eagerly so the first routed request is correct.
	_ = r.state.Reload()
	r.state.SeedAliases(cfg.AliasSeedFile)
	ui := r.ui
	r.mu.Unlock()

	if ui == nil {
		ui = newUIServer()
		r.mu.Lock()
		if r.ui == nil {
			r.ui = ui
		} else {
			ui = r.ui
		}
		r.mu.Unlock()
	}
	return ui.Configure(r, cfg.Listen)
}

func (r *runtime) Config() pluginConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *runtime) State() *stateStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state
}

func (r *runtime) Models() *modelCache {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.models
}

func (r *runtime) Providers() *providerIndex {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers
}

func (r *runtime) Auths() *authSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.auths
}

// AutoProvider reports which configured provider the router would pick for the
// model when no explicit provider is stored.
func (r *runtime) AutoProvider(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if index := r.Providers(); index != nil {
		for _, candidate := range index.CandidatesFor(model) {
			if health, okHealth := r.Auths().Health(candidate.Provider); okHealth && health.Usable == 0 {
				continue
			}
			return candidate.Name
		}
	}
	if cached, okProvider := r.Models().Provider(model); okProvider {
		return cached
	}
	return ""
}

// ProviderUsable reports whether at least one credential of the provider is
// usable right now. The second result is false when no snapshot exists yet.
func (r *runtime) ProviderUsable(provider string) (bool, bool) {
	health, okHealth := r.Auths().Health(openAICompatProviderKey(provider))
	if !okHealth {
		health, okHealth = r.Auths().Health(provider)
	}
	if !okHealth {
		return true, false
	}
	return health.Usable > 0, true
}

// ModelProviderOptions returns, per model, the providers that list it in
// config.yaml, best first. The settings UI uses it to render the provider
// dropdown for whichever target model is selected.
func (r *runtime) ModelProviderOptions() map[string][]providerOption {
	out := make(map[string][]providerOption)
	index := r.Providers()
	if index == nil {
		return out
	}
	for _, model := range index.Models() {
		candidates := index.CandidatesFor(model)
		options := make([]providerOption, 0, len(candidates))
		for _, candidate := range candidates {
			usable, _ := r.ProviderUsable(candidate.Name)
			options = append(options, providerOption{
				Name:     candidate.Name,
				Priority: candidate.Priority,
				Usable:   usable,
			})
		}
		if len(options) > 0 {
			out[model] = options
		}
	}
	return out
}

func (r *runtime) Sessions() *sessionStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions
}

func (r *runtime) Close() {
	r.mu.Lock()
	ui := r.ui
	models := r.models
	auths := r.auths
	r.ui = nil
	r.mu.Unlock()
	if ui != nil {
		ui.Close()
	}
	if models != nil {
		models.Close()
	}
	if auths != nil {
		auths.Close()
	}
}

func fileModeSecure(path string) (os.FileMode, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return 0, errStat
	}
	return info.Mode().Perm(), nil
}
