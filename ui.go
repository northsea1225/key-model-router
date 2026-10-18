package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName = "kmr_session"
	uiBasePath        = "/v0/resource/plugins/" + pluginIdentifier
)

type uiServer struct {
	mu      sync.Mutex
	addr    string
	srv     *http.Server
	ln      net.Listener
	limiter *loginLimiter
}

func newUIServer() *uiServer {
	return &uiServer{limiter: newLoginLimiter()}
}

func (u *uiServer) Configure(rt *runtime, addr string) error {
	addr = strings.TrimSpace(addr)
	u.mu.Lock()
	if u.srv != nil && u.addr == addr {
		u.mu.Unlock()
		return nil
	}
	old := u.srv
	oldLn := u.ln
	u.srv = nil
	u.ln = nil
	u.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if oldLn != nil {
		_ = oldLn.Close()
	}

	ln, errListen := net.Listen("tcp", addr)
	if errListen != nil {
		logError("key-model-router: UI listen failed", map[string]any{"addr": addr, "error": errListen.Error()})
		return errListen
	}
	handler := &uiHandler{rt: rt, limiter: u.limiter}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	u.mu.Lock()
	u.addr = addr
	u.srv = srv
	u.ln = ln
	u.mu.Unlock()
	go func() {
		if errServe := srv.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			logError("key-model-router: UI server stopped", map[string]any{"error": errServe.Error()})
		}
	}()
	logDebug("key-model-router: UI listening", map[string]any{"addr": addr})
	return nil
}

func (u *uiServer) Close() {
	u.mu.Lock()
	srv := u.srv
	ln := u.ln
	u.srv = nil
	u.ln = nil
	u.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

type uiHandler struct {
	rt      *runtime
	limiter *loginLimiter
}

func (h *uiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setCommonHeaders(w)
	path := r.URL.Path
	switch {
	case path == "/":
		http.Redirect(w, r, uiBasePath+"/", http.StatusFound)
		return
	case path == "/favicon.ico" || path == "/apple-touch-icon.png":
		w.WriteHeader(http.StatusNoContent)
		return
	case path == uiBasePath:
		http.Redirect(w, r, uiBasePath+"/", http.StatusMovedPermanently)
		return
	case path == uiBasePath+"/":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		_, _ = w.Write([]byte(indexHTML))
		return
	case path == uiBasePath+"/app.js":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		_, _ = w.Write([]byte(appJS))
		return
	case path == uiBasePath+"/app.css":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write([]byte(appCSS))
		return
	case path == uiBasePath+"/api/health":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": "0.1.1"})
		return
	case path == uiBasePath+"/api/state":
		if _, okSession := h.requireSession(r); !okSession {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		h.handleState(w, r)
		return
	case path == uiBasePath+"/api/login":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		h.handleLogin(w, r)
		return
	case path == uiBasePath+"/api/logout":
		if _, okSession := h.requireSession(r); !okSession {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		h.handleLogout(w, r)
		return
	case path == uiBasePath+"/api/save":
		if _, okSession := h.requireSession(r); !okSession {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		h.handleSave(w, r)
		return
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func (h *uiHandler) handleState(w http.ResponseWriter, r *http.Request) {
	sess, okSession := h.requireSession(r)
	if !okSession {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	accounts, errAccounts := h.rt.Accounts()
	if errAccounts != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot read CPA config"})
		return
	}
	models := h.rt.Models().List()
	count, fetchedAt, modelsErr := h.rt.Models().Status()
	stateErr := h.rt.State().LastError()
	cfg := h.rt.Config()
	savedAt := ""
	if info, errStat := os.Stat(cfg.StateFile); errStat == nil {
		savedAt = info.ModTime().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"accounts":            accounts,
		"models":              models,
		"models_count":        count,
		"models_fetched_at":   formatTime(fetchedAt),
		"models_error":        modelsErr,
		"state_error":         stateErr,
		"saved_at":            savedAt,
		"csrf":                sess.CSRF,
		"version":             "0.1.1",
		"password_configured": checkPasswordFile(cfg.PasswordFile) == nil,
		"listen":              cfg.Listen,
	})
}

type loginRequest struct {
	Password string `json:"password"`
}

func (h *uiHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin request rejected"})
		return
	}
	ip := remoteIP(r)
	if allowed, wait := h.limiter.Allowed(ip); !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many attempts, try again later"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req loginRequest
	if errDecode := json.NewDecoder(r.Body).Decode(&req); errDecode != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	cfg := h.rt.Config()
	if errPassword := verifyPassword(cfg.PasswordFile, req.Password); errPassword != nil {
		h.limiter.Fail(ip)
		logWarn("key-model-router: login failed ip="+ip, nil)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid password"})
		return
	}
	h.limiter.Reset(ip)
	sess, errCreate := h.rt.Sessions().Create()
	if errCreate != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot create session"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		MaxAge:   int(time.Until(sess.ExpiresAt).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	logDebug("key-model-router: login ok ip="+ip, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"csrf":       sess.CSRF,
		"expires_at": sess.ExpiresAt.Format(time.RFC3339),
	})
}

func (h *uiHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin request rejected"})
		return
	}
	sess, okSession := h.requireSession(r)
	if !okSession {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !h.requireCSRF(r, sess) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid csrf token"})
		return
	}
	h.rt.Sessions().Delete(sess.Token)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type saveRequest struct {
	Rows []stateRow `json:"rows"`
	CSRF string     `json:"csrf"`
}

func (h *uiHandler) handleSave(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin request rejected"})
		return
	}
	sess, okSession := h.requireSession(r)
	if !okSession {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !h.requireCSRF(r, sess) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid csrf token"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req saveRequest
	if errDecode := json.NewDecoder(r.Body).Decode(&req); errDecode != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}
	if req.CSRF != "" && !constantTimeEqual(req.CSRF, sess.CSRF) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "invalid csrf token"})
		return
	}
	if len(req.Rows) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "too many rows"})
		return
	}
	known := h.rt.Fingerprints()
	if known == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot read CPA config"})
		return
	}
	existing, _ := h.rt.State().Snapshot()
	next := make(map[string]stateEntry, len(existing)+len(req.Rows))
	for fingerprint, entry := range existing {
		if _, okKnown := known[fingerprint]; okKnown {
			next[fingerprint] = entry
		}
	}
	for _, row := range req.Rows {
		if !validFingerprint(row.Fingerprint) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid fingerprint"})
			return
		}
		if _, okKnown := known[row.Fingerprint]; !okKnown {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown api key"})
			return
		}
		target := normalizeTarget(row.TargetModel)
		if target != "" {
			knownModels, present := h.rt.Models().Has(target)
			if !knownModels {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "model list is unavailable; refresh before saving"})
				return
			}
			if !present {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error":       "target model not available: " + target,
					"fingerprint": row.Fingerprint,
					"field":       "target_model",
				})
				return
			}
		}
		next[row.Fingerprint] = stateEntry{
			Alias:       normalizeAlias(row.Alias),
			TargetModel: target,
		}
	}
	rows := make([]stateRow, 0, len(next))
	for fingerprint, entry := range next {
		rows = append(rows, stateRow{
			Fingerprint: fingerprint,
			Alias:       entry.Alias,
			TargetModel: entry.TargetModel,
		})
	}
	savedAt, errSave := h.rt.State().SaveRows(rows)
	if errSave != nil {
		logError("key-model-router: state save failed error="+errSave.Error(), nil)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot save state"})
		return
	}
	routed := 0
	for _, entry := range next {
		if entry.TargetModel != "" {
			routed++
		}
	}
	logDebug("key-model-router: state saved keys="+fmt.Sprintf("%d", len(next))+" routed="+fmt.Sprintf("%d", routed), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"saved_at": savedAt.Format(time.RFC3339),
		"csrf":     sess.CSRF,
	})
}

func (h *uiHandler) requireSession(r *http.Request) (session, bool) {
	cookie, errCookie := r.Cookie(sessionCookieName)
	if errCookie != nil || cookie == nil {
		return session{}, false
	}
	return h.rt.Sessions().Get(cookie.Value)
}

func (h *uiHandler) requireCSRF(r *http.Request, sess session) bool {
	value := strings.TrimSpace(r.Header.Get("X-KMR-CSRF"))
	return value != "" && constantTimeEqual(value, sess.CSRF)
}

func setCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func methodNotAllowed(w http.ResponseWriter, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func remoteIP(r *http.Request) string {
	host, _, errSplit := net.SplitHostPort(r.RemoteAddr)
	if errSplit != nil {
		return r.RemoteAddr
	}
	return host
}

func sameOrigin(r *http.Request) bool {
	if site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); site == "cross-site" {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, errParse := url.Parse(origin)
	if errParse != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	// Treat 127.0.0.1 and localhost as equivalent, but keep the port exact.
	originHost, originPort, errOrigin := net.SplitHostPort(parsed.Host)
	requestHost, requestPort, errRequest := net.SplitHostPort(r.Host)
	if errOrigin != nil || errRequest != nil || originPort != requestPort {
		return false
	}
	return isLoopbackHost(originHost) && isLoopbackHost(requestHost)
}

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}
