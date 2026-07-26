package secretbox

import (
	"strings"
	"testing"
)

func TestRoundTripAndAssociatedData(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	aad := AssociatedData("tenant-1", "endpoint-1")
	ciphertext, nonce, err := box.Encrypt("webhook-secret", aad)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "webhook-secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	plaintext, err := box.Decrypt(ciphertext, nonce, aad)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "webhook-secret" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	if _, err := box.Decrypt(ciphertext, nonce, AssociatedData("other", "endpoint-1")); err == nil {
		t.Fatal("decryption accepted different associated data")
	}
}

func TestRejectsInvalidKeyLength(t *testing.T) {
	if _, err := New("dG9vLXNob3J0"); err == nil {
		t.Fatal("short encryption key accepted")
	}
}
