// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/json"
	"math/big"
)

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
