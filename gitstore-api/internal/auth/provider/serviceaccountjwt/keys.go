// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package serviceaccountjwt

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// signingKey is one parsed entry from auth.serviceaccount.signing_key.
// Multiple PEM blocks may be concatenated in that single config value to
// support FR-013's signing-key rotation overlap window: during rotation an
// operator prepends the new key's PEM block ahead of the outgoing key's
// block (so the new key becomes active for signing new tokens) and leaves
// the outgoing block in place until the overlap window ends, at which point
// it is removed from the config value entirely. This spec's design docs
// (data-model.md/plan.md) describe this key set as needing "kid-based
// lookup, overlap-window support" but do not specify a config shape beyond
// the single signing_key string, so a multi-PEM-block bundle in that one
// string is the mechanism chosen here to satisfy both constraints without a
// config schema change.
type signingKey struct {
	kid        string
	method     jwt.SigningMethod
	privateKey crypto.PrivateKey // nil for a verification-only (non-active) entry — not currently produced, all parsed keys carry their private half
	publicKey  crypto.PublicKey
}

// keySet holds every signing key parsed out of auth.serviceaccount.signing_key,
// keyed by kid for verification, with the first parsed key treated as the
// active signing key for newly issued tokens.
type keySet struct {
	active *signingKey
	byKID  map[string]*signingKey
}

// newKeySet parses one or more concatenated PEM blocks (Ed25519 or ECDSA P-256
// private keys) out of raw. The first block becomes the active signing key;
// every block is added to the verification set. Returns an error if raw is
// empty, contains no valid PEM block, or contains a key of an unsupported
// type/curve (FR-012: only Ed25519 and ECDSA P-256 are supported).
func newKeySet(raw string) (*keySet, error) {
	if raw == "" {
		return nil, errors.New("serviceaccountjwt: signing key material is empty")
	}

	ks := &keySet{byKID: make(map[string]*signingKey)}
	rest := []byte(raw)
	parsedAny := false
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		parsedAny = true
		key, err := parsePrivateKeyBlock(block)
		if err != nil {
			return nil, err
		}
		if _, exists := ks.byKID[key.kid]; exists {
			// Two PEM blocks reduced to the same public key (duplicate entry) — harmless, keep the first.
			continue
		}
		ks.byKID[key.kid] = key
		if ks.active == nil {
			ks.active = key
		}
	}
	if !parsedAny {
		return nil, errors.New("serviceaccountjwt: signing key material contains no PEM block")
	}
	return ks, nil
}

// parsePrivateKeyBlock parses one PEM block as an Ed25519 or ECDSA P-256
// private key and derives its kid from a SHA-256 hash of the public key
// bytes so kid is stable, collision-resistant, and never leaks any private
// material.
func parsePrivateKeyBlock(block *pem.Block) (*signingKey, error) {
	pemBytes := pem.EncodeToMemory(block)

	if edKey, err := jwt.ParseEdPrivateKeyFromPEM(pemBytes); err == nil {
		priv, ok := edKey.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("serviceaccountjwt: unsupported Ed25519 key implementation %T", edKey)
		}
		pub := priv.Public().(ed25519.PublicKey)
		kid, err := kidFromPublicKey(pub)
		if err != nil {
			return nil, err
		}
		return &signingKey{
			kid:        kid,
			method:     jwt.SigningMethodEdDSA,
			privateKey: priv,
			publicKey:  pub,
		}, nil
	}

	if ecKey, err := jwt.ParseECPrivateKeyFromPEM(pemBytes); err == nil {
		if ecKey.Curve.Params().Name != "P-256" {
			return nil, fmt.Errorf("serviceaccountjwt: unsupported ECDSA curve %q; only P-256 is supported", ecKey.Curve.Params().Name)
		}
		pub := &ecKey.PublicKey
		kid, err := kidFromPublicKey(pub)
		if err != nil {
			return nil, err
		}
		return &signingKey{
			kid:        kid,
			method:     jwt.SigningMethodES256,
			privateKey: ecKey,
			publicKey:  pub,
		}, nil
	}

	return nil, errors.New("serviceaccountjwt: PEM block is neither a valid Ed25519 nor ECDSA P-256 private key")
}

// kidFromPublicKey derives a stable key ID from canonical SubjectPublicKeyInfo
// bytes, so every API replica derives the same ID for an issuer key.
func kidFromPublicKey(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("serviceaccountjwt: encode public key for kid: %w", err)
	}
	sum := sha256.Sum256(der)
	return base64.RawURLEncoding.EncodeToString(sum[:16]), nil
}

// lookup returns the trusted verification key for kid, or false if kid is
// not (or is no longer) part of the trusted set — e.g. because an
// administrator has ended the rotation overlap window by removing that
// key's PEM block from configuration.
func (ks *keySet) lookup(kid string) (*signingKey, bool) {
	k, ok := ks.byKID[kid]
	return k, ok
}
