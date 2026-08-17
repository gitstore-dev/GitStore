// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scalar

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalLong(t *testing.T) {
	for _, value := range []int64{math.MinInt64, -1, 0, 52_428_800, math.MaxInt64} {
		t.Run(json.Number(valueString(value)).String(), func(t *testing.T) {
			var output bytes.Buffer
			MarshalLong(value).MarshalGQL(&output)
			assert.Equal(t, valueString(value), output.String())
		})
	}
}

func TestUnmarshalLongAcceptsSigned64BitIntegers(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  int64
	}{
		{name: "minimum", input: json.Number("-9223372036854775808"), want: math.MinInt64},
		{name: "negative", input: json.Number("-1"), want: -1},
		{name: "zero", input: json.Number("0"), want: 0},
		{name: "normal byte size", input: json.Number("52428800"), want: 52_428_800},
		{name: "maximum", input: json.Number("9223372036854775807"), want: math.MaxInt64},
		{name: "int", input: 10, want: 10},
		{name: "int64", input: int64(11), want: 11},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UnmarshalLong(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUnmarshalLongRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{name: "overflow", input: json.Number("9223372036854775808")},
		{name: "underflow", input: json.Number("-9223372036854775809")},
		{name: "fraction", input: json.Number("1.5")},
		{name: "float", input: float64(1)},
		{name: "string", input: "42"},
		{name: "boolean", input: true},
		{name: "nil", input: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := UnmarshalLong(tt.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "Long")
		})
	}
}

func valueString(value int64) string {
	return strconv.FormatInt(value, 10)
}
