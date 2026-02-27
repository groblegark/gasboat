package main

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// basicAuthMiddleware wraps a handler with HTTP Basic Authentication.
// When both username and password are empty, auth is disabled (dev mode).
func basicAuthMiddleware(next http.Handler, username, password string) http.Handler {
	if username == "" && password == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="advice-viewer"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ipWhitelistMiddleware restricts access to the given CIDR ranges.
// When allowedCIDRs is empty, all IPs are allowed (dev mode).
func ipWhitelistMiddleware(next http.Handler, allowedCIDRs string, logger *slog.Logger) http.Handler {
	if allowedCIDRs == "" {
		return next
	}

	var nets []*net.IPNet
	for _, cidr := range strings.Split(allowedCIDRs, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			logger.Warn("invalid CIDR in ALLOWED_CIDRS, skipping", "cidr", cidr, "error", err)
			continue
		}
		nets = append(nets, network)
	}

	if len(nets) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if ip == nil || !containsIP(nets, ip) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP extracts the client IP from the request, checking X-Forwarded-For first.
func clientIP(r *http.Request) net.IP {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Use the first (leftmost) IP in the chain.
		parts := strings.SplitN(xff, ",", 2)
		if ip := net.ParseIP(strings.TrimSpace(parts[0])); ip != nil {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return net.ParseIP(r.RemoteAddr)
	}
	return net.ParseIP(host)
}

func containsIP(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
