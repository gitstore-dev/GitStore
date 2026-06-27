// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//! Shared tree-walking and tree-diff helpers used by both the validation handler
//! (which collects `ResourceBlob`s) and the admission handler (which collects
//! changed file paths for `AdmitResourcesRequest.changed_paths`).

/// Resolve the root tree OID from a commit OID.  Returns `None` on any error.
pub fn get_tree_id(repo: &gix::Repository, commit_id: gix::ObjectId) -> Option<gix::ObjectId> {
    repo.find_object(commit_id)
        .ok()
        .and_then(|o| o.try_into_commit().ok())
        .and_then(|c| c.tree_id().map(|id| id.detach()).ok())
}

/// Decode a tree object into its entries.  Returns an empty vec on any error.
pub fn decode_tree(repo: &gix::Repository, tree_id: gix::ObjectId) -> Vec<gix::objs::tree::Entry> {
    let Some(obj) = repo.find_object(tree_id).ok() else {
        return vec![];
    };
    let Some(tree) = obj.try_into_tree().ok() else {
        return vec![];
    };
    tree.decode()
        .ok()
        .map(|d| {
            d.entries
                .iter()
                .map(|e| gix::objs::tree::Entry {
                    mode: e.mode,
                    filename: e.filename.to_owned(),
                    oid: e.oid.to_owned(),
                })
                .collect()
        })
        .unwrap_or_default()
}

/// Walk `tree_id` recursively and push every file path under `prefix` into `out`.
pub fn collect_paths_from_tree(
    repo: &gix::Repository,
    tree_id: gix::ObjectId,
    prefix: &str,
    out: &mut Vec<String>,
) {
    for entry in decode_tree(repo, tree_id) {
        let name = entry.filename.to_string();
        let path = make_path(prefix, &name);
        match entry.mode.kind() {
            gix::object::tree::EntryKind::Tree => {
                collect_paths_from_tree(repo, entry.oid, &path, out);
            }
            gix::object::tree::EntryKind::Blob | gix::object::tree::EntryKind::BlobExecutable => {
                out.push(path);
            }
            _ => {}
        }
    }
}

/// Walk two trees and push every path that was added, modified, or deleted into
/// `out`.  Deleted entries are included so that the Go admission handler can
/// derive `OperationDelete` for removed resources.
///
/// The deletion pass uses `(name, kind)` as the identity key so that replacing
/// a blob with a same-named tree (or vice versa) correctly emits the old paths
/// as deletions.
pub fn collect_diff_paths_from_trees(
    repo: &gix::Repository,
    old_tree_id: gix::ObjectId,
    new_tree_id: gix::ObjectId,
    prefix: &str,
    out: &mut Vec<String>,
) {
    let old_entries = decode_tree(repo, old_tree_id);
    let new_entries = decode_tree(repo, new_tree_id);

    let old_map: std::collections::HashMap<String, (gix::ObjectId, gix::object::tree::EntryKind)> =
        old_entries
            .iter()
            .map(|e| (e.filename.to_string(), (e.oid, e.mode.kind())))
            .collect();

    // Added / modified entries from the new tree.
    for entry in &new_entries {
        let name = entry.filename.to_string();
        let path = make_path(prefix, &name);
        match entry.mode.kind() {
            gix::object::tree::EntryKind::Tree => {
                let new_sub: gix::ObjectId = entry.oid;
                let old_sub = old_map
                    .get(&name)
                    .filter(|(_, k)| *k == gix::object::tree::EntryKind::Tree)
                    .map(|(id, _)| *id);
                match old_sub {
                    Some(o) if o == new_sub => {} // unchanged subtree
                    Some(o) => collect_diff_paths_from_trees(repo, o, new_sub, &path, out),
                    None => collect_paths_from_tree(repo, new_sub, &path, out),
                }
            }
            gix::object::tree::EntryKind::Blob | gix::object::tree::EntryKind::BlobExecutable => {
                let new_blob: gix::ObjectId = entry.oid;
                let old_blob = old_map
                    .get(&name)
                    .filter(|(_, k)| {
                        matches!(
                            k,
                            gix::object::tree::EntryKind::Blob
                                | gix::object::tree::EntryKind::BlobExecutable
                        )
                    })
                    .map(|(id, _)| *id);
                if old_blob != Some(new_blob) {
                    out.push(path);
                }
            }
            _ => {}
        }
    }

    // Deleted entries: present in old, absent in new, or replaced by a different kind.
    // Keyed by (name, kind) so a blob replaced by a same-named tree emits the blob path.
    let new_kind_map: std::collections::HashMap<String, gix::object::tree::EntryKind> = new_entries
        .iter()
        .map(|e| (e.filename.to_string(), e.mode.kind()))
        .collect();
    for entry in &old_entries {
        if new_kind_map.get(&entry.filename.to_string()).copied() != Some(entry.mode.kind()) {
            let name = entry.filename.to_string();
            let path = make_path(prefix, &name);
            match entry.mode.kind() {
                gix::object::tree::EntryKind::Tree => {
                    collect_paths_from_tree(repo, entry.oid, &path, out);
                }
                gix::object::tree::EntryKind::Blob
                | gix::object::tree::EntryKind::BlobExecutable => {
                    out.push(path);
                }
                _ => {}
            }
        }
    }
}

#[inline]
pub fn make_path(prefix: &str, name: &str) -> String {
    if prefix.is_empty() {
        name.to_string()
    } else {
        format!("{}/{}", prefix, name)
    }
}
