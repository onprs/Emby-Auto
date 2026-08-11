package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type SecretCipher struct {
	aead   cipher.AEAD
	random io.Reader
}

func NewSecretCipher(key []byte) (*SecretCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("configuration encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize configuration cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize configuration AEAD: %w", err)
	}
	return &SecretCipher{aead: aead, random: rand.Reader}, nil
}

func (secretCipher *SecretCipher) Encrypt(name, plaintext string) (domain.EncryptedSecret, error) {
	nonce := make([]byte, secretCipher.aead.NonceSize())
	if _, err := io.ReadFull(secretCipher.random, nonce); err != nil {
		return domain.EncryptedSecret{}, fmt.Errorf("generate secret nonce: %w", err)
	}
	ciphertext := secretCipher.aead.Seal(nil, nonce, []byte(plaintext), []byte(name))
	return domain.EncryptedSecret{
		Name:       name,
		Ciphertext: ciphertext,
		Nonce:      nonce,
		MaskedHint: maskSecret(plaintext),
	}, nil
}

func (secretCipher *SecretCipher) Decrypt(secret domain.EncryptedSecret) (string, error) {
	plaintext, err := secretCipher.aead.Open(nil, secret.Nonce, secret.Ciphertext, []byte(secret.Name))
	if err != nil {
		return "", fmt.Errorf("decrypt secret %q: %w", secret.Name, err)
	}
	return string(plaintext), nil
}

func maskSecret(value string) string {
	runes := []rune(value)
	if len(runes) <= 4 {
		return "********"
	}
	return "********" + string(runes[len(runes)-4:])
}
