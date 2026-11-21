// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
)

// RPCSignatureMetadata is exclusively a signed, single-use request envelope.
// A shared key is never sent over the wire. Callers must consume the returned
// replay digest atomically before executing a verified request.
const RPCSignatureMetadata = "x-gitstore-rpc"

func rpcBodyDigest(message proto.Message) (string, error) {
	if message == nil {
		return "", errors.New("missing RPC message")
	}
	// The wire contract has no maps or oneofs. Go deterministic encoding and
	// prost encode fields in tag order; shared golden vectors pin that contract.
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return "", errors.New("cannot encode RPC message")
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func rpcMAC(secret, audience, method, payload string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "gitstore-rpc-v1\n%s\n%s\n%s", audience, method, payload)
	return mac.Sum(nil)
}

func SignRPC(secret, audience, method string, message proto.Message) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", errors.New("RPC nonce unavailable")
	}
	return signRPCAt(secret, audience, method, message, time.Now(), hex.EncodeToString(nonce[:]))
}

func signRPCAt(secret, audience, method string, message proto.Message, now time.Time, nonce string) (string, error) {
	if secret == "" || audience == "" || method == "" {
		return "", errors.New("RPC signing configuration required")
	}
	digest, err := rpcBodyDigest(message)
	if err != nil {
		return "", err
	}
	payload := fmt.Sprintf("%d:%d:%s:%s", now.UnixMilli(), now.Add(time.Minute).UnixMilli(), nonce, digest)
	return payload + ":" + hex.EncodeToString(rpcMAC(secret, audience, method, payload)), nil
}

// VerifyRPC validates signature, audience, method, full decoded request and
// bounded lifetime. It does not authorize the actor or consume replay state.
func VerifyRPC(secret, audience, method, envelope string, message proto.Message, now time.Time) (string, time.Time, error) {
	invalid := errors.New("invalid signed RPC envelope")
	if secret == "" || len(envelope) > 256 {
		return "", time.Time{}, invalid
	}
	parts := strings.Split(envelope, ":")
	if len(parts) != 5 {
		return "", time.Time{}, invalid
	}
	iat, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", time.Time{}, invalid
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || iat < 0 || exp <= iat || exp-iat > 60000 || iat > now.UnixMilli()+5000 || exp <= now.UnixMilli() {
		return "", time.Time{}, invalid
	}
	for i, size := range map[int]int{2: 16, 3: 32, 4: 32} {
		decoded, err := hex.DecodeString(parts[i])
		if err != nil || len(decoded) != size || parts[i] != strings.ToLower(parts[i]) {
			return "", time.Time{}, invalid
		}
	}
	mac, _ := hex.DecodeString(parts[4])
	if !hmac.Equal(mac, rpcMAC(secret, audience, method, strings.Join(parts[:4], ":"))) {
		return "", time.Time{}, invalid
	}
	digest, err := rpcBodyDigest(message)
	if err != nil || !hmac.Equal([]byte(digest), []byte(parts[3])) {
		return "", time.Time{}, invalid
	}
	// Domain separation keeps callback replay entries distinct from client
	// assertions when reusing the datastore's replica-safe single-use table.
	replay := sha256.Sum256([]byte("gitstore-rpc-v1\n" + audience + "\n" + parts[2]))
	return hex.EncodeToString(replay[:]), time.UnixMilli(exp), nil
}
