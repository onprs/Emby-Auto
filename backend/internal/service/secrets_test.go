package service

import (
	"bytes"
	"testing"
)

func TestSecretCipherEncryptsWithAuthenticatedNameAndMasksResponse(t *testing.T) {
	cipher, err := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewSecretCipher() error = %v", err)
	}
	cipher.random = bytes.NewReader([]byte("nonce-value!"))

	encrypted, err := cipher.Encrypt("emby.api_key", "secret-api-key-1234")
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if string(encrypted.Ciphertext) == "secret-api-key-1234" {
		t.Fatal("ciphertext contains plaintext")
	}
	if encrypted.MaskedHint != "********1234" {
		t.Fatalf("masked hint = %q, want %q", encrypted.MaskedHint, "********1234")
	}
	plaintext, err := cipher.Decrypt(encrypted)
	if err != nil || plaintext != "secret-api-key-1234" {
		t.Fatalf("Decrypt() = %q, %v, want original plaintext", plaintext, err)
	}

	encrypted.Name = "tmdb.api_token"
	if _, err := cipher.Decrypt(encrypted); err == nil {
		t.Fatal("Decrypt() accepted ciphertext under a different secret name")
	}
}

func TestSecretCipherRequiresAES256Key(t *testing.T) {
	if _, err := NewSecretCipher([]byte("too-short")); err == nil {
		t.Fatal("NewSecretCipher() error = nil, want invalid key length error")
	}
}
