// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Code generated from shared/schemas/authorization-actions.json; DO NOT EDIT.

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ActionDefinition {
    pub action: &'static str,
    pub kind: &'static str,
    pub scope: &'static str,
    pub resource: &'static str,
    pub verb: &'static str,
    pub reserved: bool,
}

pub const CATALOGRELEASE_CREATE: &str = "catalogRelease.create";
pub const CATEGORYTAXONOMY_CREATE: &str = "categoryTaxonomy.create";
pub const CATEGORYTAXONOMY_DELETE: &str = "categoryTaxonomy.delete";
pub const CATEGORYTAXONOMY_LIST: &str = "categoryTaxonomy.list";
pub const CATEGORYTAXONOMY_PURGE: &str = "categoryTaxonomy.purge";
pub const CATEGORYTAXONOMY_READ: &str = "categoryTaxonomy.read";
pub const CATEGORYTAXONOMY_STATUS_WRITE: &str = "categoryTaxonomy.status.write";
pub const CATEGORYTAXONOMY_TRANSFER: &str = "categoryTaxonomy.transfer";
pub const CATEGORYTAXONOMY_UPDATE: &str = "categoryTaxonomy.update";
pub const CATEGORYTAXONOMY_WATCH: &str = "categoryTaxonomy.watch";
pub const COLLECTION_CREATE: &str = "collection.create";
pub const COLLECTION_DELETE: &str = "collection.delete";
pub const COLLECTION_LIST: &str = "collection.list";
pub const COLLECTION_READ: &str = "collection.read";
pub const COLLECTION_STATUS_WRITE: &str = "collection.status.write";
pub const COLLECTION_TRANSFER: &str = "collection.transfer";
pub const COLLECTION_UPDATE: &str = "collection.update";
pub const COLLECTION_WATCH: &str = "collection.watch";
pub const FILE_CREATE: &str = "file.create";
pub const FILE_DELETE: &str = "file.delete";
pub const FILE_LIST: &str = "file.list";
pub const FILE_READ: &str = "file.read";
pub const FILE_STATUS_WRITE: &str = "file.status.write";
pub const FILE_TRANSFER: &str = "file.transfer";
pub const FILE_UPDATE: &str = "file.update";
pub const FILE_WATCH: &str = "file.watch";
pub const IAM_MANAGE: &str = "iam.manage";
pub const NAMESPACE_CREATE: &str = "namespace.create";
pub const NAMESPACE_DELETE: &str = "namespace.delete";
pub const NAMESPACE_LIST: &str = "namespace.list";
pub const NAMESPACE_PURGE: &str = "namespace.purge";
pub const NAMESPACE_READ: &str = "namespace.read";
pub const NAMESPACE_STATUS_WRITE: &str = "namespace.status.write";
pub const NAMESPACE_SYSTEM_REPOSITORY_PROVISION: &str = "namespace.system-repository.provision";
pub const NAMESPACE_TRANSFER: &str = "namespace.transfer";
pub const NAMESPACE_UPDATE: &str = "namespace.update";
pub const NAMESPACE_WATCH: &str = "namespace.watch";
pub const PRODUCT_CREATE: &str = "product.create";
pub const PRODUCT_DELETE: &str = "product.delete";
pub const PRODUCT_LIST: &str = "product.list";
pub const PRODUCT_MANAGEMENT_LIST: &str = "product.management.list";
pub const PRODUCT_MANAGEMENT_READ: &str = "product.management.read";
pub const PRODUCT_PURGE: &str = "product.purge";
pub const PRODUCT_READ: &str = "product.read";
pub const PRODUCT_STATUS_WRITE: &str = "product.status.write";
pub const PRODUCT_TRANSFER: &str = "product.transfer";
pub const PRODUCT_UPDATE: &str = "product.update";
pub const PRODUCT_WATCH: &str = "product.watch";
pub const PRODUCTVARIANT_CREATE: &str = "productVariant.create";
pub const PRODUCTVARIANT_DELETE: &str = "productVariant.delete";
pub const PRODUCTVARIANT_LIST: &str = "productVariant.list";
pub const PRODUCTVARIANT_MANAGEMENT_LIST: &str = "productVariant.management.list";
pub const PRODUCTVARIANT_MANAGEMENT_READ: &str = "productVariant.management.read";
pub const PRODUCTVARIANT_READ: &str = "productVariant.read";
pub const PRODUCTVARIANT_STATUS_WRITE: &str = "productVariant.status.write";
pub const PRODUCTVARIANT_TRANSFER: &str = "productVariant.transfer";
pub const PRODUCTVARIANT_UPDATE: &str = "productVariant.update";
pub const PRODUCTVARIANT_WATCH: &str = "productVariant.watch";
pub const PUBLICATION_CREATE: &str = "publication.create";
pub const PUBLICATION_READ: &str = "publication.read";
pub const REPOSITORY_CONTENTS_READ: &str = "repository.contents.read";
pub const REPOSITORY_CONTENTS_WRITE: &str = "repository.contents.write";
pub const REPOSITORY_CREATE: &str = "repository.create";
pub const REPOSITORY_DELETE: &str = "repository.delete";
pub const REPOSITORY_LIST: &str = "repository.list";
pub const REPOSITORY_PURGE: &str = "repository.purge";
pub const REPOSITORY_READ: &str = "repository.read";
pub const REPOSITORY_STATUS_WRITE: &str = "repository.status.write";
pub const REPOSITORY_TRANSFER: &str = "repository.transfer";
pub const REPOSITORY_UPDATE: &str = "repository.update";
pub const REPOSITORY_WATCH: &str = "repository.watch";
pub const SERVICEACCOUNT_CREATE: &str = "serviceAccount.create";
pub const SERVICEACCOUNT_DELETE: &str = "serviceAccount.delete";
pub const SERVICEACCOUNT_KEY_ROTATE: &str = "serviceAccount.key.rotate";
pub const SERVICEACCOUNT_LIST: &str = "serviceAccount.list";
pub const SERVICEACCOUNT_READ: &str = "serviceAccount.read";
pub const SERVICEACCOUNT_TOKEN_ISSUE: &str = "serviceAccount.token.issue";
pub const SERVICEACCOUNT_UPDATE: &str = "serviceAccount.update";
pub const WORKFLOW_BUNDLE_INSTALL: &str = "workflow.bundle.install";
pub const WORKFLOW_DEFINITION_MANAGE: &str = "workflow.definition.manage";
pub const WORKFLOW_EXECUTION_TRANSITION: &str = "workflow.execution.transition";
pub const WORKFLOW_RELEASE_APPROVE: &str = "workflow.release.approve";

#[rustfmt::skip]
pub const AUTHORIZATION_ACTIONS: &[ActionDefinition] = &[
    ActionDefinition { action: "catalogRelease.create", kind: "catalogRelease", scope: "namespace", resource: "catalogRelease", verb: "create", reserved: true },
    ActionDefinition { action: "categoryTaxonomy.create", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "create", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.delete", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "delete", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.list", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "list", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.purge", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "purge", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.read", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "read", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.status.write", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy/status", verb: "write", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.transfer", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "transfer", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.update", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "update", reserved: false },
    ActionDefinition { action: "categoryTaxonomy.watch", kind: "categoryTaxonomy", scope: "namespace", resource: "categoryTaxonomy", verb: "watch", reserved: false },
    ActionDefinition { action: "collection.create", kind: "collection", scope: "namespace", resource: "collection", verb: "create", reserved: false },
    ActionDefinition { action: "collection.delete", kind: "collection", scope: "namespace", resource: "collection", verb: "delete", reserved: false },
    ActionDefinition { action: "collection.list", kind: "collection", scope: "namespace", resource: "collection", verb: "list", reserved: false },
    ActionDefinition { action: "collection.read", kind: "collection", scope: "namespace", resource: "collection", verb: "read", reserved: false },
    ActionDefinition { action: "collection.status.write", kind: "collection", scope: "namespace", resource: "collection/status", verb: "write", reserved: false },
    ActionDefinition { action: "collection.transfer", kind: "collection", scope: "namespace", resource: "collection", verb: "transfer", reserved: false },
    ActionDefinition { action: "collection.update", kind: "collection", scope: "namespace", resource: "collection", verb: "update", reserved: false },
    ActionDefinition { action: "collection.watch", kind: "collection", scope: "namespace", resource: "collection", verb: "watch", reserved: false },
    ActionDefinition { action: "file.create", kind: "file", scope: "namespace", resource: "file", verb: "create", reserved: false },
    ActionDefinition { action: "file.delete", kind: "file", scope: "namespace", resource: "file", verb: "delete", reserved: false },
    ActionDefinition { action: "file.list", kind: "file", scope: "namespace", resource: "file", verb: "list", reserved: false },
    ActionDefinition { action: "file.read", kind: "file", scope: "namespace", resource: "file", verb: "read", reserved: false },
    ActionDefinition { action: "file.status.write", kind: "file", scope: "namespace", resource: "file/status", verb: "write", reserved: false },
    ActionDefinition { action: "file.transfer", kind: "file", scope: "namespace", resource: "file", verb: "transfer", reserved: false },
    ActionDefinition { action: "file.update", kind: "file", scope: "namespace", resource: "file", verb: "update", reserved: false },
    ActionDefinition { action: "file.watch", kind: "file", scope: "namespace", resource: "file", verb: "watch", reserved: false },
    ActionDefinition { action: "iam.manage", kind: "iam", scope: "cluster", resource: "iam", verb: "manage", reserved: true },
    ActionDefinition { action: "namespace.create", kind: "namespace", scope: "cluster", resource: "namespace", verb: "create", reserved: false },
    ActionDefinition { action: "namespace.delete", kind: "namespace", scope: "cluster", resource: "namespace", verb: "delete", reserved: false },
    ActionDefinition { action: "namespace.list", kind: "namespace", scope: "cluster", resource: "namespace", verb: "list", reserved: false },
    ActionDefinition { action: "namespace.purge", kind: "namespace", scope: "cluster", resource: "namespace", verb: "purge", reserved: false },
    ActionDefinition { action: "namespace.read", kind: "namespace", scope: "cluster", resource: "namespace", verb: "read", reserved: false },
    ActionDefinition { action: "namespace.status.write", kind: "namespace", scope: "cluster", resource: "namespace/status", verb: "write", reserved: false },
    ActionDefinition { action: "namespace.system-repository.provision", kind: "namespace", scope: "cluster", resource: "namespace/system-repository", verb: "provision", reserved: false },
    ActionDefinition { action: "namespace.transfer", kind: "namespace", scope: "cluster", resource: "namespace", verb: "transfer", reserved: false },
    ActionDefinition { action: "namespace.update", kind: "namespace", scope: "cluster", resource: "namespace", verb: "update", reserved: false },
    ActionDefinition { action: "namespace.watch", kind: "namespace", scope: "cluster", resource: "namespace", verb: "watch", reserved: false },
    ActionDefinition { action: "product.create", kind: "product", scope: "namespace", resource: "product", verb: "create", reserved: false },
    ActionDefinition { action: "product.delete", kind: "product", scope: "namespace", resource: "product", verb: "delete", reserved: false },
    ActionDefinition { action: "product.list", kind: "product", scope: "namespace", resource: "product", verb: "list", reserved: false },
    ActionDefinition { action: "product.management.list", kind: "product", scope: "namespace", resource: "product/management", verb: "list", reserved: false },
    ActionDefinition { action: "product.management.read", kind: "product", scope: "namespace", resource: "product/management", verb: "read", reserved: false },
    ActionDefinition { action: "product.purge", kind: "product", scope: "namespace", resource: "product", verb: "purge", reserved: false },
    ActionDefinition { action: "product.read", kind: "product", scope: "namespace", resource: "product", verb: "read", reserved: false },
    ActionDefinition { action: "product.status.write", kind: "product", scope: "namespace", resource: "product/status", verb: "write", reserved: false },
    ActionDefinition { action: "product.transfer", kind: "product", scope: "namespace", resource: "product", verb: "transfer", reserved: false },
    ActionDefinition { action: "product.update", kind: "product", scope: "namespace", resource: "product", verb: "update", reserved: false },
    ActionDefinition { action: "product.watch", kind: "product", scope: "namespace", resource: "product", verb: "watch", reserved: false },
    ActionDefinition { action: "productVariant.create", kind: "productVariant", scope: "namespace", resource: "productVariant", verb: "create", reserved: false },
    ActionDefinition { action: "productVariant.delete", kind: "productVariant", scope: "namespace", resource: "productVariant", verb: "delete", reserved: false },
    ActionDefinition { action: "productVariant.list", kind: "productVariant", scope: "namespace", resource: "productVariant", verb: "list", reserved: false },
    ActionDefinition { action: "productVariant.management.list", kind: "productVariant", scope: "namespace", resource: "productVariant/management", verb: "list", reserved: false },
    ActionDefinition { action: "productVariant.management.read", kind: "productVariant", scope: "namespace", resource: "productVariant/management", verb: "read", reserved: false },
    ActionDefinition { action: "productVariant.read", kind: "productVariant", scope: "namespace", resource: "productVariant", verb: "read", reserved: false },
    ActionDefinition { action: "productVariant.status.write", kind: "productVariant", scope: "namespace", resource: "productVariant/status", verb: "write", reserved: false },
    ActionDefinition { action: "productVariant.transfer", kind: "productVariant", scope: "namespace", resource: "productVariant", verb: "transfer", reserved: false },
    ActionDefinition { action: "productVariant.update", kind: "productVariant", scope: "namespace", resource: "productVariant", verb: "update", reserved: false },
    ActionDefinition { action: "productVariant.watch", kind: "productVariant", scope: "namespace", resource: "productVariant", verb: "watch", reserved: false },
    ActionDefinition { action: "publication.create", kind: "publication", scope: "namespace", resource: "publication", verb: "create", reserved: true },
    ActionDefinition { action: "publication.read", kind: "publication", scope: "namespace", resource: "publication", verb: "read", reserved: true },
    ActionDefinition { action: "repository.contents.read", kind: "repository", scope: "namespace", resource: "repository/contents", verb: "read", reserved: false },
    ActionDefinition { action: "repository.contents.write", kind: "repository", scope: "namespace", resource: "repository/contents", verb: "write", reserved: false },
    ActionDefinition { action: "repository.create", kind: "repository", scope: "namespace", resource: "repository", verb: "create", reserved: false },
    ActionDefinition { action: "repository.delete", kind: "repository", scope: "namespace", resource: "repository", verb: "delete", reserved: false },
    ActionDefinition { action: "repository.list", kind: "repository", scope: "namespace", resource: "repository", verb: "list", reserved: false },
    ActionDefinition { action: "repository.purge", kind: "repository", scope: "namespace", resource: "repository", verb: "purge", reserved: false },
    ActionDefinition { action: "repository.read", kind: "repository", scope: "namespace", resource: "repository", verb: "read", reserved: false },
    ActionDefinition { action: "repository.status.write", kind: "repository", scope: "namespace", resource: "repository/status", verb: "write", reserved: false },
    ActionDefinition { action: "repository.transfer", kind: "repository", scope: "namespace", resource: "repository", verb: "transfer", reserved: false },
    ActionDefinition { action: "repository.update", kind: "repository", scope: "namespace", resource: "repository", verb: "update", reserved: false },
    ActionDefinition { action: "repository.watch", kind: "repository", scope: "namespace", resource: "repository", verb: "watch", reserved: false },
    ActionDefinition { action: "serviceAccount.create", kind: "serviceAccount", scope: "cluster", resource: "serviceAccount", verb: "create", reserved: false },
    ActionDefinition { action: "serviceAccount.delete", kind: "serviceAccount", scope: "cluster", resource: "serviceAccount", verb: "delete", reserved: false },
    ActionDefinition { action: "serviceAccount.key.rotate", kind: "serviceAccount", scope: "cluster", resource: "serviceAccount/key", verb: "rotate", reserved: false },
    ActionDefinition { action: "serviceAccount.list", kind: "serviceAccount", scope: "cluster", resource: "serviceAccount", verb: "list", reserved: false },
    ActionDefinition { action: "serviceAccount.read", kind: "serviceAccount", scope: "cluster", resource: "serviceAccount", verb: "read", reserved: false },
    ActionDefinition { action: "serviceAccount.token.issue", kind: "serviceAccount", scope: "cluster", resource: "serviceAccount/token", verb: "issue", reserved: false },
    ActionDefinition { action: "serviceAccount.update", kind: "serviceAccount", scope: "cluster", resource: "serviceAccount", verb: "update", reserved: false },
    ActionDefinition { action: "workflow.bundle.install", kind: "workflow", scope: "namespace", resource: "workflow/bundle", verb: "install", reserved: true },
    ActionDefinition { action: "workflow.definition.manage", kind: "workflow", scope: "namespace", resource: "workflow/definition", verb: "manage", reserved: true },
    ActionDefinition { action: "workflow.execution.transition", kind: "workflow", scope: "namespace", resource: "workflow/execution", verb: "transition", reserved: true },
    ActionDefinition { action: "workflow.release.approve", kind: "workflow", scope: "namespace", resource: "workflow/release", verb: "approve", reserved: true },
];

pub fn lookup_action(action: &str) -> Option<&'static ActionDefinition> {
    AUTHORIZATION_ACTIONS
        .binary_search_by_key(&action, |entry| entry.action)
        .ok()
        .map(|index| &AUTHORIZATION_ACTIONS[index])
}
