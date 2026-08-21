// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Type converters between datastore and GraphQL models

package resolver

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

const (
	namespaceAPIVersion             = "gitstore.dev/v1beta1"
	namespaceKind                   = "Namespace"
	namespaceInitialResourceVersion = "1"
	namespaceInitialGeneration      = int32(1)
	repositoryAPIVersion            = "gitstore.dev/v1beta1"
	repositoryKind                  = "Repository"
)

// datastoreNamespaceToModel converts a datastore Namespace to a GraphQL model Namespace.
func datastoreNamespaceToModel(ns *datastore.Namespace) *model.Namespace {
	if ns == nil {
		return nil
	}
	normalized := *ns
	normalized.Status = append([]byte(nil), ns.Status...)
	normalized.Finalizers = append([]string(nil), ns.Finalizers...)
	datastore.NormalizeNamespaceContract(&normalized)
	ns = &normalized
	spec, err := namespaceSpecFromDatastore(ns)
	if err != nil {
		converterLogger.Error("failed to decode Namespace spec", zap.String("uid", ns.UID), zap.Error(err))
		return nil
	}
	status, err := namespaceStatusFromJSON(ns.Status)
	if err != nil {
		converterLogger.Error("failed to decode Namespace status", zap.String("uid", ns.UID), zap.Error(err))
		return nil
	}
	ownerReferences, err := ownerRefsFromJSONStrict(ns.OwnerReferences)
	if err != nil {
		converterLogger.Error("failed to decode Namespace owner references", zap.String("uid", ns.UID), zap.Error(err))
		return nil
	}
	if ns.DeletionTimestamp != nil {
		status.Conditions = upsertTerminatingCondition(status.Conditions, ns.Generation, *ns.DeletionTimestamp)
	}
	var revision *string
	if ns.Revision != "" {
		value := ns.Revision
		revision = &value
	}
	displayName := spec.Title
	apiVersion := ns.APIVersion
	if apiVersion == "" {
		apiVersion = namespaceAPIVersion
	}
	kind := ns.Kind
	if kind == "" {
		kind = namespaceKind
	}
	body := ns.Body
	return &model.Namespace{
		ID:         mustEncodeNodeID(nodeKindNamespace, ns.UID),
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata: &model.NamespaceMetadata{
			Name:              ns.Name,
			Labels:            stringMapToJSONMap(ns.Labels),
			Annotations:       stringMapToJSONMap(ns.Annotations),
			UID:               ns.UID,
			ResourceVersion:   ns.ResourceVersion,
			Generation:        int32(ns.Generation),
			CreationTimestamp: ns.CreationTimestamp,
			Revision:          revision,
			OwnerReferences:   ownerReferences,
			Finalizers:        append([]string{}, ns.Finalizers...),
		},
		Spec:        spec,
		Status:      status,
		Identifier:  ns.Name,
		DisplayName: displayName,
		Tier:        datastoreNamespaceTierToModel(ns.Tier),
		CreatedAt:   ns.CreationTimestamp,
		CreatedBy:   ns.CreationActor,
		UpdatedAt:   ns.UpdateTimestamp,
		UpdatedBy:   ns.UpdateActor,
		Body:        &body,
	}
}

func namespaceSpecFromDatastore(ns *datastore.Namespace) (*model.NamespaceSpec, error) {
	if len(ns.Spec) == 0 {
		var title *string
		if ns.Title != "" {
			value := ns.Title
			title = &value
		}
		return &model.NamespaceSpec{
			Title: title,
			Tier:  datastoreNamespaceTierToModel(ns.Tier),
		}, nil
	}
	var stored catalog.NamespaceSpec
	if err := json.Unmarshal(ns.Spec, &stored); err != nil {
		return nil, fmt.Errorf("unmarshal Namespace spec: %w", err)
	}
	var title *string
	if stored.Title != "" {
		value := stored.Title
		title = &value
	}
	spec := &model.NamespaceSpec{
		Title: title,
		Tier:  datastoreNamespaceTierToModel(ns.Tier),
	}
	if stored.RepositoryDefaults != nil {
		spec.RepositoryDefaults = &model.NamespaceRepositoryDefaults{}
		if stored.RepositoryDefaults.DefaultBranch != "" {
			value := stored.RepositoryDefaults.DefaultBranch
			spec.RepositoryDefaults.DefaultBranch = &value
		}
		if stored.RepositoryDefaults.Visibility != "" {
			value := model.RepositoryVisibility(strings.ToUpper(stored.RepositoryDefaults.Visibility))
			if !value.IsValid() {
				return nil, fmt.Errorf("invalid Repository visibility %q", stored.RepositoryDefaults.Visibility)
			}
			spec.RepositoryDefaults.Visibility = &value
		}
	}
	if stored.PushPolicyDefaults != nil {
		maxPackSizeBytes := stored.PushPolicyDefaults.MaxPackSizeBytes
		maxFileSizeBytes := stored.PushPolicyDefaults.MaxFileSizeBytes
		spec.PushPolicyDefaults = &model.NamespacePushPolicyDefaults{
			MaxPackSizeBytes: &maxPackSizeBytes,
			MaxFileSizeBytes: &maxFileSizeBytes,
		}
	}
	return spec, nil
}

type rawNamespaceStatus struct {
	ObservedGeneration  int32          `json:"observedGeneration"`
	LastAppliedRevision string         `json:"lastAppliedRevision"`
	Conditions          []rawCondition `json:"conditions"`
}

func namespaceStatusFromJSON(raw json.RawMessage) (*model.NamespaceStatus, error) {
	status := &model.NamespaceStatus{Conditions: []*model.Condition{}}
	if len(raw) == 0 {
		return status, nil
	}
	var stored rawNamespaceStatus
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("unmarshal Namespace status: %w", err)
	}
	status.ObservedGeneration = stored.ObservedGeneration
	if stored.LastAppliedRevision != "" {
		revision := stored.LastAppliedRevision
		status.LastAppliedRevision = &revision
	}
	for _, condition := range stored.Conditions {
		status.Conditions = append(status.Conditions, conditionFromRaw(condition))
	}
	return status, nil
}

func upsertTerminatingCondition(conditions []*model.Condition, generation int64, since time.Time) []*model.Condition {
	out := make([]*model.Condition, 0, len(conditions)+1)
	for _, condition := range conditions {
		if condition != nil && condition.Type == catalog.ConditionTerminating {
			continue
		}
		out = append(out, condition)
	}
	out = append(out, &model.Condition{
		Type:               catalog.ConditionTerminating,
		Status:             model.ConditionStatusTrue,
		ObservedGeneration: int32Pointer(int32(generation)),
		LastTransitionTime: since,
		Reason:             stringPointer("DeletionRequested"),
		Message:            stringPointer("Namespace is awaiting foreground deletion completion."),
	})
	return out
}

func stringPointer(value string) *string { return &value }
func int32Pointer(value int32) *int32    { return &value }

// DatastoreNamespaceToGraphQL is the exported version of datastoreNamespaceToModel.
func DatastoreNamespaceToGraphQL(ns *datastore.Namespace) *model.Namespace {
	return datastoreNamespaceToModel(ns)
}

// converterLogger is a package-level logger for blob-unmarshal warnings.
// It is initialised to a no-op logger by default; callers that have a real
// logger can replace it via SetConverterLogger.
var converterLogger *zap.Logger = zap.NewNop()

// SetConverterLogger replaces the package-level logger used by converter helpers.
func SetConverterLogger(l *zap.Logger) { converterLogger = l }

// specFromJSON deserialises a ProductSpec blob. A nil/empty blob returns a
// non-nil empty spec (FR-001). Unmarshal errors are logged at WARN level and
// also return the empty spec.
func specFromJSON(raw json.RawMessage) *model.ProductSpec {
	empty := &model.ProductSpec{
		Tags:    []string{},
		Media:   []*model.MediaDefinition{},
		Options: []*model.ProductOptionDefinition{},
	}
	if len(raw) == 0 {
		return empty
	}
	var s model.ProductSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "spec"), zap.Error(err))
		return empty
	}
	if s.Tags == nil {
		s.Tags = []string{}
	}
	if s.Media == nil {
		s.Media = []*model.MediaDefinition{}
	}
	if s.Options == nil {
		s.Options = []*model.ProductOptionDefinition{}
	}
	return &s
}

// rawCondition mirrors catalog.Condition so we can unmarshal Kubernetes-style
// values before mapping status to the GraphQL enum representation.
type rawCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	ObservedGeneration int32     `json:"observedGeneration"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
}

type rawProductStatus struct {
	ObservedGeneration  int32                            `json:"observedGeneration"`
	LastAppliedRevision string                           `json:"lastAppliedRevision"`
	Conditions          []rawCondition                   `json:"conditions"`
	Resolved            *model.ResolvedProductDefinition `json:"resolved,omitempty"`
}

// k8sConditionStatusToGraphQL maps "True"/"False"/"Unknown" to their GraphQL equivalents.
var k8sConditionStatusToGraphQL = map[string]model.ConditionStatus{
	"True":    model.ConditionStatusTrue,
	"False":   model.ConditionStatusFalse,
	"Unknown": model.ConditionStatusUnknown,
}

func conditionStatusFromString(status string) model.ConditionStatus {
	status = strings.TrimSpace(status)
	if condStatus, ok := k8sConditionStatusToGraphQL[status]; ok {
		return condStatus
	}
	return model.ConditionStatus(strings.ToUpper(status))
}

func conditionFromRaw(c rawCondition) *model.Condition {
	condStatus := conditionStatusFromString(c.Status)
	gen := c.ObservedGeneration
	cond := &model.Condition{
		Type:               c.Type,
		Status:             condStatus,
		ObservedGeneration: &gen,
		LastTransitionTime: c.LastTransitionTime,
	}
	if c.Reason != "" {
		r := c.Reason
		cond.Reason = &r
	}
	if c.Message != "" {
		m := c.Message
		cond.Message = &m
	}
	return cond
}

// statusFromJSON deserialises a ProductStatus blob. A nil/empty blob returns
// nil (FR-002). Unmarshal errors are logged at WARN and also return nil.
// Condition statuses are normalised from Kubernetes TitleCase to GraphQL UPPER_SNAKE_CASE.
func statusFromJSON(raw json.RawMessage) *model.ProductStatus {
	if len(raw) == 0 {
		return nil
	}
	var rs rawProductStatus
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "status"), zap.Error(err))
		return nil
	}
	conditions := make([]*model.Condition, 0, len(rs.Conditions))
	for _, c := range rs.Conditions {
		conditions = append(conditions, conditionFromRaw(c))
	}
	var lastApplied *string
	if rs.LastAppliedRevision != "" {
		s := rs.LastAppliedRevision
		lastApplied = &s
	}
	return &model.ProductStatus{
		ObservedGeneration:  rs.ObservedGeneration,
		LastAppliedRevision: lastApplied,
		Conditions:          conditions,
		Resolved:            rs.Resolved,
	}
}

// ownerRefsFromJSON deserialises an OwnerReferences blob. Nil/empty or unmarshal
// errors return an empty (never nil) slice.
func ownerRefsFromJSON(raw json.RawMessage) []*model.OwnerReference {
	refs, err := ownerRefsFromJSONStrict(raw)
	if err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "ownerRefs"), zap.Error(err))
		return []*model.OwnerReference{}
	}
	return refs
}

func ownerRefsFromJSONStrict(raw json.RawMessage) ([]*model.OwnerReference, error) {
	if len(raw) == 0 {
		return []*model.OwnerReference{}, nil
	}
	var refs []*model.OwnerReference
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, fmt.Errorf("unmarshal owner references: %w", err)
	}
	if refs == nil {
		return []*model.OwnerReference{}, nil
	}
	return refs, nil
}

// DatastoreProductToGraphQL converts a datastore Product to a GraphQL model Product.
func DatastoreProductToGraphQL(p *datastore.Product) *model.Product {
	if p == nil {
		return nil
	}
	gen := int32(p.Generation)
	meta := &model.ObjectMeta{
		Name:              p.Name,
		Namespace:         p.Namespace,
		Labels:            stringMapToJSONMap(p.Labels),
		Annotations:       stringMapToJSONMap(p.Annotations),
		UID:               mustEncodeNodeID(nodeKindProduct, p.UID),
		ResourceVersion:   p.ResourceVersion,
		Generation:        gen,
		CreationTimestamp: p.CreationTimestamp,
		OwnerReferences:   ownerRefsFromJSON(p.OwnerReferences),
		Finalizers:        append([]string{}, p.Finalizers...),
		DeletionTimestamp: p.DeletionTimestamp,
	}
	if p.Revision != "" {
		meta.Revision = &p.Revision
	}
	out := &model.Product{
		ID:         mustEncodeNodeID(nodeKindProduct, p.UID),
		APIVersion: p.APIVersion,
		Kind:       p.Kind,
		Metadata:   meta,
		Spec:       specFromJSON(p.Spec),
		Status:     statusFromJSON(p.Status),
		ProductVariants: &model.ProductVariantConnection{
			Edges:    []*model.ProductVariantEdge{},
			PageInfo: &model.PageInfo{},
		},
	}
	if p.Body != "" {
		out.Body = &p.Body
	}
	return out
}

// DatastoreCategoryTaxonomyToGraphQL converts a CategoryTaxonomy datastore entity
// to the GraphQL model.Category.
func DatastoreCategoryTaxonomyToGraphQL(c *datastore.CategoryTaxonomy) *model.Category {
	if c == nil {
		return nil
	}

	// Compute path and depth from materialized AncestorPath.
	var path []string
	var depth int32
	if c.AncestorPath != "" {
		path = strings.Split(c.AncestorPath, "/")
		depth = int32(len(path) - 1)
	}

	emptyProducts := &model.ProductConnection{
		Edges:    []*model.ProductEdge{},
		PageInfo: &model.PageInfo{},
	}

	// Extract title from spec JSON.
	title := ""
	var parentRef *model.CatalogObjectReference
	var specMedia []*model.MediaDefinition
	if len(c.Spec) > 0 {
		var raw struct {
			Title     string `json:"title"`
			ParentRef *struct {
				APIVersion string `json:"apiVersion"`
				Kind       string `json:"kind"`
				Name       string `json:"name"`
				Namespace  string `json:"namespace"`
			} `json:"parentRef"`
			Media []struct {
				FileRef *struct {
					Name     string `json:"name"`
					Kind     string `json:"kind"`
					Optional bool   `json:"optional"`
				} `json:"fileRef"`
			} `json:"media"`
		}
		if err := json.Unmarshal(c.Spec, &raw); err == nil {
			title = raw.Title
			if raw.ParentRef != nil && raw.ParentRef.Name != "" {
				parentRef = &model.CatalogObjectReference{
					Name: raw.ParentRef.Name,
				}
				if raw.ParentRef.APIVersion != "" {
					parentRef.APIVersion = &raw.ParentRef.APIVersion
				}
				if raw.ParentRef.Kind != "" {
					parentRef.Kind = &raw.ParentRef.Kind
				}
				if raw.ParentRef.Namespace != "" {
					parentRef.Namespace = &raw.ParentRef.Namespace
				}
			}
			for _, m := range raw.Media {
				if m.FileRef == nil {
					continue
				}
				specMedia = append(specMedia, &model.MediaDefinition{
					FileRef: &model.FileReference{
						Name:     m.FileRef.Name,
						Kind:     m.FileRef.Kind,
						Optional: m.FileRef.Optional,
					},
				})
			}
		}
	}

	gen := int32(c.Generation)
	rv := c.ResourceVersion
	meta := &model.ObjectMeta{
		Name:              c.Name,
		Namespace:         c.Namespace,
		Labels:            stringMapToJSONMap(c.Labels),
		Annotations:       stringMapToJSONMap(c.Annotations),
		UID:               mustEncodeNodeID(nodeKindCategory, c.UID),
		ResourceVersion:   rv,
		Generation:        gen,
		CreationTimestamp: c.CreationTimestamp,
		OwnerReferences:   ownerRefsFromJSON(c.OwnerReferences),
		Finalizers:        append([]string{}, c.Finalizers...),
		DeletionTimestamp: c.DeletionTimestamp,
	}
	if c.Revision != "" {
		meta.Revision = &c.Revision
	}

	spec := &model.CategorySpec{
		Title:     title,
		ParentRef: parentRef,
		Media:     specMedia,
	}
	if spec.Media == nil {
		spec.Media = []*model.MediaDefinition{}
	}

	categoryStatus := categoryStatusFromJSON(c.Status)
	if c.DeletionTimestamp != nil {
		if categoryStatus == nil {
			categoryStatus = &model.CategoryTaxonomyStatus{Conditions: []*model.Condition{}}
		}
		categoryStatus.Conditions = upsertTerminatingCondition(categoryStatus.Conditions, c.Generation, *c.DeletionTimestamp)
	}

	apiVersion := c.APIVersion
	kind := c.Kind
	cat := &model.Category{
		ID:         mustEncodeNodeID(nodeKindCategory, c.UID),
		APIVersion: &apiVersion,
		Kind:       &kind,
		Metadata:   meta,
		Spec:       spec,
		Status:     categoryStatus,
		Body:       nil,
		Parent:     nil,
		Children:   []*model.Category{},
		Path:       path,
		Depth:      depth,
		Products:   emptyProducts,
	}
	if c.Body != "" {
		cat.Body = &c.Body
	}
	return cat
}

// stringMapToJSONMap converts string maps to GraphQL JSON-map metadata fields.
func stringMapToJSONMap(m map[string]string) map[string]any {
	if len(m) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// DatastoreFileToGraphQL converts a datastore File to its typed GraphQL view.
func DatastoreFileToGraphQL(f *datastore.File) *model.File {
	if f == nil {
		return nil
	}
	ownerRefs, err := ownerRefsFromJSONStrict(f.OwnerReferences)
	if err != nil {
		return nil
	}
	meta := &model.ObjectMeta{
		Name: f.Name, Namespace: f.Namespace, Labels: stringMapToJSONMap(f.Labels),
		Annotations: stringMapToJSONMap(f.Annotations), UID: mustEncodeNodeID(nodeKindFile, f.UID),
		ResourceVersion: f.ResourceVersion, Generation: int32(f.Generation),
		CreationTimestamp: f.CreationTimestamp, OwnerReferences: ownerRefs,
		Finalizers: append([]string{}, f.Finalizers...), DeletionTimestamp: f.DeletionTimestamp,
	}
	if f.Revision != "" {
		revision := f.Revision
		meta.Revision = &revision
	}
	spec := &catalog.FileSpec{}
	if len(f.Spec) > 0 && json.Unmarshal(f.Spec, spec) != nil {
		return nil
	}
	fileSpec := &model.FileSpec{
		ContentType: spec.ContentType,
		Source:      &model.FileSource{Type: spec.Source.Type, URI: spec.Source.URI},
	}
	if spec.Type != "" {
		fileSpec.Type = &spec.Type
	}
	if spec.Source.Checksum != nil {
		fileSpec.Source.Checksum = &model.FileChecksum{Algorithm: spec.Source.Checksum.Algorithm, Value: spec.Source.Checksum.Value}
	}
	if spec.Source.CredentialsRef != nil {
		ref := spec.Source.CredentialsRef
		fileSpec.Source.CredentialsRef = &model.SecretRef{Kind: ref.Kind, Name: ref.Name}
		if ref.Key != "" {
			fileSpec.Source.CredentialsRef.Key = &ref.Key
		}
		if ref.Namespace != "" {
			fileSpec.Source.CredentialsRef.Namespace = &ref.Namespace
		}
	}
	if spec.Processing != nil && spec.Processing.Image != nil {
		fileSpec.Processing = &model.FileProcessing{Image: &model.FileImageProcessing{}}
		for _, variant := range spec.Processing.Image.Variants {
			fileSpec.Processing.Image.Variants = append(fileSpec.Processing.Image.Variants, &model.FileVariantRequest{Name: variant.Name})
		}
	}
	var status *model.FileStatus
	if len(f.Status) > 0 {
		var raw struct {
			ObservedGeneration  int32                           `json:"observedGeneration"`
			LastAppliedRevision string                          `json:"lastAppliedRevision"`
			Conditions          []rawCondition                  `json:"conditions"`
			Resolved            *catalog.ResolvedFileDefinition `json:"resolved,omitempty"`
		}
		if json.Unmarshal(f.Status, &raw) == nil {
			conditions := make([]*model.Condition, 0, len(raw.Conditions))
			for _, condition := range raw.Conditions {
				conditions = append(conditions, conditionFromRaw(condition))
			}
			status = &model.FileStatus{ObservedGeneration: raw.ObservedGeneration, LastAppliedRevision: raw.LastAppliedRevision, Conditions: conditions}
			if raw.Resolved != nil {
				resolved := &model.ResolvedFileDefinition{Name: raw.Resolved.Name, URL: raw.Resolved.URL}
				if raw.Resolved.ContentType != "" {
					resolved.ContentType = &raw.Resolved.ContentType
				}
				for _, variant := range raw.Resolved.ResolvedVariants {
					url := variant.URL
					resolved.ResolvedVariants = append(resolved.ResolvedVariants, &model.ResolvedFileVariant{Name: variant.Name, URL: &url})
				}
				status.Resolved = resolved
			}
		}
	}
	body := f.Body
	return &model.File{ID: mustEncodeNodeID(nodeKindFile, f.UID), APIVersion: f.APIVersion, Kind: f.Kind, Metadata: meta, Spec: fileSpec, Status: status, Body: &body}
}

// categoryStatusFromJSON deserialises a CategoryTaxonomyStatus blob.
func categoryStatusFromJSON(raw json.RawMessage) *model.CategoryTaxonomyStatus {
	if len(raw) == 0 {
		return nil
	}
	var rs struct {
		ObservedGeneration  int32          `json:"observedGeneration"`
		LastAppliedRevision string         `json:"lastAppliedRevision"`
		Conditions          []rawCondition `json:"conditions"`
		Resolved            *struct {
			Depth        int32    `json:"depth"`
			Path         []string `json:"path"`
			ChildCount   int32    `json:"childCount"`
			ProductCount int32    `json:"productCount"`
		} `json:"resolved"`
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("category blob unmarshal error", zap.String("field", "status"), zap.Error(err))
		return nil
	}
	conditions := make([]*model.Condition, 0, len(rs.Conditions))
	for _, c := range rs.Conditions {
		conditions = append(conditions, conditionFromRaw(c))
	}
	var resolved *model.ResolvedCategoryTaxonomy
	if rs.Resolved != nil {
		resolved = &model.ResolvedCategoryTaxonomy{
			Depth:        rs.Resolved.Depth,
			Path:         rs.Resolved.Path,
			ChildCount:   rs.Resolved.ChildCount,
			ProductCount: rs.Resolved.ProductCount,
		}
	}
	return &model.CategoryTaxonomyStatus{
		ObservedGeneration:  rs.ObservedGeneration,
		LastAppliedRevision: rs.LastAppliedRevision,
		Conditions:          conditions,
		Resolved:            resolved,
	}
}

// DatastoreCollectionToGraphQL converts a datastore Collection to a GraphQL model Collection.
func DatastoreCollectionToGraphQL(c *datastore.Collection) *model.Collection {
	if c == nil {
		return nil
	}
	gen := int32(c.Generation)
	meta := &model.ObjectMeta{
		Name:              c.Name,
		Namespace:         c.Namespace,
		Labels:            stringMapToJSONMap(c.Labels),
		Annotations:       stringMapToJSONMap(c.Annotations),
		UID:               mustEncodeNodeID(nodeKindCollection, c.UID),
		ResourceVersion:   c.ResourceVersion,
		Generation:        gen,
		CreationTimestamp: c.CreationTimestamp,
		OwnerReferences:   ownerRefsFromJSON(c.OwnerReferences),
		Finalizers:        append([]string{}, c.Finalizers...),
		DeletionTimestamp: c.DeletionTimestamp,
	}
	if c.Revision != "" {
		r := c.Revision
		meta.Revision = &r
	}
	out := &model.Collection{
		ID:       mustEncodeNodeID(nodeKindCollection, c.UID),
		Metadata: meta,
		Spec:     collectionSpecFromJSON(c.Spec),
		Status:   collectionStatusFromJSON(c.Status),
		Products: &model.ProductConnection{Edges: []*model.ProductEdge{}, PageInfo: &model.PageInfo{}},
	}
	if c.APIVersion != "" {
		v := c.APIVersion
		out.APIVersion = &v
	}
	if c.Kind != "" {
		k := c.Kind
		out.Kind = &k
	}
	if c.Body != "" {
		out.Body = &c.Body
	}
	return out
}

// collectionSpecFromJSON deserialises a CollectionSpec blob.
func collectionSpecFromJSON(raw json.RawMessage) *model.CollectionSpec {
	empty := &model.CollectionSpec{Media: []*model.MediaDefinition{}}
	if len(raw) == 0 {
		return empty
	}
	var rs struct {
		Title    string `json:"title"`
		Selector *struct {
			MatchLabels      map[string]string `json:"matchLabels"`
			MatchExpressions []*struct {
				Key      string   `json:"key"`
				Operator string   `json:"operator"`
				Values   []string `json:"values"`
			} `json:"matchExpressions"`
		} `json:"selector"`
		Media []struct {
			FileRef *struct {
				Name     string `json:"name"`
				Kind     string `json:"kind"`
				Optional bool   `json:"optional"`
			} `json:"fileRef"`
		} `json:"media"`
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("collection blob unmarshal error", zap.String("field", "spec"), zap.Error(err))
		return empty
	}
	spec := &model.CollectionSpec{Title: rs.Title, Media: []*model.MediaDefinition{}}
	if rs.Selector != nil {
		sel := &model.LabelSelector{MatchLabels: map[string]any{}}
		for k, v := range rs.Selector.MatchLabels {
			sel.MatchLabels[k] = v
		}
		for _, e := range rs.Selector.MatchExpressions {
			sel.MatchExpressions = append(sel.MatchExpressions, &model.LabelSelectorRequirement{
				Key:      e.Key,
				Operator: model.LabelSelectorOperator(e.Operator),
				Values:   e.Values,
			})
		}
		spec.Selector = sel
	}
	for _, m := range rs.Media {
		if m.FileRef == nil {
			continue
		}
		spec.Media = append(spec.Media, &model.MediaDefinition{
			FileRef: &model.FileReference{
				Name:     m.FileRef.Name,
				Kind:     m.FileRef.Kind,
				Optional: m.FileRef.Optional,
			},
		})
	}
	return spec
}

// collectionStatusFromJSON deserialises a CollectionStatus blob.
func collectionStatusFromJSON(raw json.RawMessage) *model.CollectionStatus {
	if len(raw) == 0 {
		return nil
	}
	var rs struct {
		ObservedGeneration  int32          `json:"observedGeneration"`
		LastAppliedRevision string         `json:"lastAppliedRevision"`
		Conditions          []rawCondition `json:"conditions"`
		Resolved            *struct {
			MemberCount int32 `json:"memberCount"`
		} `json:"resolved"`
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("collection blob unmarshal error", zap.String("field", "status"), zap.Error(err))
		return nil
	}
	conditions := make([]*model.Condition, 0, len(rs.Conditions))
	for _, c := range rs.Conditions {
		conditions = append(conditions, conditionFromRaw(c))
	}
	status := &model.CollectionStatus{
		ObservedGeneration: rs.ObservedGeneration,
		Conditions:         conditions,
	}
	if rs.LastAppliedRevision != "" {
		s := rs.LastAppliedRevision
		status.LastAppliedRevision = &s
	}
	if rs.Resolved != nil {
		status.Resolved = &model.ResolvedCollectionDefinition{MemberCount: rs.Resolved.MemberCount}
	}
	return status
}

// jsonUnmarshal is a thin wrapper so resolver files can call it without importing encoding/json.
func jsonUnmarshal(data json.RawMessage, v any) error {
	return json.Unmarshal(data, v)
}

// specSelectorToCatalog converts an inline spec selector struct to catalog.LabelSelector.
func specSelectorToCatalog(sel *struct {
	MatchLabels      map[string]string `json:"matchLabels"`
	MatchExpressions []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	} `json:"matchExpressions"`
}) catalog.LabelSelector {
	s := catalog.LabelSelector{MatchLabels: sel.MatchLabels}
	for _, e := range sel.MatchExpressions {
		s.MatchExpressions = append(s.MatchExpressions, catalog.LabelSelectorRequirement{
			Key:      e.Key,
			Operator: e.Operator,
			Values:   e.Values,
		})
	}
	return s
}

// DatastoreRepositoryToGraphQL converts a datastore Repository without
// namespace or storage-root context. Resolver paths should use
// datastoreRepositoryToModel so all non-null fields are populated.
func DatastoreRepositoryToGraphQL(r *datastore.Repository) *model.Repository {
	return datastoreRepositoryToModel(r, nil, "")
}

func datastoreNamespaceTierToModel(t datastore.NamespaceTier) model.NamespaceTier {
	switch t {
	case datastore.NamespaceTierOrganization:
		return model.NamespaceTierOrganization
	default:
		return model.NamespaceTierUser
	}
}

// DatastoreVariantToGraphQL converts a datastore ProductVariant to the GraphQL model.
func DatastoreVariantToGraphQL(v *datastore.ProductVariant) *model.ProductVariant {
	if v == nil {
		return nil
	}
	gen := int32(v.Generation)
	meta := &model.ObjectMeta{
		Name:              v.Name,
		Namespace:         v.Namespace,
		Labels:            stringMapToJSONMap(v.Labels),
		Annotations:       stringMapToJSONMap(v.Annotations),
		UID:               mustEncodeNodeID(nodeKindProductVariant, v.UID),
		ResourceVersion:   v.ResourceVersion,
		Generation:        gen,
		CreationTimestamp: v.CreationTimestamp,
		OwnerReferences:   ownerRefsFromJSON(v.OwnerReferences),
		Finalizers:        append([]string{}, v.Finalizers...),
	}
	if v.Revision != "" {
		r := v.Revision
		meta.Revision = &r
	}
	out := &model.ProductVariant{
		ID:       mustEncodeNodeID(nodeKindProductVariant, v.UID),
		Metadata: meta,
		Spec:     variantSpecFromJSON(v.Spec),
		Status:   variantStatusFromJSON(v.Status),
	}
	out.APIVersion = v.APIVersion
	out.Kind = v.Kind
	if v.Body != "" {
		b := v.Body
		out.Body = &b
	}
	return out
}

// variantSpecFromJSON deserialises a ProductVariantSpec JSON blob.
func variantSpecFromJSON(raw json.RawMessage) *model.ProductVariantSpec {
	empty := &model.ProductVariantSpec{
		SelectedOptions: []*model.SelectedOptionDefinition{},
		Media:           []*model.MediaDefinition{},
	}
	if len(raw) == 0 {
		return empty
	}
	var rs struct {
		Title      string `json:"title"`
		SKU        string `json:"sku"`
		ProductRef *struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"productRef"`
		Inventory *struct {
			Managed           bool   `json:"managed"`
			Policy            string `json:"policy"`
			StockLocationRefs []struct {
				Name string `json:"name"`
				Kind string `json:"kind"`
			} `json:"stockLocationRefs"`
		} `json:"inventory"`
		Pricing *struct {
			PriceSet *struct {
				Name   string `json:"name"`
				Prices []struct {
					Name           string     `json:"name"`
					ValidFromTime  *time.Time `json:"validFromTime"`
					ValidUntilTime *time.Time `json:"validUntilTime"`
					CurrencyCode   string     `json:"currencyCode"`
					Amount         string     `json:"amount"`
					Priority       int32      `json:"priority"`
					Strategy       *struct {
						Type string `json:"type"`
					} `json:"strategy"`
					Quantity *struct {
						Min int32  `json:"min"`
						Max *int32 `json:"max"`
					} `json:"quantity"`
					Eligibility *struct {
						Operator    string `json:"operator"`
						Constraints []struct {
							Name       *string `json:"name"`
							Expression string  `json:"expression"`
						} `json:"constraints"`
					} `json:"eligibility"`
				} `json:"prices"`
			} `json:"priceSet"`
		} `json:"pricing"`
		SelectedOptions []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"selectedOptions"`
		Media []struct {
			FileRef *struct {
				Name     string `json:"name"`
				Kind     string `json:"kind"`
				Optional bool   `json:"optional"`
			} `json:"fileRef"`
		} `json:"media"`
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("variant blob unmarshal error", zap.String("field", "spec"), zap.Error(err))
		return empty
	}
	spec := &model.ProductVariantSpec{
		Title:           rs.Title,
		Sku:             rs.SKU,
		SelectedOptions: []*model.SelectedOptionDefinition{},
		Media:           []*model.MediaDefinition{},
	}
	if rs.ProductRef != nil {
		ref := &model.CatalogObjectReference{Name: rs.ProductRef.Name}
		if rs.ProductRef.Kind != "" {
			k := rs.ProductRef.Kind
			ref.Kind = &k
		}
		spec.ProductRef = ref
	} else {
		spec.ProductRef = &model.CatalogObjectReference{}
	}
	if rs.Inventory != nil {
		inv := &model.InventoryDefinition{
			Managed:           rs.Inventory.Managed,
			StockLocationRefs: []*model.CatalogObjectReference{},
		}
		if rs.Inventory.Policy != "" {
			p := model.InventoryPolicy(strings.ToUpper(rs.Inventory.Policy))
			inv.Policy = &p
		}
		for _, sl := range rs.Inventory.StockLocationRefs {
			slRef := &model.CatalogObjectReference{Name: sl.Name}
			if sl.Kind != "" {
				k := sl.Kind
				slRef.Kind = &k
			}
			inv.StockLocationRefs = append(inv.StockLocationRefs, slRef)
		}
		spec.Inventory = inv
	}
	if rs.Pricing != nil && rs.Pricing.PriceSet != nil {
		ps := &model.PriceSet{Name: rs.Pricing.PriceSet.Name}
		for _, p := range rs.Pricing.PriceSet.Prices {
			pt := &model.PriceTemplate{
				Name:         p.Name,
				CurrencyCode: p.CurrencyCode,
				Priority:     p.Priority,
			}
			if p.Strategy != nil {
				pt.Strategy = &model.StrategyDefinition{Type: p.Strategy.Type}
			} else {
				pt.Strategy = &model.StrategyDefinition{}
			}
			if p.Amount != "" {
				if d, err := decimal.NewFromString(p.Amount); err == nil {
					pt.Amount = d
				}
			}
			pt.ValidFromTime = p.ValidFromTime
			pt.ValidUntilTime = p.ValidUntilTime
			if p.Quantity != nil {
				pt.Quantity = &model.QuantityDefinition{Min: p.Quantity.Min, Max: p.Quantity.Max}
			}
			if p.Eligibility != nil {
				el := &model.EligibilityDefinition{
					Operator:    model.EligibilityOperator(strings.ToUpper(p.Eligibility.Operator)),
					Constraints: []*model.PriceRuleConstraint{},
				}
				for _, c := range p.Eligibility.Constraints {
					prc := &model.PriceRuleConstraint{Expression: c.Expression}
					prc.Name = c.Name
					el.Constraints = append(el.Constraints, prc)
				}
				pt.Eligibility = el
			}
			ps.Prices = append(ps.Prices, pt)
		}
		spec.Pricing = &model.PricingDefinition{PriceSet: ps}
	}
	for _, o := range rs.SelectedOptions {
		spec.SelectedOptions = append(spec.SelectedOptions, &model.SelectedOptionDefinition{Name: o.Name, Value: o.Value})
	}
	for _, m := range rs.Media {
		if m.FileRef == nil {
			continue
		}
		spec.Media = append(spec.Media, &model.MediaDefinition{
			FileRef: &model.FileReference{Name: m.FileRef.Name, Kind: m.FileRef.Kind, Optional: m.FileRef.Optional},
		})
	}
	return spec
}

// variantStatusFromJSON deserialises a ProductVariant status JSON blob.
func variantStatusFromJSON(raw json.RawMessage) *model.ProductVariantStatus {
	if len(raw) == 0 {
		return nil
	}
	var rs struct {
		ObservedGeneration  int32          `json:"observedGeneration"`
		LastAppliedRevision string         `json:"lastAppliedRevision"`
		Conditions          []rawCondition `json:"conditions"`
		Resolved            *struct {
			Product *struct {
				Name string `json:"name"`
				UID  string `json:"uid"`
			} `json:"product,omitempty"`
			SelectedOptionsHash string `json:"selectedOptionsHash,omitempty"`
			PriceSet            *struct {
				Name                string   `json:"name"`
				Hash                string   `json:"hash,omitempty"`
				CompiledExpressions int32    `json:"compiledExpressions"`
				PriceCount          int64    `json:"priceCount"`
				Currencies          []string `json:"currencies"`
				Strategies          []string `json:"strategies"`
			} `json:"priceSet,omitempty"`
			Inventory *struct {
				Managed           bool   `json:"managed"`
				AvailableQuantity int64  `json:"availableQuantity"`
				Policy            string `json:"policy,omitempty"`
			} `json:"inventory,omitempty"`
		} `json:"resolved,omitempty"`
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("variant blob unmarshal error", zap.String("field", "status"), zap.Error(err))
		return nil
	}
	conditions := make([]*model.Condition, 0, len(rs.Conditions))
	for _, c := range rs.Conditions {
		conditions = append(conditions, conditionFromRaw(c))
	}
	status := &model.ProductVariantStatus{
		ObservedGeneration: rs.ObservedGeneration,
		Conditions:         conditions,
	}
	if rs.LastAppliedRevision != "" {
		s := rs.LastAppliedRevision
		status.LastAppliedRevision = &s
	}
	if rs.Resolved != nil {
		resolved := &model.ResolvedProductVariantDefinition{}
		if rs.Resolved.Product != nil {
			resolved.Product = &model.ResolvedProductRef{
				Name: rs.Resolved.Product.Name,
				UID:  rs.Resolved.Product.UID,
			}
		}
		if rs.Resolved.SelectedOptionsHash != "" {
			h := rs.Resolved.SelectedOptionsHash
			resolved.SelectedOptionsHash = &h
		}
		if ps := rs.Resolved.PriceSet; ps != nil {
			gps := &model.ResolvedPriceSetDefinition{
				Name:                ps.Name,
				PriceCount:          int32(ps.PriceCount),
				CompiledExpressions: ps.CompiledExpressions,
				Currencies:          ps.Currencies,
				Strategies:          ps.Strategies,
			}
			if ps.Hash != "" {
				h := ps.Hash
				gps.Hash = &h
			}
			resolved.PriceSet = gps
		}
		if inv := rs.Resolved.Inventory; inv != nil {
			ginv := &model.ResolvedInventoryDefinition{
				Managed:           inv.Managed,
				AvailableQuantity: int32(inv.AvailableQuantity),
			}
			switch inv.Policy {
			case "deny":
				p := model.InventoryPolicyDeny
				ginv.Policy = &p
			case "backorder":
				p := model.InventoryPolicyBackorder
				ginv.Policy = &p
			}
			resolved.Inventory = ginv
		}
		status.Resolved = resolved
	}
	return status
}

func datastoreRepositoryToModel(r *datastore.Repository, ns *datastore.Namespace, dataDir string) *model.Repository {
	repository, err := datastoreRepositoryToModelStrict(r, ns, dataDir)
	if err != nil {
		converterLogger.Error("failed to convert Repository", zap.Error(err))
		return nil
	}
	return repository
}

func datastoreRepositoryToModelStrict(r *datastore.Repository, ns *datastore.Namespace, dataDir string) (*model.Repository, error) {
	if r == nil {
		return nil, nil
	}
	repository := *r
	repository.Spec = append(json.RawMessage(nil), r.Spec...)
	repository.Status = append(json.RawMessage(nil), r.Status...)
	repository.OwnerReferences = append(json.RawMessage(nil), r.OwnerReferences...)
	datastore.NormalizeRepositoryContract(&repository)
	if repository.Namespace == "" {
		return nil, fmt.Errorf("Repository %q has no canonical Namespace name", repository.UID)
	}
	nodeID := mustEncodeNodeID(nodeKindRepository, repository.UID)
	namespace := repository.Namespace
	var legacyNamespace *model.Namespace
	if ns != nil {
		if ns.Name != namespace {
			return nil, fmt.Errorf("Repository %q namespace %q does not match resolved Namespace %q", repository.UID, namespace, ns.Name)
		}
		legacyNamespace = DatastoreNamespaceToGraphQL(ns)
		if legacyNamespace == nil {
			return nil, fmt.Errorf("convert Namespace %q", ns.Name)
		}
	}
	ownerReferences, err := ownerRefsFromJSONStrict(repository.OwnerReferences)
	if err != nil {
		return nil, fmt.Errorf("Repository %q: %w", repository.UID, err)
	}
	spec, err := repositorySpecFromDatastore(&repository)
	if err != nil {
		return nil, fmt.Errorf("Repository %q: %w", repository.UID, err)
	}
	storagePath := fanoutStoragePath(dataDir, repository.UID)
	status, err := repositoryStatusFromJSON(repository.Status, repository.UID, storagePath, repository.StorageClass)
	if err != nil {
		return nil, err
	}
	apiVersion := repository.APIVersion
	if apiVersion == "" {
		apiVersion = repositoryAPIVersion
	}
	kind := repository.Kind
	if kind == "" {
		kind = repositoryKind
	}
	var revision *string
	if repository.Revision != "" {
		value := repository.Revision
		revision = &value
	}
	body := repository.Body
	repo := &model.Repository{
		ID:         nodeID,
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata: &model.ObjectMeta{
			Name:              repository.Name,
			Namespace:         namespace,
			Labels:            stringMapToJSONMap(repository.Labels),
			Annotations:       stringMapToJSONMap(repository.Annotations),
			UID:               nodeID,
			ResourceVersion:   repository.ResourceVersion,
			Generation:        int32(repository.Generation),
			CreationTimestamp: repository.CreationTimestamp,
			Revision:          revision,
			OwnerReferences:   ownerReferences,
			Finalizers:        append([]string{}, repository.Finalizers...),
		},
		Spec:          spec,
		Status:        status,
		Name:          repository.Name,
		Namespace:     legacyNamespace,
		DefaultBranch: repository.DefaultBranch,
		StorageClass:  repository.StorageClass,
		StoragePath:   storagePath,
		CreatedAt:     repository.CreationTimestamp,
		CreatedBy:     repository.CreationActor,
		UpdatedAt:     repository.UpdateTimestamp,
		UpdatedBy:     repository.UpdateActor,
		Body:          &body,
	}
	return repo, nil
}

func repositorySpecFromDatastore(repository *datastore.Repository) (*model.RepositorySpec, error) {
	if len(repository.Spec) == 0 {
		return &model.RepositorySpec{
			DefaultBranch: repository.DefaultBranch,
			Visibility:    model.RepositoryVisibilityPrivate,
			PushPolicy: &model.RepositoryPushPolicy{
				MaxPackSizeBytes: repository.MaxPackSizeBytes,
				MaxFileSizeBytes: repository.MaxFileSizeBytes,
			},
		}, nil
	}
	var spec model.RepositorySpec
	if err := json.Unmarshal(repository.Spec, &spec); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}
	if spec.DefaultBranch == "" {
		spec.DefaultBranch = repository.DefaultBranch
	}
	if spec.Visibility == "" {
		spec.Visibility = model.RepositoryVisibilityPrivate
	} else if !spec.Visibility.IsValid() {
		return nil, fmt.Errorf("invalid visibility %q", spec.Visibility)
	}
	if spec.PushPolicy == nil {
		spec.PushPolicy = &model.RepositoryPushPolicy{
			MaxPackSizeBytes: repository.MaxPackSizeBytes,
			MaxFileSizeBytes: repository.MaxFileSizeBytes,
		}
	}
	return &spec, nil
}

func repositoryStatusFromJSON(raw json.RawMessage, repositoryID, storagePath, storageClass string) (*model.RepositoryStatus, error) {
	var stored struct {
		ObservedGeneration  int32          `json:"observedGeneration"`
		LastAppliedRevision string         `json:"lastAppliedRevision"`
		Conditions          []rawCondition `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("Repository %q: unmarshal status: %w", repositoryID, err)
	}
	conditions := make([]*model.Condition, 0, len(stored.Conditions))
	for _, condition := range stored.Conditions {
		conditions = append(conditions, conditionFromRaw(condition))
	}
	status := &model.RepositoryStatus{
		ObservedGeneration: stored.ObservedGeneration,
		Conditions:         conditions,
		Resolved: &model.ResolvedRepositoryDefinition{
			StoragePath:  storagePath,
			StorageClass: storageClass,
		},
	}
	if stored.LastAppliedRevision != "" {
		revision := stored.LastAppliedRevision
		status.LastAppliedRevision = &revision
	}
	return status, nil
}
