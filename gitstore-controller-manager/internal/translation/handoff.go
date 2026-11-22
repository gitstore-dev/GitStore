// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	contract "github.com/gitstore-dev/gitstore/translation"
)

type TargetHint struct {
	Namespace, APIVersion, Kind, Name, UID, ResourceVersion string
	Terminating                                             bool
}

type targetObserver interface {
	ObserveTarget(context.Context, string, string, string, string) (*TargetObservation, error)
}

type handoffStore interface {
	fanoutRequester
	ReadHint(context.Context, string) ([]byte, error)
	WriteHint(context.Context, string, []byte) error
}

// Persisted observations classify invalidation only; they never authorize a
// read, establish current Ready or replace a fresh validation snapshot.
type targetHandoffState struct {
	Hint        TargetHint
	Observation TargetObservation
}

type TargetHandoff struct {
	store    handoffStore
	observer targetObserver
	metrics  *metrics
}

func NewTargetHandoff(store handoffStore, observer targetObserver) *TargetHandoff {
	return &TargetHandoff{store: store, observer: observer, metrics: defaultMetrics}
}

func SourceDependencyIndexes(source Resource, binding *BindingState) ([]string, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	var spec contract.Spec
	if err := json.Unmarshal(source.Spec, &spec); err != nil {
		return nil, err
	}
	ref := spec.TargetRef
	hint := TargetHint{Namespace: source.Metadata.Namespace, APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name}
	if err := hint.validate(); err != nil || ref.Namespace != "" && ref.Namespace != hint.Namespace {
		return nil, errors.New("translation: invalid source dependency")
	}
	indexes := []string{namespaceDependencyIndex(hint.Namespace), hintIndex(hint, "material")}
	if ref.ResourceVersion != "" {
		indexes = append(indexes, hintIndex(hint, "pinned"))
	}
	if binding == nil {
		return indexes, nil
	}
	if binding.validate() != nil || binding.TranslationID != source.Metadata.UID || binding.SourceVersion != source.Metadata.ResourceVersion {
		return nil, errors.New("translation: contradictory source dependency binding")
	}
	target := DependencyTarget{NamespaceUID: binding.NamespaceUID, APIVersion: ref.APIVersion,
		Kind: ref.Kind, Name: ref.Name, UID: ref.UID}
	if binding.BoundUID != nil {
		target.UID = *binding.BoundUID
	}
	authoritative, err := DependencyIndexes(target, ref.ResourceVersion != "")
	if err != nil {
		return nil, err
	}
	return append(indexes, authoritative...), nil
}

func QueueNamespaceChange(ctx context.Context, pending fanoutRequester, namespace string) error {
	if namespace == "" || len(namespace) > 2048 || !utf8.ValidString(namespace) || pending == nil {
		return errors.New("translation: invalid namespace invalidation")
	}
	return pending.RequestCoalescedFanout(ctx, namespaceDependencyIndex(namespace))
}

func (h *TargetHandoff) Change(ctx context.Context, previous, current *TargetHint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h == nil || h.store == nil || h.observer == nil {
		return errors.New("translation: incomplete target handoff")
	}
	for _, hint := range []*TargetHint{previous, current} {
		if hint != nil {
			if err := hint.validate(); err != nil {
				return err
			}
		}
	}
	if previous != nil && current != nil && hintKey(*previous) != hintKey(*current) {
		if err := h.change(ctx, *previous, nil); err != nil {
			return err
		}
	}
	if current != nil {
		return h.change(ctx, *current, current)
	}
	if previous != nil {
		return h.change(ctx, *previous, nil)
	}
	return nil
}

func (h *TargetHandoff) change(ctx context.Context, identity TargetHint, current *TargetHint) (err error) {
	started, scope := time.Now(), "unknown"
	defer func() { h.metrics.observeHandoff(ctx, scope, started, err) }()
	key := hintKey(identity)
	raw, err := h.store.ReadHint(ctx, key)
	if err != nil {
		return err
	}
	var previous *targetHandoffState
	if len(raw) != 0 {
		previous = &targetHandoffState{}
		if json.Unmarshal(raw, previous) != nil || previous.Hint.validate() != nil ||
			hintKey(previous.Hint) != key || validateDependencyTarget(previous.Observation.Target) != nil ||
			previous.Observation.Target.UID == "" || previous.Observation.ContentDigest == "" ||
			previous.Observation.ResourceVersion == "" || previous.Observation.Generation < 1 || previous.Observation.Terminating {
			return errors.New("translation: invalid persisted target hint")
		}
	}
	var next *targetHandoffState
	if current != nil && !current.Terminating {
		observed, err := h.observer.ObserveTarget(ctx, identity.Namespace, identity.APIVersion, identity.Kind, identity.Name)
		h.metrics.observeLookup(ctx, "target", observed != nil, err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil && observed != nil && observed.Target.APIVersion == identity.APIVersion &&
			observed.Target.Kind == identity.Kind && observed.Target.Name == identity.Name &&
			validateDependencyTarget(observed.Target) == nil && observed.Target.UID != "" &&
			observed.ResourceVersion != "" && observed.Generation > 0 && observed.ContentDigest != "" && !observed.Terminating {
			next = &targetHandoffState{Hint: *current, Observation: *observed}
		}
		// Unavailable or denied proof may trigger conservative work, never a
		// material no-op certificate or a claim that the target is absent.
	}
	scope = "material"
	if previous != nil && next != nil {
		before, after := previous.Observation, next.Observation
		before.ResourceVersion, after.ResourceVersion = "", ""
		if before == after && previous.Hint.UID == next.Hint.UID {
			scope = "pinned"
			if previous.Hint.ResourceVersion == next.Hint.ResourceVersion &&
				previous.Observation.ResourceVersion == next.Observation.ResourceVersion {
				scope = ""
			}
		}
	}
	if scope != "" {
		indexes := []string{hintIndex(identity, scope)}
		for _, state := range []*targetHandoffState{previous, next} {
			if state != nil {
				indexes = append(indexes, targetIndexes(state.Observation.Target, scope)...)
			}
		}
		seen := make(map[string]bool, len(indexes))
		for _, index := range indexes {
			if seen[index] {
				continue
			}
			if err := h.store.RequestCoalescedFanout(ctx, index); err != nil {
				return fmt.Errorf("translation: persist target handoff: %w", err)
			}
			seen[index] = true
		}
	}
	var data []byte
	if next != nil {
		data, err = json.Marshal(next)
		if err != nil {
			return err
		}
	}
	// Requests precede the observation. Replaying a saved observation is safe
	// only because every associated fan-out request is already durable.
	return h.store.WriteHint(ctx, key, data)
}

func (h TargetHint) validate() error {
	for _, part := range []string{h.Namespace, h.APIVersion, h.Kind, h.Name} {
		if part == "" || len(part) > 2048 || !utf8.ValidString(part) || strings.ContainsRune(part, 0) {
			return errors.New("translation: invalid target hint")
		}
	}
	if len(h.UID) > 2048 || len(h.ResourceVersion) > 2048 {
		return errors.New("translation: oversized target hint")
	}
	_, err := contract.DescriptorFor(h.APIVersion, h.Kind)
	return err
}

func hintKey(hint TargetHint) string {
	return "translation-target/" + dependencyDigest(hint.Namespace, hint.APIVersion, hint.Kind, hint.Name)
}

func hintIndex(hint TargetHint, scope string) string {
	return "translation/" + scope + "/local-name/" + dependencyDigest(hint.Namespace, hint.APIVersion, hint.Kind, hint.Name)
}

func namespaceDependencyIndex(namespace string) string {
	return "translation/namespace/" + dependencyDigest(namespace)
}

func dependencyDigest(parts ...string) string {
	raw, _ := json.Marshal(parts)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
