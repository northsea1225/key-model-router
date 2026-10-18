package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/sjson"
)

func executeRequest(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	target, fingerprint, rewrite := resolveExecutionTarget(req)
	body := requestBody(req)
	if rewrite {
		body = rewriteModel(body, target)
	}
	if errValidate := validateTargetModel(fingerprint, target); errValidate != nil {
		return errValidate, nil
	}

	entryProtocol := strings.TrimSpace(req.SourceFormat)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(req.Format)
	}
	exitProtocol := strings.TrimSpace(req.Format)
	if exitProtocol == "" {
		exitProtocol = entryProtocol
	}
	rawResp, errCall := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entryProtocol,
			ExitProtocol:  exitProtocol,
			Model:         target,
			Stream:        false,
			Body:          body,
			Headers:       sanitizedHeaders(req.Headers),
			Query:         req.Query,
			Alt:           req.Alt,
		},
		HostCallbackID: req.HostCallbackID,
	})
	if errCall != nil {
		status := hostStatusFromError(errCall)
		return mustErrorEnvelope("model_execution_error", errCall.Error(), status), nil
	}
	var resp pluginapi.HostModelExecutionResponse
	if errUnmarshal := json.Unmarshal(rawResp, &resp); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if resp.StatusCode >= 400 {
		message := fmt.Sprintf("target model %q failed with status %d", target, resp.StatusCode)
		return mustErrorEnvelope("model_execution_error", message, resp.StatusCode), nil
	}
	if rewrite {
		logDebug("key-model-router: forwarded non-stream fp="+fingerprintPrefix(fingerprint)+
			" from="+strings.TrimSpace(req.Model)+" to="+target, nil)
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: resp.Body,
		Headers: resp.Headers,
	})
}

func executeStreamRequest(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		return mustErrorEnvelope("executor_error", "stream_id is required", 500), nil
	}
	target, fingerprint, rewrite := resolveExecutionTarget(req)
	if errValidate := validateTargetModel(fingerprint, target); errValidate != nil {
		closePluginStream(streamID, errorMessageFromEnvelope(errValidate))
		return errValidate, nil
	}
	body := requestBody(req)
	if rewrite {
		body = rewriteModel(body, target)
	}

	entryProtocol := strings.TrimSpace(req.SourceFormat)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(req.Format)
	}
	exitProtocol := strings.TrimSpace(req.Format)
	if exitProtocol == "" {
		exitProtocol = entryProtocol
	}
	if rewrite {
		logDebug("key-model-router: forwarded stream fp="+fingerprintPrefix(fingerprint)+
			" from="+strings.TrimSpace(req.Model)+" to="+target, nil)
	}

	go func() {
		rawResp, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
			HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
				EntryProtocol: entryProtocol,
				ExitProtocol:  exitProtocol,
				Model:         target,
				Stream:        true,
				Body:          body,
				Headers:       sanitizedHeaders(req.Headers),
				Query:         req.Query,
				Alt:           req.Alt,
			},
			HostCallbackID: req.HostCallbackID,
		})
		if errCall != nil {
			closePluginStream(streamID, errCall.Error())
			return
		}
		var start pluginapi.HostModelStreamResponse
		if errUnmarshal := json.Unmarshal(rawResp, &start); errUnmarshal != nil {
			closePluginStream(streamID, errUnmarshal.Error())
			return
		}
		if start.StatusCode >= 400 {
			closePluginStream(streamID, fmt.Sprintf("target model %q failed with status %d", target, start.StatusCode))
			return
		}
		if strings.TrimSpace(start.StreamID) == "" {
			closePluginStream(streamID, "host model stream: empty stream_id")
			return
		}
		defer closeHostModelStream(start.StreamID)
		for {
			chunkRaw, errRead := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: start.StreamID})
			if errRead != nil {
				closePluginStream(streamID, errRead.Error())
				return
			}
			var chunk pluginapi.HostModelStreamReadResponse
			if errUnmarshal := json.Unmarshal(chunkRaw, &chunk); errUnmarshal != nil {
				closePluginStream(streamID, errUnmarshal.Error())
				return
			}
			if chunk.Error != "" {
				closePluginStream(streamID, chunk.Error)
				return
			}
			if len(chunk.Payload) > 0 {
				if errEmit := emitPluginStreamChunk(streamID, chunk.Payload); errEmit != nil {
					closePluginStream(streamID, errEmit.Error())
					return
				}
			}
			if chunk.Done {
				closePluginStream(streamID, "")
				return
			}
		}
	}()

	return okEnvelope(map[string]any{
		"headers": http.Header{"Content-Type": []string{"text/event-stream"}},
	})
}

func resolveExecutionTarget(req rpcExecutorRequest) (target, fingerprint string, rewrite bool) {
	key := clientKeyFromHeaders(req.Headers)
	if key == "" {
		return strings.TrimSpace(req.Model), "", false
	}
	fingerprint = sha256Hex(key)
	state := app.State()
	if state == nil {
		return strings.TrimSpace(req.Model), fingerprint, false
	}
	target = state.Target(fingerprint)
	if target == "" {
		return strings.TrimSpace(req.Model), fingerprint, false
	}
	return target, fingerprint, true
}

func validateTargetModel(fingerprint, target string) (errorEnvelopeBytes []byte) {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	known, present := app.Models().Has(target)
	if !known || present {
		return nil
	}
	logWarn("key-model-router: target model unavailable fp="+fingerprintPrefix(fingerprint)+
		" model="+target, nil)
	message := fmt.Sprintf("target model %q is not available on this CPA instance", target)
	return mustErrorEnvelope("model_not_found", message, http.StatusBadRequest)
}

func requestBody(req rpcExecutorRequest) []byte {
	if len(req.OriginalRequest) > 0 {
		return append([]byte(nil), req.OriginalRequest...)
	}
	return append([]byte(nil), req.Payload...)
}

// sanitizedHeaders removes downstream client credentials before the nested
// host execution selects an upstream credential. Protocol headers stay intact.
func sanitizedHeaders(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	out := headers.Clone()
	for _, name := range []string{
		"Authorization",
		"X-Api-Key",
		"Api-Key",
		"X-Goog-Api-Key",
		"Proxy-Authorization",
		"Cookie",
	} {
		out.Del(name)
	}
	return out
}

func rewriteModel(body []byte, target string) []byte {
	target = strings.TrimSpace(target)
	if target == "" || len(body) == 0 {
		return body
	}
	updated, errSet := sjson.SetBytes(body, "model", target)
	if errSet != nil {
		return body
	}
	return updated
}

func emitPluginStreamChunk(streamID string, payload []byte) error {
	_, errCall := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
	return errCall
}

func closePluginStream(streamID, errMessage string) {
	_, _ = callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{
		StreamID: streamID,
		Error:    errMessage,
	})
}

func closeHostModelStream(streamID string) {
	_, _ = callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
}

func errorMessageFromEnvelope(raw []byte) string {
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal == nil && env.Error != nil {
		return env.Error.Message
	}
	return "target model is not available"
}
