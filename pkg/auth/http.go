package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

// HTTPMiddleware returns middleware that validates Bearer tokens and injects
// tenant info into the request context. The /healthz endpoint is excluded
// from authentication.
func HTTPMiddleware(authn *Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Health check is always public.
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}

			header := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || token == "" {
				writeUnauthorized(w)
				return
			}

			tenant, valid := authn.Authenticate(token)
			if !valid {
				writeUnauthorized(w)
				return
			}

			ctx := WithTenant(r.Context(), tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
}
