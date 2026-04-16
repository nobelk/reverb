package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

func testConfig() reverb.AuthConfig {
	return reverb.AuthConfig{
		Enabled: true,
		Tenants: []reverb.Tenant{
			{ID: "tenant-a", Name: "Acme", APIKeys: []string{"key-a1", "key-a2"}},
			{ID: "tenant-b", Name: "Widgets", APIKeys: []string{"key-b1"}},
		},
	}
}

func TestNewAuthenticator_Success(t *testing.T) {
	authn, err := NewAuthenticator(testConfig())
	require.NoError(t, err)
	assert.NotNil(t, authn)
}

func TestNewAuthenticator_NoTenants(t *testing.T) {
	_, err := NewAuthenticator(reverb.AuthConfig{Tenants: nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tenants")
}

func TestNewAuthenticator_EmptyTenantID(t *testing.T) {
	cfg := reverb.AuthConfig{
		Tenants: []reverb.Tenant{{ID: "", APIKeys: []string{"k"}}},
	}
	_, err := NewAuthenticator(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty id")
}

func TestNewAuthenticator_NoAPIKeys(t *testing.T) {
	cfg := reverb.AuthConfig{
		Tenants: []reverb.Tenant{{ID: "t1", APIKeys: nil}},
	}
	_, err := NewAuthenticator(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no api_keys")
}

func TestNewAuthenticator_DuplicateKey(t *testing.T) {
	cfg := reverb.AuthConfig{
		Tenants: []reverb.Tenant{
			{ID: "t1", APIKeys: []string{"shared-key"}},
			{ID: "t2", APIKeys: []string{"shared-key"}},
		},
	}
	_, err := NewAuthenticator(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestAuthenticate_ValidKey(t *testing.T) {
	authn, err := NewAuthenticator(testConfig())
	require.NoError(t, err)

	tenant, ok := authn.Authenticate("key-a1")
	assert.True(t, ok)
	assert.Equal(t, "tenant-a", tenant.ID)
	assert.Equal(t, "Acme", tenant.Name)
}

func TestAuthenticate_SecondKey(t *testing.T) {
	authn, err := NewAuthenticator(testConfig())
	require.NoError(t, err)

	tenant, ok := authn.Authenticate("key-a2")
	assert.True(t, ok)
	assert.Equal(t, "tenant-a", tenant.ID)
}

func TestAuthenticate_DifferentTenant(t *testing.T) {
	authn, err := NewAuthenticator(testConfig())
	require.NoError(t, err)

	tenant, ok := authn.Authenticate("key-b1")
	assert.True(t, ok)
	assert.Equal(t, "tenant-b", tenant.ID)
}

func TestAuthenticate_InvalidKey(t *testing.T) {
	authn, err := NewAuthenticator(testConfig())
	require.NoError(t, err)

	_, ok := authn.Authenticate("wrong-key")
	assert.False(t, ok)
}

func TestAuthenticate_EmptyToken(t *testing.T) {
	authn, err := NewAuthenticator(testConfig())
	require.NoError(t, err)

	_, ok := authn.Authenticate("")
	assert.False(t, ok)
}

func TestTenantContext_RoundTrip(t *testing.T) {
	tenant := &TenantInfo{ID: "t1", Name: "Test"}
	ctx := WithTenant(context.Background(), tenant)

	got, ok := TenantFromContext(ctx)
	assert.True(t, ok)
	assert.Equal(t, tenant, got)
}

func TestTenantFromContext_Missing(t *testing.T) {
	_, ok := TenantFromContext(context.Background())
	assert.False(t, ok)
}

func TestScopedNamespace_WithTenant(t *testing.T) {
	ctx := WithTenant(context.Background(), &TenantInfo{ID: "acme"})
	assert.Equal(t, "acme::my-ns", ScopedNamespace(ctx, "my-ns"))
}

func TestScopedNamespace_WithoutTenant(t *testing.T) {
	assert.Equal(t, "my-ns", ScopedNamespace(context.Background(), "my-ns"))
}

func TestUnscopeNamespace_Match(t *testing.T) {
	ns, ok := UnscopeNamespace("acme", "acme::my-ns")
	assert.True(t, ok)
	assert.Equal(t, "my-ns", ns)
}

func TestUnscopeNamespace_NoMatch(t *testing.T) {
	ns, ok := UnscopeNamespace("acme", "other::my-ns")
	assert.False(t, ok)
	assert.Equal(t, "other::my-ns", ns)
}

func TestUnscopeNamespace_NoPrefix(t *testing.T) {
	ns, ok := UnscopeNamespace("acme", "plain-ns")
	assert.False(t, ok)
	assert.Equal(t, "plain-ns", ns)
}

func TestNamespaceBelongsToTenant(t *testing.T) {
	assert.True(t, NamespaceBelongsToTenant("acme", "acme::my-ns"))
	assert.False(t, NamespaceBelongsToTenant("acme", "other::my-ns"))
	assert.False(t, NamespaceBelongsToTenant("acme", "plain-ns"))
	// Guard against prefix-without-separator collisions.
	assert.False(t, NamespaceBelongsToTenant("acme", "acme-extra::ns"))
}
