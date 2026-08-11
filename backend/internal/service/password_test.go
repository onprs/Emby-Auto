package service

import (
	"bytes"
	"testing"
)

func TestPasswordHasherHashesAndVerifiesArgon2id(t *testing.T) {
	hasher := NewPasswordHasher()
	hasher.memory = 8 * 1024
	hasher.iterations = 1
	hasher.parallelism = 1
	hasher.random = bytes.NewReader([]byte("0123456789abcdef"))

	hash, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	want := "$argon2id$v=19$m=8192,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$E00YDK579zCFKHdylDWWbt4Db/32fqyFEYPCGpqFXXw"
	if hash != want {
		t.Fatalf("Hash() = %q, want fixed Argon2id encoding %q", hash, want)
	}

	valid, err := hasher.Verify("correct horse battery staple", hash)
	if err != nil || !valid {
		t.Fatalf("Verify(correct) = %t, %v, want true, nil", valid, err)
	}
	valid, err = hasher.Verify("wrong password", hash)
	if err != nil || valid {
		t.Fatalf("Verify(wrong) = %t, %v, want false, nil", valid, err)
	}
}

func TestDummyPasswordHashUsesValidProductionCost(t *testing.T) {
	hasher := NewPasswordHasher()
	valid, err := hasher.Verify("an unknown user's password", dummyPasswordHash)
	if err != nil || valid {
		t.Fatalf("Verify(dummy) = %t, %v, want false with a valid production-cost hash", valid, err)
	}
}

func TestPasswordHasherRejectsMalformedAndExcessiveParameters(t *testing.T) {
	hasher := NewPasswordHasher()

	for _, encoded := range []string{
		"plaintext",
		"$argon2id$v=18$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=2097152,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if valid, err := hasher.Verify("password", encoded); err == nil || valid {
			t.Fatalf("Verify(%q) = %t, %v, want false with error", encoded, valid, err)
		}
	}
}
