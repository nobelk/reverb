package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

func newTestAuthn(t *testing.T) *Authenticator {
	t.Helper()
	authn, err := NewAuthenticator(reverb.AuthConfig{
		Tenants: []reverb.Tenant{
			{ID: "t1", Name: "Tenant One", APIKeys: []string{"valid-key"}},
		},
	})
	require.NoError(t, err)
	return authn
}

// echoHandler writes the tenant ID from context into the response body.
func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := TenantFromContext(r.Context())
		if ok {
			w.Write([]byte(tenant.ID))
		} else {
			w.Write([]byte("no-tenant"))
		}
	})
}

func TestHTTPMiddleware_ValidToken(t *testing.T) {
	handler := HTTPMiddleware(newTestAuthn(t))(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer valid-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "t1", rec.Body.String())
}

func TestHTTPMiddleware_InvalidToken(t *testing.T) {
	handler := HTTPMiddleware(newTestAuthn(t))(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHTTPMiddleware_MissingHeader(t *testing.T) {
	handler := HTTPMiddleware(newTestAuthn(t))(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHTTPMiddleware_MalformedHeader(t *testing.T) {
	handler := HTTPMiddleware(newTestAuthn(t))(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	req.Header.Set("Authorization", "Basic abc123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHTTPMiddleware_HealthzBypassesAuth(t *testing.T) {
	handler := HTTPMiddleware(newTestAuthn(t))(echoHandler())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	// No Authorization header.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-tenant", rec.Body.String())
}
