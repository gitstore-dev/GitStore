// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestValidationProtobufFieldNumbersAndKindsRemainStable(t *testing.T) {
	assertProtoField(t, (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor(), "trees", 2, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor(), "repository_id", 15, protoreflect.StringKind, false)
	assertProtoField(t, (&catalogv1.ValidateResourcesResponse{}).ProtoReflect().Descriptor(), "accepted", 1, protoreflect.BoolKind, false)
	assertProtoField(t, (&catalogv1.ValidateResourcesResponse{}).ProtoReflect().Descriptor(), "errors", 2, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ResourceValidationTree{}).ProtoReflect().Descriptor(), "old_blobs", 1, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ResourceValidationTree{}).ProtoReflect().Descriptor(), "proposed_blobs", 2, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourceDeletionsRequest{}).ProtoReflect().Descriptor(), "trees", 1, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourceDeletionsRequest{}).ProtoReflect().Descriptor(), "repository_id", 15, protoreflect.StringKind, false)

	errorDescriptor := (&catalogv1.ValidationError{}).ProtoReflect().Descriptor()
	assertProtoField(t, errorDescriptor, "file_path", 1, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "field", 2, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "constraint", 3, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "message", 4, protoreflect.StringKind, false)

	file := (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor().ParentFile()
	service := file.Services().ByName("CatalogService")
	require.NotNil(t, service)
	method := service.Methods().ByName("ValidateResourceDeletions")
	require.NotNil(t, method)
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
