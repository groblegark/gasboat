package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestBasicAuth_Disabled(t *testing.T) {
	handler := basicAuthMiddleware(okHandler(), "", "")
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when auth disabled, got %d", w.Code)
	}
}

func TestBasicAuth_ValidCredentials(t *testing.T) {
	handler := basicAuthMiddleware(okHandler(), "admin", "secret")
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with valid credentials, got %d", w.Code)
	}
}

func TestBasicAuth_InvalidCredentials(t *testing.T) {
	handler := basicAuthMiddleware(okHandler(), "admin", "secret")
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "wrong")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with invalid credentials, got %d", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("expected WWW-Authenticate header")
	}
}

func TestBasicAuth_NoCredentials(t *testing.T) {
	handler := basicAuthMiddleware(okHandler(), "admin", "secret")
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with no credentials, got %d", w.Code)
	}
}

func TestIPWhitelist_Disabled(t *testing.T) {
	logger := slog.Default()
	handler := ipWhitelistMiddleware(okHandler(), "", logger)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when whitelist disabled, got %d", w.Code)
	}
}

func TestIPWhitelist_AllowedIP(t *testing.T) {
	logger := slog.Default()
	handler := ipWhitelistMiddleware(okHandler(), "10.0.0.0/8", logger)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.1.5:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for allowed IP, got %d", w.Code)
	}
}

func TestIPWhitelist_BlockedIP(t *testing.T) {
	logger := slog.Default()
	handler := ipWhitelistMiddleware(okHandler(), "10.0.0.0/8", logger)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.5:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for blocked IP, got %d", w.Code)
	}
}

func TestIPWhitelist_MultipleCIDRs(t *testing.T) {
	logger := slog.Default()
	handler := ipWhitelistMiddleware(okHandler(), "10.0.0.0/8, 192.168.0.0/16", logger)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.5:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for IP in second CIDR, got %d", w.Code)
	}
}

func TestIPWhitelist_XForwardedFor(t *testing.T) {
	logger := slog.Default()
	handler := ipWhitelistMiddleware(okHandler(), "10.0.0.0/8", logger)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.5:1234"
	req.Header.Set("X-Forwarded-For", "10.0.1.5, 192.168.1.5")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for X-Forwarded-For IP in range, got %d", w.Code)
	}
}

func TestIPWhitelist_InvalidCIDR(t *testing.T) {
	logger := slog.Default()
	// Invalid CIDR should be skipped, leaving no valid CIDRs -> all allowed
	handler := ipWhitelistMiddleware(okHandler(), "not-a-cidr", logger)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 when all CIDRs invalid (passthrough), got %d", w.Code)
	}
}
