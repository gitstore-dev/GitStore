// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/gocql/gocql"
)

type translationTargetSourceProof struct {
	Namespace        string
	PreviousIdentity *string
}

type translationTargetSourceRecord struct {
	datastore.TranslationTargetSourceIdentity
	Canceled    bool
	Invalidated bool   `json:",omitempty"`
	MutationID  string `json:",omitempty"`
}

type translationTargetMutation struct {
	NamespaceUID      string
	Namespace         string
	Kind              string
	Name              string
	APIVersion        string
	UID               string
	CreationTimestamp time.Time
	PreviousIdentity  *string
	NextIdentity      string
	CancellationToken string
	Creating          bool
	Retiring          bool
	Revoked           bool
}

type translationTargetMutationClock struct {
	Epoch         string
	Owner         string
	CoverageEpoch string
}

// Arbitration follows the source partition name, including writes begun before
// Namespace birth. The frozen source record alone supplies birth authority.
const translationTargetMutationClockTable = "translation_target_mutations_by_namespace"

func translationTargetRowMutation(row any, previous *string, creating bool, pending *datastore.TranslationTargetFence) translationTargetMutation {
	kind, resource, identity := translationTargetSourceRow(row)
	if resource == nil {
		return translationTargetMutation{}
	}
	mutation := translationTargetMutation{
		Namespace: resource.Namespace, Kind: kind, Name: resource.Name, APIVersion: resource.APIVersion,
		UID: resource.UID.String(), CreationTimestamp: resource.CreationTimestamp,
		PreviousIdentity: previous, NextIdentity: *identity, Creating: creating,
	}
	if pending != nil {
		mutation.CancellationToken = pending.Token
	}
	return mutation
}

func (s *scyllaDatastore) readTranslationTargetMutationClock(ctx context.Context, namespace string) (translationTargetMutationClock, error) {
	if namespace == "" || len(namespace) > 253 || !utf8.ValidString(namespace) || strings.ContainsRune(namespace, 0) {
		return translationTargetMutationClock{}, datastore.ErrInvalidArgument
	}
	for range 4 {
		var epoch, owner, coverage *string
		err := s.session.Session.Query(
			"SELECT translation_epoch,translation_owner,translation_coverage_epoch FROM "+translationTargetMutationClockTable+" WHERE namespace=?", namespace,
		).WithContext(ctx).Consistency(gocql.Serial).Scan(&epoch, &owner, &coverage)
		if errors.Is(err, gocql.ErrNotFound) || (err == nil && epoch == nil && owner == nil && coverage == nil) {
			id, err := gocql.RandomUUID()
			if err != nil {
				return translationTargetMutationClock{}, err
			}
			if _, err := s.translationIndexCAS(ctx,
				"UPDATE "+translationTargetMutationClockTable+" SET translation_epoch=?,translation_coverage_epoch=?,translation_owner='' WHERE namespace=? IF translation_epoch=null AND translation_owner=null AND translation_coverage_epoch=null",
				id.String(), (gocql.UUID{}).String(), namespace); err != nil {
				return translationTargetMutationClock{}, err
			}
			continue
		}
		if err != nil {
			return translationTargetMutationClock{}, err
		}
		if epoch == nil || owner == nil || coverage == nil || len(*owner) > 4<<10 {
			return translationTargetMutationClock{}, datastore.ErrRepairRequired
		}
		if id, err := gocql.ParseUUID(*epoch); err != nil || id.String() != *epoch {
			return translationTargetMutationClock{}, datastore.ErrRepairRequired
		}
		if id, err := gocql.ParseUUID(*coverage); err != nil || id.String() != *coverage {
			return translationTargetMutationClock{}, datastore.ErrRepairRequired
		}
		return translationTargetMutationClock{Epoch: *epoch, Owner: *owner, CoverageEpoch: *coverage}, nil
	}
	return translationTargetMutationClock{}, datastore.ErrConflict
}

func decodeTranslationTargetMutation(namespace, raw string) (translationTargetMutation, error) {
	var mutation translationTargetMutation
	if len(raw) > 4<<10 || json.Unmarshal([]byte(raw), &mutation) != nil ||
		mutation.Namespace != namespace || authoritativeTable(mutation.Kind) == "" ||
		mutation.Namespace == "" || len(mutation.Namespace) > 253 ||
		mutation.CreationTimestamp.IsZero() ||
		len(mutation.NextIdentity) > 512 || (mutation.PreviousIdentity != nil && len(*mutation.PreviousIdentity) > 512) ||
		(mutation.Creating && (mutation.Retiring || mutation.Name == "" || len(mutation.Name) > 253 || len(mutation.APIVersion) > 253)) {
		return mutation, datastore.ErrRepairRequired
	}
	switch mutation.Kind {
	case "Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File":
	default:
		return mutation, datastore.ErrRepairRequired
	}
	if len(mutation.CancellationToken) > 256 || (mutation.Creating && mutation.PreviousIdentity != nil) {
		return mutation, datastore.ErrRepairRequired
	}
	if !mutation.Creating && (mutation.PreviousIdentity == nil || *mutation.PreviousIdentity == "") && mutation.NamespaceUID != "" {
		return mutation, datastore.ErrRepairRequired
	}
	if mutation.NamespaceUID != "" {
		if id, err := gocql.ParseUUID(mutation.NamespaceUID); err != nil || id.String() != mutation.NamespaceUID {
			return mutation, datastore.ErrRepairRequired
		}
	}
	if id, err := gocql.ParseUUID(mutation.UID); err != nil || id.String() != mutation.UID {
		return mutation, datastore.ErrRepairRequired
	}
	var next translationTargetSourceRecord
	if json.Unmarshal([]byte(mutation.NextIdentity), &next) != nil || next.NamespaceUID != mutation.NamespaceUID {
		return mutation, datastore.ErrRepairRequired
	}
	if mutation.PreviousIdentity != nil && *mutation.PreviousIdentity != "" {
		var previous translationTargetSourceRecord
		if json.Unmarshal([]byte(*mutation.PreviousIdentity), &previous) != nil || previous.NamespaceUID != mutation.NamespaceUID {
			return mutation, datastore.ErrRepairRequired
		}
	}
	return mutation, nil
}

func (s *scyllaDatastore) reserveTranslationTargetMutation(ctx context.Context, mutation translationTargetMutation) (translationTargetMutationClock, error) {
	ctx, cancel := context.WithTimeout(ctx, namespaceCatalogReservationTimeout)
	defer cancel()
	raw, err := json.Marshal(mutation)
	if err != nil {
		return translationTargetMutationClock{}, err
	}
	if _, err := decodeTranslationTargetMutation(mutation.Namespace, string(raw)); err != nil {
		return translationTargetMutationClock{}, err
	}
	var busyEpoch string
	var busySince time.Time
	for {
		if err := ctx.Err(); err != nil {
			return translationTargetMutationClock{}, err
		}
		current, err := s.readTranslationTargetMutationClock(ctx, mutation.Namespace)
		if err != nil {
			return translationTargetMutationClock{}, err
		}
		if current.Owner == "" {
			id, err := gocql.RandomUUID()
			if err != nil {
				return translationTargetMutationClock{}, err
			}
			applied, err := s.translationIndexCAS(ctx,
				"UPDATE "+translationTargetMutationClockTable+" SET translation_epoch=?,translation_owner=? WHERE namespace=? IF translation_epoch=? AND translation_owner=''",
				id.String(), string(raw), mutation.Namespace, current.Epoch)
			if err != nil {
				return translationTargetMutationClock{}, err
			}
			if applied {
				return translationTargetMutationClock{Epoch: id.String(), Owner: string(raw), CoverageEpoch: current.CoverageEpoch}, nil
			}
		} else {
			owner, err := decodeTranslationTargetMutation(mutation.Namespace, current.Owner)
			if err != nil {
				return translationTargetMutationClock{}, err
			}
			if busyEpoch != current.Epoch {
				busyEpoch, busySince = current.Epoch, time.Now()
			}
			if owner.Revoked || time.Since(busySince) >= time.Second {
				// Waiting triggers cancellation, never release by expiry.
				if err := s.recoverTranslationTargetMutation(ctx, mutation.Namespace, false); err != nil {
					return translationTargetMutationClock{}, err
				}
				continue
			}
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return translationTargetMutationClock{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *scyllaDatastore) releaseTranslationTargetMutation(ctx context.Context, namespace string, clock translationTargetMutationClock) error {
	mutation, err := decodeTranslationTargetMutation(namespace, clock.Owner)
	if err != nil {
		return err
	}
	covered, err := s.translationTargetMutationPreservesCoverage(ctx, mutation)
	if err != nil {
		return err
	}
	coverage := clock.CoverageEpoch
	if !covered {
		coverage = clock.Epoch
	}
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE "+translationTargetMutationClockTable+" SET translation_owner='',translation_coverage_epoch=? WHERE namespace=? IF translation_epoch=? AND translation_owner=?",
		coverage, namespace, clock.Epoch, clock.Owner)
	if err != nil || applied {
		return err
	}
	current, err := s.readTranslationTargetMutationClock(ctx, namespace)
	if err != nil {
		return err
	}
	if current.Epoch != clock.Epoch || current.Owner == "" {
		return nil
	}
	return datastore.ErrConflict
}

func (s *scyllaDatastore) translationTargetMutationPreservesCoverage(ctx context.Context, mutation translationTargetMutation) (bool, error) {
	if mutation.Retiring {
		return true, nil
	}
	var next, previous translationTargetSourceRecord
	if json.Unmarshal([]byte(mutation.NextIdentity), &next) != nil ||
		next.NamespaceUID == "" || next.WriteToken == "" || next.ContentDigest == "" || next.Invalidated {
		return false, nil
	}
	if !mutation.Creating && (mutation.PreviousIdentity == nil ||
		json.Unmarshal([]byte(*mutation.PreviousIdentity), &previous) != nil ||
		previous.NamespaceUID != next.NamespaceUID || previous.WriteToken == "" || previous.Invalidated) {
		return false, nil
	}
	// Read current lifetime while the name grant remains held. A frozen source
	// from an older Namespace cannot preserve the new lifetime's coverage.
	var uid gocql.UUID
	err := s.session.Session.Query("SELECT uid FROM namespaces_by_name WHERE name=?", mutation.Namespace).
		WithContext(ctx).Consistency(gocql.Serial).Scan(&uid)
	if errors.Is(err, gocql.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if uid.String() != next.NamespaceUID {
		return false, nil
	}
	var name string
	err = s.session.Session.Query("SELECT name FROM namespaces_by_uid WHERE uid=?", uid).
		WithContext(ctx).Consistency(gocql.Serial).Scan(&name)
	if errors.Is(err, gocql.ErrNotFound) {
		return false, nil
	}
	return name == mutation.Namespace, err
}

// Each grant covers one source CAS, not a callback or its later compensations.
func (s *scyllaDatastore) executeTranslationTargetCAS(ctx context.Context, mutation translationTargetMutation, apply func() (bool, error)) (bool, error) {
	var next translationTargetSourceRecord
	if mutation.NextIdentity == "" {
		return apply()
	}
	if json.Unmarshal([]byte(mutation.NextIdentity), &next) != nil {
		return false, datastore.ErrRepairRequired
	}
	if next.NamespaceUID != "" {
		if id, err := gocql.ParseUUID(next.NamespaceUID); err != nil || id.String() != next.NamespaceUID {
			return false, datastore.ErrRepairRequired
		}
	}
	mutation.NamespaceUID = next.NamespaceUID
	clock, err := s.reserveTranslationTargetMutation(ctx, mutation)
	if err != nil {
		return false, fmt.Errorf("scylla: reserve target source mutation: %w", err)
	}
	if err := s.mutations.injector.Inject("commit-target-source", failureBefore); err != nil {
		return false, err
	}
	applied, err := apply()
	if err != nil {
		return applied, err
	}
	if err := s.mutations.injector.Inject("release-target-source", failureBefore); err != nil {
		return applied, err
	}
	if err := s.releaseTranslationTargetMutation(ctx, mutation.Namespace, clock); err != nil {
		return applied, fmt.Errorf("scylla: release target source mutation: %w", err)
	}
	return applied, nil
}

func (s *scyllaDatastore) RecoverTranslationTargetMutation(ctx context.Context, namespace string) error {
	if id, err := gocql.ParseUUID(namespace); err != nil || id.String() != namespace {
		return datastore.ErrInvalidArgument
	}
	name, err := s.translationTargetAuditNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	return s.recoverTranslationTargetMutation(ctx, name, false)
}

func (s *scyllaDatastore) recoverTranslationTargetMutation(ctx context.Context, namespace string, unprovedOnly bool) error {
	for range 4 {
		clock, err := s.readTranslationTargetMutationClock(ctx, namespace)
		if err != nil || clock.Owner == "" {
			return err
		}
		mutation, err := decodeTranslationTargetMutation(namespace, clock.Owner)
		if err != nil {
			return err
		}
		if unprovedOnly {
			preserves, err := s.translationTargetMutationPreservesCoverage(ctx, mutation)
			if err != nil || preserves {
				return err
			}
		}
		if !mutation.Revoked {
			mutation.Revoked = true
			raw, err := json.Marshal(mutation)
			if err != nil {
				return err
			}
			applied, err := s.translationIndexCAS(ctx,
				"UPDATE "+translationTargetMutationClockTable+" SET translation_owner=? WHERE namespace=? IF translation_epoch=? AND translation_owner=?",
				string(raw), namespace, clock.Epoch, clock.Owner)
			if err != nil {
				return err
			}
			if !applied {
				continue
			}
			clock.Owner = string(raw)
		}
		if err := s.cancelTranslationTargetMutation(ctx, mutation); err != nil {
			return err
		}
		return s.releaseTranslationTargetMutation(ctx, namespace, clock)
	}
	return datastore.ErrConflict
}

func (s *scyllaDatastore) cancelTranslationTargetMutation(ctx context.Context, mutation translationTargetMutation) error {
	table, uid := authoritativeTable(mutation.Kind), mustParseUUID(mutation.UID)
	for range 4 {
		var identity *string
		var retired *bool
		var version string
		err := s.session.Session.Query(
			"SELECT translation_source_identity,translation_retired,resource_version FROM "+table+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
			mutation.Namespace, mutation.CreationTimestamp, uid,
		).WithContext(ctx).Consistency(gocql.Serial).Scan(&identity, &retired, &version)
		if errors.Is(err, gocql.ErrNotFound) && mutation.Creating {
			var source translationTargetSourceRecord
			if json.Unmarshal([]byte(mutation.NextIdentity), &source) != nil {
				return datastore.ErrRepairRequired
			}
			source.Canceled = true
			raw, err := json.Marshal(source)
			if err != nil {
				return err
			}
			if err := s.prepareTranslationTargetRetirement(ctx, mutation.Kind, mutation.Namespace, mutation.CreationTimestamp, mutation.UID); err != nil {
				return err
			}
			if _, err := s.translationIndexCAS(ctx,
				"INSERT INTO "+table+" (namespace,creation_timestamp,uid,name,api_version,kind,translation_source_identity,translation_retired) VALUES (?,?,?,?,?,?,?,true) IF NOT EXISTS",
				mutation.Namespace, mutation.CreationTimestamp, uid, mutation.Name, mutation.APIVersion, mutation.Kind, string(raw)); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, gocql.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if retired != nil {
			if !*retired {
				return datastore.ErrRepairRequired
			}
			return s.retireTranslationTargetUID(ctx, mutation.Kind, mutation.UID)
		}
		if mutation.Creating {
			return nil
		}
		if (identity == nil) != (mutation.PreviousIdentity == nil) ||
			(identity != nil && *identity != *mutation.PreviousIdentity) {
			return nil
		}
		var source translationTargetSourceRecord
		if identity != nil && *identity != "" && json.Unmarshal([]byte(*identity), &source) != nil {
			return datastore.ErrRepairRequired
		}
		id, err := gocql.RandomUUID()
		if err != nil {
			return err
		}
		source.Canceled, source.MutationID = true, id.String()
		if mutation.CancellationToken != "" {
			if source.WriteToken == "" {
				source.Invalidated = true
			}
			source.WriteToken = mutation.CancellationToken
		}
		raw, err := json.Marshal(source)
		if err != nil || len(raw) > 512 {
			return datastore.ErrRepairRequired
		}
		applied, err := s.translationIndexCAS(ctx,
			"UPDATE "+table+" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=? AND translation_retired=null AND translation_source_identity=?",
			string(raw), mutation.Namespace, mutation.CreationTimestamp, uid, version, identity)
		if err != nil || applied {
			return err
		}
	}
	return datastore.ErrConflict
}

// Recovery reads only frozen-key source metadata. A cancellation CAS
// fences the source writer before restoring old material under the new token.
func (s *scyllaDatastore) RecoverTranslationTargetWrite(ctx context.Context, target datastore.TranslationTargetKey) error {
	for range 4 {
		fence, err := s.GetTranslationTargetFence(ctx, target)
		if err != nil {
			return err
		}
		if fence.Phase == datastore.TranslationTargetStable {
			return nil
		}
		intent := fence.Intent
		if intent.Backfilling {
			return s.recoverTranslationTargetBackfill(ctx, fence)
		}
		if intent.PreviousUID == "" && !intent.Deleting {
			return s.recoverTranslationTargetCreation(ctx, fence)
		}
		var proof translationTargetSourceProof
		if intent.PreviousUID == "" || (!intent.Deleting && intent.PreviousUID != intent.NextUID) ||
			intent.CreationTimestamp.IsZero() || len(intent.SourceProof) > datastore.MaxTranslationTargetIntentBytes ||
			!scyllaTimestampEqual(intent.CreationTimestamp, fence.CreationTimestamp) ||
			json.Unmarshal([]byte(intent.SourceProof), &proof) != nil ||
			proof.Namespace == "" || !utf8.ValidString(proof.Namespace) || strings.ContainsRune(proof.Namespace, 0) ||
			proof.PreviousIdentity == nil || len(*proof.PreviousIdentity) > 512 || fence.Evidence == nil ||
			proof.Namespace != fence.Evidence.Namespace {
			return datastore.ErrRepairRequired
		}
		var previous translationTargetSourceRecord
		if json.Unmarshal([]byte(*proof.PreviousIdentity), &previous) != nil ||
			previous.NamespaceUID != target.NamespaceUID ||
			(!intent.Deleting && (previous.WriteToken == "" || previous.ContentDigest != fence.Evidence.ContentDigest)) {
			return datastore.ErrRepairRequired
		}
		uid := intent.NextUID
		if intent.Deleting {
			uid = intent.PreviousUID
		}
		current := &catalog.TranslationTargetEvidence{Namespace: proof.Namespace, UID: uid}
		var identity *string
		var retired *bool
		err = s.session.Session.Query(
			"SELECT name,api_version,kind,generation,resource_version,revision,translation_source_identity,translation_retired FROM "+
				authoritativeTable(target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
			proof.Namespace, intent.CreationTimestamp, mustParseUUID(uid),
		).WithContext(ctx).Consistency(gocql.Serial).Scan(
			&current.Name, &current.APIVersion, &current.Kind, &current.Generation, &current.ResourceVersion,
			&current.Revision, &identity, &retired,
		)
		if errors.Is(err, gocql.ErrNotFound) {
			return datastore.ErrRepairRequired
		}
		if err != nil {
			return err
		}
		var source translationTargetSourceRecord
		if identity == nil || len(*identity) > 512 ||
			json.Unmarshal([]byte(*identity), &source) != nil || source.NamespaceUID != target.NamespaceUID {
			return datastore.ErrRepairRequired
		}
		current.ContentDigest = source.ContentDigest
		if retired != nil {
			if !*retired || !intent.Deleting {
				return datastore.ErrRepairRequired
			}
			if err := s.retireTranslationTargetUID(ctx, target.Kind, uid); err != nil {
				return err
			}
			if err := s.CompleteTranslationTargetWrite(ctx, target, fence.Token, nil); err != nil {
				return err
			}
			_, err := s.translationIndexCAS(ctx,
				"DELETE FROM "+authoritativeTable(target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=? IF translation_retired=true",
				proof.Namespace, intent.CreationTimestamp, mustParseUUID(uid))
			return err
		}
		if source.WriteToken == fence.Token {
			if source.Canceled {
				return s.abortTranslationTargetWrite(ctx, fence, current)
			}
			if intent.Deleting {
				return datastore.ErrRepairRequired
			}
			if intent.Invalidating {
				if !source.Invalidated {
					return datastore.ErrRepairRequired
				}
				return s.CompleteTranslationTargetWrite(ctx, target, fence.Token, nil)
			}
			if source.Invalidated {
				return datastore.ErrRepairRequired
			}
			return s.CompleteTranslationTargetWrite(ctx, target, fence.Token, current)
		}
		if *identity != *proof.PreviousIdentity {
			return datastore.ErrRepairRequired
		}
		if _, err := datastore.PlanTranslationTargetAbort(fence, fence.Token, current); err != nil {
			return err
		}
		source.WriteToken, source.Canceled = fence.Token, true
		marker, err := json.Marshal(source)
		if err != nil || len(marker) > 512 {
			return datastore.ErrRepairRequired
		}
		applied, err := s.translationIndexCAS(ctx,
			"UPDATE "+authoritativeTable(target.Kind)+" SET translation_source_identity=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=? AND translation_retired=null AND translation_source_identity=?",
			string(marker), proof.Namespace, intent.CreationTimestamp, mustParseUUID(uid), current.ResourceVersion, identity)
		if err != nil {
			return err
		}
		if applied {
			return s.abortTranslationTargetWrite(ctx, fence, current)
		}
	}
	return datastore.ErrConflict
}

func translationTargetSourceRow(row any) (string, *productRow, *string) {
	switch resource := row.(type) {
	case *productRow:
		return "Product", resource, &resource.TranslationSourceIdentity
	case *fileRow:
		return "File", (*productRow)(resource), &resource.TranslationSourceIdentity
	case *collectionRow:
		return "Collection", (*productRow)(resource), &resource.TranslationSourceIdentity
	case *categoryTaxonomyRow:
		return "CategoryTaxonomy", &productRow{
			Namespace: resource.Namespace, Name: resource.Name, UID: resource.UID, CreationTimestamp: resource.CreationTimestamp,
			Generation: resource.Generation, ResourceVersion: resource.ResourceVersion, Revision: resource.Revision,
			RepositoryID: resource.RepositoryID, SourcePath: resource.SourcePath, GitCommitSHA: resource.GitCommitSHA, GitRef: resource.GitRef,
			APIVersion: resource.APIVersion, Kind: resource.Kind, Spec: resource.Spec, Body: resource.Body,
		}, &resource.TranslationSourceIdentity
	case *productVariantRow:
		return "ProductVariant", &productRow{
			Namespace: resource.Namespace, Name: resource.Name, UID: resource.UID, CreationTimestamp: resource.CreationTimestamp,
			Generation: resource.Generation, ResourceVersion: resource.ResourceVersion, Revision: resource.Revision,
			RepositoryID: resource.RepositoryID, SourcePath: resource.SourcePath, GitCommitSHA: resource.GitCommitSHA, GitRef: resource.GitRef,
			APIVersion: resource.APIVersion, Kind: resource.Kind, Spec: resource.Spec, Body: resource.Body,
		}, &resource.TranslationSourceIdentity
	default:
		return "", nil, nil
	}
}

func (s *scyllaDatastore) freezeTranslationTargetBirth(ctx context.Context, row any) error {
	_, resource, destination := translationTargetSourceRow(row)
	namespace, err := s.GetNamespaceByName(ctx, resource.Namespace)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return err
	}
	identity := datastore.TranslationTargetSourceIdentity{}
	if namespace != nil {
		identity.NamespaceUID = namespace.UID
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	*destination = string(raw)
	return nil
}

// Namespace ownership and digest travel with the source CAS. Fence reservation
// separately assigns a token only after validating a supported transition.
func (s *scyllaDatastore) prepareTranslationTargetSourceIdentity(ctx context.Context, row any, creating bool) (*string, error) {
	kind, resource, destination := translationTargetSourceRow(row)
	if resource == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity := datastore.TranslationTargetSourceIdentity{}
	var previous *string
	if creating {
		if *destination == "" || json.Unmarshal([]byte(*destination), &identity) != nil {
			return nil, datastore.ErrRepairRequired
		}
		namespace, err := s.GetNamespaceByName(ctx, resource.Namespace)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return nil, err
		}
		currentUID := ""
		if namespace != nil {
			currentUID = namespace.UID
		}
		if currentUID != identity.NamespaceUID {
			return nil, datastore.ErrConflict
		}
	} else {
		err := s.session.Session.Query(
			"SELECT translation_source_identity FROM "+authoritativeTable(kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
			resource.Namespace, resource.CreationTimestamp, resource.UID,
		).WithContext(ctx).Consistency(gocql.Serial).Scan(&previous)
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrConflict
		}
		if err != nil {
			return nil, err
		}
		if previous != nil && *previous != "" {
			if len(*previous) > 512 || json.Unmarshal([]byte(*previous), &identity) != nil {
				return nil, datastore.ErrRepairRequired
			}
			if identity.NamespaceUID != "" {
				uid, err := gocql.ParseUUID(identity.NamespaceUID)
				if err != nil || uid.String() != identity.NamespaceUID {
					return nil, datastore.ErrRepairRequired
				}
			}
		}
	}
	identity.WriteToken, identity.ContentDigest = "", ""
	if resource.Kind == kind {
		digest, err := translation.TargetContentDigestStringSpec(ctx, resource.APIVersion, resource.Kind, resource.Spec, resource.Body)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			identity.ContentDigest = digest
		}
	}
	id, err := gocql.RandomUUID()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(translationTargetSourceRecord{
		TranslationTargetSourceIdentity: identity, MutationID: id.String(),
	})
	if err != nil {
		return nil, err
	}
	*destination = string(raw)
	return previous, nil
}

func (s *scyllaDatastore) beginTranslationTargetCreation(ctx context.Context, row any) (*datastore.TranslationTargetFence, error) {
	kind, resource, destination := translationTargetSourceRow(row)
	if resource == nil {
		return nil, nil
	}
	var source datastore.TranslationTargetSourceIdentity
	if json.Unmarshal([]byte(*destination), &source) != nil {
		return nil, datastore.ErrRepairRequired
	}
	target := datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceUID, APIVersion: resource.APIVersion, Kind: kind, Name: resource.Name,
	}
	proof, err := json.Marshal(translationTargetSourceProof{Namespace: resource.Namespace})
	if err != nil {
		return nil, err
	}
	id, err := gocql.RandomUUID()
	if err != nil {
		return nil, err
	}
	intent := datastore.TranslationTargetWriteIntent{
		ID: id.String(), NextUID: resource.UID.String(), SourceDigest: source.ContentDigest,
		CreationTimestamp: resource.CreationTimestamp, SourceProof: string(proof),
		SourcePath: resource.SourcePath, GitCommitSHA: resource.GitCommitSHA, GitRef: resource.GitRef,
	}
	if resource.RepositoryID != nil {
		intent.RepositoryID = resource.RepositoryID.String()
	}
	planned, err := datastore.PlanTranslationTargetWrite(nil, target, intent, "")
	if errors.Is(err, datastore.ErrInvalidArgument) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := datastore.PlanTranslationTargetCompletion(planned, planned.Token, &catalog.TranslationTargetEvidence{
		APIVersion: resource.APIVersion, Kind: resource.Kind, Namespace: resource.Namespace, Name: resource.Name,
		UID: resource.UID.String(), Generation: resource.Generation, ResourceVersion: resource.ResourceVersion,
		Revision: resource.Revision, ContentDigest: source.ContentDigest,
	}); err != nil {
		if errors.Is(err, datastore.ErrInvalidArgument) || errors.Is(err, datastore.ErrConflict) {
			return nil, nil
		}
		return nil, err
	}
	var existing gocql.UUID
	err = s.session.Session.Query(
		"SELECT uid FROM "+authoritativeTable(kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
		resource.Namespace, resource.CreationTimestamp, resource.UID,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&existing)
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, gocql.ErrNotFound) {
		return nil, err
	}
	current, err := s.GetTranslationTargetFence(ctx, target)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, err
	}
	expected := ""
	if current != nil {
		if current.Phase == datastore.TranslationTargetUpdating {
			if err := s.RecoverTranslationTargetWrite(ctx, target); err != nil {
				return nil, err
			}
			return nil, datastore.ErrConflict
		}
		if current.Evidence != nil {
			return nil, datastore.ErrRepairRequired
		}
		expected = current.Token
	}
	pending, err := s.BeginTranslationTargetWrite(ctx, target, intent, expected)
	if err != nil {
		return nil, err
	}
	source.WriteToken = pending.Token
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	*destination = string(raw)
	return pending, nil
}

func (s *scyllaDatastore) recoverTranslationTargetCreation(ctx context.Context, fence *datastore.TranslationTargetFence) error {
	intent, target := fence.Intent, fence.Target
	var proof translationTargetSourceProof
	if fence.Evidence != nil || intent.CreationTimestamp.IsZero() ||
		json.Unmarshal([]byte(intent.SourceProof), &proof) != nil || proof.Namespace == "" ||
		!utf8.ValidString(proof.Namespace) || strings.ContainsRune(proof.Namespace, 0) || proof.PreviousIdentity != nil {
		return datastore.ErrRepairRequired
	}
	for range 4 {
		current := &catalog.TranslationTargetEvidence{Namespace: proof.Namespace, UID: intent.NextUID}
		var identity *string
		var retired *bool
		err := s.session.Session.Query(
			"SELECT name,api_version,kind,generation,resource_version,revision,translation_source_identity,translation_retired FROM "+
				authoritativeTable(target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
			proof.Namespace, intent.CreationTimestamp, mustParseUUID(intent.NextUID),
		).WithContext(ctx).Consistency(gocql.Serial).Scan(
			&current.Name, &current.APIVersion, &current.Kind, &current.Generation, &current.ResourceVersion,
			&current.Revision, &identity, &retired)
		if errors.Is(err, gocql.ErrNotFound) {
			if err := s.prepareTranslationTargetRetirement(ctx, target.Kind, proof.Namespace, intent.CreationTimestamp, intent.NextUID); err != nil {
				return err
			}
			marker, err := json.Marshal(translationTargetSourceRecord{
				TranslationTargetSourceIdentity: datastore.TranslationTargetSourceIdentity{
					NamespaceUID: target.NamespaceUID, WriteToken: fence.Token, ContentDigest: intent.SourceDigest,
				},
				Canceled: true,
			})
			if err != nil {
				return err
			}
			_, err = s.translationIndexCAS(ctx,
				"INSERT INTO "+authoritativeTable(target.Kind)+
					" (namespace,creation_timestamp,uid,name,api_version,kind,translation_source_identity,translation_retired) VALUES (?,?,?,?,?,?,?,true) IF NOT EXISTS",
				proof.Namespace, intent.CreationTimestamp, mustParseUUID(intent.NextUID), target.Name, target.APIVersion, target.Kind, string(marker))
			if err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		var source translationTargetSourceRecord
		if identity == nil || len(*identity) > 512 || json.Unmarshal([]byte(*identity), &source) != nil ||
			source.NamespaceUID != target.NamespaceUID || source.WriteToken != fence.Token ||
			source.ContentDigest != intent.SourceDigest || current.Name != target.Name ||
			current.Kind != target.Kind || current.APIVersion != target.APIVersion {
			return datastore.ErrRepairRequired
		}
		if retired != nil {
			if !*retired || !source.Canceled {
				return datastore.ErrRepairRequired
			}
			if err := s.retireTranslationTargetUID(ctx, target.Kind, intent.NextUID); err != nil {
				return err
			}
			if err := s.abortTranslationTargetWrite(ctx, fence, nil); err != nil {
				return err
			}
			_, err := s.translationIndexCAS(ctx,
				"DELETE FROM "+authoritativeTable(target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=? IF translation_retired=true",
				proof.Namespace, intent.CreationTimestamp, mustParseUUID(intent.NextUID))
			return err
		}
		if source.Canceled {
			return datastore.ErrRepairRequired
		}
		current.ContentDigest = source.ContentDigest
		return s.CompleteTranslationTargetWrite(ctx, target, fence.Token, current)
	}
	return datastore.ErrConflict
}

func (s *scyllaDatastore) beginProvenTranslationTargetUpdate(ctx context.Context, row any, previous *string, expected string) (*datastore.TranslationTargetFence, error) {
	pending, err := s.planProvenTranslationTargetUpdate(ctx, row, previous, expected)
	if err != nil {
		return nil, err
	}
	if err := s.mutations.injector.Inject("publish-target-source", failureBefore); err != nil {
		return nil, err
	}
	return pending, nil
}

func (s *scyllaDatastore) planProvenTranslationTargetUpdate(ctx context.Context, row any, previous *string, expected string) (*datastore.TranslationTargetFence, error) {
	if previous == nil || *previous == "" {
		return nil, nil
	}
	var before translationTargetSourceRecord
	if json.Unmarshal([]byte(*previous), &before) != nil {
		return nil, datastore.ErrRepairRequired
	}
	if before.WriteToken == "" || before.Invalidated {
		return nil, nil
	}
	kind, resource, destination := translationTargetSourceRow(row)
	version := "catalog.gitstore.dev/v1beta1"
	if kind == "File" {
		version = "storage.gitstore.dev/v1beta1"
	}
	target := datastore.TranslationTargetKey{
		NamespaceUID: before.NamespaceUID, APIVersion: version, Kind: kind, Name: resource.Name,
	}
	current, err := s.GetTranslationTargetFence(ctx, target)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, datastore.ErrRepairRequired
	}
	if err != nil {
		return nil, err
	}
	if current.Phase == datastore.TranslationTargetUpdating {
		if err := s.RecoverTranslationTargetWrite(ctx, target); err != nil {
			return nil, err
		}
		return nil, datastore.ErrConflict
	}
	if current.Token != before.WriteToken || current.Evidence == nil ||
		current.Evidence.UID != resource.UID.String() || current.Evidence.ContentDigest != before.ContentDigest ||
		current.Evidence.Namespace != resource.Namespace || !scyllaTimestampEqual(current.CreationTimestamp, resource.CreationTimestamp) {
		return nil, datastore.ErrRepairRequired
	}
	namespace, err := s.GetNamespaceByName(ctx, resource.Namespace)
	if errors.Is(err, datastore.ErrNotFound) || (err == nil && namespace.UID != before.NamespaceUID) {
		return nil, datastore.ErrRepairRequired
	}
	if err != nil {
		return nil, err
	}
	var desired datastore.TranslationTargetSourceIdentity
	if json.Unmarshal([]byte(*destination), &desired) != nil {
		return nil, datastore.ErrRepairRequired
	}
	nextVersion, nextOK := new(big.Int).SetString(resource.ResourceVersion, 10)
	priorVersion, priorOK := new(big.Int).SetString(expected, 10)
	invalidating := desired.ContentDigest == "" || resource.APIVersion != target.APIVersion || resource.Kind != kind ||
		!nextOK || !priorOK || nextVersion.Cmp(priorVersion) <= 0
	proof, err := json.Marshal(translationTargetSourceProof{Namespace: resource.Namespace, PreviousIdentity: previous})
	if err != nil {
		return nil, err
	}
	id, err := gocql.RandomUUID()
	if err != nil {
		return nil, err
	}
	intent := datastore.TranslationTargetWriteIntent{
		ID: id.String(), PreviousUID: resource.UID.String(), NextUID: resource.UID.String(),
		ExpectedResourceVersion: expected, SourceDigest: desired.ContentDigest, SourceProof: string(proof),
		Invalidating:      invalidating,
		CreationTimestamp: resource.CreationTimestamp, SourcePath: resource.SourcePath,
		GitCommitSHA: resource.GitCommitSHA, GitRef: resource.GitRef,
	}
	if resource.RepositoryID != nil {
		intent.RepositoryID = resource.RepositoryID.String()
	}
	planned, err := datastore.PlanTranslationTargetWrite(current, target, intent, current.Token)
	if err != nil && !errors.Is(err, datastore.ErrInvalidArgument) {
		return nil, err
	}
	evidence := &catalog.TranslationTargetEvidence{
		APIVersion: resource.APIVersion, Kind: resource.Kind, Namespace: resource.Namespace, Name: resource.Name,
		UID: resource.UID.String(), Generation: resource.Generation, ResourceVersion: resource.ResourceVersion,
		Revision: resource.Revision, ContentDigest: desired.ContentDigest,
	}
	if err == nil && !invalidating {
		_, err = datastore.PlanTranslationTargetCompletion(planned, planned.Token, evidence)
	}
	if invalidating || errors.Is(err, datastore.ErrInvalidArgument) || errors.Is(err, datastore.ErrConflict) {
		intent.Invalidating = true
		intent.RepositoryID, intent.SourcePath, intent.GitCommitSHA, intent.GitRef = "", "", "", ""
		planned, err = datastore.PlanTranslationTargetWrite(current, target, intent, current.Token)
		if err == nil {
			_, err = datastore.PlanTranslationTargetCompletion(planned, planned.Token, nil)
		}
	}
	if err != nil {
		return nil, err
	}
	pending, err := s.BeginTranslationTargetWrite(ctx, target, intent, current.Token)
	if err != nil {
		return nil, err
	}
	desired.WriteToken = pending.Token
	var identity any = desired
	if intent.Invalidating {
		identity = translationTargetSourceRecord{TranslationTargetSourceIdentity: desired, Invalidated: true}
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	*destination = string(raw)
	return pending, nil
}

func (s *scyllaDatastore) finishTranslationTargetSourceUpdate(ctx context.Context, row any, pending *datastore.TranslationTargetFence, applied bool, err error) error {
	if err != nil {
		return err
	}
	if !applied {
		if pending != nil {
			return errors.Join(datastore.ErrConflict, s.RecoverTranslationTargetWrite(ctx, pending.Target))
		}
		return datastore.ErrConflict
	}
	if pending == nil {
		return nil
	}
	kind, resource, _ := translationTargetSourceRow(row)
	err = s.mutations.injector.Inject("stabilize-target-source", failureBefore)
	if err == nil {
		evidence := &catalog.TranslationTargetEvidence{
			APIVersion: resource.APIVersion, Kind: resource.Kind, Namespace: resource.Namespace, Name: resource.Name,
			UID: resource.UID.String(), Generation: resource.Generation, ResourceVersion: resource.ResourceVersion,
			Revision: resource.Revision, ContentDigest: pending.Intent.SourceDigest,
		}
		if pending.Intent.Invalidating {
			evidence = nil
		}
		err = s.CompleteTranslationTargetWrite(ctx, pending.Target, pending.Token, evidence)
	}
	if err != nil {
		operation := "update"
		if pending.Intent.PreviousUID == "" {
			operation = "create"
		}
		return datastore.NewRepairRequiredError(
			catalogueStep(operation, kind, resource.UID.String(), "translation_target_fences_by_name", pending.Target.Name, "stabilize-target-source"),
			err, fmt.Errorf("authoritative resource version %s committed; target fence requires roll-forward", resource.ResourceVersion))
	}
	return nil
}

func (s *scyllaDatastore) checkTranslationTargetUID(ctx context.Context, kind string, uid gocql.UUID) error {
	var claimed gocql.UUID
	err := s.session.Session.Query(
		"SELECT uid FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", kind, uid,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&claimed)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return datastore.ErrAlreadyExists
}

func translationTargetUIDRetired(ctx context.Context, session *gocql.Session, kind string, uid gocql.UUID) (bool, error) {
	var retired *bool
	var namespace string
	var created time.Time
	err := session.Query(
		"SELECT retired,namespace,creation_timestamp FROM translation_target_tombstones_by_uid WHERE kind=? AND uid=?", kind, uid,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&retired, &namespace, &created)
	if errors.Is(err, gocql.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if retired == nil || *retired {
		return true, nil
	}
	if namespace == "" || created.IsZero() {
		return false, datastore.ErrRepairRequired
	}
	proven, err := translationTargetPrimaryRetired(ctx, session, AuthoritativeResource{
		Kind: kind, UID: uid.String(), Namespace: namespace, CreationTimestamp: created,
	})
	if errors.Is(err, datastore.ErrNotFound) {
		return false, datastore.ErrRepairRequired
	}
	return proven, err
}

func (s *scyllaDatastore) checkLiveTranslationTargetUID(ctx context.Context, kind string, uid gocql.UUID, primaryRetired *bool) error {
	if primaryRetired != nil {
		if !*primaryRetired {
			return datastore.ErrRepairRequired
		}
		return datastore.ErrNotFound
	}
	retired, err := translationTargetUIDRetired(ctx, s.session.Session, kind, uid)
	if err == nil && retired {
		return datastore.ErrNotFound
	}
	return err
}

func (s *scyllaDatastore) checkTranslationTargetPageUID(ctx context.Context, kind string, uid gocql.UUID, primaryRetired *bool) error {
	err := s.checkLiveTranslationTargetUID(ctx, kind, uid, primaryRetired)
	if errors.Is(err, datastore.ErrNotFound) {
		return datastore.ErrRepairRequired
	}
	return err
}

func (s *scyllaDatastore) retireTranslationTargetUID(ctx context.Context, kind, uid string) error {
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translation_target_tombstones_by_uid SET retired=true WHERE kind=? AND uid=? IF EXISTS", kind, mustParseUUID(uid))
	if err == nil && !applied {
		return datastore.ErrRepairRequired
	}
	return err
}

func (s *scyllaDatastore) retireTranslationTargetAuthoritative(ctx context.Context, kind, namespace string, created time.Time, uid, expected string, sourceIdentity *string) error {
	table := authoritativeTable(kind)
	if table == "" {
		return datastore.ErrInvalidArgument
	}
	pending, previousIdentity, err := s.beginProvenTranslationTargetDeletion(ctx, kind, namespace, created, uid, expected, sourceIdentity)
	if err != nil {
		return err
	}
	if err := s.prepareTranslationTargetRetirement(ctx, kind, namespace, created, uid); err != nil {
		return err
	}
	if err := s.mutations.injector.Inject("retire-target-source", failureBefore); err != nil {
		return err
	}
	mutation := translationTargetMutation{
		Namespace: namespace, Kind: kind, UID: uid, CreationTimestamp: created,
		PreviousIdentity: previousIdentity, NextIdentity: "{}", Retiring: true,
	}
	if previousIdentity != nil && *previousIdentity != "" {
		mutation.NextIdentity = *previousIdentity
	}
	if pending != nil {
		mutation.CancellationToken = pending.Token
	}
	applied, err := s.executeTranslationTargetCAS(ctx, mutation, func() (bool, error) {
		return s.session.Query(
			"UPDATE "+table+" SET translation_retired=true WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=? AND translation_retired=null AND translation_source_identity=?", nil,
		).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).
			Bind(namespace, created, mustParseUUID(uid), expected, previousIdentity).ExecCASRelease()
	})
	if err != nil {
		return err
	}
	if !applied {
		if pending != nil {
			return errors.Join(datastore.ErrConflict, s.RecoverTranslationTargetWrite(ctx, pending.Target))
		}
		return datastore.ErrConflict
	}
	// Keep the source proof and its UID locator until permanent retirement is durable.
	if err := s.mutations.injector.Inject("publish-target-retirement", failureBefore); err != nil {
		return err
	}
	if err := s.retireTranslationTargetUID(ctx, kind, uid); err != nil {
		return err
	}
	if pending != nil {
		if err := s.CompleteTranslationTargetWrite(ctx, pending.Target, pending.Token, nil); err != nil {
			return err
		}
	} else if err := s.settleRetiredTranslationTargetHead(ctx, AuthoritativeResource{
		Kind: kind, Namespace: namespace, CreationTimestamp: created, UID: uid,
	}); err != nil {
		return err
	}
	if err := s.mutations.injector.Inject("compact-target-retirement", failureBefore); err != nil {
		return err
	}
	_, err = s.session.Query(
		"DELETE FROM "+table+" WHERE namespace=? AND creation_timestamp=? AND uid=? IF translation_retired=true", nil,
	).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).
		Bind(namespace, created, mustParseUUID(uid)).ExecCASRelease()
	return err
}

func (s *scyllaDatastore) prepareTranslationTargetRetirement(ctx context.Context, kind, namespace string, created time.Time, uid string) error {
	existing := make(map[string]any)
	claimed, err := s.session.Session.Query(
		"INSERT INTO translation_target_tombstones_by_uid (kind,uid,retired,namespace,creation_timestamp) VALUES (?,?,false,?,?) IF NOT EXISTS",
		kind, mustParseUUID(uid), namespace, created,
	).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).MapScanCAS(existing)
	if err != nil || claimed {
		return err
	}
	priorNamespace, _ := existing["namespace"].(string)
	priorCreated, _ := existing["creation_timestamp"].(time.Time)
	if priorNamespace != namespace || !scyllaTimestampEqual(priorCreated, created) {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) rollbackTranslationTargetCreation(ctx context.Context, row any, expected string) error {
	kind, resource, identity := translationTargetSourceRow(row)
	return s.retireTranslationTargetAuthoritative(ctx, kind, resource.Namespace, resource.CreationTimestamp,
		resource.UID.String(), expected, identity)
}

func (s *scyllaDatastore) beginProvenTranslationTargetDeletion(ctx context.Context, kind, namespace string, created time.Time, uid, expected string, sourceIdentity *string) (*datastore.TranslationTargetFence, *string, error) {
	var name, version, resourceVersion string
	var previous *string
	var retired *bool
	err := s.session.Session.Query(
		"SELECT name,api_version,resource_version,translation_source_identity,translation_retired FROM "+
			authoritativeTable(kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
		namespace, created, mustParseUUID(uid),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&name, &version, &resourceVersion, &previous, &retired)
	if errors.Is(err, gocql.ErrNotFound) || (err == nil && resourceVersion != expected) {
		return nil, nil, datastore.ErrConflict
	}
	if err != nil {
		return nil, nil, err
	}
	if sourceIdentity != nil && (previous == nil || *previous != *sourceIdentity) {
		return nil, nil, datastore.ErrConflict
	}
	if retired != nil {
		if !*retired {
			return nil, nil, datastore.ErrRepairRequired
		}
		return nil, nil, datastore.ErrNotFound
	}
	if previous == nil || *previous == "" {
		return nil, previous, nil
	}
	var source translationTargetSourceRecord
	if len(*previous) > 512 || json.Unmarshal([]byte(*previous), &source) != nil {
		return nil, nil, datastore.ErrRepairRequired
	}
	if source.WriteToken == "" || source.Invalidated {
		return nil, previous, nil
	}
	target := datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceUID, APIVersion: version, Kind: kind, Name: name,
	}
	current, err := s.GetTranslationTargetFence(ctx, target)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, nil, datastore.ErrRepairRequired
	}
	if err != nil {
		return nil, nil, err
	}
	if current.Phase == datastore.TranslationTargetUpdating {
		if err := s.RecoverTranslationTargetWrite(ctx, target); err != nil {
			return nil, nil, err
		}
		return nil, nil, datastore.ErrConflict
	}
	if current.Token != source.WriteToken || current.Evidence == nil || current.Evidence.UID != uid ||
		current.Evidence.Namespace != namespace || current.Evidence.ContentDigest != source.ContentDigest ||
		!scyllaTimestampEqual(current.CreationTimestamp, created) {
		return nil, nil, datastore.ErrRepairRequired
	}
	proof, err := json.Marshal(translationTargetSourceProof{Namespace: namespace, PreviousIdentity: previous})
	if err != nil {
		return nil, nil, err
	}
	id, err := gocql.RandomUUID()
	if err != nil {
		return nil, nil, err
	}
	pending, err := s.BeginTranslationTargetWrite(ctx, target, datastore.TranslationTargetWriteIntent{
		ID: id.String(), PreviousUID: uid, ExpectedResourceVersion: expected, Deleting: true,
		CreationTimestamp: created, SourceProof: string(proof),
	}, current.Token)
	return pending, previous, err
}

func translationTargetPrimaryRetired(ctx context.Context, session *gocql.Session, row AuthoritativeResource) (bool, error) {
	table := authoritativeTable(row.Kind)
	uid, err := gocql.ParseUUID(row.UID)
	if table == "" || err != nil || row.Namespace == "" || row.CreationTimestamp.IsZero() {
		return false, datastore.ErrInvalidArgument
	}
	var retired *bool
	err = session.Query(
		"SELECT translation_retired FROM "+table+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
		row.Namespace, row.CreationTimestamp, uid,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&retired)
	if errors.Is(err, gocql.ErrNotFound) {
		return false, datastore.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if retired != nil && !*retired {
		return false, datastore.ErrRepairRequired
	}
	return retired != nil, nil
}

func (s *scyllaDatastore) checkTranslationTargetInsert(ctx context.Context, row any) error {
	switch resource := row.(type) {
	case *productRow:
		return s.checkTranslationTargetUID(ctx, "Product", resource.UID)
	case *productVariantRow:
		return s.checkTranslationTargetUID(ctx, "ProductVariant", resource.UID)
	case *categoryTaxonomyRow:
		return s.checkTranslationTargetUID(ctx, "CategoryTaxonomy", resource.UID)
	case *collectionRow:
		return s.checkTranslationTargetUID(ctx, "Collection", resource.UID)
	case *fileRow:
		return s.checkTranslationTargetUID(ctx, "File", resource.UID)
	default:
		return nil
	}
}

func (s *scyllaProjectionRepairStore) CleanupRetiredTargetRows(ctx context.Context, rows []AuthoritativeResource) error {
	for _, row := range rows {
		table := authoritativeTable(row.Kind)
		uid, err := gocql.ParseUUID(row.UID)
		if table == "" || err != nil || row.Namespace == "" || row.CreationTimestamp.IsZero() {
			return datastore.ErrInvalidArgument
		}
		retired, err := translationTargetUIDRetired(ctx, s.session.Session, row.Kind, uid)
		if err != nil {
			return err
		}
		if !retired {
			proven, err := translationTargetPrimaryRetired(ctx, s.session.Session, row)
			if err != nil {
				return err
			}
			if !proven {
				return datastore.ErrRepairRequired
			}
		}
		if _, err := s.session.Query(
			"INSERT INTO translation_target_tombstones_by_uid (kind,uid,retired) VALUES (?,?,true) IF NOT EXISTS", nil,
		).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).
			Bind(row.Kind, uid).ExecCASRelease(); err != nil {
			return err
		}
		published, err := s.session.Query(
			"UPDATE translation_target_tombstones_by_uid SET retired=true WHERE kind=? AND uid=? IF EXISTS", nil,
		).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).
			Bind(row.Kind, uid).ExecCASRelease()
		if err != nil {
			return err
		}
		if !published {
			return datastore.ErrRepairRequired
		}
		native := &scyllaDatastore{session: s.session, mutations: newMutationExecutor(nil)}
		if err := native.settleRetiredTranslationTargetHead(ctx, row); err != nil {
			return err
		}
		_, err = s.session.Query(
			"DELETE FROM "+table+" WHERE namespace=? AND creation_timestamp=? AND uid=? IF EXISTS", nil,
		).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).
			Bind(row.Namespace, row.CreationTimestamp, uid).ExecCASRelease()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *scyllaDatastore) settleRetiredTranslationTargetHead(ctx context.Context, row AuthoritativeResource) error {
	var name string
	var identity *string
	err := s.session.Session.Query(
		"SELECT name,translation_source_identity FROM "+authoritativeTable(row.Kind)+
			" WHERE namespace=? AND creation_timestamp=? AND uid=?",
		row.Namespace, row.CreationTimestamp, mustParseUUID(row.UID),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&name, &identity)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if identity == nil || *identity == "" {
		return nil
	}
	var source datastore.TranslationTargetSourceIdentity
	if len(*identity) > 512 || json.Unmarshal([]byte(*identity), &source) != nil {
		return datastore.ErrRepairRequired
	}
	if source.NamespaceUID == "" {
		return nil
	}
	version := "catalog.gitstore.dev/v1beta1"
	if row.Kind == "File" {
		version = "storage.gitstore.dev/v1beta1"
	}
	target := datastore.TranslationTargetKey{
		NamespaceUID: source.NamespaceUID, APIVersion: version, Kind: row.Kind, Name: name,
	}
	current, err := s.GetTranslationTargetFence(ctx, target)
	if errors.Is(err, datastore.ErrNotFound) && source.WriteToken == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Phase == datastore.TranslationTargetUpdating {
		if current.Intent.PreviousUID == "" && current.Intent.NextUID == row.UID &&
			scyllaTimestampEqual(current.Intent.CreationTimestamp, row.CreationTimestamp) {
			return s.RecoverTranslationTargetWrite(ctx, target)
		}
		if current.Intent.PreviousUID == row.UID && current.Intent.Deleting &&
			scyllaTimestampEqual(current.Intent.CreationTimestamp, row.CreationTimestamp) {
			return s.RecoverTranslationTargetWrite(ctx, target)
		}
		if current.Intent.PreviousUID == row.UID || current.Intent.NextUID == row.UID {
			return datastore.ErrRepairRequired
		}
		return nil
	}
	if current.Evidence == nil || current.Evidence.UID != row.UID {
		return nil
	}
	proof, err := json.Marshal(translationTargetSourceProof{Namespace: row.Namespace, PreviousIdentity: identity})
	if err != nil {
		return err
	}
	id, err := gocql.RandomUUID()
	if err != nil {
		return err
	}
	pending, err := s.BeginTranslationTargetWrite(ctx, target, datastore.TranslationTargetWriteIntent{
		ID: id.String(), PreviousUID: row.UID, ExpectedResourceVersion: current.Evidence.ResourceVersion,
		Deleting: true, CreationTimestamp: row.CreationTimestamp, SourceProof: string(proof),
	}, current.Token)
	if err != nil {
		return err
	}
	// Permanent UID retirement was freshly published by the caller and cannot
	// be undone by a delayed source write or projection replay.
	return s.CompleteTranslationTargetWrite(ctx, target, pending.Token, nil)
}
