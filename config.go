package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type pluginConfig struct {
	Enabled             bool   `yaml:"enabled"`
	Listen              string `yaml:"listen"`
	CPABaseURL          string `yaml:"cpa_base_url"`
	CPAConfigPath       string `yaml:"cpa_config_path"`
	StateFile           string `yaml:"state_file"`
	PasswordFile        string `yaml:"password_file"`
	AliasSeedFile       string `yaml:"alias_seed_file"`
	ModelRefreshSeconds int    `yaml:"model_refresh_seconds"`
	SessionTTLSeconds   int    `yaml:"session_ttl_seconds"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:             true,
		Listen:              "127.0.0.1:8320",
		CPABaseURL:          "http://127.0.0.1:8318",
		CPAConfigPath:       "/opt/cpa/config.yaml",
		StateFile:           "/opt/cpa/key-model-router.json",
		PasswordFile:        "/opt/cpa/key-model-router.pass",
		AliasSeedFile:       "",
		ModelRefreshSeconds: 300,
		SessionTTLSeconds:   8 * 60 * 60,
	}
}

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
	if errValidate := validatePluginConfig(cfg); errValidate != nil {
		return pluginConfig{}, errValidate
	}
	return cfg, nil
}

func validatePluginConfig(cfg pluginConfig) error {
	if cfg.Listen == "" {
		return fmt.Errorf("listen is required")
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
	mu       sync.RWMutex
	cfg      pluginConfig
	state    *stateStore
	models   *modelCache
	sessions *sessionStore
	ui       *uiServer
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

func (r *runtime) Sessions() *sessionStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions
}

func (r *runtime) Close() {
	r.mu.Lock()
	ui := r.ui
	models := r.models
	r.ui = nil
	r.mu.Unlock()
	if ui != nil {
		ui.Close()
	}
	if models != nil {
		models.Close()
	}
}

func fileModeSecure(path string) (os.FileMode, error) {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return 0, errStat
	}
	return info.Mode().Perm(), nil
}
