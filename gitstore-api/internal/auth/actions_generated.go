// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Code generated from shared/schemas/authorization-actions.json; DO NOT EDIT.
package auth

// ActionDefinition is a finite registered authorization capability.
type ActionDefinition struct {
	Action, Kind, Scope, Resource, Verb string
	Reserved                            bool
}

const (
	ActionCatalogReleaseCreate               = "catalogRelease.create"
	ActionCategoryTaxonomyCreate             = "categoryTaxonomy.create"
	ActionCategoryTaxonomyDelete             = "categoryTaxonomy.delete"
	ActionCategoryTaxonomyList               = "categoryTaxonomy.list"
	ActionCategoryTaxonomyPurge              = "categoryTaxonomy.purge"
	ActionCategoryTaxonomyRead               = "categoryTaxonomy.read"
	ActionCategoryTaxonomyStatusWrite        = "categoryTaxonomy.status.write"
	ActionCategoryTaxonomyTransfer           = "categoryTaxonomy.transfer"
	ActionCategoryTaxonomyUpdate             = "categoryTaxonomy.update"
	ActionCategoryTaxonomyWatch              = "categoryTaxonomy.watch"
	ActionCollectionCreate                   = "collection.create"
	ActionCollectionDelete                   = "collection.delete"
	ActionCollectionList                     = "collection.list"
	ActionCollectionRead                     = "collection.read"
	ActionCollectionStatusWrite              = "collection.status.write"
	ActionCollectionTransfer                 = "collection.transfer"
	ActionCollectionUpdate                   = "collection.update"
	ActionCollectionWatch                    = "collection.watch"
	ActionFileCreate                         = "file.create"
	ActionFileDelete                         = "file.delete"
	ActionFileList                           = "file.list"
	ActionFileRead                           = "file.read"
	ActionFileStatusWrite                    = "file.status.write"
	ActionFileTransfer                       = "file.transfer"
	ActionFileUpdate                         = "file.update"
	ActionFileWatch                          = "file.watch"
	ActionIamManage                          = "iam.manage"
	ActionNamespaceCreate                    = "namespace.create"
	ActionNamespaceDelete                    = "namespace.delete"
	ActionNamespaceList                      = "namespace.list"
	ActionNamespacePurge                     = "namespace.purge"
	ActionNamespaceRead                      = "namespace.read"
	ActionNamespaceStatusWrite               = "namespace.status.write"
	ActionNamespaceSystemRepositoryProvision = "namespace.system-repository.provision"
	ActionNamespaceTransfer                  = "namespace.transfer"
	ActionNamespaceUpdate                    = "namespace.update"
	ActionNamespaceWatch                     = "namespace.watch"
	ActionProductCreate                      = "product.create"
	ActionProductDelete                      = "product.delete"
	ActionProductList                        = "product.list"
	ActionProductManagementList              = "product.management.list"
	ActionProductManagementRead              = "product.management.read"
	ActionProductPurge                       = "product.purge"
	ActionProductRead                        = "product.read"
	ActionProductStatusWrite                 = "product.status.write"
	ActionProductTransfer                    = "product.transfer"
	ActionProductUpdate                      = "product.update"
	ActionProductWatch                       = "product.watch"
	ActionProductVariantCreate               = "productVariant.create"
	ActionProductVariantDelete               = "productVariant.delete"
	ActionProductVariantList                 = "productVariant.list"
	ActionProductVariantManagementList       = "productVariant.management.list"
	ActionProductVariantManagementRead       = "productVariant.management.read"
	ActionProductVariantRead                 = "productVariant.read"
	ActionProductVariantStatusWrite          = "productVariant.status.write"
	ActionProductVariantTransfer             = "productVariant.transfer"
	ActionProductVariantUpdate               = "productVariant.update"
	ActionProductVariantWatch                = "productVariant.watch"
	ActionPublicationCreate                  = "publication.create"
	ActionPublicationRead                    = "publication.read"
	ActionRepositoryContentsRead             = "repository.contents.read"
	ActionRepositoryContentsWrite            = "repository.contents.write"
	ActionRepositoryCreate                   = "repository.create"
	ActionRepositoryDelete                   = "repository.delete"
	ActionRepositoryList                     = "repository.list"
	ActionRepositoryPurge                    = "repository.purge"
	ActionRepositoryRead                     = "repository.read"
	ActionRepositoryStatusWrite              = "repository.status.write"
	ActionRepositoryTransfer                 = "repository.transfer"
	ActionRepositoryUpdate                   = "repository.update"
	ActionRepositoryWatch                    = "repository.watch"
	ActionServiceAccountCreate               = "serviceAccount.create"
	ActionServiceAccountDelete               = "serviceAccount.delete"
	ActionServiceAccountKeyRotate            = "serviceAccount.key.rotate"
	ActionServiceAccountList                 = "serviceAccount.list"
	ActionServiceAccountRead                 = "serviceAccount.read"
	ActionServiceAccountTokenIssue           = "serviceAccount.token.issue"
	ActionServiceAccountUpdate               = "serviceAccount.update"
	ActionWorkflowBundleInstall              = "workflow.bundle.install"
	ActionWorkflowDefinitionManage           = "workflow.definition.manage"
	ActionWorkflowExecutionTransition        = "workflow.execution.transition"
	ActionWorkflowReleaseApprove             = "workflow.release.approve"
)

var authorizationActions = [...]ActionDefinition{
	{"catalogRelease.create", "catalogRelease", "namespace", "catalogRelease", "create", true},
	{"categoryTaxonomy.create", "categoryTaxonomy", "namespace", "categoryTaxonomy", "create", false},
	{"categoryTaxonomy.delete", "categoryTaxonomy", "namespace", "categoryTaxonomy", "delete", false},
	{"categoryTaxonomy.list", "categoryTaxonomy", "namespace", "categoryTaxonomy", "list", false},
	{"categoryTaxonomy.purge", "categoryTaxonomy", "namespace", "categoryTaxonomy", "purge", false},
	{"categoryTaxonomy.read", "categoryTaxonomy", "namespace", "categoryTaxonomy", "read", false},
	{"categoryTaxonomy.status.write", "categoryTaxonomy", "namespace", "categoryTaxonomy/status", "write", false},
	{"categoryTaxonomy.transfer", "categoryTaxonomy", "namespace", "categoryTaxonomy", "transfer", false},
	{"categoryTaxonomy.update", "categoryTaxonomy", "namespace", "categoryTaxonomy", "update", false},
	{"categoryTaxonomy.watch", "categoryTaxonomy", "namespace", "categoryTaxonomy", "watch", false},
	{"collection.create", "collection", "namespace", "collection", "create", false},
	{"collection.delete", "collection", "namespace", "collection", "delete", false},
	{"collection.list", "collection", "namespace", "collection", "list", false},
	{"collection.read", "collection", "namespace", "collection", "read", false},
	{"collection.status.write", "collection", "namespace", "collection/status", "write", false},
	{"collection.transfer", "collection", "namespace", "collection", "transfer", false},
	{"collection.update", "collection", "namespace", "collection", "update", false},
	{"collection.watch", "collection", "namespace", "collection", "watch", false},
	{"file.create", "file", "namespace", "file", "create", false},
	{"file.delete", "file", "namespace", "file", "delete", false},
	{"file.list", "file", "namespace", "file", "list", false},
	{"file.read", "file", "namespace", "file", "read", false},
	{"file.status.write", "file", "namespace", "file/status", "write", false},
	{"file.transfer", "file", "namespace", "file", "transfer", false},
	{"file.update", "file", "namespace", "file", "update", false},
	{"file.watch", "file", "namespace", "file", "watch", false},
	{"iam.manage", "iam", "cluster", "iam", "manage", true},
	{"namespace.create", "namespace", "cluster", "namespace", "create", false},
	{"namespace.delete", "namespace", "cluster", "namespace", "delete", false},
	{"namespace.list", "namespace", "cluster", "namespace", "list", false},
	{"namespace.purge", "namespace", "cluster", "namespace", "purge", false},
	{"namespace.read", "namespace", "cluster", "namespace", "read", false},
	{"namespace.status.write", "namespace", "cluster", "namespace/status", "write", false},
	{"namespace.system-repository.provision", "namespace", "cluster", "namespace/system-repository", "provision", false},
	{"namespace.transfer", "namespace", "cluster", "namespace", "transfer", false},
	{"namespace.update", "namespace", "cluster", "namespace", "update", false},
	{"namespace.watch", "namespace", "cluster", "namespace", "watch", false},
	{"product.create", "product", "namespace", "product", "create", false},
	{"product.delete", "product", "namespace", "product", "delete", false},
	{"product.list", "product", "namespace", "product", "list", false},
	{"product.management.list", "product", "namespace", "product/management", "list", false},
	{"product.management.read", "product", "namespace", "product/management", "read", false},
	{"product.purge", "product", "namespace", "product", "purge", false},
	{"product.read", "product", "namespace", "product", "read", false},
	{"product.status.write", "product", "namespace", "product/status", "write", false},
	{"product.transfer", "product", "namespace", "product", "transfer", false},
	{"product.update", "product", "namespace", "product", "update", false},
	{"product.watch", "product", "namespace", "product", "watch", false},
	{"productVariant.create", "productVariant", "namespace", "productVariant", "create", false},
	{"productVariant.delete", "productVariant", "namespace", "productVariant", "delete", false},
	{"productVariant.list", "productVariant", "namespace", "productVariant", "list", false},
	{"productVariant.management.list", "productVariant", "namespace", "productVariant/management", "list", false},
	{"productVariant.management.read", "productVariant", "namespace", "productVariant/management", "read", false},
	{"productVariant.read", "productVariant", "namespace", "productVariant", "read", false},
	{"productVariant.status.write", "productVariant", "namespace", "productVariant/status", "write", false},
	{"productVariant.transfer", "productVariant", "namespace", "productVariant", "transfer", false},
	{"productVariant.update", "productVariant", "namespace", "productVariant", "update", false},
	{"productVariant.watch", "productVariant", "namespace", "productVariant", "watch", false},
	{"publication.create", "publication", "namespace", "publication", "create", true},
	{"publication.read", "publication", "namespace", "publication", "read", true},
	{"repository.contents.read", "repository", "namespace", "repository/contents", "read", false},
	{"repository.contents.write", "repository", "namespace", "repository/contents", "write", false},
	{"repository.create", "repository", "namespace", "repository", "create", false},
	{"repository.delete", "repository", "namespace", "repository", "delete", false},
	{"repository.list", "repository", "namespace", "repository", "list", false},
	{"repository.purge", "repository", "namespace", "repository", "purge", false},
	{"repository.read", "repository", "namespace", "repository", "read", false},
	{"repository.status.write", "repository", "namespace", "repository/status", "write", false},
	{"repository.transfer", "repository", "namespace", "repository", "transfer", false},
	{"repository.update", "repository", "namespace", "repository", "update", false},
	{"repository.watch", "repository", "namespace", "repository", "watch", false},
	{"serviceAccount.create", "serviceAccount", "cluster", "serviceAccount", "create", false},
	{"serviceAccount.delete", "serviceAccount", "cluster", "serviceAccount", "delete", false},
	{"serviceAccount.key.rotate", "serviceAccount", "cluster", "serviceAccount/key", "rotate", false},
	{"serviceAccount.list", "serviceAccount", "cluster", "serviceAccount", "list", false},
	{"serviceAccount.read", "serviceAccount", "cluster", "serviceAccount", "read", false},
	{"serviceAccount.token.issue", "serviceAccount", "cluster", "serviceAccount/token", "issue", false},
	{"serviceAccount.update", "serviceAccount", "cluster", "serviceAccount", "update", false},
	{"workflow.bundle.install", "workflow", "namespace", "workflow/bundle", "install", true},
	{"workflow.definition.manage", "workflow", "namespace", "workflow/definition", "manage", true},
	{"workflow.execution.transition", "workflow", "namespace", "workflow/execution", "transition", true},
	{"workflow.release.approve", "workflow", "namespace", "workflow/release", "approve", true},
}

var authorizationActionIndex = func() map[string]ActionDefinition {
	m := make(map[string]ActionDefinition, len(authorizationActions))
	for _, a := range authorizationActions {
		m[a.Action] = a
	}
	return m
}()

// LookupAction returns an immutable copy; unknown and legacy spellings never match.
func LookupAction(action string) (ActionDefinition, bool) {
	a, ok := authorizationActionIndex[action]
	return a, ok
}

// AuthorizationActions returns a defensive copy in canonical action order.
func AuthorizationActions() []ActionDefinition {
	return append([]ActionDefinition(nil), authorizationActions[:]...)
}
