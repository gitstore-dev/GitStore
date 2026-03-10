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

type Decimal struct {
	decimal.Decimal
}

// UnmarshalGQL implements the graphql.Unmarshaler interface found in gqlgen,
// allowing the type to be received by a graphql client and unmarshaled.
func (d *Decimal) UnmarshalGQL(v interface{}) error {
	switch value := v.(type) {
	case string:
		dec, err := decimal.NewFromString(value)
		if err != nil {
			return fmt.Errorf("invalid decimal value: %w", err)
		}
		d.Decimal = dec
	case float64:
		d.Decimal = decimal.NewFromFloat(value)
	case int:
		d.Decimal = decimal.NewFromInt(int64(value))
	case int64:
		d.Decimal = decimal.NewFromInt(value)
	default:
		return errors.New("invalid type for Decimal")
	}
	return nil
}

// MarshalGQL implements the graphql.Marshaler interface found in gqlgen,
// allowing the type to be marshaled by gqlgen and sent over the wire.
// This will convert the Decimal to a JSON number as a string.
func (d Decimal) MarshalGQL(w io.Writer) {
	io.WriteString(w, `"`+d.String()+`"`) // Wrap in quotes to ensure it's treated as a string
}

func MarshalDateTime(t time.Time) graphql.Marshaler {
	return graphql.MarshalTime(t)
}

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
