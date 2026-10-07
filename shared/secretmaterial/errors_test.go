// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorsAndMaterialNeverFormatBytes(t *testing.T) {
	for _, class := range []error{ErrInvalidRef, ErrNotFound, ErrMissingKey, ErrForbidden, ErrProviderUnavailable, ErrUnsupportedType, ErrValueTooLarge} {
		err := failure(class, "record", fmt.Errorf("%s: %w", marker, context.Canceled))
		if !errors.Is(err, class) || !errors.Is(err, context.Canceled) {
			t.Fatal("lost class or cancellation")
		}
		for _, format := range []string{"%s", "%v", "%+v", "%#v", "%q"} {
			if strings.Contains(fmt.Sprintf(format, err), marker) {
				t.Fatal("error leaked its cause")
			}
		}
	}
	material := SecretMaterial{values: map[string][]byte{"privateKey": []byte(marker)}}
	for _, value := range []any{material, &material, []SecretMaterial{material}, map[string]any{"material": material}} {
		for _, format := range []string{"%s", "%v", "%+v", "%#v", "%q"} {
			if strings.Contains(fmt.Sprintf(format, value), marker) || strings.Contains(fmt.Sprintf(format, value), "115 121 110") {
				t.Fatal("formatted material leaked")
			}
		}
		if data, err := json.Marshal(value); err == nil || strings.Contains(string(data), marker) {
			t.Fatal("material serialization did not fail closed")
		}
	}
	if _, err := material.MarshalYAML(); !errors.Is(err, ErrForbidden) {
		t.Fatal("YAML serialization must fail closed")
	}
	value := material.Value("privateKey")
	value[0] = 'x'
	if string(material.Value("privateKey")) != marker {
		t.Fatal("value accessor aliases owned material")
	}
	values := material.Values()
	values["privateKey"][0] = 'y'
	if string(material.Value("privateKey")) != marker {
		t.Fatal("map accessor aliases owned material")
	}
	material.Clear()
	if len(material.Values()) != 0 {
		t.Fatal("material retained after clear")
	}
}
