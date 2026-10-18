package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func routeModelRequest(raw []byte) ([]byte, error) {
	var req pluginapi.ModelRouteRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := app.Config()
	if !cfg.Enabled {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	if isCountTokensRequest(req.Metadata) {
		// Count-token calls are not model executions; let CPA serve them with
		// the original model instead of routing them through this plugin.
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	key := clientKeyFromHeaders(http.Header(req.Headers))
	if key == "" {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	fingerprint := sha256Hex(key)
	state := app.State()
	if state == nil {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	target, provider := state.Route(fingerprint)
	if target == "" {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}

	if cfg.RouteMode == routeModeExecutor {
		logDebug("key-model-router: executor route fp="+fingerprintPrefix(fingerprint)+
			" from="+req.RequestedModel+" to="+target, nil)
		return okEnvelope(pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Reason:     "key-model-router",
		})
	}

	// Provider mode: CPA runs the request on its own provider path. The plugin
	// only names the provider and the target model, so the request body and the
	// response stream are never copied through the plugin process.
	resolved, reason := resolveProvider(provider, target, req.AvailableProviders)
	if resolved == "" {
		logWarn("key-model-router: cannot route to provider; leaving request unrouted fp="+
			fingerprintPrefix(fingerprint)+" model="+target+" reason="+reason+
			" available="+strings.Join(req.AvailableProviders, ","), nil)
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	provider = resolved
	logDebug("key-model-router: provider route fp="+fingerprintPrefix(fingerprint)+
		" from="+req.RequestedModel+" to="+target+" provider="+provider, nil)
	return okEnvelope(pluginapi.ModelRouteResponse{
		Handled:     true,
		TargetKind:  pluginapi.ModelRouteTargetProvider,
		Target:      provider,
		TargetModel: target,
		Reason:      "key-model-router",
	})
}

// resolveProvider picks a provider key that CPA reports as available. An
// explicit rule wins; otherwise the /v1/models owner is preferred, then any
// configured provider that lists the model and has a usable credential.
func resolveProvider(explicit, target string, available []string) (string, string) {
	if explicit = normalizeProvider(explicit); explicit != "" {
		if matched, okMatch := matchAvailableProvider(available, explicit); okMatch {
			return matched, ""
		}
		return "", "explicit provider " + explicit + " is not available"
	}
	if cached, okProvider := app.Models().Provider(target); okProvider {
		if matched, okMatch := matchAvailableProvider(available, cached); okMatch {
			return matched, ""
		}
	}
	if index := app.Providers(); index != nil {
		for _, candidate := range index.ProvidersFor(target) {
			if containsFold(available, candidate) {
				return candidate, ""
			}
		}
	}
	return "", "no available provider serves this model"
}

// matchAvailableProvider resolves a bare provider name (entry name, or the
// owned_by value from /v1/models) to the provider key CPA registers it under.
func matchAvailableProvider(available []string, want string) (string, bool) {
	want = normalizeProvider(want)
	if want == "" {
		return "", false
	}
	for _, candidate := range []string{want, "openai-compatible-" + want} {
		if containsFold(available, candidate) {
			return candidate, true
		}
	}
	return "", false
}

func isCountTokensRequest(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	for _, key := range []string{"request_path", "path"} {
		raw, okValue := metadata[key]
		if !okValue {
			continue
		}
		path, okString := raw.(string)
		if okString && strings.Contains(strings.ToLower(path), "count_tokens") {
			return true
		}
	}
	return false
}
