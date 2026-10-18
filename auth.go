package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func checkPasswordFile(path string) error {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return errStat
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("password file %s must not be group/world readable (mode %o)", path, info.Mode().Perm())
	}
	if stat, okStat := info.Sys().(*syscall.Stat_t); okStat {
		if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
			return fmt.Errorf("password file %s must be owned by root or the CPA user", path)
		}
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return errRead
	}
	hash := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(hash, "$2") {
		return fmt.Errorf("password file %s does not contain a bcrypt hash", path)
	}
	return nil
}

func verifyPassword(path, password string) error {
	if errFile := checkPasswordFile(path); errFile != nil {
		return errFile
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return errRead
	}
	hash := strings.TrimSpace(string(raw))
	if errCompare := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); errCompare != nil {
		return fmt.Errorf("invalid password")
	}
	return nil
}

type loginAttempt struct {
	Count        int
	FirstAt      time.Time
	BlockedUntil time.Time
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: make(map[string]loginAttempt)}
}

func (l *loginLimiter) Allowed(ip string) (bool, time.Duration) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, okEntry := l.attempts[ip]
	if !okEntry {
		return true, 0
	}
	if now.Before(entry.BlockedUntil) {
		return false, entry.BlockedUntil.Sub(now)
	}
	if now.Sub(entry.FirstAt) > 10*time.Minute {
		delete(l.attempts, ip)
		return true, 0
	}
	return true, 0
}

func (l *loginLimiter) Fail(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.attempts[ip]
	if entry.FirstAt.IsZero() || now.Sub(entry.FirstAt) > 10*time.Minute {
		entry = loginAttempt{FirstAt: now}
	}
	entry.Count++
	if entry.Count >= 5 {
		entry.BlockedUntil = now.Add(10 * time.Minute)
	}
	l.attempts[ip] = entry
}

func (l *loginLimiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.attempts, ip)
	l.mu.Unlock()
}
