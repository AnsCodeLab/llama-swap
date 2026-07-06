package server

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/mostlygeek/llama-swap/internal/chain"
	"github.com/mostlygeek/llama-swap/internal/config"
	"github.com/mostlygeek/llama-swap/internal/router"
)

// globalAuthPathSkips lists routes that bypass auth entirely, even when
// apiKeys or auth.username/password is configured: they return no
// sensitive data, and gating them risks crashlooping health-probed
// deployments (load balancers, k8s liveness/readiness probes).
var globalAuthPathSkips = []string{"/health", "/wol-health"}

// CreateGlobalAuthMiddleware returns middleware that gates every request
// llama-swap serves (the UI, /metrics, and inference/API routes alike)
// when either apiKeys or auth.username/password is configured. /health and
// /wol-health are always exempt (see globalAuthPathSkips), regardless of
// configuration, since they return no sensitive data and are relied on by
// monitoring/load-balancer health probes. A request to any other route is
// admitted if it presents *either* a valid API key (Authorization: Bearer,
// Authorization: Basic password field, or x-api-key, unchanged from
// before) *or* valid HTTP Basic Auth username/password. When neither
// apiKeys nor auth is configured it is a pass-through (today's
// default-allow behavior). On success the auth headers are stripped so
// they never leak to upstream.
func CreateGlobalAuthMiddleware(cfg config.Config) chain.Middleware {
	keys := cfg.RequiredAPIKeys
	username := cfg.Auth.Username
	password := cfg.Auth.Password
	authEnabled := username != "" || password != ""

	return func(next http.Handler) http.Handler {
		if len(keys) == 0 && !authEnabled {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, path := range globalAuthPathSkips {
				if r.URL.Path == path {
					next.ServeHTTP(w, r)
					return
				}
			}

			provided := extractAPIKey(r)
			for _, entry := range keys {
				if provided != "" && provided == entry.Key {
					r.Header.Del("Authorization")
					r.Header.Del("x-api-key")
					next.ServeHTTP(w, r)
					return
				}
			}

			if authEnabled {
				if u, p, ok := r.BasicAuth(); ok &&
					subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1 &&
					subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1 {
					r.Header.Del("Authorization")
					r.Header.Del("x-api-key")
					next.ServeHTTP(w, r)
					return
				}
			}

			w.Header().Set("WWW-Authenticate", `Basic realm="llama-swap"`)
			router.SendResponse(w, r, http.StatusUnauthorized, "unauthorized: invalid or missing credentials")
		})
	}
}

// extractAPIKey pulls a candidate API key from the request, preferring Basic,
// then Bearer, then x-api-key.
func extractAPIKey(r *http.Request) string {
	var bearerKey, basicKey string
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			bearerKey = strings.TrimPrefix(auth, "Bearer ")
		} else if strings.HasPrefix(auth, "Basic ") {
			encoded := strings.TrimPrefix(auth, "Basic ")
			if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				if parts := strings.SplitN(string(decoded), ":", 2); len(parts) == 2 {
					basicKey = parts[1] // password field is the API key
				}
			}
		}
	}

	switch {
	case basicKey != "":
		return basicKey
	case bearerKey != "":
		return bearerKey
	default:
		return r.Header.Get("x-api-key")
	}
}

// CreateCORSMiddleware returns middleware that answers OPTIONS preflight
// requests with permissive CORS headers (see issues #81, #77, #42). Non-OPTIONS
// requests pass through untouched.
func CreateCORSMiddleware() chain.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
				w.Header().Set("Access-Control-Allow-Headers", sanitizeAccessControlRequestHeaderValues(headers))
			} else {
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, X-Requested-With")
			}
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

func isTokenChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
	case r >= 'A' && r <= 'Z':
	case r >= '0' && r <= '9':
	case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
	default:
		return false
	}
	return true
}

// sanitizeAccessControlRequestHeaderValues drops any header names that contain
// characters outside the HTTP token grammar before echoing them back.
func sanitizeAccessControlRequestHeaderValues(headerValues string) string {
	parts := strings.Split(headerValues, ",")
	valid := make([]string, 0, len(parts))

	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}

		validPart := true
		for _, c := range v {
			if !isTokenChar(c) {
				validPart = false
				break
			}
		}
		if validPart {
			valid = append(valid, v)
		}
	}

	return strings.Join(valid, ", ")
}
