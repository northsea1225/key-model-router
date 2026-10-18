package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func fingerprintPrefix(fingerprint string) string {
	fingerprint = strings.TrimSpace(fingerprint)
	if len(fingerprint) > 10 {
		return fingerprint[:10]
	}
	return fingerprint
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 10 {
		return string(runes[:1]) + "…" + string(runes[len(runes)-1:])
	}
	return string(runes[:5]) + "…" + string(runes[len(runes)-4:])
}

func randomToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, errRead := rand.Read(buf); errRead != nil {
		return "", errRead
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func normalizeTarget(value string) string {
	return strings.TrimSpace(value)
}

func normalizeAlias(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, errDecode := hex.DecodeString(value)
	return errDecode == nil
}

func hostStatusFromError(err error) int {
	if err == nil {
		return 0
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "model_not_found"), strings.Contains(msg, "unknown provider for model"), strings.Contains(msg, "invalid_request"):
		return 400
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "401"):
		return 401
	case strings.Contains(msg, "forbidden"), strings.Contains(msg, "403"):
		return 403
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "429"):
		return 429
	default:
		return 502
	}
}

func clientKeyFromHeaders(headers http.Header) string {
	get := func(name string) string {
		for key, values := range headers {
			if !strings.EqualFold(key, name) || len(values) == 0 {
				continue
			}
			return strings.TrimSpace(values[0])
		}
		return ""
	}
	if auth := get("Authorization"); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			return strings.TrimSpace(parts[1])
		}
		return strings.TrimSpace(auth)
	}
	if key := get("X-Api-Key"); key != "" {
		return key
	}
	return ""
}

func debugFields(fields map[string]any) map[string]any {
	if fields == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}

func logDebug(message string, fields map[string]any) {
	payload := map[string]any{
		"level":   "debug",
		"message": message,
		"fields":  debugFields(fields),
	}
	_, _ = callHost("host.log", payload)
}

func logWarn(message string, fields map[string]any) {
	payload := map[string]any{
		"level":   "warn",
		"message": message,
		"fields":  debugFields(fields),
	}
	_, _ = callHost("host.log", payload)
}

func logError(message string, fields map[string]any) {
	payload := map[string]any{
		"level":   "error",
		"message": message,
		"fields":  debugFields(fields),
	}
	_, _ = callHost("host.log", payload)
}

func mustJSON(v any) []byte {
	raw, errMarshal := jsonMarshal(v)
	if errMarshal != nil {
		return []byte(fmt.Sprintf(`{"error":%q}`, errMarshal.Error()))
	}
	return raw
}
