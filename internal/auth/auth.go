package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const (
	tokenMarker      = "hf"
	tokenPrefixBytes = 9
	tokenSecretBytes = 32
	maxAuthHeader    = 256
)

var tenantSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

var (
	ErrInvalidCredentials = errors.New("invalid API key")
	ErrNoAPIKeys          = errors.New("no API keys are configured")
)

type Principal struct {
	TenantID   string
	TenantSlug string
	TenantName string
	APIKeyID   string
}

type Candidate struct {
	Principal Principal
	KeyHash   []byte
}

type Repository interface {
	FindCandidate(context.Context, string) (Candidate, error)
	MarkUsed(context.Context, string) error
	KeyCount(context.Context) (int, error)
	Bootstrap(context.Context, string, string, string, []byte) error
	ProvisionTenant(context.Context, string, string, string, []byte) error
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func GenerateToken() (string, error) {
	prefix, err := randomBase64(tokenPrefixBytes)
	if err != nil {
		return "", err
	}
	secret, err := randomBase64(tokenSecretBytes)
	if err != nil {
		return "", err
	}
	return tokenMarker + "." + prefix + "." + secret, nil
}

func Prefix(token string) (string, error) {
	if len(token) > maxAuthHeader {
		return "", ErrInvalidCredentials
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenMarker || len(parts[1]) != base64.RawURLEncoding.EncodedLen(tokenPrefixBytes) {
		return "", ErrInvalidCredentials
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		return "", ErrInvalidCredentials
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(secret) != tokenSecretBytes {
		return "", ErrInvalidCredentials
	}
	return parts[1], nil
}

func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	prefix, err := Prefix(token)
	if err != nil {
		return Principal{}, ErrInvalidCredentials
	}
	candidate, err := s.repository.FindCandidate(ctx, prefix)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			return Principal{}, ErrInvalidCredentials
		}
		return Principal{}, fmt.Errorf("look up API key: %w", err)
	}
	actualHash := Hash(token)
	if len(candidate.KeyHash) != sha256.Size ||
		subtle.ConstantTimeCompare(candidate.KeyHash, actualHash) != 1 {
		return Principal{}, ErrInvalidCredentials
	}
	if err := s.repository.MarkUsed(ctx, candidate.Principal.APIKeyID); err != nil {
		return Principal{}, fmt.Errorf("mark API key used: %w", err)
	}
	return candidate.Principal, nil
}

func (s *Service) EnsureBootstrap(
	ctx context.Context,
	tenantSlug, tenantName, token string,
) error {
	if err := ValidateTenant(tenantSlug, tenantName); err != nil {
		return err
	}
	count, err := s.repository.KeyCount(ctx)
	if err != nil {
		return fmt.Errorf("count API keys: %w", err)
	}
	if token == "" {
		if count == 0 {
			return ErrNoAPIKeys
		}
		return nil
	}
	prefix, err := Prefix(token)
	if err != nil {
		return errors.New("HOOKFORGE_BOOTSTRAP_API_KEY has an invalid format; generate it with `hookforge generate-secrets`")
	}
	if err := s.repository.Bootstrap(ctx, tenantSlug, tenantName, prefix, Hash(token)); err != nil {
		return fmt.Errorf("bootstrap tenant: %w", err)
	}
	return nil
}

func (s *Service) ProvisionTenant(ctx context.Context, slug, name string) (string, error) {
	if err := ValidateTenant(slug, name); err != nil {
		return "", err
	}
	token, err := GenerateToken()
	if err != nil {
		return "", err
	}
	prefix, err := Prefix(token)
	if err != nil {
		return "", err
	}
	if err := s.repository.ProvisionTenant(ctx, slug, name, prefix, Hash(token)); err != nil {
		return "", fmt.Errorf("provision tenant: %w", err)
	}
	return token, nil
}

func ValidateTenant(slug, name string) error {
	if !tenantSlugPattern.MatchString(slug) {
		return errors.New("tenant slug must contain 2 to 63 lowercase letters, digits, or hyphens")
	}
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 120 {
		return errors.New("tenant name must contain 1 to 120 characters")
	}
	return nil
}

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if len(header) > maxAuthHeader || !strings.HasPrefix(header, "Bearer ") {
			writeUnauthorized(w)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		principal, err := s.Authenticate(r.Context(), token)
		if err != nil {
			if errors.Is(err, ErrInvalidCredentials) {
				writeUnauthorized(w)
			} else {
				writeAuthUnavailable(w)
			}
			return
		}
		ctx := WithPrincipal(r.Context(), principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

type principalContextKey struct{}

func randomBase64(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="hookforge"`)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    "unauthorized",
			"message": "a valid Bearer API key is required",
		},
	})
}

func writeAuthUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    "authentication_unavailable",
			"message": "authentication is temporarily unavailable",
		},
	})
}
