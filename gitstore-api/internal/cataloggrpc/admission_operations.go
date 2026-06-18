// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
)

type resourceIdentity struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

func (i resourceIdentity) key() string {
	return strings.Join([]string{i.APIVersion, i.Kind, i.Namespace, i.Name}, "\x00")
}

type parsedEntry struct {
	path        string
	parsed      *validate.ParsedResource
	body        []byte
	identity    resourceIdentity
	contentHash string
}

type resourceAdmissionOperation struct {
	operation      admission.Operation
	identity       resourceIdentity
	oldEntry       *parsedEntry
	newEntry       *parsedEntry
	pathChanged    bool
	contentChanged bool
}

type comparableResource struct {
	APIVersion  string            `json:"apiVersion"`
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Spec        any               `json:"spec,omitempty"`
	Body        string            `json:"body,omitempty"`
}

func newParsedEntry(path string, parsed *validate.ParsedResource, body []byte, defaultNamespace string) (*parsedEntry, bool, error) {
	cmp, ok := comparableForParsed(parsed, body, defaultNamespace)
	if !ok {
		return nil, false, nil
	}
	identity := resourceIdentity{
		APIVersion: cmp.APIVersion,
		Kind:       cmp.Kind,
		Namespace:  cmp.Namespace,
		Name:       cmp.Name,
	}
	contentHash, err := hashJSON(cmp)
	if err != nil {
		return nil, false, err
	}
	return &parsedEntry{
		path:        path,
		parsed:      parsed,
		body:        body,
		identity:    identity,
		contentHash: contentHash,
	}, true, nil
}

func comparableForParsed(parsed *validate.ParsedResource, body []byte, defaultNamespace string) (comparableResource, bool) {
	if parsed == nil {
		return comparableResource{}, false
	}
	switch parsed.Kind {
	case "Product":
		r := parsed.Product
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "CategoryTaxonomy":
		r := parsed.CategoryTaxonomy
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "Collection":
		r := parsed.Collection
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "ProductVariant":
		r := parsed.ProductVariant
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	default:
		return comparableResource{}, false
	}
}

func comparableFromMeta(apiVersion, kind string, meta catalog.ObjectMeta, spec any, body []byte, defaultNamespace string) comparableResource {
	namespace := meta.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}
	return comparableResource{
		APIVersion:  apiVersion,
		Kind:        kind,
		Namespace:   namespace,
		Name:        meta.Name,
		Labels:      meta.Labels,
		Annotations: meta.Annotations,
		Spec:        spec,
		Body:        string(body),
	}
}

func hashJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func deriveResourceAdmissionOperations(oldEntries, newEntries []*parsedEntry, changedPaths []string) []resourceAdmissionOperation {
	oldByID := make(map[string]*parsedEntry, len(oldEntries))
	newByID := make(map[string]*parsedEntry, len(newEntries))
	for _, e := range oldEntries {
		oldByID[e.identity.key()] = e
	}
	for _, e := range newEntries {
		newByID[e.identity.key()] = e
	}

	changedPathSet := make(map[string]struct{}, len(changedPaths))
	for _, path := range changedPaths {
		if path != "" {
			changedPathSet[path] = struct{}{}
		}
	}
	pathFilterEnabled := len(changedPathSet) > 0
	pathSelected := func(oldEntry, newEntry *parsedEntry) bool {
		if !pathFilterEnabled {
			return true
		}
		if oldEntry != nil {
			if _, ok := changedPathSet[oldEntry.path]; ok {
				return true
			}
		}
		if newEntry != nil {
			if _, ok := changedPathSet[newEntry.path]; ok {
				return true
			}
		}
		return false
	}

	var ops []resourceAdmissionOperation
	for key, oldEntry := range oldByID {
		newEntry := newByID[key]
		if newEntry == nil {
			if pathSelected(oldEntry, nil) {
				ops = append(ops, resourceAdmissionOperation{
					operation: admission.OperationDelete,
					identity:  oldEntry.identity,
					oldEntry:  oldEntry,
				})
			}
			continue
		}
		pathChanged := oldEntry.path != newEntry.path
		contentChanged := oldEntry.contentHash != newEntry.contentHash
		if (pathChanged || contentChanged) && pathSelected(oldEntry, newEntry) {
			ops = append(ops, resourceAdmissionOperation{
				operation:      admission.OperationUpdate,
				identity:       newEntry.identity,
				oldEntry:       oldEntry,
				newEntry:       newEntry,
				pathChanged:    pathChanged,
				contentChanged: contentChanged,
			})
		}
	}
	for key, newEntry := range newByID {
		if _, ok := oldByID[key]; ok {
			continue
		}
		if pathSelected(nil, newEntry) {
			ops = append(ops, resourceAdmissionOperation{
				operation: admission.OperationCreate,
				identity:  newEntry.identity,
				newEntry:  newEntry,
			})
		}
	}

	sort.SliceStable(ops, func(i, j int) bool {
		pi := operationSortPriority(ops[i].operation)
		pj := operationSortPriority(ops[j].operation)
		if pi != pj {
			return pi < pj
		}
		return fmt.Sprintf("%s/%s/%s", ops[i].identity.Kind, ops[i].identity.Namespace, ops[i].identity.Name) <
			fmt.Sprintf("%s/%s/%s", ops[j].identity.Kind, ops[j].identity.Namespace, ops[j].identity.Name)
	})
	return ops
}

func operationSortPriority(op admission.Operation) int {
	switch op {
	case admission.OperationDelete:
		return 0
	case admission.OperationCreate:
		return 1
	case admission.OperationUpdate:
		return 2
	default:
		return 3
	}
}
