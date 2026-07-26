package ssrf

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addresses, ok := f[host]
	if !ok {
		return nil, errors.New("not found")
	}
	return addresses, nil
}

func TestBlocksPrivateAndMetadataAddresses(t *testing.T) {
	policy := NewPolicy(false)
	for _, raw := range []string{
		"http://127.0.0.1/hook",
		"http://10.0.0.5/hook",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]/hook",
		"http://localhost/hook",
	} {
		if err := policy.ValidateURL(context.Background(), raw); !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("%s: error = %v, want blocked address", raw, err)
		}
	}
}

func TestRejectsDNSNameIfAnyAnswerIsPrivate(t *testing.T) {
	policy := NewPolicy(false)
	policy.resolver = fakeResolver{
		"rebind.example": {
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("127.0.0.1"),
		},
	}
	if err := policy.ValidateURL(context.Background(), "https://rebind.example/hook"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("error = %v, want blocked address", err)
	}
}

func TestAllowsPublicAddress(t *testing.T) {
	policy := NewPolicy(false)
	policy.resolver = fakeResolver{
		"hooks.example": {netip.MustParseAddr("93.184.216.34")},
	}
	if err := policy.ValidateURL(context.Background(), "https://hooks.example/webhook"); err != nil {
		t.Fatal(err)
	}
}

func TestDevelopmentOverrideAllowsPrivate(t *testing.T) {
	policy := NewPolicy(true)
	if err := policy.ValidateURL(context.Background(), "http://127.0.0.1:8090/hook"); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsCredentialsAndNonHTTP(t *testing.T) {
	policy := NewPolicy(true)
	for _, raw := range []string{
		"file:///etc/passwd",
		"http://user:pass@example.com/hook",
		"gopher://example.com/",
	} {
		if err := policy.ValidateURL(context.Background(), raw); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
}
