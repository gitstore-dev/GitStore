// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
)

const rbacCDCMaxFrameChanges = rbac.MaxRolesPerTier + rbac.MaxBindingsPerTier + 1

type rbacCDCImage struct {
	Tier, Entry, UID string
	Payload          []byte
	Revision         int64
	Projection       []byte
	Repair           []byte
	RoleCount        int
	BindingCount     int
	Retired          bool
	CompleteState    bool
}

func rbacCDCChanges(stream string, change scyllacdc.Change) ([]watchjournal.Change, error) {
	if len(change.PreImage) > rbacCDCMaxFrameChanges || len(change.PostImage) > rbacCDCMaxFrameChanges {
		return nil, rbacCDCDiscontinuity("frame exceeds tier image bound")
	}
	if len(change.Delta) > 0 && len(change.PreImage) == 0 && len(change.PostImage) == 0 {
		return nil, rbacCDCDiscontinuity("delta has no full images")
	}
	var before, after []*rbacCDCImage
	for side, rows := range [][]*scyllacdc.ChangeRow{change.PreImage, change.PostImage} {
		for _, row := range rows {
			if row == nil {
				return nil, rbacCDCDiscontinuity("nil CDC row")
			}
			image := &rbacCDCImage{}
			assignCDC(row, "tier", &image.Tier)
			assignCDC(row, "entry", &image.Entry)
			assignCDC(row, "uid", &image.UID)
			assignCDC(row, "payload", &image.Payload)
			assignCDC(row, "revision", &image.Revision)
			if image.Entry == "" {
				image.CompleteState = true
				for _, column := range []string{"projection", "repair", "role_count", "binding_count", "namespace_retired"} {
					if _, present := row.GetValue(column); !present {
						image.CompleteState = false
					}
				}
				assignCDC(row, "projection", &image.Projection)
				assignCDC(row, "repair", &image.Repair)
				assignCDC(row, "role_count", &image.RoleCount)
				assignCDC(row, "binding_count", &image.BindingCount)
				assignCDC(row, "namespace_retired", &image.Retired)
			}
			if _, present := row.GetValue("entry"); !present {
				return nil, rbacCDCDiscontinuity("missing clustering key")
			}
			if side == 0 {
				before = append(before, image)
			} else {
				after = append(after, image)
			}
		}
	}
	return rbacCDCFrameChanges(stream, change.Time, before, after)
}

func rbacCDCDiscontinuity(reason string) error {
	return fmt.Errorf("%w: RBAC CDC %s", datastore.ErrNamespaceWatchDiscontinuity, reason)
}

func rbacCDCResource(image *rbacCDCImage) (*rbac.Resource, error) {
	var resource rbac.Resource
	if image.Entry == "" || json.Unmarshal(image.Payload, &resource) != nil ||
		resource.APIVersion != rbac.APIVersion || resource.Metadata.UID != image.UID ||
		rbacEntry(rbac.ResourceKey{Kind: resource.Kind, Name: resource.Metadata.Name}) != image.Entry ||
		rbac.ValidateKey(rbac.ResourceKey{TierID: image.Tier, Kind: resource.Kind, Name: resource.Metadata.Name}) != nil ||
		resource.Kind.Namespaced() != (resource.Metadata.Namespace != "") {
		return nil, rbacCDCDiscontinuity("invalid full resource image")
	}
	if uid, err := uuid.Parse(image.UID); err != nil || uid.String() != image.UID {
		return nil, rbacCDCDiscontinuity("invalid resource UID")
	}
	if resource.Kind.Namespaced() {
		resource.NamespaceUID = image.Tier
	}
	return &resource, nil
}

func rbacCDCFrameChanges(stream string, at gocql.UUID, before, after []*rbacCDCImage) ([]watchjournal.Change, error) {
	if len(before) > rbacCDCMaxFrameChanges || len(after) > rbacCDCMaxFrameChanges {
		return nil, rbacCDCDiscontinuity("frame exceeds tier image bound")
	}
	type pair struct{ before, after *rbacCDCImage }
	images := map[string]*pair{}
	tier, total := "", 0
	for side, rows := range [][]*rbacCDCImage{before, after} {
		for _, image := range rows {
			if image == nil || !rbac.ValidTier(image.Tier) || image.Revision < 1 || tier != "" && tier != image.Tier {
				return nil, rbacCDCDiscontinuity("invalid tier or revision")
			}
			tier = image.Tier
			if len(image.Payload) > 2*rbac.MaxSnapshotBytes-total {
				return nil, rbacCDCDiscontinuity("frame exceeds tier payload bound")
			}
			total += len(image.Payload)
			row := images[image.Entry]
			if row == nil {
				row = &pair{}
				images[image.Entry] = row
			}
			if side == 0 {
				if row.before != nil {
					return nil, rbacCDCDiscontinuity("duplicate preimage")
				}
				row.before = image
			} else {
				if row.after != nil {
					return nil, rbacCDCDiscontinuity("duplicate postimage")
				}
				row.after = image
			}
		}
	}
	if len(images) == 0 {
		return nil, nil
	}
	sentinel := images[""]
	if sentinel == nil || sentinel.after == nil || sentinel.before != nil && sentinel.before.Revision > sentinel.after.Revision {
		return nil, rbacCDCDiscontinuity("missing or regressing revision sentinel")
	}
	if sentinel.before != nil && sentinel.before.Revision == sentinel.after.Revision && len(images) > 1 {
		return nil, rbacCDCDiscontinuity("resource change without tier revision")
	}
	keys := make([]string, 0, len(images)-1)
	for entry := range images {
		if entry != "" {
			keys = append(keys, entry)
		}
	}
	if len(keys) >= rbacCDCMaxFrameChanges {
		return nil, rbacCDCDiscontinuity("frame exceeds changed resource bound")
	}
	slices.Sort(keys)
	changes := make([]watchjournal.Change, 0, len(keys)+1)
	policyUnchanged := len(keys) > 0
	var purged []rbacRepairStep
	prefix := "RBAC:" + stream + ":" + at.String() + ":" + tier + ":"
	for _, entry := range keys {
		pair := images[entry]
		var old, current *rbac.Resource
		var err error
		if pair.before != nil {
			if sentinel.before == nil || pair.before.Revision > sentinel.before.Revision {
				return nil, rbacCDCDiscontinuity("resource preimage has no matching tier revision")
			}
			old, err = rbacCDCResource(pair.before)
			if err != nil {
				return nil, err
			}
		}
		if pair.after != nil {
			if pair.after.Revision != sentinel.after.Revision {
				return nil, rbacCDCDiscontinuity("resource and sentinel revisions differ")
			}
			current, err = rbacCDCResource(pair.after)
			if err != nil {
				return nil, err
			}
		}
		if old != nil && current != nil && (old.Metadata.UID != current.Metadata.UID || old.Metadata.Namespace != current.Metadata.Namespace) {
			return nil, rbacCDCDiscontinuity("resource identity changed")
		}
		policyUnchanged = policyUnchanged && rbac.RolePolicyUnchanged(old, current)
		if old != nil && old.Kind.IsRole() && current == nil {
			purged = append(purged, rbacRepairStep{
				Key: rbac.ResourceKey{TierID: tier, Kind: old.Kind, Name: old.Metadata.Name},
				UID: old.Metadata.UID, Removed: true,
			})
		}
		change, err := watchjournal.RBACChange(old, current)
		if err != nil {
			return nil, rbacCDCDiscontinuity("resource serialization failed")
		}
		change.StreamID, change.Position, change.At = stream, at.Bytes(), at.Time().UTC()
		change.DeduplicationKey = prefix + entry
		changes = append(changes, change)
	}
	policyUnchanged = policyUnchanged && rbacCDCPolicyStateUnchanged(sentinel.before, sentinel.after, purged)
	if !policyUnchanged && (sentinel.before == nil || sentinel.before.Revision != sentinel.after.Revision) {
		changes = append(changes, watchjournal.Change{
			Kind: "RBAC", StreamID: stream, Position: at.Bytes(), At: at.Time().UTC(),
			DeduplicationKey: prefix + "progress",
			TierProgress:     &datastore.RBACTierProgress{TierID: tier, Revision: uint64(sentinel.after.Revision)},
		})
	}
	return changes, nil
}

func rbacCDCPolicyStateUnchanged(before, after *rbacCDCImage, purged []rbacRepairStep) bool {
	if before == nil || after == nil || !before.CompleteState || !after.CompleteState ||
		before.Revision+1 != after.Revision || before.Retired || after.Retired ||
		before.RoleCount < 1 || before.BindingCount < 0 ||
		after.RoleCount < 0 || before.RoleCount-len(purged) != after.RoleCount || before.BindingCount != after.BindingCount ||
		len(before.Projection) == 0 || len(before.Projection) > 4096 ||
		!bytes.Equal(before.Projection, after.Projection) || !json.Valid(before.Projection) {
		return false
	}
	var projection rbac.ProjectionIdentity
	if json.Unmarshal(before.Projection, &projection) != nil || projection.RepositoryID == "" ||
		projection.GitRef == "" || projection.GitRevision == "" || projection.TreeDigest == "" || projection.Actor == "" {
		return false
	}
	if len(before.Repair) > 0 && !bytes.Equal(before.Repair, []byte("null")) && !bytes.Equal(before.Repair, []byte("[]")) {
		return false
	}
	if len(purged) == 0 {
		return len(after.Repair) == 0 || bytes.Equal(after.Repair, []byte("null")) || bytes.Equal(after.Repair, []byte("[]"))
	}
	if len(after.Repair) > len(purged)*4096 {
		return false
	}
	var steps []rbacRepairStep
	if json.Unmarshal(after.Repair, &steps) != nil || len(steps) != len(purged) {
		return false
	}
	canonical, err := json.Marshal(steps)
	if err != nil || !bytes.Equal(canonical, after.Repair) {
		return false
	}
	expected := make(map[rbac.ResourceKey]rbacRepairStep, len(purged))
	for _, step := range purged {
		expected[step.Key] = step
	}
	for _, step := range steps {
		if want, found := expected[step.Key]; !found || step != want {
			return false
		}
		delete(expected, step.Key)
	}
	return len(expected) == 0
}
