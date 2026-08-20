// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3"
)

type FindingType string

const (
	FindingMissing   FindingType = "missing"
	FindingDangling  FindingType = "dangling"
	FindingStale     FindingType = "stale"
	FindingDuplicate FindingType = "duplicate"
)

type RepairActionType string

const (
	RepairInsert RepairActionType = "insert"
	RepairUpdate RepairActionType = "update"
	RepairDelete RepairActionType = "delete"
)

type AuthoritativeResource struct {
	Kind              string    `json:"kind"`
	UID               string    `json:"uid"`
	Namespace         string    `json:"namespace,omitempty"`
	Name              string    `json:"name"`
	ResourceVersion   string    `json:"resourceVersion"`
	CreationTimestamp time.Time `json:"creationTimestamp"`
	SKU               string    `json:"sku,omitempty"`
	ProductRefName    string    `json:"productRefName,omitempty"`
}

type ProjectionRecord struct {
	Table             string    `json:"table"`
	UID               string    `json:"uid"`
	Namespace         string    `json:"namespace,omitempty"`
	Name              string    `json:"name,omitempty"`
	Bucket            string    `json:"bucket,omitempty"`
	CreationTimestamp time.Time `json:"creationTimestamp,omitempty"`
	SKU               string    `json:"sku,omitempty"`
	ProductRefName    string    `json:"productRefName,omitempty"`
}

type ProjectionSnapshot struct {
	Authoritative []AuthoritativeResource `json:"authoritative"`
	Projections   []ProjectionRecord      `json:"projections"`
}

type ProjectionFinding struct {
	Type       FindingType       `json:"type"`
	Kind       string            `json:"kind"`
	UID        string            `json:"uid"`
	Table      string            `json:"table"`
	Key        string            `json:"key"`
	Expected   *ProjectionRecord `json:"expected,omitempty"`
	Actual     *ProjectionRecord `json:"actual,omitempty"`
	Repairable bool              `json:"repairable"`
	Reason     string            `json:"reason,omitempty"`
}

type RepairAction struct {
	Type                    RepairActionType  `json:"type"`
	Kind                    string            `json:"kind"`
	UID                     string            `json:"uid"`
	ResourceNamespace       string            `json:"resourceNamespace,omitempty"`
	ExpectedResourceVersion string            `json:"expectedResourceVersion,omitempty"`
	RequireAbsentResource   bool              `json:"requireAbsentResource,omitempty"`
	Before                  *ProjectionRecord `json:"before,omitempty"`
	After                   *ProjectionRecord `json:"after,omitempty"`
}

type RepairPlan struct {
	Findings []ProjectionFinding `json:"findings"`
	Actions  []RepairAction      `json:"actions"`
}

type RepairResult struct {
	PlannedActions int        `json:"plannedActions"`
	AppliedActions int        `json:"appliedActions"`
	Verification   RepairPlan `json:"verification"`
}

type projectionRepairStore interface {
	Snapshot(context.Context) (ProjectionSnapshot, error)
	LookupResource(context.Context, RepairAction) (*AuthoritativeResource, error)
	ApplyAction(context.Context, RepairAction) (bool, error)
	Close()
}

type ProjectionRepairService struct {
	store projectionRepairStore
}

func OpenProjectionRepairService(cfg config.ScyllaConfig) (*ProjectionRepairService, error) {
	hosts, port := parseRepairHosts(cfg.Hosts)
	cluster := gocql.NewCluster(hosts...)
	cluster.Keyspace = cfg.Keyspace
	cluster.Consistency = gocql.Quorum
	cluster.DisableShardAwarePort = cfg.DisableShardAwarePort
	cluster.IgnorePeerAddr = cfg.IgnorePeerAddr
	if port > 0 {
		cluster.Port = port
	}
	if cfg.Username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{Username: cfg.Username, Password: cfg.Password}
	}
	if cfg.TLS {
		cluster.SslOpts = &gocql.SslOptions{EnableHostVerification: true}
	}
	raw, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("scylla projection repair: open session: %w", err)
	}
	return &ProjectionRepairService{store: &scyllaProjectionRepairStore{
		session: gocqlx.NewSession(raw),
		raw:     raw,
	}}, nil
}

func parseRepairHosts(hosts []string) ([]string, int) {
	parsed := make([]string, 0, len(hosts))
	port := 0
	for _, address := range hosts {
		address = strings.TrimSpace(address)
		host, rawPort, err := net.SplitHostPort(address)
		if err != nil {
			parsed = append(parsed, address)
			continue
		}
		parsed = append(parsed, host)
		if value, parseErr := strconv.Atoi(rawPort); parseErr == nil && value > 0 {
			port = value
		}
	}
	return parsed, port
}

func (s *ProjectionRepairService) Close() {
	if s != nil && s.store != nil {
		s.store.Close()
	}
}

func (s *ProjectionRepairService) Audit(ctx context.Context) (RepairPlan, error) {
	if s == nil || s.store == nil {
		return RepairPlan{}, errors.New("projection repair service is not initialized")
	}
	snapshot, err := s.store.Snapshot(ctx)
	if err != nil {
		return RepairPlan{}, err
	}
	return BuildRepairPlan(snapshot)
}

func (s *ProjectionRepairService) Apply(ctx context.Context, plan RepairPlan) (RepairResult, error) {
	if s == nil || s.store == nil {
		return RepairResult{}, errors.New("projection repair service is not initialized")
	}
	if err := ValidateRepairPlan(plan); err != nil {
		return RepairResult{}, err
	}
	result := RepairResult{PlannedActions: len(plan.Actions)}
	for _, action := range plan.Actions {
		resource, err := s.store.LookupResource(ctx, action)
		if err != nil {
			return result, fmt.Errorf("guard %s %s/%s: %w", action.Type, action.Kind, action.UID, err)
		}
		if action.RequireAbsentResource {
			if resource != nil {
				return result, fmt.Errorf("guard %s %s/%s: authoritative resource appeared concurrently", action.Type, action.Kind, action.UID)
			}
		} else {
			if resource == nil {
				return result, fmt.Errorf("guard %s %s/%s: authoritative resource disappeared concurrently", action.Type, action.Kind, action.UID)
			}
			if resource.ResourceVersion != action.ExpectedResourceVersion {
				return result, fmt.Errorf(
					"guard %s %s/%s: resource version changed from %q to %q",
					action.Type, action.Kind, action.UID, action.ExpectedResourceVersion, resource.ResourceVersion,
				)
			}
		}
		applied, err := s.store.ApplyAction(ctx, action)
		if err != nil {
			return result, fmt.Errorf("apply %s to %s: %w", action.Type, actionTable(action), err)
		}
		if !applied {
			return result, fmt.Errorf("apply %s to %s: conditional mutation was not applied", action.Type, actionTable(action))
		}
		result.AppliedActions++
	}

	verification, err := s.Audit(ctx)
	if err != nil {
		return result, fmt.Errorf("post-repair audit: %w", err)
	}
	result.Verification = verification
	if len(verification.Findings) != 0 {
		return result, fmt.Errorf("post-repair verification found %d projection inconsistencies", len(verification.Findings))
	}
	return result, nil
}

func BuildRepairPlan(snapshot ProjectionSnapshot) (RepairPlan, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return RepairPlan{}, err
	}

	resources := make(map[string]AuthoritativeResource, len(snapshot.Authoritative))
	expectedByKey := make(map[string]ProjectionRecord)
	expectedByOwner := make(map[string][]ProjectionRecord)
	for _, resource := range snapshot.Authoritative {
		resources[resourceKey(resource.Kind, resource.UID)] = resource
		for _, projection := range expectedProjections(resource) {
			key := projectionMapKey(projection)
			if owner, exists := expectedByKey[key]; exists && owner.UID != projection.UID {
				return RepairPlan{}, fmt.Errorf(
					"authoritative conflict: %s key %q is claimed by %s and %s",
					projection.Table, projection.Key(), owner.UID, projection.UID,
				)
			}
			expectedByKey[key] = projection
			ownerKey := projectionOwnerKey(resource.Kind, projection.Table, projection.UID)
			expectedByOwner[ownerKey] = append(expectedByOwner[ownerKey], projection)
		}
	}

	actualByKey := make(map[string]ProjectionRecord, len(snapshot.Projections))
	for _, projection := range snapshot.Projections {
		key := projectionMapKey(projection)
		if _, exists := actualByKey[key]; exists {
			return RepairPlan{}, fmt.Errorf("snapshot contains duplicate physical projection key %s/%s", projection.Table, projection.Key())
		}
		actualByKey[key] = projection
	}

	plan := RepairPlan{}
	for key, expected := range expectedByKey {
		resource := resources[resourceKey(projectionKind(expected.Table), expected.UID)]
		actual, exists := actualByKey[key]
		switch {
		case !exists:
			plan.Findings = append(plan.Findings, finding(FindingMissing, resource.Kind, expected.UID, expected, ProjectionRecord{}, true, "projection row is absent"))
			plan.Actions = append(plan.Actions, insertAction(resource, expected))
		case actual.UID == expected.UID && !actual.Equal(expected):
			plan.Findings = append(plan.Findings, finding(FindingStale, resource.Kind, expected.UID, expected, actual, true, "projection values do not match the authoritative row"))
			plan.Actions = append(plan.Actions, updateAction(resource, actual, expected))
		case actual.UID != expected.UID:
			competingKind := projectionKind(actual.Table)
			competing, competingExists := resources[resourceKey(competingKind, actual.UID)]
			competingClaimsKey := false
			if competingExists {
				for _, candidate := range expectedByOwner[projectionOwnerKey(competing.Kind, actual.Table, actual.UID)] {
					if projectionMapKey(candidate) == key {
						competingClaimsKey = true
						break
					}
				}
			}
			repairable := !competingClaimsKey && projectionDeleteRepairable(actual.Table)
			reason := "projection key points to a different resource"
			if competingClaimsKey {
				reason = "a valid competing authoritative resource claims this unique key"
			} else if !projectionDeleteRepairable(actual.Table) {
				reason = "projection participates in write reservation and cannot be deleted safely online"
			}
			plan.Findings = append(plan.Findings, finding(FindingStale, resource.Kind, expected.UID, expected, actual, repairable, reason))
			if repairable {
				if !competingExists {
					competing = AuthoritativeResource{Kind: competingKind, UID: actual.UID, Namespace: actual.Namespace}
				}
				plan.Actions = append(plan.Actions, deleteAction(competing, competingExists, actual))
				plan.Actions = append(plan.Actions, insertAction(resource, expected))
			}
		}
	}

	for key, actual := range actualByKey {
		expectedAtKey, expectedKeyExists := expectedByKey[key]
		if expectedKeyExists {
			if expectedAtKey.UID == actual.UID {
				continue
			}
			continue
		}
		kind := projectionKind(actual.Table)
		resource, exists := resources[resourceKey(kind, actual.UID)]
		if !exists {
			repairable := projectionDeleteRepairable(actual.Table)
			reason := "projection owner has no authoritative row"
			if !repairable {
				reason = "projection may be an in-flight write reservation and cannot be deleted safely online"
			}
			plan.Findings = append(plan.Findings, finding(FindingDangling, kind, actual.UID, ProjectionRecord{}, actual, repairable, reason))
			if repairable {
				plan.Actions = append(plan.Actions, deleteAction(AuthoritativeResource{Kind: kind, UID: actual.UID}, false, actual))
			}
			continue
		}
		owned := expectedByOwner[projectionOwnerKey(kind, actual.Table, actual.UID)]
		findingType := FindingStale
		reason := "projection key does not match the authoritative row"
		for _, expected := range owned {
			if candidate, ok := actualByKey[projectionMapKey(expected)]; ok && candidate.Equal(expected) {
				findingType = FindingDuplicate
				reason = "a valid projection exists at the authoritative key"
				break
			}
		}
		repairable := projectionDeleteRepairable(actual.Table)
		if !repairable {
			reason = "projection participates in write reservation and cannot be deleted safely online"
		}
		plan.Findings = append(plan.Findings, finding(findingType, kind, actual.UID, firstProjection(owned), actual, repairable, reason))
		if repairable {
			plan.Actions = append(plan.Actions, deleteAction(resource, true, actual))
		}
	}

	sort.Slice(plan.Findings, func(i, j int) bool {
		left, right := plan.Findings[i], plan.Findings[j]
		return strings.Join([]string{left.Table, left.Key, string(left.Type), left.UID}, "\x00") <
			strings.Join([]string{right.Table, right.Key, string(right.Type), right.UID}, "\x00")
	})
	sort.SliceStable(plan.Actions, func(i, j int) bool {
		left, right := plan.Actions[i], plan.Actions[j]
		leftOrder, rightOrder := actionOrder(left.Type), actionOrder(right.Type)
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return strings.Join([]string{actionTable(left), actionKey(left), left.UID}, "\x00") <
			strings.Join([]string{actionTable(right), actionKey(right), right.UID}, "\x00")
	})
	return plan, nil
}

func ValidateRepairPlan(plan RepairPlan) error {
	for i, finding := range plan.Findings {
		if !finding.Repairable {
			return fmt.Errorf("finding %d for %s/%s is not automatically repairable: %s", i, finding.Table, finding.Key, finding.Reason)
		}
	}
	for i, action := range plan.Actions {
		if action.Type != RepairInsert && action.Type != RepairUpdate && action.Type != RepairDelete {
			return fmt.Errorf("action %d has invalid type %q", i, action.Type)
		}
		if action.Kind == "" || action.UID == "" {
			return fmt.Errorf("action %d requires kind and uid", i)
		}
		if _, err := gocql.ParseUUID(action.UID); err != nil {
			return fmt.Errorf("action %d has invalid uid %q", i, action.UID)
		}
		if action.RequireAbsentResource && action.ExpectedResourceVersion != "" {
			return fmt.Errorf("action %d cannot require both an absent resource and a resource version", i)
		}
		if !action.RequireAbsentResource && action.ExpectedResourceVersion == "" {
			return fmt.Errorf("action %d requires an expected resource version", i)
		}
		switch action.Type {
		case RepairInsert:
			if action.After == nil || action.Before != nil {
				return fmt.Errorf("action %d insert requires only an after projection", i)
			}
			if action.RequireAbsentResource {
				return fmt.Errorf("action %d insert requires an authoritative resource", i)
			}
		case RepairUpdate:
			if action.Before == nil || action.After == nil {
				return fmt.Errorf("action %d update requires before and after projections", i)
			}
			if action.Before.Table != action.After.Table {
				return fmt.Errorf("action %d update cannot change projection tables", i)
			}
		case RepairDelete:
			if action.Before == nil || action.After != nil {
				return fmt.Errorf("action %d delete requires only a before projection", i)
			}
		}
		projection := action.Before
		if projection == nil {
			projection = action.After
		}
		if projection == nil || !knownProjectionTable(projection.Table) {
			return fmt.Errorf("action %d has an invalid projection table", i)
		}
		if projection.UID != action.UID {
			return fmt.Errorf("action %d uid does not match its projection owner", i)
		}
		if projectionKind(projection.Table) != action.Kind {
			return fmt.Errorf("action %d kind does not match projection table %s", i, projection.Table)
		}
	}
	return nil
}

func validateSnapshot(snapshot ProjectionSnapshot) error {
	seenResources := make(map[string]struct{}, len(snapshot.Authoritative))
	for i, resource := range snapshot.Authoritative {
		if resource.Kind == "" || resource.UID == "" || resource.Name == "" || resource.ResourceVersion == "" || resource.CreationTimestamp.IsZero() {
			return fmt.Errorf("authoritative resource %d is missing kind, uid, name, version, or creation timestamp", i)
		}
		if _, err := gocql.ParseUUID(resource.UID); err != nil {
			return fmt.Errorf("authoritative resource %s has invalid uid %q", resource.Kind, resource.UID)
		}
		if resource.Kind != "Namespace" && resource.Namespace == "" {
			return fmt.Errorf("authoritative resource %s/%s requires namespace", resource.Kind, resource.UID)
		}
		key := resourceKey(resource.Kind, resource.UID)
		if _, exists := seenResources[key]; exists {
			return fmt.Errorf("duplicate authoritative resource %s", key)
		}
		seenResources[key] = struct{}{}
	}
	for i, projection := range snapshot.Projections {
		if projection.Table == "" || projection.UID == "" || !knownProjectionTable(projection.Table) {
			return fmt.Errorf("projection %d has invalid table or uid", i)
		}
		if _, err := gocql.ParseUUID(projection.UID); err != nil {
			return fmt.Errorf("projection %d has invalid uid %q", i, projection.UID)
		}
	}
	return nil
}

func expectedProjections(resource AuthoritativeResource) []ProjectionRecord {
	base := ProjectionRecord{
		UID: resource.UID, Namespace: resource.Namespace, Name: resource.Name,
		CreationTimestamp: resource.CreationTimestamp, Bucket: resource.CreationTimestamp.UTC().Format("2006-01"),
		SKU: resource.SKU, ProductRefName: resource.ProductRefName,
	}
	switch resource.Kind {
	case "Namespace":
		name, bucket := base, base
		name.Table = "namespaces_by_name"
		bucket.Table = "namespaces_by_bucket"
		return []ProjectionRecord{name, bucket}
	case "Repository":
		namespace, global, forward, reverse := base, base, base, base
		namespace.Table = "repositories_by_namespace"
		global.Table = "repositories_by_bucket"
		forward.Table = "namespace_mappings"
		reverse.Table = "namespace_mappings_by_repository"
		return []ProjectionRecord{namespace, global, forward, reverse}
	case "Product":
		return catalogProjections(base, "products_by_name", "products_by_uid")
	case "CategoryTaxonomy":
		return catalogProjections(base, "category_taxonomy_by_name", "category_taxonomy_by_uid")
	case "Collection":
		return catalogProjections(base, "collection_by_name", "collection_by_uid")
	case "ProductVariant":
		result := catalogProjections(base, "product_variant_by_name", "product_variant_by_uid")
		if resource.SKU != "" {
			sku := base
			sku.Table = "product_variant_by_sku"
			result = append(result, sku)
		}
		if resource.ProductRefName != "" {
			ref := base
			ref.Table = "product_variant_by_product_ref"
			result = append(result, ref)
		}
		return result
	default:
		return nil
	}
}

func catalogProjections(base ProjectionRecord, nameTable, uidTable string) []ProjectionRecord {
	name, uid := base, base
	name.Table = nameTable
	uid.Table = uidTable
	return []ProjectionRecord{name, uid}
}

func (p ProjectionRecord) Key() string {
	ts := p.CreationTimestamp.UTC().Format(time.RFC3339Nano)
	switch p.Table {
	case "namespaces_by_name":
		return p.Name
	case "namespaces_by_bucket":
		return strings.Join([]string{p.Bucket, ts, p.UID}, "/")
	case "repositories_by_namespace":
		return strings.Join([]string{p.Namespace, p.Bucket, ts, p.UID}, "/")
	case "repositories_by_bucket":
		return strings.Join([]string{p.Bucket, ts, p.UID}, "/")
	case "namespace_mappings":
		return p.Namespace + "/" + p.Name
	case "namespace_mappings_by_repository":
		return p.UID
	case "products_by_name", "category_taxonomy_by_name", "collection_by_name", "product_variant_by_name":
		return p.Namespace + "/" + p.Name
	case "products_by_uid", "category_taxonomy_by_uid", "collection_by_uid", "product_variant_by_uid":
		return p.UID
	case "product_variant_by_sku":
		return p.Namespace + "/" + p.SKU
	case "product_variant_by_product_ref":
		return strings.Join([]string{p.Namespace, p.ProductRefName, ts, p.UID}, "/")
	default:
		return ""
	}
}

func (p ProjectionRecord) Equal(other ProjectionRecord) bool {
	if p.Table != other.Table || p.UID != other.UID {
		return false
	}
	switch p.Table {
	case "namespaces_by_name":
		return p.Name == other.Name
	case "namespaces_by_bucket":
		return p.Bucket == other.Bucket && p.CreationTimestamp.Equal(other.CreationTimestamp)
	case "repositories_by_namespace":
		return p.Namespace == other.Namespace && p.Bucket == other.Bucket && p.CreationTimestamp.Equal(other.CreationTimestamp)
	case "repositories_by_bucket":
		return p.Bucket == other.Bucket && p.CreationTimestamp.Equal(other.CreationTimestamp)
	case "namespace_mappings", "namespace_mappings_by_repository":
		return p.Namespace == other.Namespace && p.Name == other.Name
	case "products_by_name", "category_taxonomy_by_name", "collection_by_name", "product_variant_by_name":
		return p.Namespace == other.Namespace && p.Name == other.Name && p.CreationTimestamp.Equal(other.CreationTimestamp)
	case "products_by_uid", "category_taxonomy_by_uid", "collection_by_uid", "product_variant_by_uid":
		return p.Namespace == other.Namespace && p.CreationTimestamp.Equal(other.CreationTimestamp)
	case "product_variant_by_sku":
		return p.Namespace == other.Namespace && p.SKU == other.SKU && p.CreationTimestamp.Equal(other.CreationTimestamp)
	case "product_variant_by_product_ref":
		return p.Namespace == other.Namespace && p.ProductRefName == other.ProductRefName && p.CreationTimestamp.Equal(other.CreationTimestamp)
	default:
		return false
	}
}

func projectionKind(table string) string {
	switch {
	case strings.HasPrefix(table, "namespaces_"):
		return "Namespace"
	case strings.HasPrefix(table, "repositories_"), strings.HasPrefix(table, "namespace_mappings"):
		return "Repository"
	case strings.HasPrefix(table, "products_"):
		return "Product"
	case strings.HasPrefix(table, "category_taxonomy_"):
		return "CategoryTaxonomy"
	case strings.HasPrefix(table, "collection_"):
		return "Collection"
	case strings.HasPrefix(table, "product_variant_"):
		return "ProductVariant"
	default:
		return ""
	}
}

func knownProjectionTable(table string) bool {
	switch table {
	case "namespaces_by_name",
		"namespaces_by_bucket",
		"repositories_by_namespace",
		"repositories_by_bucket",
		"namespace_mappings",
		"namespace_mappings_by_repository",
		"products_by_name",
		"products_by_uid",
		"category_taxonomy_by_name",
		"category_taxonomy_by_uid",
		"collection_by_name",
		"collection_by_uid",
		"product_variant_by_name",
		"product_variant_by_uid",
		"product_variant_by_sku",
		"product_variant_by_product_ref":
		return true
	default:
		return false
	}
}

func resourceKey(kind, uid string) string {
	return kind + "\x00" + uid
}

func projectionMapKey(projection ProjectionRecord) string {
	return projection.Table + "\x00" + projection.Key()
}

func projectionOwnerKey(kind, table, uid string) string {
	return strings.Join([]string{kind, table, uid}, "\x00")
}

func finding(kind FindingType, resourceKind, uid string, expected, actual ProjectionRecord, repairable bool, reason string) ProjectionFinding {
	result := ProjectionFinding{
		Type: kind, Kind: resourceKind, UID: uid, Repairable: repairable, Reason: reason,
	}
	if expected.Table != "" {
		copy := expected
		result.Expected = &copy
		result.Table = expected.Table
		result.Key = expected.Key()
	}
	if actual.Table != "" {
		copy := actual
		result.Actual = &copy
		if result.Table == "" {
			result.Table = actual.Table
			result.Key = actual.Key()
		}
	}
	return result
}

func insertAction(resource AuthoritativeResource, after ProjectionRecord) RepairAction {
	return RepairAction{
		Type: RepairInsert, Kind: resource.Kind, UID: resource.UID,
		ResourceNamespace:       resource.Namespace,
		ExpectedResourceVersion: resource.ResourceVersion, After: &after,
	}
}

func updateAction(resource AuthoritativeResource, before, after ProjectionRecord) RepairAction {
	return RepairAction{
		Type: RepairUpdate, Kind: resource.Kind, UID: resource.UID,
		ResourceNamespace:       resource.Namespace,
		ExpectedResourceVersion: resource.ResourceVersion, Before: &before, After: &after,
	}
}

func deleteAction(resource AuthoritativeResource, exists bool, before ProjectionRecord) RepairAction {
	action := RepairAction{
		Type: RepairDelete, Kind: resource.Kind, UID: resource.UID,
		ResourceNamespace: resource.Namespace, Before: &before,
	}
	if exists {
		action.ExpectedResourceVersion = resource.ResourceVersion
	} else {
		action.RequireAbsentResource = true
	}
	return action
}

func firstProjection(projections []ProjectionRecord) ProjectionRecord {
	if len(projections) == 0 {
		return ProjectionRecord{}
	}
	return projections[0]
}

func actionOrder(action RepairActionType) int {
	switch action {
	case RepairDelete:
		return 0
	case RepairUpdate:
		return 1
	default:
		return 2
	}
}

func actionTable(action RepairAction) string {
	if action.After != nil {
		return action.After.Table
	}
	if action.Before != nil {
		return action.Before.Table
	}
	return ""
}

func actionKey(action RepairAction) string {
	if action.After != nil {
		return action.After.Key()
	}
	if action.Before != nil {
		return action.Before.Key()
	}
	return ""
}

func projectionDeleteRepairable(table string) bool {
	switch table {
	case "namespaces_by_name",
		"namespace_mappings",
		"namespace_mappings_by_repository",
		"products_by_name",
		"products_by_uid",
		"category_taxonomy_by_name",
		"category_taxonomy_by_uid",
		"collection_by_name",
		"collection_by_uid",
		"product_variant_by_name",
		"product_variant_by_uid",
		"product_variant_by_sku":
		return false
	default:
		return true
	}
}

type scyllaProjectionRepairStore struct {
	session gocqlx.Session
	raw     *gocql.Session
}

func (s *scyllaProjectionRepairStore) Close() {
	s.raw.Close()
}

type auditRow struct {
	UID               gocql.UUID `db:"uid"`
	RepositoryID      gocql.UUID `db:"repository_id"`
	Namespace         string     `db:"namespace"`
	Name              string     `db:"name"`
	ResourceVersion   string     `db:"resource_version"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
	Bucket            string     `db:"bucket"`
	SKU               string     `db:"sku"`
	ProductRefName    string     `db:"product_ref_name"`
}

func (s *scyllaProjectionRepairStore) Snapshot(ctx context.Context) (ProjectionSnapshot, error) {
	var snapshot ProjectionSnapshot
	authoritative := []struct {
		kind      string
		table     string
		extraCols string
	}{
		{"Namespace", "namespaces_by_uid", ""},
		{"Repository", "repositories_by_uid", "namespace,"},
		{"Product", "products_by_namespace", "namespace,"},
		{"CategoryTaxonomy", "category_taxonomy", "namespace,"},
		{"Collection", "collection", "namespace,"},
		{"ProductVariant", "product_variant_by_namespace", "namespace,sku,product_ref_name,"},
	}
	for _, source := range authoritative {
		statement := fmt.Sprintf(
			"SELECT %s uid,name,resource_version,creation_timestamp FROM %s",
			source.extraCols, source.table,
		)
		rows, err := s.scanAuditRows(ctx, statement)
		if err != nil {
			return ProjectionSnapshot{}, fmt.Errorf("audit authoritative table %s: %w", source.table, err)
		}
		for _, row := range rows {
			snapshot.Authoritative = append(snapshot.Authoritative, AuthoritativeResource{
				Kind: source.kind, UID: row.UID.String(), Namespace: row.Namespace, Name: row.Name,
				ResourceVersion: row.ResourceVersion, CreationTimestamp: row.CreationTimestamp,
				SKU: row.SKU, ProductRefName: row.ProductRefName,
			})
		}
	}

	projections := []struct {
		table   string
		columns string
		uid     func(auditRow) gocql.UUID
	}{
		{"namespaces_by_name", "name,uid", rowUID},
		{"namespaces_by_bucket", "bucket,creation_timestamp,uid", rowUID},
		{"repositories_by_namespace", "namespace,bucket,creation_timestamp,uid", rowUID},
		{"repositories_by_bucket", "bucket,creation_timestamp,uid", rowUID},
		{"namespace_mappings", "namespace,name,repository_id", rowRepositoryID},
		{"namespace_mappings_by_repository", "repository_id,namespace,name", rowRepositoryID},
		{"products_by_name", "namespace,name,uid,creation_timestamp", rowUID},
		{"products_by_uid", "uid,namespace,creation_timestamp", rowUID},
		{"category_taxonomy_by_name", "namespace,name,uid,creation_timestamp", rowUID},
		{"category_taxonomy_by_uid", "uid,namespace,creation_timestamp", rowUID},
		{"collection_by_name", "namespace,name,uid,creation_timestamp", rowUID},
		{"collection_by_uid", "uid,namespace,creation_timestamp", rowUID},
		{"product_variant_by_name", "namespace,name,uid,creation_timestamp", rowUID},
		{"product_variant_by_uid", "uid,namespace,creation_timestamp", rowUID},
		{"product_variant_by_sku", "namespace,sku,uid,creation_timestamp", rowUID},
		{"product_variant_by_product_ref", "namespace,product_ref_name,creation_timestamp,uid", rowUID},
	}
	for _, source := range projections {
		rows, err := s.scanAuditRows(ctx, "SELECT "+source.columns+" FROM "+source.table)
		if err != nil {
			return ProjectionSnapshot{}, fmt.Errorf("audit projection table %s: %w", source.table, err)
		}
		for _, row := range rows {
			uid := source.uid(row)
			snapshot.Projections = append(snapshot.Projections, ProjectionRecord{
				Table: source.table, UID: uid.String(), Namespace: row.Namespace, Name: row.Name,
				Bucket: row.Bucket, CreationTimestamp: row.CreationTimestamp,
				SKU: row.SKU, ProductRefName: row.ProductRefName,
			})
		}
	}
	return snapshot, nil
}

func (s *scyllaProjectionRepairStore) scanAuditRows(ctx context.Context, statement string) ([]auditRow, error) {
	iter := s.session.Query(statement, nil).WithContext(ctx).PageSize(1000).Iter()
	rows := make([]auditRow, 0, 1000)
	var row auditRow
	for iter.StructScan(&row) {
		rows = append(rows, row)
		row = auditRow{}
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return rows, nil
}

func rowUID(row auditRow) gocql.UUID {
	return row.UID
}

func rowRepositoryID(row auditRow) gocql.UUID {
	return row.RepositoryID
}

func (s *scyllaProjectionRepairStore) LookupResource(ctx context.Context, action RepairAction) (*AuthoritativeResource, error) {
	var rows []auditRow
	var statement string
	var args []any
	switch action.Kind {
	case "Namespace":
		statement, args = "SELECT uid,name,resource_version,creation_timestamp FROM namespaces_by_uid WHERE uid=?", []any{mustRepairUUID(action.UID)}
	case "Repository":
		statement, args = "SELECT uid,namespace,name,resource_version,creation_timestamp FROM repositories_by_uid WHERE uid=?", []any{mustRepairUUID(action.UID)}
	default:
		table := authoritativeTable(action.Kind)
		namespace := actionNamespace(action)
		if table == "" || namespace == "" {
			return nil, fmt.Errorf("cannot locate authoritative %s/%s", action.Kind, action.UID)
		}
		extras := ""
		if action.Kind == "ProductVariant" {
			extras = ",sku,product_ref_name"
		}
		statement = "SELECT uid,namespace,name,resource_version,creation_timestamp" + extras + " FROM " + table + " WHERE namespace=?"
		args = []any{namespace}
	}
	if err := s.session.Query(statement, nil).WithContext(ctx).Bind(args...).SelectRelease(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.UID.String() == action.UID {
			return &AuthoritativeResource{
				Kind: action.Kind, UID: action.UID, Namespace: row.Namespace, Name: row.Name,
				ResourceVersion: row.ResourceVersion, CreationTimestamp: row.CreationTimestamp,
				SKU: row.SKU, ProductRefName: row.ProductRefName,
			}, nil
		}
	}
	return nil, nil
}

func authoritativeTable(kind string) string {
	switch kind {
	case "Product":
		return "products_by_namespace"
	case "CategoryTaxonomy":
		return "category_taxonomy"
	case "Collection":
		return "collection"
	case "ProductVariant":
		return "product_variant_by_namespace"
	default:
		return ""
	}
}

func actionNamespace(action RepairAction) string {
	if action.ResourceNamespace != "" {
		return action.ResourceNamespace
	}
	if action.After != nil && action.After.Namespace != "" {
		return action.After.Namespace
	}
	if action.Before != nil {
		return action.Before.Namespace
	}
	return ""
}

func mustRepairUUID(value string) gocql.UUID {
	uid, _ := gocql.ParseUUID(value)
	return uid
}

func (s *scyllaProjectionRepairStore) ApplyAction(ctx context.Context, action RepairAction) (bool, error) {
	switch action.Type {
	case RepairInsert:
		return s.insertProjection(ctx, *action.After)
	case RepairUpdate:
		return s.updateProjection(ctx, *action.Before, *action.After)
	case RepairDelete:
		resource, err := s.LookupResource(ctx, action)
		if err != nil {
			return false, err
		}
		if resource != nil {
			return false, nil
		}
		return s.deleteProjection(ctx, *action.Before)
	default:
		return false, fmt.Errorf("unsupported repair action %q", action.Type)
	}
}

func (s *scyllaProjectionRepairStore) insertProjection(ctx context.Context, row ProjectionRecord) (bool, error) {
	uid := mustRepairUUID(row.UID)
	var statement string
	var args []any
	switch row.Table {
	case "namespaces_by_name":
		statement, args = "INSERT INTO namespaces_by_name (name,uid) VALUES (?,?) IF NOT EXISTS", []any{row.Name, uid}
	case "namespaces_by_bucket":
		statement, args = "INSERT INTO namespaces_by_bucket (bucket,creation_timestamp,uid) VALUES (?,?,?) IF NOT EXISTS", []any{row.Bucket, row.CreationTimestamp, uid}
	case "repositories_by_namespace":
		statement, args = "INSERT INTO repositories_by_namespace (namespace,bucket,creation_timestamp,uid) VALUES (?,?,?,?) IF NOT EXISTS", []any{row.Namespace, row.Bucket, row.CreationTimestamp, uid}
	case "repositories_by_bucket":
		statement, args = "INSERT INTO repositories_by_bucket (bucket,creation_timestamp,uid) VALUES (?,?,?) IF NOT EXISTS", []any{row.Bucket, row.CreationTimestamp, uid}
	case "namespace_mappings":
		statement, args = "INSERT INTO namespace_mappings (namespace,name,repository_id) VALUES (?,?,?) IF NOT EXISTS", []any{row.Namespace, row.Name, uid}
	case "namespace_mappings_by_repository":
		statement, args = "INSERT INTO namespace_mappings_by_repository (repository_id,namespace,name) VALUES (?,?,?) IF NOT EXISTS", []any{uid, row.Namespace, row.Name}
	case "products_by_name", "category_taxonomy_by_name", "collection_by_name", "product_variant_by_name":
		statement, args = fmt.Sprintf("INSERT INTO %s (namespace,name,uid,creation_timestamp) VALUES (?,?,?,?) IF NOT EXISTS", row.Table), []any{row.Namespace, row.Name, uid, row.CreationTimestamp}
	case "products_by_uid", "category_taxonomy_by_uid", "collection_by_uid", "product_variant_by_uid":
		statement, args = fmt.Sprintf("INSERT INTO %s (uid,namespace,creation_timestamp) VALUES (?,?,?) IF NOT EXISTS", row.Table), []any{uid, row.Namespace, row.CreationTimestamp}
	case "product_variant_by_sku":
		statement, args = "INSERT INTO product_variant_by_sku (namespace,sku,uid,creation_timestamp) VALUES (?,?,?,?) IF NOT EXISTS", []any{row.Namespace, row.SKU, uid, row.CreationTimestamp}
	case "product_variant_by_product_ref":
		statement, args = "INSERT INTO product_variant_by_product_ref (namespace,product_ref_name,creation_timestamp,uid) VALUES (?,?,?,?) IF NOT EXISTS", []any{row.Namespace, row.ProductRefName, row.CreationTimestamp, uid}
	default:
		return false, fmt.Errorf("unsupported projection table %q", row.Table)
	}
	return s.session.Query(statement, nil).WithContext(ctx).Bind(args...).ExecCASRelease()
}

func (s *scyllaProjectionRepairStore) updateProjection(ctx context.Context, before, after ProjectionRecord) (bool, error) {
	uid := mustRepairUUID(after.UID)
	var statement string
	var args []any
	switch after.Table {
	case "namespaces_by_name":
		statement, args = "UPDATE namespaces_by_name SET uid=? WHERE name=? IF uid=?", []any{uid, after.Name, mustRepairUUID(before.UID)}
	case "namespace_mappings":
		statement, args = "UPDATE namespace_mappings SET repository_id=? WHERE namespace=? AND name=? IF repository_id=?", []any{uid, after.Namespace, after.Name, mustRepairUUID(before.UID)}
	case "namespace_mappings_by_repository":
		statement, args = "UPDATE namespace_mappings_by_repository SET namespace=?,name=? WHERE repository_id=? IF namespace=? AND name=?", []any{after.Namespace, after.Name, uid, before.Namespace, before.Name}
	case "products_by_name", "category_taxonomy_by_name", "collection_by_name", "product_variant_by_name":
		statement = fmt.Sprintf("UPDATE %s SET uid=?,creation_timestamp=? WHERE namespace=? AND name=? IF uid=? AND creation_timestamp=?", after.Table)
		args = []any{uid, after.CreationTimestamp, after.Namespace, after.Name, mustRepairUUID(before.UID), before.CreationTimestamp}
	case "products_by_uid", "category_taxonomy_by_uid", "collection_by_uid", "product_variant_by_uid":
		statement = fmt.Sprintf("UPDATE %s SET namespace=?,creation_timestamp=? WHERE uid=? IF namespace=? AND creation_timestamp=?", after.Table)
		args = []any{after.Namespace, after.CreationTimestamp, uid, before.Namespace, before.CreationTimestamp}
	case "product_variant_by_sku":
		statement = "UPDATE product_variant_by_sku SET uid=?,creation_timestamp=? WHERE namespace=? AND sku=? IF uid=? AND creation_timestamp=?"
		args = []any{uid, after.CreationTimestamp, after.Namespace, after.SKU, mustRepairUUID(before.UID), before.CreationTimestamp}
	default:
		return false, fmt.Errorf("projection %s cannot be updated in place", after.Table)
	}
	return s.session.Query(statement, nil).WithContext(ctx).Bind(args...).ExecCASRelease()
}

func (s *scyllaProjectionRepairStore) deleteProjection(ctx context.Context, row ProjectionRecord) (bool, error) {
	uid := mustRepairUUID(row.UID)
	var statement string
	var args []any
	switch row.Table {
	case "namespaces_by_name":
		statement, args = "DELETE FROM namespaces_by_name WHERE name=? IF uid=?", []any{row.Name, uid}
	case "namespaces_by_bucket":
		statement, args = "DELETE FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=? IF EXISTS", []any{row.Bucket, row.CreationTimestamp, uid}
	case "repositories_by_namespace":
		statement, args = "DELETE FROM repositories_by_namespace WHERE namespace=? AND bucket=? AND creation_timestamp=? AND uid=? IF EXISTS", []any{row.Namespace, row.Bucket, row.CreationTimestamp, uid}
	case "repositories_by_bucket":
		statement, args = "DELETE FROM repositories_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=? IF EXISTS", []any{row.Bucket, row.CreationTimestamp, uid}
	case "namespace_mappings":
		statement, args = "DELETE FROM namespace_mappings WHERE namespace=? AND name=? IF repository_id=?", []any{row.Namespace, row.Name, uid}
	case "namespace_mappings_by_repository":
		statement, args = "DELETE FROM namespace_mappings_by_repository WHERE repository_id=? IF namespace=? AND name=?", []any{uid, row.Namespace, row.Name}
	case "products_by_name", "category_taxonomy_by_name", "collection_by_name", "product_variant_by_name":
		statement, args = fmt.Sprintf("DELETE FROM %s WHERE namespace=? AND name=? IF uid=?", row.Table), []any{row.Namespace, row.Name, uid}
	case "products_by_uid", "category_taxonomy_by_uid", "collection_by_uid", "product_variant_by_uid":
		statement, args = fmt.Sprintf("DELETE FROM %s WHERE uid=? IF namespace=? AND creation_timestamp=?", row.Table), []any{uid, row.Namespace, row.CreationTimestamp}
	case "product_variant_by_sku":
		statement, args = "DELETE FROM product_variant_by_sku WHERE namespace=? AND sku=? IF uid=?", []any{row.Namespace, row.SKU, uid}
	case "product_variant_by_product_ref":
		statement, args = "DELETE FROM product_variant_by_product_ref WHERE namespace=? AND product_ref_name=? AND creation_timestamp=? AND uid=? IF EXISTS", []any{row.Namespace, row.ProductRefName, row.CreationTimestamp, uid}
	default:
		return false, fmt.Errorf("unsupported projection table %q", row.Table)
	}
	return s.session.Query(statement, nil).WithContext(ctx).Bind(args...).ExecCASRelease()
}
