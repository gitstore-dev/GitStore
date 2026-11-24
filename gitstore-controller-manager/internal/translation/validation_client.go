// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	contract "github.com/gitstore-dev/gitstore/translation"
)

const translationValidationSnapshotQuery = `
query($namespace: String!, $name: String!, $sourceID: ID!, $sourceVersion: String!) {
  translationValidationSnapshot(namespace: $namespace, name: $name, sourceID: $sourceID, sourceVersion: $sourceVersion) {
    sourceID sourceVersion sourceDigest namespaceUID
    binding { ` + bindingFields + ` }
    observation { ` + translationCommonStatusFields + ` resolved }
    target {
      namespace
      evidence { apiVersion kind namespace name uid generation revision resourceVersion contentDigest }
      fields
    }
  }
}`

const translationTargetObservationQuery = `
query($namespace: String!, $apiVersion: String!, $kind: String!, $name: String!) {
  translationTargetObservation(namespace: $namespace, apiVersion: $apiVersion, kind: $kind, name: $name) {
    namespaceUID
    evidence { apiVersion kind namespace name uid generation revision resourceVersion contentDigest }
  }
}`

var _ ValidationClient = (*GraphQLClient)(nil)

func (c *GraphQLClient) ObserveTarget(ctx context.Context, namespace, apiVersion, kind, name string) (*TargetObservation, error) {
	if namespace == "" || name == "" || len(namespace) > 2048 || len(name) > 2048 {
		return nil, errors.New("translation: incomplete target observation request")
	}
	if _, err := contract.DescriptorFor(apiVersion, kind); err != nil {
		return nil, err
	}
	var response struct {
		Observation *struct {
			NamespaceUID string              `json:"namespaceUID"`
			Evidence     *validationEvidence `json:"evidence"`
		} `json:"translationTargetObservation"`
	}
	if err := c.client.Query(ctx, translationTargetObservationQuery, map[string]any{
		"namespace": namespace, "apiVersion": apiVersion, "kind": kind, "name": name,
	}, &response); err != nil {
		return nil, clientError(err)
	}
	wire := response.Observation
	if wire == nil || wire.Evidence == nil {
		return nil, errors.New("translation: missing target observation")
	}
	evidence := wire.Evidence
	target := DependencyTarget{NamespaceUID: wire.NamespaceUID, APIVersion: evidence.APIVersion,
		Kind: evidence.Kind, Name: evidence.Name, UID: evidence.UID}
	if validateDependencyTarget(target) != nil || target.APIVersion != apiVersion || target.Kind != kind ||
		target.Name != name || evidence.Namespace != namespace || evidence.UID == "" || evidence.Generation < 1 ||
		evidence.ResourceVersion == "" || len(evidence.ResourceVersion) > 2048 ||
		evidence.ContentDigest == "" || len(evidence.ContentDigest) > 256 {
		return nil, errors.New("translation: contradictory target observation")
	}
	return &TargetObservation{Target: target, ResourceVersion: evidence.ResourceVersion,
		Generation: evidence.Generation, Revision: evidence.Revision, ContentDigest: evidence.ContentDigest}, nil
}

func (c *GraphQLClient) ValidationSnapshot(ctx context.Context, key types.WorkItemKey, sourceID, sourceVersion string) (*ValidationSnapshot, error) {
	if key.Kind != "Translation" || key.Namespace == "" || key.Name == "" || sourceID == "" || sourceVersion == "" {
		return nil, errors.New("translation: incomplete validation snapshot request")
	}
	var response struct {
		Snapshot *struct {
			SourceID      string                 `json:"sourceID"`
			SourceVersion string                 `json:"sourceVersion"`
			SourceDigest  string                 `json:"sourceDigest"`
			NamespaceUID  string                 `json:"namespaceUID"`
			Binding       *BindingState          `json:"binding"`
			Target        json.RawMessage        `json:"target"`
			Observation   *status.ResourceStatus `json:"observation"`
		} `json:"translationValidationSnapshot"`
	}
	if err := c.client.Query(ctx, translationValidationSnapshotQuery, map[string]any{
		"namespace": key.Namespace, "name": key.Name, "sourceID": sourceID, "sourceVersion": sourceVersion,
	}, &response); err != nil {
		return nil, clientError(err)
	}
	wire := response.Snapshot
	if wire == nil || wire.SourceID != sourceID || wire.SourceVersion != sourceVersion ||
		wire.SourceDigest == "" || wire.NamespaceUID == "" || wire.Binding.validate() != nil ||
		wire.Binding.TranslationID != sourceID || wire.Binding.SourceVersion != sourceVersion ||
		wire.Binding.NamespaceUID != wire.NamespaceUID || len(wire.Target) == 0 {
		return nil, errors.New("translation: contradictory validation snapshot")
	}
	result := &ValidationSnapshot{
		SourceID: wire.SourceID, SourceVersion: wire.SourceVersion, SourceDigest: wire.SourceDigest,
		NamespaceUID: wire.NamespaceUID, Binding: wire.Binding, Observation: wire.Observation,
	}
	if result.Observation != nil {
		if err := validateConditions(result.Observation.Conditions); err != nil {
			return nil, err
		}
		result.Observation.ResourceVersion = wire.SourceVersion
	}
	if string(wire.Target) == "null" {
		return result, nil
	}
	var target struct {
		Namespace string             `json:"namespace"`
		Evidence  validationEvidence `json:"evidence"`
		Fields    json.RawMessage    `json:"fields"`
	}
	if json.Unmarshal(wire.Target, &target) != nil || len(target.Fields) > 8*contract.MaxTargetBytes {
		return nil, errors.New("translation: invalid validation target")
	}
	evidence := target.Evidence
	if target.Namespace != key.Namespace || evidence.Namespace != key.Namespace || evidence.Name == "" ||
		evidence.UID == "" || evidence.Generation < 1 || evidence.ResourceVersion == "" || evidence.ContentDigest == "" {
		return nil, errors.New("translation: incomplete validation target evidence")
	}
	if _, err := contract.DescriptorFor(evidence.APIVersion, evidence.Kind); err != nil {
		return nil, err
	}
	var fields map[string]any
	if json.Unmarshal(target.Fields, &fields) != nil || fields == nil {
		return nil, errors.New("translation: incomplete validation target fields")
	}
	result.Target = &ValidationTarget{
		Namespace: target.Namespace, Fields: fields,
		Observation: TargetObservation{
			Target: DependencyTarget{
				NamespaceUID: wire.NamespaceUID, APIVersion: evidence.APIVersion, Kind: evidence.Kind,
				Name: evidence.Name, UID: evidence.UID,
			},
			Generation: evidence.Generation, ResourceVersion: evidence.ResourceVersion,
			Revision: evidence.Revision, ContentDigest: evidence.ContentDigest,
		},
	}
	return result, nil
}
