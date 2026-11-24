// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const translationCommonStatusFields = `
    observedGeneration lastAppliedRevision
    conditions { type status observedGeneration lastTransitionTime reason message }
`

const translationStatusFields = translationCommonStatusFields + `
    resolved {
      canonicalLocale validatedFieldPaths
      target { apiVersion kind namespace name uid generation revision resourceVersion contentDigest }
    }
`

const GraphQLFields = `
  metadata {
    uid name namespace resourceVersion generation revision
    creationTimestamp finalizers deletionTimestamp
  }
  spec {
    locale
    targetRef { apiVersion kind namespace name uid resourceVersion }
    patches { fieldPath translation }
  }
  body
  status { ` + translationStatusFields + ` }
`

const translationQuery = `
query($namespace: String!, $name: String!) {
  translation(by: {namespacePath: {namespace: $namespace, name: $name}}) {
` + GraphQLFields + `
  }
}`

const translationStatusMutation = `
mutation($input: UpdateTranslationStatusInput!) {
  updateTranslationStatus(input: $input) {
    translation { metadata { resourceVersion } }
  }
}`

const translationByIDQuery = `
query($id: ID!) {
  translation(by: {id: $id}) {
` + GraphQLFields + `
  }
}`

const bindingFields = `translationID sourceVersion bindingVersion referenceEpoch namespaceUID boundUID`

const translationBindingQuery = `
query($namespace: String!, $name: String!) {
  translationBinding(namespace: $namespace, name: $name) { ` + bindingFields + ` }
}`

const bindTranslationTargetMutation = `
mutation($input: BindTranslationTargetInput!) {
  bindTranslationTarget(input: $input) { binding { ` + bindingFields + ` } }
}`

const scanTranslationDependentsQuery = `
query($target: TranslationDependencyInput!, $limit: Int!, $continuation: String) {
  scanTranslationDependents(target: $target, limit: $limit, continuation: $continuation) {
    items { translationID sourceVersion referenceEpoch }
    continuation
  }
}`

type BindingState struct {
	TranslationID  string  `json:"translationID"`
	SourceVersion  string  `json:"sourceVersion"`
	BindingVersion string  `json:"bindingVersion"`
	ReferenceEpoch string  `json:"referenceEpoch"`
	NamespaceUID   string  `json:"namespaceUID"`
	BoundUID       *string `json:"boundUID"`
}

func (s *BindingState) validate() error {
	if s == nil || s.TranslationID == "" || s.SourceVersion == "" || s.BindingVersion == "" || s.ReferenceEpoch == "" || s.NamespaceUID == "" {
		return errors.New("translation: incomplete binding response")
	}
	return nil
}

type DependentHint struct {
	TranslationID  string `json:"translationID"`
	SourceVersion  string `json:"sourceVersion"`
	ReferenceEpoch string `json:"referenceEpoch"`
}

type DependentScan struct {
	Items        []DependentHint `json:"items"`
	Continuation string          `json:"continuation"`
}

type Metadata struct {
	UID               string     `json:"uid"`
	Name              string     `json:"name"`
	Namespace         string     `json:"namespace"`
	ResourceVersion   string     `json:"resourceVersion"`
	Generation        int64      `json:"generation"`
	Revision          string     `json:"revision"`
	CreationTimestamp time.Time  `json:"creationTimestamp"`
	Finalizers        []string   `json:"finalizers"`
	DeletionTimestamp *time.Time `json:"deletionTimestamp"`
}

type Resource struct {
	Metadata Metadata              `json:"metadata"`
	Spec     json.RawMessage       `json:"spec"`
	Body     string                `json:"body"`
	Status   status.ResourceStatus `json:"status"`
}

func (r *Resource) UnmarshalJSON(data []byte) error {
	type wire Resource
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := validateConditions(decoded.Status.Conditions); err != nil {
		return err
	}
	decoded.Status.ResourceVersion = decoded.Metadata.ResourceVersion
	*r = Resource(decoded)
	return nil
}

func (r Resource) Validate() error {
	if r.Metadata.UID == "" || r.Metadata.Namespace == "" || r.Metadata.Name == "" ||
		r.Metadata.ResourceVersion == "" || r.Metadata.Generation < 1 ||
		len(r.Spec) == 0 || string(r.Spec) == "null" {
		return errors.New("translation: incomplete source response")
	}
	return nil
}

func validateConditions(conditions []*status.Condition) error {
	for _, condition := range conditions {
		if condition == nil {
			return errors.New("translation: nil condition")
		}
		switch condition.Status {
		case "TRUE", "FALSE", "UNKNOWN":
		default:
			return errors.New("translation: invalid GraphQL condition status")
		}
	}
	return nil
}

type GraphQLClient struct{ client *graphqlclient.Client }

func NewGraphQLClient(client *graphqlclient.Client) *GraphQLClient {
	return &GraphQLClient{client: client}
}

func (c *GraphQLClient) Get(ctx context.Context, namespace, name string) (*Resource, error) {
	if namespace == "" || name == "" {
		return nil, errors.New("translation: missing source identity")
	}
	var response struct {
		Translation *Resource `json:"translation"`
	}
	if err := c.client.Query(ctx, translationQuery, map[string]any{"namespace": namespace, "name": name}, &response); err != nil {
		return nil, clientError(err)
	}
	if response.Translation == nil {
		return nil, types.ErrNotFound
	}
	resource := response.Translation
	if err := resource.Validate(); err != nil {
		return nil, err
	}
	if resource.Metadata.Namespace != namespace || resource.Metadata.Name != name {
		return nil, errors.New("translation: source response identity mismatch")
	}
	return resource, nil
}

func (c *GraphQLClient) GetBinding(ctx context.Context, namespace, name string) (*BindingState, error) {
	if namespace == "" || name == "" {
		return nil, errors.New("translation: missing binding source identity")
	}
	var response struct {
		Binding *BindingState `json:"translationBinding"`
	}
	if err := c.client.Query(ctx, translationBindingQuery, map[string]any{"namespace": namespace, "name": name}, &response); err != nil {
		return nil, clientError(err)
	}
	if err := response.Binding.validate(); err != nil {
		return nil, err
	}
	return response.Binding, nil
}

func (c *GraphQLClient) GetByID(ctx context.Context, id string) (*Resource, error) {
	if id == "" {
		return nil, errors.New("translation: missing source ID")
	}
	var response struct {
		Translation *Resource `json:"translation"`
	}
	if err := c.client.Query(ctx, translationByIDQuery, map[string]any{"id": id}, &response); err != nil {
		return nil, clientError(err)
	}
	if response.Translation == nil {
		return nil, types.ErrNotFound
	}
	resource := response.Translation
	if err := resource.Validate(); err != nil {
		return nil, err
	}
	if resource.Metadata.UID != id {
		return nil, errors.New("translation: source ID response mismatch")
	}
	return resource, nil
}

func (c *GraphQLClient) BindTarget(ctx context.Context, namespace, name string, observed *BindingState, targetUID string) (*BindingState, error) {
	if namespace == "" || name == "" || targetUID == "" {
		return nil, errors.New("translation: incomplete binding request")
	}
	if err := observed.validate(); err != nil {
		return nil, err
	}
	input := map[string]any{
		"namespace": namespace, "name": name, "translationID": observed.TranslationID,
		"sourceVersion": observed.SourceVersion, "bindingVersion": observed.BindingVersion,
		"referenceEpoch": observed.ReferenceEpoch, "targetUID": targetUID,
	}
	var response struct {
		Payload *struct {
			Binding *BindingState `json:"binding"`
		} `json:"bindTranslationTarget"`
	}
	if err := c.client.Mutate(ctx, bindTranslationTargetMutation, map[string]any{"input": input}, &response); err != nil {
		return nil, clientError(err)
	}
	if response.Payload == nil {
		return nil, errors.New("translation: missing binding payload")
	}
	bound := response.Payload.Binding
	if err := bound.validate(); err != nil {
		return nil, err
	}
	if bound.TranslationID != observed.TranslationID || bound.SourceVersion != observed.SourceVersion ||
		bound.ReferenceEpoch != observed.ReferenceEpoch || bound.NamespaceUID != observed.NamespaceUID ||
		bound.BoundUID == nil || *bound.BoundUID != targetUID {
		return nil, errors.New("translation: binding acknowledgement changed identity")
	}
	return bound, nil
}

func (c *GraphQLClient) ScanDependents(ctx context.Context, namespace string, target DependencyTarget, continuation string, limit int) (DependentScan, error) {
	if err := validateDependencyTarget(target); err != nil {
		return DependentScan{}, err
	}
	if namespace == "" || limit < 1 || limit > 256 || len(continuation) > 32<<10 {
		return DependentScan{}, errors.New("translation: invalid dependent scan request")
	}
	input := map[string]any{"namespace": namespace, "namespaceUID": target.NamespaceUID,
		"apiVersion": target.APIVersion, "kind": target.Kind, "name": target.Name}
	if target.UID != "" {
		input["uid"] = target.UID
	}
	variables := map[string]any{"target": input, "limit": limit}
	if continuation != "" {
		variables["continuation"] = continuation
	}
	var response struct {
		Scan *DependentScan `json:"scanTranslationDependents"`
	}
	if err := c.client.Query(ctx, scanTranslationDependentsQuery, variables, &response); err != nil {
		return DependentScan{}, clientError(err)
	}
	scan := response.Scan
	if scan == nil || len(scan.Items) > limit || len(scan.Continuation) > 32<<10 || scan.Continuation != "" && scan.Continuation == continuation {
		return DependentScan{}, errors.New("translation: invalid dependent scan response")
	}
	for _, hint := range scan.Items {
		if hint.TranslationID == "" || hint.SourceVersion == "" || hint.ReferenceEpoch == "" {
			return DependentScan{}, errors.New("translation: incomplete dependent hint")
		}
	}
	return *scan, nil
}

func (c *GraphQLClient) Apply(ctx context.Context, key types.WorkItemKey, patch *status.StatusPatch) error {
	if key.Kind != "Translation" || key.Namespace == "" || key.Name == "" || patch == nil ||
		patch.UID == "" || patch.ResourceVersion == "" || patch.ObservedGeneration == nil || patch.LastAppliedRevision == nil || patch.Conditions == nil {
		return errors.New("translation: incomplete status CAS")
	}
	if err := validateConditions(patch.Conditions); err != nil {
		return err
	}
	input := map[string]any{
		"namespace": key.Namespace, "name": key.Name, "uid": patch.UID, "resourceVersion": patch.ResourceVersion,
		"observedGeneration": *patch.ObservedGeneration, "lastAppliedRevision": *patch.LastAppliedRevision,
		"conditions": status.ToConditionInputs(patch.Conditions),
	}
	if patch.Resolved != nil {
		var resolved map[string]any
		if err := json.Unmarshal(patch.Resolved, &resolved); err != nil {
			return fmt.Errorf("translation: invalid resolved status: %w", err)
		}
		input["resolved"] = resolved
	}
	var response struct {
		UpdateTranslationStatus struct {
			Translation *struct {
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
			} `json:"translation"`
		} `json:"updateTranslationStatus"`
	}
	if err := c.client.Mutate(ctx, translationStatusMutation, map[string]any{"input": input}, &response); err != nil {
		return clientError(err)
	}
	if response.UpdateTranslationStatus.Translation == nil ||
		response.UpdateTranslationStatus.Translation.Metadata.ResourceVersion == "" {
		return errors.New("translation: missing status acknowledgement")
	}
	return nil
}

func clientError(err error) error {
	var structured *graphqlclient.Error
	if errors.As(err, &structured) {
		switch {
		case graphqlclient.IsConflictCode(structured.Extensions["code"]):
			return fmt.Errorf("translation: %w: %w", types.ErrConflict, err)
		case structured.Extensions["code"] == "NOT_FOUND":
			return fmt.Errorf("translation: %w: %w", types.ErrNotFound, err)
		}
	}
	return fmt.Errorf("translation: API request: %w", err)
}

var _ status.StatusClient = (*GraphQLClient)(nil)
