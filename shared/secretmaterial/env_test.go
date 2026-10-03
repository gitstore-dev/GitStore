// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestEnvironmentReplacementProcess(t *testing.T) {
	const variable = "SECRET_TEST_PROCESS_RECORD"
	const child = "SECRET_TEST_REPLACEMENT_CHILD"
	read := func(want string) {
		t.Helper()
		provider, err := NewEnvironmentProvider(FormatJSONRecord, []EnvBinding{{
			Scope: Scope{Tier: TierRuntime, Environment: "dev", Namespace: "shop"},
			Name:  "catalog", Variable: variable,
		}})
		if err != nil {
			t.Fatal(err)
		}
		resolver := runtimeResolver(t, provider)
		ref := CredentialsRef{Kind: "CredentialsRef", Type: "aws-access-key/v1", SecretRef: reference()}
		err = consumeCredentials(t.Context(), resolver, ref, request(), func(material SecretMaterial) {
			if string(material.Value("accessKeyId")) != want {
				t.Error("process read an unexpected environment revision")
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv(child) == "1" {
		read("replacement")
		return
	}
	t.Setenv(variable, string(record(t, map[string]string{"accessKeyId": "original", "secretAccessKey": marker})))
	read("original")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestEnvironmentReplacementProcess$")
	cmd.Env = append(os.Environ(), child+"=1", variable+"="+string(record(t, map[string]string{
		"accessKeyId": "replacement", "secretAccessKey": marker,
	})))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("replacement process failed: %v\n%s", err, output)
	}
	read("original")
}

func TestEnvironmentBindingsAreExplicitAndInjective(t *testing.T) {
	scope := Scope{Tier: TierBootstrap}
	binding := EnvBinding{Scope: scope, Name: "catalog", Variable: "SECRET_TEST_RECORD"}
	t.Setenv(binding.Variable, string(record(t, map[string]string{"key": marker})))
	p, err := NewEnvironmentProvider(FormatJSONRecord, []EnvBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(context.Background(), reference(), scope); err != nil {
		t.Fatal(err)
	}
	foreign := Scope{Tier: TierRuntime, Environment: "dev", Namespace: "shop"}
	if _, err := p.Read(context.Background(), reference(), foreign); !errors.Is(err, ErrForbidden) {
		t.Fatal("binding crossed tiers")
	}
	other := binding
	other.Name = "other"
	if _, err := NewEnvironmentProvider(FormatJSONRecord, []EnvBinding{binding, other}); !errors.Is(err, ErrInvalidRef) {
		t.Fatal("noninjective variable mapping accepted")
	}
	other = binding
	other.Variable = "SECRET_TEST_OTHER"
	if _, err := NewEnvironmentProvider(FormatJSONRecord, []EnvBinding{binding, other}); !errors.Is(err, ErrInvalidRef) {
		t.Fatal("duplicate logical mapping accepted")
	}
	t.Setenv(binding.Variable, strings.Repeat("x", MaxEncodedBytes+1))
	if _, err := p.Read(context.Background(), reference(), scope); !errors.Is(err, ErrValueTooLarge) {
		t.Fatal("oversized environment value accepted")
	}
}

func TestLegacyEnvironmentMappingIsFrozenToConfiguredReference(t *testing.T) {
	ref := reference()
	ref.Name = "controller-manager"
	ref.Key = ptr("private_key.pem")
	name, err := BootstrapEnvironmentVariable("GITSTORE_SECRET__", ref)
	if err != nil {
		t.Fatal(err)
	}
	if name != "GITSTORE_SECRET__CONTROLLER_DASH_MANAGER__PRIVATE_UNDERSCORE_KEY_DOT_PEM" {
		t.Fatal("legacy mapping changed")
	}
	t.Setenv(name, marker)
	p, err := NewEnvironmentProvider(FormatRaw, []EnvBinding{{
		Scope: Scope{Tier: TierBootstrap}, Name: ref.Name, Key: *ref.Key, Variable: name,
	}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Read(context.Background(), ref, Scope{Tier: TierBootstrap})
	if err != nil || string(data) != marker {
		t.Fatal("legacy mapping changed")
	}
	ref.Key = ptr("PRIVATE_key.pem")
	if _, err := p.Read(context.Background(), ref, Scope{Tier: TierBootstrap}); !errors.Is(err, ErrForbidden) {
		t.Fatal("case-folded collision accessed configured binding")
	}
}
