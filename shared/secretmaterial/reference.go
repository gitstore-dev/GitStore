// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	keyPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	typePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*/v[1-9][0-9]*$`)
)

func validName(s string) bool {
	return len(s) >= 1 && len(s) <= 63 && namePattern.MatchString(s)
}

func validKey(s string) bool {
	return len(s) >= 1 && len(s) <= 253 && s != "." && s != ".." && keyPattern.MatchString(s)
}

func ValidateSecretRef(ref SecretRef, namespace string) error {
	if ref.Kind != "SecretRef" {
		return failure(ErrInvalidRef, "kind", nil)
	}
	if !validName(ref.Name) {
		return failure(ErrInvalidRef, "name", nil)
	}
	if ref.Key != nil && !validKey(*ref.Key) {
		return failure(ErrInvalidRef, "key", nil)
	}
	if ref.Namespace != nil && (!validName(*ref.Namespace) || *ref.Namespace != namespace) {
		return failure(ErrInvalidRef, "namespace", nil)
	}
	return nil
}

func ValidateCredentialsRef(ref CredentialsRef, namespace string) error {
	if ref.Kind != "CredentialsRef" {
		return failure(ErrInvalidRef, "kind", nil)
	}
	if len(ref.Type) > 128 || !typePattern.MatchString(ref.Type) {
		return failure(ErrInvalidRef, "type", nil)
	}
	return ValidateSecretRef(ref.SecretRef, namespace)
}

// Decoder.DisallowUnknownFields alone does not reject duplicate or case-folded
// properties. Decode exact keys before binding to the public structs.
func object(data []byte, allowed map[string]bool, limit int) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, failure(ErrInvalidRef, "object", nil)
	}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, failure(ErrInvalidRef, "object", nil)
		}
		key, ok := token.(string)
		if !ok || (allowed != nil && !allowed[key]) {
			return nil, failure(ErrInvalidRef, "field", nil)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, failure(ErrInvalidRef, "duplicate-field", nil)
		}
		if len(values) == limit {
			return nil, failure(ErrValueTooLarge, "items", nil)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, failure(ErrInvalidRef, "value", nil)
		}
		values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, failure(ErrInvalidRef, "object", nil)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, failure(ErrInvalidRef, "trailing-data", nil)
	}
	return values, nil
}

func (ref *SecretRef) UnmarshalJSON(data []byte) error {
	*ref = SecretRef{}
	values, err := object(data, map[string]bool{"kind": true, "name": true, "key": true, "namespace": true}, 4)
	if err != nil {
		return err
	}
	var result SecretRef
	if json.Unmarshal(values["kind"], &result.Kind) != nil || json.Unmarshal(values["name"], &result.Name) != nil {
		return failure(ErrInvalidRef, "reference", nil)
	}
	for field, target := range map[string]**string{"key": &result.Key, "namespace": &result.Namespace} {
		if raw, ok := values[field]; ok {
			var value string
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || value == "" {
				return failure(ErrInvalidRef, field, nil)
			}
			*target = &value
		}
	}
	namespace := ""
	if result.Namespace != nil {
		namespace = *result.Namespace
	}
	if err := ValidateSecretRef(result, namespace); err != nil {
		return err
	}
	*ref = result
	return nil
}

func (ref *CredentialsRef) UnmarshalJSON(data []byte) error {
	*ref = CredentialsRef{}
	values, err := object(data, map[string]bool{"kind": true, "type": true, "secretRef": true}, 3)
	if err != nil {
		return err
	}
	var result CredentialsRef
	if json.Unmarshal(values["kind"], &result.Kind) != nil || json.Unmarshal(values["type"], &result.Type) != nil ||
		json.Unmarshal(values["secretRef"], &result.SecretRef) != nil {
		return failure(ErrInvalidRef, "credentialsRef", nil)
	}
	namespace := ""
	if result.SecretRef.Namespace != nil {
		namespace = *result.SecretRef.Namespace
	}
	if err := ValidateCredentialsRef(result, namespace); err != nil {
		return err
	}
	*ref = result
	return nil
}
