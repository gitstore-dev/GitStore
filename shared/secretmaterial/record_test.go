// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRecordClosedEnvelope(t *testing.T) {
	for _, raw := range []string{
		`null`, `{}`, `[]`,
		`{"format":"secret-record/v1","values":null}`,
		`{"format":"secret-record/v1","values":{},"extra":true}`,
		`{"format":"secret-record/v1","format":"secret-record/v1","values":{}}`,
		`{"format":"secret-record/v1","values":{"x":"eA==","x":"eQ=="}}`,
		`{"format":"secret-record/v1","values":{"x":null}}`,
		`{"format":"secret-record/v1","values":{"x":"!"}}`,
		`{"format":"secret-record/v1","values":{"../x":"eA=="}}`,
		`{"format":"secret-record/v1","values":{}} {}`,
	} {
		if m, err := parseRecord([]byte(raw), FormatJSONRecord, reference()); !errors.Is(err, ErrInvalidRef) || len(m.Values()) != 0 {
			t.Errorf("invalid envelope accepted: %v", err)
		}
	}
	if _, err := parseRecord([]byte(`{"format":"future/v1","values":{}}`), FormatJSONRecord, reference()); !errors.Is(err, ErrUnsupportedType) {
		t.Fatal(err)
	}
	data := record(t, map[string]string{"first": marker, "second": "value"})
	ref := reference()
	m, err := parseRecord(data, FormatJSONRecord, ref)
	if err != nil || len(m.Values()) != 2 || m.RecordFormat() != "secret-record/v1" {
		t.Fatal("whole record not returned")
	}
	ref.Key = ptr("first")
	m, err = parseRecord(data, FormatJSONRecord, ref)
	if err != nil || len(m.Values()) != 1 || string(m.Value("first")) != marker {
		t.Fatal("item selection not honored")
	}
	ref.Key = ptr("absent")
	if m, err := parseRecord(data, FormatJSONRecord, ref); !errors.Is(err, ErrMissingKey) || len(m.Values()) != 0 {
		t.Fatal("missing key returned material")
	}
}

func TestRecordLimits(t *testing.T) {
	ref := reference()
	for _, n := range []int{MaxItemBytes, MaxItemBytes + 1} {
		data := record(t, map[string]string{"key": strings.Repeat("x", n)})
		_, err := parseRecord(data, FormatJSONRecord, ref)
		if (err == nil) != (n == MaxItemBytes) {
			t.Fatal("item bound violated")
		}
		ref.Key = ptr("key")
		_, err = parseRecord([]byte(strings.Repeat("x", n)), FormatRaw, ref)
		if (err == nil) != (n == MaxItemBytes) {
			t.Fatal("raw item bound violated")
		}
		ref.Key = nil
	}
	for _, n := range []int{MaxItems, MaxItems + 1} {
		values := make(map[string]string)
		for i := 0; i < n; i++ {
			values[fmt.Sprintf("key%d", i)] = "x"
		}
		_, err := parseRecord(record(t, values), FormatJSONRecord, ref)
		if (err == nil) != (n == MaxItems) {
			t.Fatal("item count bound violated")
		}
	}
	values := map[string]string{"a": strings.Repeat("x", MaxItemBytes), "b": strings.Repeat("x", MaxItemBytes)}
	if _, err := parseRecord(record(t, values), FormatJSONRecord, ref); err != nil {
		t.Fatal("exact aggregate bound rejected")
	}
	values["c"] = "x"
	if _, err := parseRecord(record(t, values), FormatJSONRecord, ref); !errors.Is(err, ErrValueTooLarge) {
		t.Fatal("aggregate bound violated")
	}
	data := record(t, map[string]string{"key": "x"})
	data = append(data, []byte(strings.Repeat(" ", MaxEncodedBytes-len(data)))...)
	if _, err := parseRecord(data, FormatJSONRecord, ref); err != nil {
		t.Fatal("exact encoded bound rejected")
	}
	if _, err := parseRecord(append(data, ' '), FormatJSONRecord, ref); !errors.Is(err, ErrValueTooLarge) {
		t.Fatal("encoded bound violated")
	}
	if _, err := parseRecord([]byte(marker), FormatRaw, ref); !errors.Is(err, ErrInvalidRef) {
		t.Fatal("ambiguous whole raw record accepted")
	}
	ref.Key = ptr("key")
	if _, err := parseRecord(nil, FormatRaw, ref); !errors.Is(err, ErrMissingKey) {
		t.Fatal("empty raw item accepted")
	}
}
