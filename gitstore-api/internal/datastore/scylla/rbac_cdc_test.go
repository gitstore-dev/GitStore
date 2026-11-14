// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	scyllacdc "github.com/scylladb/scylla-cdc-go"
	"github.com/stretchr/testify/require"
)

func rbacCDCFixture(t *testing.T, tier string, kind rbac.Kind, version string) *rbacCDCImage {
	t.Helper()
	name := "reader"
	resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: kind,
		Metadata: rbac.Metadata{Name: name, UID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(tier+string(kind))).String(),
			Generation: 1, ResourceVersion: version, Labels: map[string]string{"version": version}},
		Spec: json.RawMessage(`{"rules":[]}`)}
	if kind.Namespaced() {
		resource.Metadata.Namespace, resource.NamespaceUID = "shop", tier
	}
	if !kind.IsRole() {
		resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"reader"},"subjects":[{"kind":"User","name":"alice"}]}`)
	}
	payload, err := json.Marshal(resource)
	require.NoError(t, err)
	return &rbacCDCImage{Tier: tier, Entry: string(kind) + "/" + name, UID: resource.Metadata.UID, Payload: payload, Revision: 1}
}

func TestRBACCDCDiscriminatesWholeFramesAndRetainsDeletionUID(t *testing.T) {
	for _, tier := range []string{rbac.ClusterTierID, uuid.NewString()} {
		kinds := []rbac.Kind{rbac.ClusterRole, rbac.ClusterRoleBinding}
		if tier != rbac.ClusterTierID {
			kinds = []rbac.Kind{rbac.Role, rbac.RoleBinding}
		}
		t.Run(tier, func(t *testing.T) {
			first, second := rbacCDCFixture(t, tier, kinds[0], "1"), rbacCDCFixture(t, tier, kinds[1], "1")
			sentinel := &rbacCDCImage{Tier: tier, Revision: 1}
			at := gocql.TimeUUID()
			images := []*rbacCDCImage{second, sentinel, first}
			changes, err := rbacCDCFrameChanges("stream", at, nil, images)
			require.NoError(t, err)
			require.Len(t, changes, 3)
			slices.Reverse(images)
			reordered, err := rbacCDCFrameChanges("stream", at, nil, images)
			require.NoError(t, err)
			require.Equal(t, changes, reordered)
			duplicate, err := rbacCDCFrameChanges("stream", at, nil, images)
			require.NoError(t, err)
			require.Equal(t, changes, duplicate, "whole-frame replay must retain exact deduplication identities")
			for index, change := range changes[:2] {
				event, ok := watchjournal.ClassifyResource(change, nil)
				require.True(t, ok)
				require.Equal(t, datastore.ResourceWatchAdded, event.Type)
				require.Contains(t, []string{string(kinds[0]), string(kinds[1])}, event.Kind)
				require.Equal(t, "1", event.SelectorLabels["version"])
				require.NotEqual(t, changes[2].DeduplicationKey, event.DeduplicationKey)
				if index == 1 {
					require.NotEqual(t, changes[0].DeduplicationKey, event.DeduplicationKey)
				}
			}
			require.Equal(t, &datastore.RBACTierProgress{TierID: tier, Revision: 1}, changes[2].TierProgress)
			nextSentinel := &rbacCDCImage{Tier: tier, Revision: 2}
			deleted, err := rbacCDCFrameChanges("stream", gocql.TimeUUID(), []*rbacCDCImage{first, sentinel}, []*rbacCDCImage{nextSentinel})
			require.NoError(t, err)
			require.Len(t, deleted, 2)
			event, ok := watchjournal.ClassifyResource(deleted[0], nil)
			require.True(t, ok)
			require.Equal(t, datastore.ResourceWatchDeleted, event.Type)
			var identity rbac.Resource
			require.NoError(t, json.Unmarshal(event.Payload, &identity))
			require.Equal(t, first.UID, identity.Metadata.UID)
			require.Empty(t, identity.Spec)
		})
	}
}

func TestRBACCDCRejectsPartialCorruptAndUnboundedFrames(t *testing.T) {
	for _, invalid := range []string{"nil-image", "missing-sentinel", "duplicate-row", "cross-tier", "payload-uid", "wrong-entry", "wrong-scope", "too-many", "missing-revision", "same-revision-resource", "future-preimage", "missing-preimage-sentinel"} {
		t.Run(invalid, func(t *testing.T) {
			image := rbacCDCFixture(t, rbac.ClusterTierID, rbac.ClusterRole, "1")
			images := []*rbacCDCImage{image, {Tier: image.Tier, Revision: 1}}
			var before []*rbacCDCImage
			switch invalid {
			case "nil-image":
				images = append(images, nil)
			case "missing-sentinel":
				images = images[:1]
			case "duplicate-row":
				images = append(images, image)
			case "cross-tier":
				images[1].Tier = uuid.NewString()
			case "payload-uid":
				image.UID = uuid.NewString()
			case "wrong-entry":
				image.Entry = "ClusterRole/other"
			case "wrong-scope":
				image.Tier, images[1].Tier = uuid.NewString(), uuid.NewString()
			case "too-many":
				images = make([]*rbacCDCImage, rbacCDCMaxFrameChanges+1)
			case "missing-revision":
				image.Revision = 0
			case "same-revision-resource":
				before = []*rbacCDCImage{images[1]}
			case "future-preimage", "missing-preimage-sentinel":
				before = []*rbacCDCImage{image}
				if invalid == "future-preimage" {
					image.Revision = 2
					before = append(before, &rbacCDCImage{Tier: image.Tier, Revision: 1})
				}
				images = []*rbacCDCImage{{Tier: image.Tier, Revision: 3}}
			}
			_, err := rbacCDCFrameChanges("stream", gocql.TimeUUID(), before, images)
			require.ErrorIs(t, err, datastore.ErrNamespaceWatchDiscontinuity)
		})
	}
}

func TestRBACCDCCompleteCatchupExceedsExternalCandidateBound(t *testing.T) {
	images := []*rbacCDCImage{{Tier: rbac.ClusterTierID, Revision: 1}}
	for index := range rbac.MaxCandidateResources + 1 {
		image := rbacCDCFixture(t, rbac.ClusterTierID, rbac.ClusterRole, "1")
		var resource rbac.Resource
		require.NoError(t, json.Unmarshal(image.Payload, &resource))
		resource.Metadata.Name, resource.Metadata.UID = fmt.Sprintf("reader-%d", index), uuid.NewString()
		image.Entry, image.UID = string(resource.Kind)+"/"+resource.Metadata.Name, resource.Metadata.UID
		var err error
		image.Payload, err = json.Marshal(resource)
		require.NoError(t, err)
		images = append(images, image)
	}
	changes, err := rbacCDCFrameChanges("stream", gocql.TimeUUID(), nil, images)
	require.NoError(t, err)
	require.Len(t, changes, len(images))
	require.Equal(t, &datastore.RBACTierProgress{TierID: rbac.ClusterTierID, Revision: 1}, changes[len(changes)-1].TierProgress)
}

func TestRBACCDCRejectsDeltaWithoutFullImages(t *testing.T) {
	_, err := rbacCDCChanges("stream", scyllacdc.Change{Delta: []*scyllacdc.ChangeRow{{}}})
	require.ErrorIs(t, err, datastore.ErrNamespaceWatchDiscontinuity)
	changes, err := rbacCDCChanges("stream", scyllacdc.Change{})
	require.NoError(t, err)
	require.Empty(t, changes)
}

func TestRBACCDCRepairIsProgressOnlyAndSharesSequencer(t *testing.T) {
	sentinel := &rbacCDCImage{Tier: rbac.ClusterTierID, Revision: 1}
	changes, err := rbacCDCFrameChanges("stream", gocql.TimeUUID(), []*rbacCDCImage{sentinel}, []*rbacCDCImage{sentinel})
	require.NoError(t, err)
	require.Empty(t, changes, "repair-only sentinel changes cannot duplicate resource or revision events")
	row := rbacCDCFixture(t, rbac.ClusterTierID, rbac.ClusterRole, "1")
	changes, err = rbacCDCFrameChanges("stream", gocql.TimeUUID(), nil, []*rbacCDCImage{row, sentinel})
	require.NoError(t, err)
	store := &sequencerStore{}
	sequencer := newNamespaceCDCSequencer(watchjournal.NewMaterializer(store, watchjournal.MaterializerConfig{}), datastore.ResourceWatchLease{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- sequencer.Run(ctx) }()
	defer func() { cancel(); require.ErrorIs(t, <-done, context.Canceled) }()
	beginSequenceTestGeneration(t, sequencer, ctx, []string{"stream"})
	require.NoError(t, sequencer.Register(ctx, "stream"))
	checkpoint := make(chan struct{}, 1)
	require.NoError(t, sequencer.Submit(ctx, namespaceCDCSequenceRequest{streamID: "stream", cdcTime: gocql.TimeUUID(), changes: changes,
		markProgress: func(context.Context) error { checkpoint <- struct{}{}; return nil }}))
	require.NoError(t, sequencer.Unregister("stream"))
	select {
	case <-checkpoint:
	case <-time.After(time.Second):
		t.Fatal("RBAC source checkpoint was not saved")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.events, 2)
	require.Equal(t, datastore.ResourceWatchAdded, store.events[0].Type)
	require.Equal(t, datastore.ResourceWatchProgress, store.events[1].Type)
}
