// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Type converters between datastore and GraphQL models

package graph

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"go.uber.org/zap"
)

// datastoreNamespaceToModel converts a datastore Namespace to a GraphQL model Namespace.
func datastoreNamespaceToModel(ns *datastore.Namespace) *model.Namespace {
	if ns == nil {
		return nil
	}
	var displayName *string
	if ns.DisplayName != "" {
		dn := ns.DisplayName
		displayName = &dn
	}
	var parentEnterpriseID *string
	if ns.ParentEnterpriseID != nil {
		encoded := mustEncodeNodeID(nodeKindNamespace, *ns.ParentEnterpriseID)
		parentEnterpriseID = &encoded
	}
	return &model.Namespace{
		ID:                 mustEncodeNodeID(nodeKindNamespace, ns.ID),
		Identifier:         ns.Identifier,
		DisplayName:        displayName,
		Tier:               datastoreNamespaceTierToModel(ns.Tier),
		ParentEnterpriseID: parentEnterpriseID,
		CreatedAt:          ns.CreatedAt,
		CreatedBy:          ns.CreatedBy,
		UpdatedAt:          ns.UpdatedAt,
		UpdatedBy:          ns.UpdatedBy,
	}
}

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
// enum strings ("Ready"/"True") before mapping them to GraphQL enum values
// ("READY"/"TRUE"). The generated UnmarshalJSON on model enums rejects the
// Kubernetes casing, which would cause valid system-written blobs to silently
// return nil status for every ingested product.
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

// k8sConditionTypeToGraphQL maps Kubernetes TitleCase condition type strings to
// the SCREAMING_SNAKE_CASE GraphQL enum values.
var k8sConditionTypeToGraphQL = map[string]model.ProductConditionType{
	"Published":         model.ProductConditionTypePublished,
	"AdmissionAccepted": model.ProductConditionTypeAdmissionAccepted,
	"CategoryResolved":  model.ProductConditionTypeCategoryResolved,
	"OptionsAccepted":   model.ProductConditionTypeOptionsAccepted,
	"VariantsResolved":  model.ProductConditionTypeVariantsResolved,
	"Ready":             model.ProductConditionTypeReady,
}

// k8sConditionStatusToGraphQL maps "True"/"False"/"Unknown" to their GraphQL equivalents.
var k8sConditionStatusToGraphQL = map[string]model.ConditionStatus{
	"True":    model.ConditionStatusTrue,
	"False":   model.ConditionStatusFalse,
	"Unknown": model.ConditionStatusUnknown,
}

// statusFromJSON deserialises a ProductStatus blob. A nil/empty blob returns
// nil (FR-002). Unmarshal errors are logged at WARN and also return nil.
// Condition enums are normalised from Kubernetes TitleCase to GraphQL UPPER_SNAKE_CASE.
func statusFromJSON(raw json.RawMessage) *model.ProductStatus {
	if len(raw) == 0 {
		return nil
	}
	var rs rawProductStatus
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "status"), zap.Error(err))
		return nil
	}
	conditions := make([]*model.ProductCondition, 0, len(rs.Conditions))
	for _, c := range rs.Conditions {
		condType, ok := k8sConditionTypeToGraphQL[c.Type]
		if !ok {
			// Already a GraphQL value or unknown — pass through uppercased.
			condType = model.ProductConditionType(strings.ToUpper(c.Type))
		}
		condStatus, ok := k8sConditionStatusToGraphQL[c.Status]
		if !ok {
			condStatus = model.ConditionStatus(strings.ToUpper(c.Status))
		}
		gen := c.ObservedGeneration
		cond := &model.ProductCondition{
			Type:               condType,
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
		conditions = append(conditions, cond)
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

// ownerRefsFromJSON deserialises an OwnerRefs blob. Nil/empty or unmarshal
// errors return an empty (never nil) slice.
func ownerRefsFromJSON(raw json.RawMessage) []*model.OwnerReference {
	empty := []*model.OwnerReference{}
	if len(raw) == 0 {
		return empty
	}
	var refs []*model.OwnerReference
	if err := json.Unmarshal(raw, &refs); err != nil {
		converterLogger.Warn("product blob unmarshal error", zap.String("field", "ownerRefs"), zap.Error(err))
		return empty
	}
	if refs == nil {
		return empty
	}
	return refs
}

// DatastoreProductToGraphQL converts a datastore Product to a GraphQL model Product.
func DatastoreProductToGraphQL(p *datastore.Product) *model.Product {
	if p == nil {
		return nil
	}
	gen := int32(p.Generation)
	meta := &model.ProductObjectMeta{
		Name:              p.Name,
		Namespace:         p.Namespace,
		UID:               mustEncodeNodeID(nodeKindProduct, p.UID),
		ResourceVersion:   p.ResourceVersion,
		Generation:        gen,
		CreationTimestamp: p.CreationTimestamp,
		OwnerReferences:   ownerRefsFromJSON(p.OwnerRefs),
	}
	if p.Revision != "" {
		meta.Revision = &p.Revision
	}
	if len(p.Labels) > 0 {
		labels := make(map[string]any, len(p.Labels))
		for k, v := range p.Labels {
			labels[k] = v
		}
		meta.Labels = labels
	}
	if len(p.Annotations) > 0 {
		annotations := make(map[string]any, len(p.Annotations))
		for k, v := range p.Annotations {
			annotations[k] = v
		}
		meta.Annotations = annotations
	}
	return &model.Product{
		ID:         mustEncodeNodeID(nodeKindProduct, p.UID),
		APIVersion: p.APIVersion,
		Kind:       p.Kind,
		Metadata:   meta,
		Spec:       specFromJSON(p.Spec),
		Status:     statusFromJSON(p.Status),
	}
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

	// Build labels and annotations as []*model.KeyValuePair.
	labels := kvPairs(c.Labels)
	annotations := kvPairs(c.Annotations)

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
	meta := &model.CategoryObjectMeta{
		Name:              c.Name,
		Labels:            labels,
		Annotations:       annotations,
		UID:               mustEncodeNodeID(nodeKindCategory, c.UID),
		ResourceVersion:   rv,
		Generation:        gen,
		CreationTimestamp: c.CreationTimestamp,
		OwnerReferences:   []*model.OwnerReference{},
	}
	if c.Namespace != "" {
		ns := c.Namespace
		meta.Namespace = &ns
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

	apiVersion := c.APIVersion
	kind := c.Kind
	cat := &model.Category{
		ID:         mustEncodeNodeID(nodeKindCategory, c.UID),
		APIVersion: &apiVersion,
		Kind:       &kind,
		Metadata:   meta,
		Spec:       spec,
		Status:     categoryStatusFromJSON(c.Status),
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

// kvPairs converts a string map to a []*model.KeyValuePair slice.
func kvPairs(m map[string]string) []*model.KeyValuePair {
	pairs := make([]*model.KeyValuePair, 0, len(m))
	for k, v := range m {
		kk, vv := k, v
		pairs = append(pairs, &model.KeyValuePair{Key: kk, Value: vv})
	}
	return pairs
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
	}
	if err := json.Unmarshal(raw, &rs); err != nil {
		converterLogger.Warn("category blob unmarshal error", zap.String("field", "status"), zap.Error(err))
		return nil
	}
	conditions := make([]*model.CategoryCondition, 0, len(rs.Conditions))
	for _, c := range rs.Conditions {
		cond := &model.CategoryCondition{
			Type:               c.Type,
			Status:             c.Status,
			ObservedGeneration: c.ObservedGeneration,
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
		conditions = append(conditions, cond)
	}
	return &model.CategoryTaxonomyStatus{
		ObservedGeneration:  rs.ObservedGeneration,
		LastAppliedRevision: rs.LastAppliedRevision,
		Conditions:          conditions,
	}
}

// DatastoreCollectionToGraphQL converts a datastore Collection to a GraphQL model Collection.
func DatastoreCollectionToGraphQL(c *datastore.Collection) *model.Collection {
	if c == nil {
		return nil
	}
	gen := int32(c.Generation)
	meta := &model.CollectionObjectMeta{
		Name:              c.Name,
		UID:               mustEncodeNodeID(nodeKindCollection, c.UID),
		ResourceVersion:   c.ResourceVersion,
		Generation:        gen,
		CreationTimestamp: c.CreationTimestamp,
		Labels:            kvPairs(c.Labels),
		Annotations:       kvPairs(c.Annotations),
	}
	if c.Namespace != "" {
		ns := c.Namespace
		meta.Namespace = &ns
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
		sel := &model.LabelSelector{}
		for k, v := range rs.Selector.MatchLabels {
			sel.MatchLabels = append(sel.MatchLabels, &model.KeyValuePair{Key: k, Value: v})
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
	conditions := make([]*model.CollectionCondition, 0, len(rs.Conditions))
	for _, c := range rs.Conditions {
		cond := &model.CollectionCondition{
			Type:   c.Type,
			Status: c.Status,
		}
		gen := c.ObservedGeneration
		cond.ObservedGeneration = &gen
		if c.Reason != "" {
			r := c.Reason
			cond.Reason = &r
		}
		if c.Message != "" {
			m := c.Message
			cond.Message = &m
		}
		conditions = append(conditions, cond)
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


// DatastoreRepositoryToGraphQL converts a datastore Repository to the GraphQL model
// without namespace (namespace is resolved separately via field resolver).
func DatastoreRepositoryToGraphQL(r *datastore.Repository) *model.Repository {
	if r == nil {
		return nil
	}
	return &model.Repository{
		ID:            mustEncodeNodeID(nodeKindRepository, r.ID),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		StorageClass:  r.StorageClass,
		CreatedAt:     r.CreatedAt,
		CreatedBy:     r.CreatedBy,
		UpdatedAt:     r.UpdatedAt,
		UpdatedBy:     r.UpdatedBy,
	}
}

func datastoreNamespaceTierToModel(t datastore.NamespaceTier) model.NamespaceTier {
	switch t {
	case datastore.NamespaceTierOrganisation:
		return model.NamespaceTierOrganisation
	case datastore.NamespaceTierEnterprise:
		return model.NamespaceTierEnterprise
	default:
		return model.NamespaceTierUser
	}
}

// datastoreRepositoryToModel converts a datastore Repository to the GraphQL model.
// ns may be nil if the namespace resolver has not been called yet; in that case
// the Namespace field is left nil and must be resolved via a field resolver.
func datastoreRepositoryToModel(r *datastore.Repository, ns *datastore.Namespace, dataDir string) *model.Repository {
	if r == nil {
		return nil
	}
	repo := &model.Repository{
		ID:            mustEncodeNodeID(nodeKindRepository, r.ID),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		StorageClass:  r.StorageClass,
		StoragePath:   fanoutStoragePath(dataDir, r.ID),
		CreatedAt:     r.CreatedAt,
		CreatedBy:     r.CreatedBy,
		UpdatedAt:     r.UpdatedAt,
		UpdatedBy:     r.UpdatedBy,
	}
	if ns != nil {
		repo.Namespace = datastoreNamespaceToModel(ns)
	}
	return repo
}
