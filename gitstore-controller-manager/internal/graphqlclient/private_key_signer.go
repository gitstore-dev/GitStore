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
	"fmt"
	"math/big"
	"strings"
	"time"
)

const assertionTokenType = "gitstore-sa-assertion+jwt"

// PrivateKeyTokenSigner signs service-account assertions using a PKCS#8 PEM
// Ed25519 or ECDSA P-256 private key resolved by the bootstrap secret resolver.
type PrivateKeyTokenSigner struct {
	keyID             string
	serviceAccountUID string
	algorithm         string
	ed25519Key        ed25519.PrivateKey
	ecdsaKey          *ecdsa.PrivateKey
}

// NewPrivateKeyTokenSigner parses a service-account private key. The input is
// copied by the parser and cleared before this function returns.
func NewPrivateKeyTokenSigner(privateKeyPEM []byte, keyID, serviceAccountUID string) (*PrivateKeyTokenSigner, error) {
	defer clear(privateKeyPEM)

	if strings.TrimSpace(keyID) == "" {
		return nil, fmt.Errorf("service account key ID must not be empty")
	}
	if strings.TrimSpace(serviceAccountUID) == "" {
		return nil, fmt.Errorf("service account UID must not be empty")
	}
	block, rest := pem.Decode(privateKeyPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, fmt.Errorf("service account private key must be one PKCS#8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse service account private key: %w", err)
	}

	switch key := parsed.(type) {
	case ed25519.PrivateKey:
		return &PrivateKeyTokenSigner{
			keyID:             keyID,
			serviceAccountUID: serviceAccountUID,
			algorithm:         "EdDSA",
			ed25519Key:        key,
		}, nil
	case *ecdsa.PrivateKey:
		if key.Curve != elliptic.P256() {
			return nil, fmt.Errorf("service account ECDSA key must use P-256")
		}
		return &PrivateKeyTokenSigner{
			keyID:             keyID,
			serviceAccountUID: serviceAccountUID,
			algorithm:         "ES256",
			ecdsaKey:          key,
		}, nil
	default:
		return nil, fmt.Errorf("service account private key must be Ed25519 or ECDSA P-256")
	}
}

// SignAssertion creates a short-lived proof-of-possession JWT.
func (s *PrivateKeyTokenSigner) SignAssertion(ctx context.Context, namespace, name string, ttl time.Duration, audience string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("service account namespace and name must not be empty")
	}
	if strings.TrimSpace(audience) == "" {
		return "", fmt.Errorf("assertion audience must not be empty")
	}
	if ttl <= 0 || ttl > time.Minute {
		return "", fmt.Errorf("assertion TTL must be greater than zero and no more than one minute")
	}
	jti, err := randomJWTID()
	if err != nil {
		return "", fmt.Errorf("generate assertion ID: %w", err)
	}
	now := time.Now().UTC()
	subject := "serviceaccount:" + namespace + ":" + name
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}{
		Algorithm: s.algorithm,
		KeyID:     s.keyID,
		Type:      assertionTokenType,
	})
	if err != nil {
		return "", fmt.Errorf("marshal assertion header: %w", err)
	}
	claims, err := json.Marshal(struct {
		Audience          []string `json:"aud"`
		ExpiresAt         int64    `json:"exp"`
		IssuedAt          int64    `json:"iat"`
		Issuer            string   `json:"iss"`
		ID                string   `json:"jti"`
		NotBefore         int64    `json:"nbf"`
		ServiceAccountUID string   `json:"sa_uid"`
		Subject           string   `json:"sub"`
	}{
		Audience:          []string{audience},
		ExpiresAt:         now.Add(ttl).Unix(),
		IssuedAt:          now.Unix(),
		Issuer:            subject,
		ID:                jti,
		NotBefore:         now.Unix(),
		ServiceAccountUID: s.serviceAccountUID,
		Subject:           subject,
	})
	if err != nil {
		return "", fmt.Errorf("marshal assertion claims: %w", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	signature, err := s.sign([]byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *PrivateKeyTokenSigner) sign(input []byte) ([]byte, error) {
	switch s.algorithm {
	case "EdDSA":
		return ed25519.Sign(s.ed25519Key, input), nil
	case "ES256":
		digest := sha256.Sum256(input)
		r, v, err := ecdsa.Sign(rand.Reader, s.ecdsaKey, digest[:])
		if err != nil {
			return nil, fmt.Errorf("sign assertion: %w", err)
		}
		return encodeECDSASignature(r, v, 32), nil
	default:
		return nil, fmt.Errorf("unsupported assertion signing algorithm")
	}
}

func encodeECDSASignature(r, s *big.Int, size int) []byte {
	signature := make([]byte, size*2)
	r.FillBytes(signature[:size])
	s.FillBytes(signature[size:])
	return signature
}

func randomJWTID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
