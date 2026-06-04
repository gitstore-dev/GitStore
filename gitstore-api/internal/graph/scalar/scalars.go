// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scalar

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/shopspring/decimal"
)

// MarshalDecimal serializes a Decimal value as a quoted string GraphQL scalar.
func MarshalDecimal(d decimal.Decimal) graphql.Marshaler {
	return graphql.WriterFunc(func(w io.Writer) {
		_, _ = io.WriteString(w, `"`+d.String()+`"`)
	})
}

// UnmarshalDecimal parses supported GraphQL Decimal input values.
func UnmarshalDecimal(v interface{}) (decimal.Decimal, error) {
	var d decimal.Decimal
	switch value := v.(type) {
	case string:
		dec, err := decimal.NewFromString(value)
		if err != nil {
			return d, fmt.Errorf("invalid decimal value: %w", err)
		}
		d = dec
	case float64:
		d = decimal.NewFromFloat(value)
	case int:
		d = decimal.NewFromInt(int64(value))
	case int64:
		d = decimal.NewFromInt(value)
	case json.Number:
		dec, err := decimal.NewFromString(value.String())
		if err != nil {
			return d, fmt.Errorf("invalid decimal value: %w", err)
		}
		d = dec
	default:
		return d, errors.New("invalid type for Decimal")
	}
	return d, nil
}

// MarshalDateTime serializes a time.Time value as a GraphQL DateTime scalar.
func MarshalDateTime(t time.Time) graphql.Marshaler {
	return graphql.MarshalTime(t)
}

// UnmarshalDateTime parses supported GraphQL DateTime input values.
func UnmarshalDateTime(v interface{}) (time.Time, error) {
	switch value := v.(type) {
	case time.Time:
		return value, nil
	case string:
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid DateTime value %q: %w", value, err)
		}
		return t, nil
	default:
		return time.Time{}, fmt.Errorf("invalid type for DateTime: %T", v)
	}
}

// MarshalJSON serializes a JSON object scalar value.
func MarshalJSON(j map[string]interface{}) graphql.Marshaler {
	return graphql.WriterFunc(func(w io.Writer) {
		payload, err := json.Marshal(j)
		if err != nil {
			_, _ = io.WriteString(w, "null")
			return
		}

		_, _ = w.Write(payload)
	})
}

// UnmarshalJSON parses supported GraphQL JSON scalar input values.
func UnmarshalJSON(v interface{}) (map[string]interface{}, error) {
	switch value := v.(type) {
	case nil:
		return nil, nil
	case map[string]interface{}:
		return value, nil
	case string:
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			return nil, fmt.Errorf("invalid JSON string: %w", err)
		}
		return parsed, nil
	case []byte:
		var parsed map[string]interface{}
		if err := json.Unmarshal(value, &parsed); err != nil {
			return nil, fmt.Errorf("invalid JSON bytes: %w", err)
		}
		return parsed, nil
	default:
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("invalid type for JSON scalar: %T", v)
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal(payload, &parsed); err != nil {
			return nil, fmt.Errorf("JSON scalar must be an object: %w", err)
		}

		return parsed, nil
	}
}
