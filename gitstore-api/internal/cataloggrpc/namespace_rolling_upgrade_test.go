// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"fmt"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestNamespaceValidationProtobufKeepsLegacyFieldNumbersAndKinds(t *testing.T) {
	assertProtoField(t, (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor(), "blobs", 1, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor(), "repository_id", 15, protoreflect.StringKind, false)
	assertProtoField(t, (&catalogv1.ValidateResourcesResponse{}).ProtoReflect().Descriptor(), "accepted", 1, protoreflect.BoolKind, false)
	assertProtoField(t, (&catalogv1.ValidateResourcesResponse{}).ProtoReflect().Descriptor(), "errors", 2, protoreflect.MessageKind, true)

	errorDescriptor := (&catalogv1.ValidationError{}).ProtoReflect().Descriptor()
	assertProtoField(t, errorDescriptor, "file_path", 1, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "field", 2, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "constraint", 3, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "message", 4, protoreflect.StringKind, false)
}

func TestLegacyGitServiceConsumerDecodesNamespaceValidationResponse(t *testing.T) {
	wire, err := proto.Marshal(&catalogv1.ValidateResourcesResponse{
		Accepted: false,
		Errors: []*catalogv1.ValidationError{{
			FilePath:   "namespaces/acme.md",
			Field:      "spec.tier",
			Constraint: "policy/tier-demotion",
			Message:    "namespace tier demotion is not allowed",
		}},
	})
	require.NoError(t, err)

	accepted, errors, err := consumeLegacyValidationResponse(wire)
	require.NoError(t, err)
	assert.False(t, accepted)
	require.Len(t, errors, 1)
	assert.Equal(t, legacyValidationError{
		FilePath:   "namespaces/acme.md",
		Field:      "spec.tier",
		Constraint: "policy/tier-demotion",
		Message:    "namespace tier demotion is not allowed",
	}, errors[0])
}

func TestCurrentAPIAcceptsLegacyValidationRequestShape(t *testing.T) {
	blobWire, err := proto.Marshal(namespaceBlob("namespaces/acme.md", "acme", "USER"))
	require.NoError(t, err)
	var legacyWire []byte
	legacyWire = protowire.AppendTag(legacyWire, 1, protowire.BytesType)
	legacyWire = protowire.AppendBytes(legacyWire, blobWire)
	legacyWire = protowire.AppendTag(legacyWire, 15, protowire.BytesType)
	legacyWire = protowire.AppendString(legacyWire, testRepoID)

	var request catalogv1.ValidateResourcesRequest
	require.NoError(t, proto.Unmarshal(legacyWire, &request))
	assert.Equal(t, testRepoID, request.RepositoryId)
	require.Len(t, request.Blobs, 1)
	assert.Equal(t, "namespaces/acme.md", request.Blobs[0].Path)
	assert.Empty(t, request.Trees)
}

func assertProtoField(
	t *testing.T,
	message protoreflect.MessageDescriptor,
	name protoreflect.Name,
	number protoreflect.FieldNumber,
	kind protoreflect.Kind,
	repeated bool,
) {
	t.Helper()
	field := message.Fields().ByName(name)
	require.NotNil(t, field, "%s.%s must remain present", message.FullName(), name)
	assert.Equal(t, number, field.Number())
	assert.Equal(t, kind, field.Kind())
	assert.Equal(t, repeated, field.Cardinality() == protoreflect.Repeated)
}

type legacyValidationError struct {
	FilePath   string
	Field      string
	Constraint string
	Message    string
}

func consumeLegacyValidationResponse(wire []byte) (bool, []legacyValidationError, error) {
	var accepted bool
	var errors []legacyValidationError
	for len(wire) > 0 {
		number, wireType, n := protowire.ConsumeTag(wire)
		if n < 0 {
			return false, nil, protowire.ParseError(n)
		}
		wire = wire[n:]
		switch number {
		case 1:
			value, consumed := protowire.ConsumeVarint(wire)
			if consumed < 0 {
				return false, nil, protowire.ParseError(consumed)
			}
			accepted = value != 0
			wire = wire[consumed:]
		case 2:
			value, consumed := protowire.ConsumeBytes(wire)
			if consumed < 0 {
				return false, nil, protowire.ParseError(consumed)
			}
			parsed, err := consumeLegacyValidationError(value)
			if err != nil {
				return false, nil, err
			}
			errors = append(errors, parsed)
			wire = wire[consumed:]
		default:
			consumed := protowire.ConsumeFieldValue(number, wireType, wire)
			if consumed < 0 {
				return false, nil, protowire.ParseError(consumed)
			}
			wire = wire[consumed:]
		}
	}
	return accepted, errors, nil
}

func consumeLegacyValidationError(wire []byte) (legacyValidationError, error) {
	var result legacyValidationError
	values := []*string{&result.FilePath, &result.Field, &result.Constraint, &result.Message}
	for len(wire) > 0 {
		number, wireType, n := protowire.ConsumeTag(wire)
		if n < 0 {
			return result, protowire.ParseError(n)
		}
		wire = wire[n:]
		if number >= 1 && number <= 4 && wireType == protowire.BytesType {
			value, consumed := protowire.ConsumeString(wire)
			if consumed < 0 {
				return result, protowire.ParseError(consumed)
			}
			*values[number-1] = value
			wire = wire[consumed:]
			continue
		}
		consumed := protowire.ConsumeFieldValue(number, wireType, wire)
		if consumed < 0 {
			return result, fmt.Errorf("consume legacy validation error: %w", protowire.ParseError(consumed))
		}
		wire = wire[consumed:]
	}
	return result, nil
}
