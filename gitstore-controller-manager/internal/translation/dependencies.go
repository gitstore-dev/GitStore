// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

type DependencyTarget struct {
	NamespaceUID string
	APIVersion   string
	Kind         string
	Name         string
	UID          string
}

type TargetObservation struct {
	Target          DependencyTarget
	ResourceVersion string
	Generation      int64
	Revision        string
	ContentDigest   string
	Terminating     bool
}

type fanoutRequester interface {
	RequestCoalescedFanout(context.Context, string) error
}

func DependencyIndexes(target DependencyTarget, versionPinned bool) ([]string, error) {
	if err := validateDependencyTarget(target); err != nil {
		return nil, err
	}
	indexes := targetIndexes(target, "material")
	if versionPinned {
		indexes = append(indexes, targetIndexes(target, "pinned")...)
	}
	return indexes, nil
}

// QueueTargetChange must succeed before advancing the target watch cursor.
// DiskStore persists range requests, then atomically checkpoints each bounded
// fan-out page together with the dependent work it enqueues.
func QueueTargetChange(ctx context.Context, pending fanoutRequester, previous, current *TargetObservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, observation := range []*TargetObservation{previous, current} {
		if observation == nil {
			continue
		}
		if err := validateDependencyTarget(observation.Target); err != nil {
			return err
		}
		if observation.Target.UID == "" || observation.ResourceVersion == "" || len(observation.ResourceVersion) > 2048 {
			return fmt.Errorf("translation: incomplete target observation")
		}
	}
	if previous == nil && current == nil {
		return nil
	}
	scope := "material"
	if previous != nil && current != nil &&
		previous.Target == current.Target && previous.Generation == current.Generation &&
		previous.Revision == current.Revision && previous.ContentDigest != "" &&
		previous.ContentDigest == current.ContentDigest &&
		previous.Terminating == current.Terminating {
		if previous.ResourceVersion == current.ResourceVersion {
			return nil
		}
		scope = "pinned"
	}
	if pending == nil {
		return fmt.Errorf("translation: missing durable fan-out store")
	}
	requested := make(map[string]struct{}, 4)
	for _, observation := range []*TargetObservation{previous, current} {
		if observation == nil {
			continue
		}
		for _, index := range targetIndexes(observation.Target, scope) {
			if _, duplicate := requested[index]; duplicate {
				continue
			}
			if err := pending.RequestCoalescedFanout(ctx, index); err != nil {
				return fmt.Errorf("translation: persist target invalidation: %w", err)
			}
			requested[index] = struct{}{}
		}
	}
	return nil
}

func targetIndexes(target DependencyTarget, scope string) []string {
	makeIndex := func(kind, identity string) string {
		encoded, _ := json.Marshal([]string{target.NamespaceUID, target.APIVersion, target.Kind, identity})
		digest := sha256.Sum256(encoded)
		return "translation/" + scope + "/" + kind + "/" + hex.EncodeToString(digest[:])
	}
	indexes := []string{makeIndex("name", target.Name)}
	if target.UID != "" {
		indexes = append(indexes, makeIndex("uid", target.UID))
	}
	return indexes
}

func validateDependencyTarget(target DependencyTarget) error {
	for _, component := range []string{target.NamespaceUID, target.APIVersion, target.Kind, target.Name} {
		if component == "" || len(component) > 2048 || !utf8.ValidString(component) || strings.ContainsRune(component, 0) {
			return fmt.Errorf("translation: invalid dependency target")
		}
	}
	if len(target.UID) > 2048 || !utf8.ValidString(target.UID) || strings.ContainsRune(target.UID, 0) {
		return fmt.Errorf("translation: invalid dependency target UID")
	}
	return nil
}
