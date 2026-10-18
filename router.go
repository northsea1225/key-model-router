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
		// the original model instead of routing them through this executor.
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
	target := state.Target(fingerprint)
	if target == "" {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	logDebug("key-model-router: route rewrite fp="+fingerprintPrefix(fingerprint)+
		" from="+req.RequestedModel+" to="+target, nil)
	return okEnvelope(pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "key-model-router",
	})
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
