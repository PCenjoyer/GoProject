package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeRepository struct {
	candidate Candidate
	findErr   error
}

func (f *fakeRepository) FindCandidate(context.Context, string) (Candidate, error) {
	if f.findErr != nil {
		return Candidate{}, f.findErr
	}
	if len(f.candidate.KeyHash) == 0 {
		return Candidate{}, ErrInvalidCredentials
	}
	return f.candidate, nil
}

func TestMiddlewareReturnsUnavailableOnRepositoryFailure(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(&fakeRepository{findErr: errors.New("database unavailable")})
	request := httptest.NewRequest(http.MethodGet, "/v1/endpoints", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()

	service.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached handler")
	})).ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}
func (f *fakeRepository) MarkUsed(context.Context, string) error { return nil }
func (f *fakeRepository) KeyCount(context.Context) (int, error)  { return 1, nil }
func (f *fakeRepository) Bootstrap(context.Context, string, string, string, []byte) error {
	return nil
}
func (f *fakeRepository) ProvisionTenant(context.Context, string, string, string, []byte) error {
	return nil
}

func TestMiddlewareAuthenticatesAndInjectsTenant(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{candidate: Candidate{
		Principal: Principal{TenantID: "tenant-1", APIKeyID: "key-1"},
		KeyHash:   Hash(token),
	}}
	service := NewService(repository)
	if _, err := service.Authenticate(context.Background(), token); err != nil {
		t.Fatalf("direct authentication failed: %v", err)
	}
	handler := service.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.TenantID != "tenant-1" {
			t.Fatal("tenant principal missing from context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/endpoints", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestMiddlewareRejectsMissingAndWrongTokens(t *testing.T) {
	valid, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(&fakeRepository{candidate: Candidate{
		Principal: Principal{TenantID: "tenant-1"},
		KeyHash:   Hash(valid),
	}})
	for name, header := range map[string]string{
		"missing": "",
		"wrong":   "Bearer " + wrong,
		"basic":   "Basic credentials",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/endpoints", nil)
			request.Header.Set("Authorization", header)
			response := httptest.NewRecorder()
			service.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("unauthenticated request reached handler")
			})).ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}
}

func TestTokenFormatRejectsMalformedValues(t *testing.T) {
	for _, token := range []string{"", "abc", "hf.short.secret", "hf.a.b.c"} {
		if _, err := Prefix(token); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("token %q accepted", token)
		}
	}
}

func TestGeneratedTokensAlwaysParse(t *testing.T) {
	for range 100 {
		token, err := GenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Prefix(token); err != nil {
			t.Fatalf("generated token did not parse: %v", err)
		}
	}
}
