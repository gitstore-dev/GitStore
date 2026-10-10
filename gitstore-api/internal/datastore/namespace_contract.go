// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"math/big"
)

// NamespaceDeletionBlocked is a bounded preflight, not a transaction. Backends
// must repeat the decision under their namespace creation fence when marking.
func NamespaceDeletionBlocked(ctx context.Context, store RepositoryStore, namespace *Namespace) (bool, error) {
	if namespace == nil || namespace.Name == "" {
		return false, ErrInvalidArgument
	}
	page, err := store.ListRepositoriesByNamespace(ctx, namespace.Name, PageParams{First: 2})
	if err != nil {
		return false, err
	}
	if page == nil {
		return false, ErrInvalidArgument
	}
	if len(page.Items) == 0 {
		return page.HasNext, nil
	}
	if len(page.Items) != 1 || page.HasNext || !IsNamespaceSystemRepository(namespace, page.Items[0]) {
		return true, nil
	}
	return store.HasCatalogResources(ctx, page.Items[0].UID)
}

// IsNamespaceSystemRepository recognizes the datastore-only provisioned
// repository, not an ordinary manifest that happens to use the reserved name.
// Provisioning currently records namespace name and self repository identity;
// it does not record a Namespace owner UID.
func IsNamespaceSystemRepository(namespace *Namespace, repository *Repository) bool {
	return namespace != nil && repository != nil && namespace.Name != "" &&
		repository.Namespace == namespace.Name && repository.Name == "gitstore-system" &&
		repository.UID != "" && repository.RepositoryID == repository.UID && repository.SourcePath == ""
}

// DeletionFinalizersClear permits only the finalizer owned by this completion.
func DeletionFinalizersClear(finalizers []string, owned string) bool {
	for _, finalizer := range finalizers {
		if finalizer != owned {
			return false
		}
	}
	return true
}

// HasDeletionIntent includes malformed status as a fail-closed lifecycle fence.
func HasDeletionIntent(status json.RawMessage) bool {
	if len(status) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(status, &fields) != nil {
		return true
	}
	value := fields["deletionIntent"]
	return len(value) > 0 && string(value) != "null"
}

// PreserveDeletionStatus retains extension fields and the pending condition
// across typed controller patches. An admitted intent is system-owned.
func PreserveDeletionStatus(previous, updated json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	next := map[string]json.RawMessage{}
	if len(previous) > 0 {
		if err := json.Unmarshal(previous, &fields); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(updated, &next); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	for key, value := range next {
		if key != "deletionIntent" || !HasDeletionIntent(previous) {
			fields[key] = value
		}
	}
	if HasDeletionIntent(previous) {
		var conditions []map[string]json.RawMessage
		if raw := fields["conditions"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &conditions); err != nil {
				return nil, err
			}
		}
		filtered := conditions[:0]
		for _, condition := range conditions {
			if string(condition["type"]) != `"DeletionPending"` {
				filtered = append(filtered, condition)
			}
		}
		var old struct {
			Conditions []map[string]json.RawMessage `json:"conditions"`
		}
		if err := json.Unmarshal(previous, &old); err != nil {
			return nil, err
		}
		found := false
		for _, condition := range old.Conditions {
			if string(condition["type"]) == `"DeletionPending"` {
				condition["status"] = json.RawMessage(`"True"`)
				filtered = append(filtered, condition)
				found = true
				break
			}
		}
		if !found {
			filtered = append(filtered, map[string]json.RawMessage{
				"type": json.RawMessage(`"DeletionPending"`), "status": json.RawMessage(`"True"`),
			})
		}
		raw, err := json.Marshal(filtered)
		if err != nil {
			return nil, err
		}
		fields["conditions"] = raw
	}
	return json.Marshal(fields)
}

// MergeInfrastructureStatus permits core lifecycle CAS writes to advance an
// intent's expected tip or attach its receipt without changing its identity.
// Controller status patches must use PreserveDeletionStatus instead.
func MergeInfrastructureStatus(previous, updated json.RawMessage) (json.RawMessage, error) {
	merged, err := PreserveDeletionStatus(previous, updated)
	if err != nil || !HasDeletionIntent(previous) || !HasDeletionIntent(updated) {
		return merged, err
	}
	var old, next, fields map[string]json.RawMessage
	if err := json.Unmarshal(previous, &old); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(updated, &next); err != nil {
		return nil, err
	}
	var oldIntent, nextIntent map[string]json.RawMessage
	if err := json.Unmarshal(old["deletionIntent"], &oldIntent); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(next["deletionIntent"], &nextIntent); err != nil {
		return nil, err
	}
	for _, key := range []string{"uid", "repositoryID", "path", "ref", "actor"} {
		if string(oldIntent[key]) != string(nextIntent[key]) {
			return nil, ErrConflict
		}
	}
	if receipt := oldIntent["removalCommit"]; len(receipt) > 0 && string(receipt) != `""` &&
		(string(receipt) != string(nextIntent["removalCommit"]) || string(oldIntent["expectedCommit"]) != string(nextIntent["expectedCommit"])) {
		return nil, ErrConflict
	}
	if err := json.Unmarshal(merged, &fields); err != nil {
		return nil, err
	}
	fields["deletionIntent"] = next["deletionIntent"]
	return json.Marshal(fields)
}

const (
	NamespaceInitialGeneration           int64  = 1
	NamespaceInitialResourceVersion      string = "1"
	NamespaceForegroundDeletionFinalizer        = "gitstore.dev/foreground-deletion"
)

var namespaceInitialStatus = json.RawMessage(`{"observedGeneration":0,"conditions":[]}`)

func NormalizeNamespaceContract(namespace *Namespace) {
	if namespace == nil {
		return
	}
	if namespace.UID == "" {
		namespace.UID = namespace.ID
	}
	if namespace.ID == "" {
		namespace.ID = namespace.UID
	}
	if namespace.Generation < NamespaceInitialGeneration {
		namespace.Generation = NamespaceInitialGeneration
	}
	if !validNamespaceResourceVersion(namespace.ResourceVersion) {
		namespace.ResourceVersion = NamespaceInitialResourceVersion
	}
	if len(namespace.Status) == 0 {
		namespace.Status = append(json.RawMessage(nil), namespaceInitialStatus...)
	}
	if namespace.Finalizers == nil {
		namespace.Finalizers = []string{}
	}
}

func AdvanceNamespaceSpecVersion(namespace *Namespace) {
	NormalizeNamespaceContract(namespace)
	namespace.Generation++
	advanceNamespaceResourceVersion(namespace)
}

func AdvanceNamespaceSystemVersion(namespace *Namespace) {
	NormalizeNamespaceContract(namespace)
	advanceNamespaceResourceVersion(namespace)
}

func validNamespaceResourceVersion(value string) bool {
	version, ok := new(big.Int).SetString(value, 10)
	return ok && version.Sign() > 0
}

func advanceNamespaceResourceVersion(namespace *Namespace) {
	version, _ := new(big.Int).SetString(namespace.ResourceVersion, 10)
	version.Add(version, big.NewInt(1))
	namespace.ResourceVersion = version.String()
}
