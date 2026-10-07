// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
)

func parseRecord(data []byte, format Format, ref SecretRef) (result SecretMaterial, err error) {
	if len(data) > MaxEncodedBytes {
		return SecretMaterial{}, failure(ErrValueTooLarge, "encoded-bytes", nil)
	}
	material := SecretMaterial{values: make(map[string][]byte)}
	defer func() {
		if err != nil {
			material.Clear()
		}
	}()
	if format == FormatRaw {
		if ref.Key == nil || !validKey(*ref.Key) {
			return SecretMaterial{}, failure(ErrInvalidRef, "key", nil)
		}
		if len(data) > MaxItemBytes {
			return SecretMaterial{}, failure(ErrValueTooLarge, "item-bytes", nil)
		}
		if len(data) == 0 {
			return SecretMaterial{}, failure(ErrMissingKey, "item", nil)
		}
		material.format = string(FormatRaw)
		material.values[*ref.Key] = bytes.Clone(data)
		return material, nil
	}
	if format != FormatJSONRecord {
		return SecretMaterial{}, failure(ErrUnsupportedType, "format", nil)
	}
	envelope, err := object(data, map[string]bool{"format": true, "values": true}, 2)
	if err != nil {
		return SecretMaterial{}, err
	}
	if json.Unmarshal(envelope["format"], &material.format) != nil || material.format == "" {
		return SecretMaterial{}, failure(ErrInvalidRef, "format", nil)
	}
	if material.format != "secret-record/v1" && material.format != "serviceaccount-signing-key/v1" {
		return SecretMaterial{}, failure(ErrUnsupportedType, "record-format", nil)
	}
	values, err := object(envelope["values"], nil, MaxItems)
	if err != nil {
		return SecretMaterial{}, err
	}
	total := 0
	for key, raw := range values {
		if !validKey(key) {
			return SecretMaterial{}, failure(ErrInvalidRef, "item-name", nil)
		}
		var encoded string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &encoded) != nil {
			return SecretMaterial{}, failure(ErrInvalidRef, "encoding", nil)
		}
		value, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			clear(value)
			return SecretMaterial{}, failure(ErrInvalidRef, "encoding", nil)
		}
		total += len(value)
		if len(value) > MaxItemBytes || total > MaxDecodedBytes {
			clear(value)
			return SecretMaterial{}, failure(ErrValueTooLarge, "decoded-bytes", nil)
		}
		material.values[key] = value
	}
	if ref.Key != nil {
		value, exists := material.values[*ref.Key]
		if !exists || len(value) == 0 {
			return SecretMaterial{}, failure(ErrMissingKey, "item", nil)
		}
		for key, other := range material.values {
			if key != *ref.Key {
				clear(other)
				delete(material.values, key)
			}
		}
	}
	return material, nil
}
