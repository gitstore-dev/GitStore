// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"fmt"
	"io"
)

func (m SecretMaterial) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "SecretMaterial([REDACTED])")
}

func (m SecretMaterial) MarshalJSON() ([]byte, error) {
	return nil, failure(ErrForbidden, "serialization", nil)
}

func (m SecretMaterial) MarshalYAML() (any, error) {
	return nil, failure(ErrForbidden, "serialization", nil)
}

func (m SecretMaterial) Value(key string) []byte {
	return append([]byte(nil), m.values[key]...)
}

func (m SecretMaterial) Values() map[string][]byte {
	values := make(map[string][]byte, len(m.values))
	for key := range m.values {
		values[key] = m.Value(key)
	}
	return values
}

func (m SecretMaterial) RecordFormat() string { return m.format }

func (m *SecretMaterial) Clear() {
	for key, value := range m.values {
		clear(value)
		delete(m.values, key)
	}
	m.values = nil
	m.format = ""
}
