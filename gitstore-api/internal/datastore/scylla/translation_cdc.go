// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
)

func translationCDCChanges(stream string, change scyllacdc.Change) ([]watchjournal.Change, error) {
	if len(change.PreImage) > catalogCDCMaxRowsPerChange || len(change.PostImage) > catalogCDCMaxRowsPerChange {
		return nil, translationCDCDiscontinuity("frame exceeds image bound")
	}
	if len(change.Delta) > 0 && len(change.PreImage) == 0 && len(change.PostImage) == 0 {
		return nil, translationCDCDiscontinuity("delta has no full images")
	}
	var before, after []*translationSourceRow
	for side, rows := range [][]*scyllacdc.ChangeRow{change.PreImage, change.PostImage} {
		for _, image := range rows {
			base := catalogCDCRow([]*scyllacdc.ChangeRow{image})
			if base == nil {
				return nil, translationCDCDiscontinuity("nil image")
			}
			row := &translationSourceRow{productRow: *base}
			assignCDC(image, "namespace_uid", &row.NamespaceUID)
			assignCDC(image, "source_blob_oid", &row.SourceBlobOID)
			assignCDC(image, "source_digest", &row.SourceDigest)
			assignCDC(image, "raw_source", &row.RawSource)
			assignCDC(image, "coordination_version", &row.CoordinationVersion)
			assignCDC(image, "phase", &row.Phase)
			assignCDC(image, "revalidation_version", &row.RevalidationVersion)
			if side == 0 {
				before = append(before, row)
			} else {
				after = append(after, row)
			}
		}
	}
	return translationCDCFrameChanges(stream, change.Time, before, after)
}

func translationCDCDiscontinuity(reason string) error {
	return fmt.Errorf("%w: Translation CDC %s", datastore.ErrNamespaceWatchDiscontinuity, reason)
}

func translationCDCFrameChanges(stream string, at gocql.UUID, before, after []*translationSourceRow) ([]watchjournal.Change, error) {
	if len(before) > catalogCDCMaxRowsPerChange || len(after) > catalogCDCMaxRowsPerChange {
		return nil, translationCDCDiscontinuity("frame exceeds image bound")
	}
	type pair struct{ before, after *translationSourceRow }
	images := map[gocql.UUID]*pair{}
	for side, rows := range [][]*translationSourceRow{before, after} {
		for _, row := range rows {
			if row == nil || row.UID == (gocql.UUID{}) || row.NamespaceUID == (gocql.UUID{}) || row.CreationTimestamp.IsZero() {
				return nil, translationCDCDiscontinuity("missing authoritative key")
			}
			if _, err := datastore.AdvanceTranslationIndexVersion(row.CoordinationVersion); err != nil {
				return nil, translationCDCDiscontinuity("invalid coordination version")
			}
			switch row.Phase {
			case "Creating", "Stable", "Updating", "Deleting":
				if datastore.ValidateTranslationSource(row.source()) != nil {
					return nil, translationCDCDiscontinuity("invalid full source")
				}
			case "Retired":
				if row.ResourceVersion == "" {
					return nil, translationCDCDiscontinuity("retired source lost public version")
				}
			default:
				return nil, translationCDCDiscontinuity("unknown source phase")
			}
			image := images[row.UID]
			if image == nil {
				if len(images) == catalogCDCMaxRowsPerChange {
					return nil, translationCDCDiscontinuity("frame exceeds resource bound")
				}
				image = &pair{}
				images[row.UID] = image
			}
			if side == 0 {
				if image.before != nil {
					return nil, translationCDCDiscontinuity("duplicate preimage")
				}
				image.before = row
			} else {
				if image.after != nil {
					return nil, translationCDCDiscontinuity("duplicate postimage")
				}
				image.after = row
			}
		}
	}
	ids := make([]gocql.UUID, 0, len(images))
	for uid := range images {
		ids = append(ids, uid)
	}
	slices.SortFunc(ids, func(a, b gocql.UUID) int { return bytes.Compare(a[:], b[:]) })
	changes := make([]watchjournal.Change, 0, len(ids))
	for _, uid := range ids {
		image := images[uid]
		old, current, err := translationCDCTransition(image.before, image.after)
		if err != nil {
			return nil, err
		}
		if len(old) == 0 && len(current) == 0 {
			continue
		}
		envelope := image.after
		var deletion json.RawMessage
		if len(current) == 0 {
			envelope = image.before
			deletion = old
		}
		changes = append(changes, watchjournal.Change{
			Kind: "Translation", Namespace: envelope.Namespace, Name: envelope.Name, Before: old, After: current,
			DeletionPayload: deletion,
			StreamID:        stream, Position: at.Bytes(), At: at.Time().UTC(),
			DeduplicationKey: "Translation:" + stream + ":" + at.String() + ":" + uid.String(),
			ForceModified:    image.before != nil && image.after != nil && image.before.RevalidationVersion != image.after.RevalidationVersion,
		})
	}
	return changes, nil
}

func translationCDCTransition(before, after *translationSourceRow) (json.RawMessage, json.RawMessage, error) {
	if after == nil {
		return nil, nil, translationCDCDiscontinuity("authoritative identity was physically deleted")
	}
	if before == nil {
		if (after.Phase != "Creating" && after.Phase != "Retired") || after.CoordinationVersion != "1" || after.ResourceVersion != "1" {
			return nil, nil, translationCDCDiscontinuity("source appeared without provisional creation")
		}
		return nil, nil, nil
	}
	if before.NamespaceUID != after.NamespaceUID || !before.CreationTimestamp.Equal(after.CreationTimestamp) {
		return nil, nil, translationCDCDiscontinuity("images disagree on immutable key")
	}
	if before.CoordinationVersion == after.CoordinationVersion {
		if before.Phase != after.Phase {
			return nil, nil, translationCDCDiscontinuity("phase changed without coordination")
		}
	} else if datastore.ValidateCatalogUpdateVersion(before.CoordinationVersion, before.CoordinationVersion, after.CoordinationVersion) != nil {
		return nil, nil, translationCDCDiscontinuity("coordination version regressed")
	}
	if after.Phase == "Retired" {
		if before.ResourceVersion != after.ResourceVersion {
			return nil, nil, translationCDCDiscontinuity("retirement changed public version")
		}
		switch before.Phase {
		case "Creating", "Retired":
			return nil, nil, nil
		case "Deleting":
			old, err := json.Marshal(before.source())
			return old, nil, err
		default:
			return nil, nil, translationCDCDiscontinuity("retirement bypassed deletion intent")
		}
	}
	sameRepository := before.RepositoryID == after.RepositoryID ||
		before.RepositoryID != nil && after.RepositoryID != nil && *before.RepositoryID == *after.RepositoryID
	if before.Phase == "Retired" || before.Namespace != after.Namespace || before.Name != after.Name ||
		!sameRepository || before.CreationActor != after.CreationActor {
		return nil, nil, translationCDCDiscontinuity("source identity changed or resurrected")
	}
	old, err := json.Marshal(before.source())
	if err != nil {
		return nil, nil, translationCDCDiscontinuity("invalid source preimage")
	}
	current, err := json.Marshal(after.source())
	if err != nil {
		return nil, nil, translationCDCDiscontinuity("invalid source postimage")
	}
	sameSource := bytes.Equal(old, current)
	if before.Phase == "Stable" && after.Phase == "Stable" && before.RevalidationVersion != after.RevalidationVersion {
		if !sameSource || before.CoordinationVersion != after.CoordinationVersion ||
			after.RevalidationVersion != after.ResourceVersion {
			return nil, nil, translationCDCDiscontinuity("revalidation changed source or lost its version fence")
		}
		return old, current, nil
	}
	switch before.Phase + "/" + after.Phase {
	case "Creating/Stable":
		if !sameSource {
			return nil, nil, translationCDCDiscontinuity("creation changed provisional source")
		}
		return nil, current, nil
	case "Updating/Stable":
		if before.ResourceVersion != after.ResourceVersion {
			if datastore.ValidateCatalogUpdateVersion(before.ResourceVersion, before.ResourceVersion, after.ResourceVersion) != nil {
				return nil, nil, translationCDCDiscontinuity("public version regressed")
			}
			return old, current, nil
		}
	case "Stable/Updating", "Stable/Deleting", "Stable/Stable", "Creating/Creating", "Updating/Updating", "Deleting/Deleting":
	default:
		return nil, nil, translationCDCDiscontinuity("invalid phase transition")
	}
	if !sameSource {
		return nil, nil, translationCDCDiscontinuity("private transition changed public source")
	}
	return nil, nil, nil
}
