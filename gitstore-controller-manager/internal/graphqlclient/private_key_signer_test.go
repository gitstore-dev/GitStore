// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestPrivateKeyTokenSignerEd25519ClaimsAndSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, privateKeyPEM := newSigner(t, privateKey)

	before := time.Now().UTC()
	token, err := signer.SignAssertion(context.Background(), "controllers", "manager", assertionSigningLifetime, "gitstore-api/serviceaccount-token")
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("SignAssertion() error: %v", err)
	}
	assertion := decodeAssertion(t, token)
	if assertion.Header.Algorithm != "EdDSA" || assertion.Header.KeyID != "key-1" || assertion.Header.Type != assertionTokenType {
		t.Errorf("header = %+v", assertion.Header)
	}
	if assertion.Claims.Issuer != "serviceaccount:controllers:manager" ||
		assertion.Claims.Subject != assertion.Claims.Issuer ||
		assertion.Claims.ServiceAccountUID != "sa-uid-1" {
		t.Errorf("claims identity = %+v", assertion.Claims)
	}
	if len(assertion.Claims.Audience) != 1 || assertion.Claims.Audience[0] != "gitstore-api/serviceaccount-token" {
		t.Errorf("audience = %v", assertion.Claims.Audience)
	}
	if assertion.Claims.ID == "" || assertion.Claims.NotBefore != assertion.Claims.IssuedAt {
		t.Errorf("claims nonce/timing = %+v", assertion.Claims)
	}
	if assertion.Claims.ExpiresAt-assertion.Claims.IssuedAt != int64(assertionSigningLifetime.Seconds()) {
		t.Errorf("lifetime = %d seconds, want %d", assertion.Claims.ExpiresAt-assertion.Claims.IssuedAt, int64(assertionSigningLifetime.Seconds()))
	}
	if time.Unix(assertion.Claims.IssuedAt, 0).Before(before.Add(-time.Second)) ||
		time.Unix(assertion.Claims.IssuedAt, 0).After(after.Add(time.Second)) {
		t.Errorf("iat = %v outside signing interval", time.Unix(assertion.Claims.IssuedAt, 0))
	}
	if !ed25519.Verify(publicKey, []byte(assertion.SigningInput), assertion.Signature) {
		t.Fatal("assertion signature did not verify")
	}
	for _, value := range privateKeyPEM {
		if value != 0 {
			t.Fatal("NewPrivateKeyTokenSigner did not clear source key bytes")
		}
	}
}

func TestPrivateKeyTokenSignerECDSAP256ClaimsAndSignature(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := newSigner(t, privateKey)

	token, err := signer.SignAssertion(context.Background(), "controllers", "manager", 30*time.Second, "gitstore-api/serviceaccount-token")
	if err != nil {
		t.Fatalf("SignAssertion() error: %v", err)
	}
	assertion := decodeAssertion(t, token)
	if assertion.Header.Algorithm != "ES256" {
		t.Errorf("algorithm = %q, want ES256", assertion.Header.Algorithm)
	}
	if len(assertion.Signature) != 64 {
		t.Fatalf("signature length = %d, want 64", len(assertion.Signature))
	}
	digest := sha256.Sum256([]byte(assertion.SigningInput))
	r := new(big.Int).SetBytes(assertion.Signature[:32])
	s := new(big.Int).SetBytes(assertion.Signature[32:])
	if !ecdsa.Verify(&privateKey.PublicKey, digest[:], r, s) {
		t.Fatal("assertion signature did not verify")
	}
}

func TestPrivateKeyTokenSignerRejectsInvalidKeysAndAssertionInputs(t *testing.T) {
	_, edPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	validPEM := privateKeyPEM(t, edPrivateKey)

	tests := []struct {
		name string
		pem  []byte
		id   string
		uid  string
	}{
		{name: "empty key", id: "key-1", uid: "sa-uid-1"},
		{name: "invalid key", pem: []byte("not a PEM"), id: "key-1", uid: "sa-uid-1"},
		{name: "missing key id", pem: append([]byte(nil), validPEM...), uid: "sa-uid-1"},
		{name: "missing uid", pem: append([]byte(nil), validPEM...), id: "key-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewPrivateKeyTokenSigner(tt.pem, tt.id, tt.uid); err == nil {
				t.Fatal("NewPrivateKeyTokenSigner() error = nil")
			}
		})
	}

	signer, _ := newSigner(t, edPrivateKey)
	for _, tt := range []struct {
		name      string
		namespace string
		nameValue string
		ttl       time.Duration
		audience  string
	}{
		{name: "missing namespace", nameValue: "manager", ttl: time.Minute, audience: "aud"},
		{name: "missing name", namespace: "controllers", ttl: time.Minute, audience: "aud"},
		{name: "missing audience", namespace: "controllers", nameValue: "manager", ttl: time.Minute},
		{name: "zero ttl", namespace: "controllers", nameValue: "manager", audience: "aud"},
		{name: "long ttl", namespace: "controllers", nameValue: "manager", ttl: time.Minute + time.Second, audience: "aud"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := signer.SignAssertion(context.Background(), tt.namespace, tt.nameValue, tt.ttl, tt.audience); err == nil {
				t.Fatal("SignAssertion() error = nil")
			}
		})
	}
}

func newSigner(t *testing.T, key any) (*PrivateKeyTokenSigner, []byte) {
	t.Helper()
	value := privateKeyPEM(t, key)
	signer, err := NewPrivateKeyTokenSigner(value, "key-1", "sa-uid-1")
	if err != nil {
		t.Fatalf("NewPrivateKeyTokenSigner() error: %v", err)
	}
	return signer, value
}

func privateKeyPEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

type decodedAssertion struct {
	Header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}
	Claims struct {
		Audience          []string `json:"aud"`
		ExpiresAt         int64    `json:"exp"`
		IssuedAt          int64    `json:"iat"`
		Issuer            string   `json:"iss"`
		ID                string   `json:"jti"`
		NotBefore         int64    `json:"nbf"`
		ServiceAccountUID string   `json:"sa_uid"`
		Subject           string   `json:"sub"`
	}
	SigningInput string
	Signature    []byte
}

func decodeAssertion(t *testing.T, token string) decodedAssertion {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token part count = %d, want 3", len(parts))
	}
	var assertion decodedAssertion
	for _, item := range []struct {
		encoded string
		target  any
	}{
		{parts[0], &assertion.Header},
		{parts[1], &assertion.Claims},
	} {
		value, err := base64.RawURLEncoding.DecodeString(item.encoded)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(value, item.target); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	assertion.Signature, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	assertion.SigningInput = parts[0] + "." + parts[1]
	return assertion
}
