package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/nobelk/reverb/pkg/reverb"
)

// TenantInfo identifies a tenant extracted from a valid API key.
type TenantInfo struct {
	ID   string
	Name string
}

type keyEntry struct {
	hash   [32]byte
	tenant TenantInfo
}

// Authenticator validates API keys and maps them to tenants.
type Authenticator struct {
	keys []keyEntry
}

// NewAuthenticator builds an Authenticator from the auth configuration.
// It pre-hashes all API keys so that runtime comparisons are constant-time
// over a fixed 32-byte digest.
func NewAuthenticator(cfg reverb.AuthConfig) (*Authenticator, error) {
	if len(cfg.Tenants) == 0 {
		return nil, fmt.Errorf("auth: no tenants configured")
	}

	seen := make(map[string]string) // hash hex → tenant ID (for duplicate detection)
	var keys []keyEntry

	for _, t := range cfg.Tenants {
		if t.ID == "" {
			return nil, fmt.Errorf("auth: tenant with empty id")
		}
		if len(t.APIKeys) == 0 {
			return nil, fmt.Errorf("auth: tenant %q has no api_keys", t.ID)
		}
		for _, k := range t.APIKeys {
			h := sha256.Sum256([]byte(k))
			hx := fmt.Sprintf("%x", h)
			if prev, ok := seen[hx]; ok {
				return nil, fmt.Errorf("auth: duplicate api key shared by tenants %q and %q", prev, t.ID)
			}
			seen[hx] = t.ID
			keys = append(keys, keyEntry{
				hash:   h,
				tenant: TenantInfo{ID: t.ID, Name: t.Name},
			})
		}
	}

	return &Authenticator{keys: keys}, nil
}

// Authenticate validates a bearer token and returns the associated tenant.
// Comparison is constant-time to prevent timing attacks.
func (a *Authenticator) Authenticate(token string) (*TenantInfo, bool) {
	h := sha256.Sum256([]byte(token))
	for i := range a.keys {
		if subtle.ConstantTimeCompare(h[:], a.keys[i].hash[:]) == 1 {
			return &a.keys[i].tenant, true
		}
	}
	return nil, false
}

// --- context helpers ---------------------------------------------------------

type ctxKey struct{}

// WithTenant stores tenant info in the context.
func WithTenant(ctx context.Context, t *TenantInfo) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// TenantFromContext retrieves the tenant info from the context.
func TenantFromContext(ctx context.Context) (*TenantInfo, bool) {
	t, ok := ctx.Value(ctxKey{}).(*TenantInfo)
	return t, ok
}

// ScopedNamespace prefixes the namespace with the tenant ID when auth is active.
// When no tenant is in the context (auth disabled), the namespace is returned
// unchanged.
func ScopedNamespace(ctx context.Context, namespace string) string {
	t, ok := TenantFromContext(ctx)
	if !ok {
		return namespace
	}
	return t.ID + "::" + namespace
}

// UnscopeNamespace strips the tenant prefix from a scoped namespace.
// Returns the original namespace and true if the prefix matched, or
// the input unchanged and false otherwise.
func UnscopeNamespace(tenantID, namespace string) (string, bool) {
	prefix := tenantID + "::"
	if after, ok := strings.CutPrefix(namespace, prefix); ok {
		return after, true
	}
	return namespace, false
}

// NamespaceBelongsToTenant reports whether the given (already-scoped) namespace
// is owned by the tenant.
func NamespaceBelongsToTenant(tenantID, namespace string) bool {
	return strings.HasPrefix(namespace, tenantID+"::")
}
