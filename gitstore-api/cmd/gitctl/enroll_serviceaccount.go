// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errServiceAccountAlreadyExists = errors.New("service account already exists")

const (
	createServiceAccountMutation = `mutation CreateServiceAccount($namespace: String!, $name: String!, $keyID: String!, $algorithm: String!, $publicKey: String!) {
  createServiceAccount(input: {
    apiVersion: "gitstore.dev/v1beta1"
    kind: "ServiceAccount"
    metadata: { namespace: $namespace, name: $name }
    publicKeys: [{ kid: $keyID, algorithm: $algorithm, publicKeyPEM: $publicKey }]
  }) { metadata { uid } }
}`
	rotateServiceAccountKeyMutation = `mutation RotateServiceAccountKey($namespace: String!, $name: String!, $keyID: String!, $algorithm: String!, $publicKey: String!) {
  rotateServiceAccountKey(input: {
    metadata: { namespace: $namespace, name: $name }
    add: [{ kid: $keyID, algorithm: $algorithm, publicKeyPEM: $publicKey }]
    removeKids: [$keyID]
  }) { metadata { uid } }
}`
)

type serviceAccountEnrollment struct {
	apiURL         string
	adminToken     string
	namespace      string
	name           string
	keyID          string
	privateKeyPath string
	identityPath   string
	replaceKey     bool
	adminUsername  string
	adminPassword  string
}

type enrollmentClient struct {
	httpClient *http.Client
}

func runEnrollServiceAccount(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("enroll-serviceaccount", flag.ContinueOnError)
	flags.SetOutput(stderr)
	enrollment := serviceAccountEnrollment{}
	flags.StringVar(&enrollment.apiURL, "api-url", envOrDefault("API_URL", "http://localhost:4000/graphql"), "GitStore GraphQL API URL")
	flags.StringVar(&enrollment.adminToken, "admin-token", "", "authenticated administrator bearer token")
	flags.StringVar(&enrollment.namespace, "namespace", "controllers", "ServiceAccount namespace")
	flags.StringVar(&enrollment.name, "name", "gitstore-controller-manager", "ServiceAccount name")
	flags.StringVar(&enrollment.keyID, "key-id", "controller-key", "enrolled public key ID")
	flags.StringVar(&enrollment.privateKeyPath, "private-key-path", "", "explicit secure local private-key file path")
	flags.StringVar(&enrollment.identityPath, "identity-output-path", "", "optional secure local ServiceAccount identity file path")
	flags.BoolVar(&enrollment.replaceKey, "replace-existing-key", false, "replace this key ID when the ServiceAccount already exists")
	flags.StringVar(&enrollment.adminUsername, "admin-username", "", "administrator username for bootstrap authentication")
	flags.StringVar(&enrollment.adminPassword, "admin-password", "", "administrator password for bootstrap authentication")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "enroll-serviceaccount does not accept positional arguments")
		return 2
	}
	if enrollment.adminToken == "" {
		enrollment.adminToken = os.Getenv("BOOTSTRAP_TOKEN")
	}
	if enrollment.adminUsername == "" {
		enrollment.adminUsername = os.Getenv("GITSTORE_BOOTSTRAP_ADMIN_USERNAME")
	}
	if enrollment.adminPassword == "" {
		enrollment.adminPassword = os.Getenv("GITSTORE_BOOTSTRAP_ADMIN_PASSWORD")
	}
	if err := enrollment.validate(); err != nil {
		fmt.Fprintln(stderr, "enroll-serviceaccount configuration is invalid")
		return 2
	}

	client := enrollmentClient{httpClient: &http.Client{Timeout: 10 * time.Second}}
	if enrollment.adminToken == "" {
		token, err := client.login(context.Background(), enrollment)
		if err != nil {
			fmt.Fprintln(stderr, "enroll-serviceaccount bootstrap authentication failed")
			return 1
		}
		enrollment.adminToken = token
	}

	privateKey, generated, err := loadOrGeneratePrivateKey(enrollment.privateKeyPath)
	if err != nil {
		fmt.Fprintln(stderr, "enroll-serviceaccount could not use the private-key path")
		return 1
	}
	privateKeyPEM, publicKeyPEM, algorithm, err := encodeEnrollmentKeyPair(privateKey)
	if err != nil {
		fmt.Fprintln(stderr, "enroll-serviceaccount could not prepare the key pair")
		return 1
	}

	uid, err := client.enroll(context.Background(), enrollment, publicKeyPEM, algorithm)
	switch {
	case err == nil:
		if generated && writePrivateKey(enrollment.privateKeyPath, privateKeyPEM) != nil {
			fmt.Fprintln(stderr, "enroll-serviceaccount could not securely persist the private key")
			return 1
		}
		if err := writeServiceAccountIdentity(enrollment.identityPath, uid); err != nil {
			fmt.Fprintln(stderr, "enroll-serviceaccount could not securely persist the ServiceAccount identity")
			return 1
		}
		fmt.Fprintln(stdout, "ServiceAccount enrollment created.")
		return 0
	case errors.Is(err, errServiceAccountAlreadyExists) && !enrollment.replaceKey && !generated:
		if err := requireServiceAccountIdentity(enrollment.identityPath); err != nil {
			fmt.Fprintln(stderr, "enroll-serviceaccount existing enrollment requires its persisted ServiceAccount identity")
			return 1
		}
		fmt.Fprintln(stdout, "ServiceAccount enrollment already exists; no changes made.")
		return 0
	case errors.Is(err, errServiceAccountAlreadyExists) && enrollment.replaceKey:
		uid, err := client.rotate(context.Background(), enrollment, publicKeyPEM, algorithm)
		if err != nil {
			fmt.Fprintln(stderr, "enroll-serviceaccount could not replace the enrolled public key")
			return 1
		}
		if generated && writePrivateKey(enrollment.privateKeyPath, privateKeyPEM) != nil {
			fmt.Fprintln(stderr, "enroll-serviceaccount could not securely persist the private key")
			return 1
		}
		if err := writeServiceAccountIdentity(enrollment.identityPath, uid); err != nil {
			fmt.Fprintln(stderr, "enroll-serviceaccount could not securely persist the ServiceAccount identity")
			return 1
		}
		fmt.Fprintln(stdout, "ServiceAccount enrollment key replaced.")
		return 0
	case errors.Is(err, errServiceAccountAlreadyExists):
		fmt.Fprintln(stderr, "enroll-serviceaccount already exists; refusing to persist an unverified private key without --replace-existing-key")
		return 1
	default:
		fmt.Fprintln(stderr, "enroll-serviceaccount request failed")
		return 1
	}
}

func (e serviceAccountEnrollment) validate() error {
	for _, value := range []string{e.apiURL, e.namespace, e.name, e.keyID, e.privateKeyPath} {
		if strings.TrimSpace(value) == "" {
			return errors.New("required enrollment value is empty")
		}
	}
	if strings.TrimSpace(e.adminToken) == "" &&
		(strings.TrimSpace(e.adminUsername) == "" || strings.TrimSpace(e.adminPassword) == "") {
		return errors.New("administrator credentials are required")
	}
	if !filepath.IsAbs(e.privateKeyPath) {
		return errors.New("private-key path must be absolute")
	}
	if e.identityPath != "" && !filepath.IsAbs(e.identityPath) {
		return errors.New("identity path must be absolute")
	}
	return nil
}

func loadOrGeneratePrivateKey(path string) (crypto.Signer, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		_, privateKey, generateErr := ed25519.GenerateKey(rand.Reader)
		return privateKey, true, generateErr
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, false, errors.New("private key is not a secure regular file")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, false, errors.New("private key cannot be read")
	}
	block, rest := pem.Decode(encoded)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, false, errors.New("private key is not a single PEM block")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, false, errors.New("private key cannot be parsed")
	}
	signer, ok := key.(crypto.Signer)
	if !ok || !supportedEnrollmentKey(signer) {
		return nil, false, errors.New("private key has an unsupported algorithm")
	}
	return signer, false, nil
}

func supportedEnrollmentKey(signer crypto.Signer) bool {
	switch key := signer.Public().(type) {
	case ed25519.PublicKey:
		return true
	case *ecdsa.PublicKey:
		return key.Curve == elliptic.P256()
	default:
		return false
	}
}

func encodeEnrollmentKeyPair(privateKey crypto.Signer) ([]byte, string, string, error) {
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, "", "", err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return nil, "", "", err
	}
	algorithm := "Ed25519"
	if _, ok := privateKey.Public().(*ecdsa.PublicKey); ok {
		algorithm = "ECDSA-P256"
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		algorithm, nil
}

func writePrivateKey(path string, value []byte) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return errors.New("private-key path must be absolute")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("private key path cannot be created")
	}
	defer file.Close() //nolint:errcheck
	if _, err := file.Write(value); err != nil {
		_ = os.Remove(path)
		return errors.New("private key write failed")
	}
	if err := file.Sync(); err != nil {
		_ = os.Remove(path)
		return errors.New("private key sync failed")
	}
	return nil
}

func writeServiceAccountIdentity(path, uid string) error {
	if path == "" {
		return nil
	}
	if !validServiceAccountUID(uid) {
		return errors.New("ServiceAccount UID is empty")
	}
	return writeNewFile(path, []byte("GITSTORE_CONTROLLER__SERVICEACCOUNT__UID="+uid+"\n"))
}

func requireServiceAccountIdentity(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("ServiceAccount identity is unavailable")
	}
	value, err := os.ReadFile(path)
	const prefix = "GITSTORE_CONTROLLER__SERVICEACCOUNT__UID="
	if err != nil || !strings.HasPrefix(string(value), prefix) ||
		!validServiceAccountUID(strings.TrimSuffix(strings.TrimPrefix(string(value), prefix), "\n")) ||
		!strings.HasSuffix(string(value), "\n") {
		return errors.New("ServiceAccount identity is invalid")
	}
	return nil
}

func validServiceAccountUID(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func writeNewFile(path string, value []byte) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return errors.New("output path must be absolute")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("output path cannot be created")
	}
	defer file.Close() //nolint:errcheck
	if _, err := file.Write(value); err != nil {
		_ = os.Remove(path)
		return errors.New("output write failed")
	}
	if err := file.Sync(); err != nil {
		_ = os.Remove(path)
		return errors.New("output sync failed")
	}
	return nil
}

func (c enrollmentClient) enroll(ctx context.Context, enrollment serviceAccountEnrollment, publicKey, algorithm string) (string, error) {
	payload, err := c.graphQL(ctx, enrollment, createServiceAccountMutation, map[string]string{
		"namespace": enrollment.namespace,
		"name":      enrollment.name,
		"keyID":     enrollment.keyID,
		"algorithm": algorithm,
		"publicKey": publicKey,
	})
	if err != nil {
		return "", err
	}
	return serviceAccountUID(payload, "createServiceAccount")
}

func (c enrollmentClient) rotate(ctx context.Context, enrollment serviceAccountEnrollment, publicKey, algorithm string) (string, error) {
	payload, err := c.graphQL(ctx, enrollment, rotateServiceAccountKeyMutation, map[string]string{
		"namespace": enrollment.namespace,
		"name":      enrollment.name,
		"keyID":     enrollment.keyID,
		"algorithm": algorithm,
		"publicKey": publicKey,
	})
	if err != nil {
		return "", err
	}
	return serviceAccountUID(payload, "rotateServiceAccountKey")
}

func (c enrollmentClient) login(ctx context.Context, enrollment serviceAccountEnrollment) (string, error) {
	payload, err := c.graphQL(ctx, enrollment, `mutation Login($username: String!, $password: String!) {
  login(input: { username: $username, password: $password }) { token { accessToken } }
}`, map[string]string{"username": enrollment.adminUsername, "password": enrollment.adminPassword})
	if err != nil {
		return "", err
	}
	var data struct {
		Login struct {
			Token struct {
				AccessToken string `json:"accessToken"`
			} `json:"token"`
		} `json:"login"`
	}
	if err := json.Unmarshal(payload, &data); err != nil || data.Login.Token.AccessToken == "" {
		return "", errors.New("bootstrap authentication response is invalid")
	}
	return data.Login.Token.AccessToken, nil
}

func (c enrollmentClient) graphQL(ctx context.Context, enrollment serviceAccountEnrollment, query string, variables map[string]string) (json.RawMessage, error) {
	body, err := json.Marshal(struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}{Query: query, Variables: variables})
	if err != nil {
		return nil, errors.New("could not encode GraphQL request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, enrollment.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not create GraphQL request")
	}
	request.Header.Set("Content-Type", "application/json")
	if enrollment.adminToken != "" {
		request.Header.Set("Authorization", "Bearer "+enrollment.adminToken)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, errors.New("could not reach GraphQL API")
	}
	defer response.Body.Close() //nolint:errcheck
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("GraphQL API returned an unsuccessful status")
	}
	var payload struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, errors.New("could not decode GraphQL response")
	}
	for _, graphQLError := range payload.Errors {
		if strings.Contains(strings.ToLower(graphQLError.Message), "already exists") {
			return nil, errServiceAccountAlreadyExists
		}
	}
	if len(payload.Errors) != 0 {
		return nil, errors.New("GraphQL API returned an error")
	}
	return payload.Data, nil
}

func serviceAccountUID(payload json.RawMessage, field string) (string, error) {
	var data map[string]struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(payload, &data); err != nil || data[field].Metadata.UID == "" {
		return "", errors.New("ServiceAccount response does not contain an identity")
	}
	return data[field].Metadata.UID, nil
}

func runGenerateServiceAccountKey(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("generate-serviceaccount-key", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("private-key-path", "", "explicit secure local private-key file path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*path) == "" || !filepath.IsAbs(*path) {
		fmt.Fprintln(stderr, "generate-serviceaccount-key requires an absolute --private-key-path")
		return 2
	}
	key, generated, err := loadOrGeneratePrivateKey(*path)
	if err != nil {
		fmt.Fprintln(stderr, "generate-serviceaccount-key could not use the private-key path")
		return 1
	}
	if !generated {
		fmt.Fprintln(stdout, "ServiceAccount key already exists; no changes made.")
		return 0
	}
	privateKey, _, _, err := encodeEnrollmentKeyPair(key)
	if err != nil || writePrivateKey(*path, privateKey) != nil {
		fmt.Fprintln(stderr, "generate-serviceaccount-key could not securely persist the private key")
		return 1
	}
	fmt.Fprintln(stdout, "ServiceAccount key created.")
	return 0
}
