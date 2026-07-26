package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const KeySize = 32

type Box struct {
	aead cipher.AEAD
}

func New(encodedKey string) (*Box, error) {
	key, err := base64.RawStdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("decode HOOKFORGE_SECRET_ENCRYPTION_KEY: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("HOOKFORGE_SECRET_ENCRYPTION_KEY must decode to %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

func GenerateKey() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate encryption key: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(key), nil
}

func (b *Box) Encrypt(plaintext string, associatedData []byte) (ciphertext, nonce []byte, err error) {
	if plaintext == "" {
		return nil, nil, errors.New("refusing to encrypt an empty secret")
	}
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate encryption nonce: %w", err)
	}
	ciphertext = b.aead.Seal(nil, nonce, []byte(plaintext), associatedData)
	return ciphertext, nonce, nil
}

func (b *Box) Decrypt(ciphertext, nonce, associatedData []byte) (string, error) {
	if len(nonce) != b.aead.NonceSize() {
		return "", errors.New("invalid encrypted secret nonce")
	}
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return "", errors.New("decrypt endpoint secret: authentication failed")
	}
	return string(plaintext), nil
}

func AssociatedData(tenantID, endpointID string) []byte {
	return []byte("hookforge:endpoint-secret:v1:" + tenantID + ":" + endpointID)
}
