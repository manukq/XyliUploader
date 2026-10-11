package main

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const csrfCookieName = "csrf_token"

func generateCSRFToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func setCSRFCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    generateCSRFToken(),
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
		Secure:   false,
	})
}

func validateCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	header := r.Header.Get("X-CSRF-Token")
	if header == "" {
		return false
	}
	return cookie.Value == header
}

func requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if !validateCSRF(r) {
		jsonError(w, "Invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

var blockedExtensions = map[string]bool{
	".exe":   true,
	".dll":   true,
	".bat":   true,
	".cmd":   true,
	".msi":   true,
	".com":   true,
	".scr":   true,
	".jar":   true,
	".sh":    true,
	".bash":  true,
	".csh":   true,
	".ps1":   true,
	".vbs":   true,
	".app":   true,
	".bin":   true,
	".run":   true,
	".deb":   true,
	".rpm":   true,
	".apk":   true,
	".dmg":   true,
	".php":   true,
	".phtml": true,
	".php3":  true,
	".php4":  true,
	".php5":  true,
	".phar":  true,
}

func isBlockedExtension(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return blockedExtensions[ext]
}

func detectFileType(r io.Reader) string {
	head := make([]byte, 512)
	n, _ := r.Read(head)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(head[:n])
}

func isExecutableType(ct string) bool {
	switch ct {
	case "application/x-dosexec",
		"application/x-executable",
		"application/x-elf-executable",
		"application/x-msdownload",
		"application/x-msdos-program",
		"application/x-sh",
		"application/x-csh",
		"application/x-bat",
		"application/java-archive":
		return true
	}
	return false
}

func safeContentType(ct, filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".html", ".htm", ".xhtml":
		return "text/plain"
	case ".svg":
		return "image/svg+xml"
	case ".js":
		return "text/plain"
	}

	return ct
}

func detectAndValidate(file io.Reader, filename string) (string, bool) {
	ct := detectFileType(file)

	if isBlockedExtension(filename) {
		return "", false
	}

	if isExecutableType(ct) {
		return "", false
	}

	ct = safeContentType(ct, filename)

	ext := strings.ToLower(filepath.Ext(filename))
	_ = mime.AddExtensionType(ext, ct)

	return ct, true
}

type rateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	times := rl.requests[key]
	valid := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		rl.requests[key] = valid
		return false
	}

	valid = append(valid, now)
	rl.requests[key] = valid
	return true
}

var uploadLimiter = newRateLimiter(10, time.Minute)
var deleteLimiter = newRateLimiter(20, time.Minute)

func rateLimit(rl *rateLimiter, w http.ResponseWriter, r *http.Request) bool {
	ip := r.RemoteAddr
	if !rl.allow(ip) {
		jsonError(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return false
	}
	return true
}
