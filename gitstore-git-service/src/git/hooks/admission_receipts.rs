// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use prost::Message;
use sha2::{Digest, Sha256};

use super::admission_handler::catalog_proto::CallbackAuthorization;

const MAGIC: &[u8] = b"GSTRCV02";
const MAX_RECEIPT_BYTES: usize = 1024 * 1024;
const MAX_RECEIPTS: usize = 128;
const MAX_PAGE_SIZE: usize = 32;
const MAX_REFS: usize = 256;

#[derive(Clone, PartialEq, Message)]
struct ReceiptSnapshot {
    #[prost(message, optional, tag = "1")]
    authorization: Option<CallbackAuthorization>,
    #[prost(string, repeated, tag = "2")]
    catalog_refs: Vec<String>,
}

#[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd)]
pub struct ReceiptId(String);

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ReceiptPhase {
    Prepared,
    Committed,
    Acknowledged,
}

impl ReceiptPhase {
    fn suffix(self) -> &'static str {
        match self {
            Self::Prepared => "prepared",
            Self::Committed => "committed",
            Self::Acknowledged => "acknowledged",
        }
    }
}

pub struct ReceiptPage {
    pub entries: Vec<(ReceiptId, ReceiptPhase)>,
    pub next: Option<ReceiptId>,
}

struct ReceiptMetadata {
    repository_id: String,
    phase: ReceiptPhase,
}

type ReceiptIndex = BTreeMap<ReceiptId, ReceiptMetadata>;

// Callers own repository exclusion and production delivery scheduling;
// retained objects and recovery do not establish that orchestration.
pub struct AdmissionReceiptStore {
    directory: PathBuf,
    gate: Mutex<()>,
    metadata: Mutex<ReceiptIndex>,
    _owner: File,
    #[cfg(test)]
    publication_entered: Mutex<Option<std::sync::Arc<tokio::sync::Notify>>>,
}

impl Drop for AdmissionReceiptStore {
    fn drop(&mut self) {
        // A transient fork may retain a duplicate until exec closes it.
        let _ = self._owner.unlock();
    }
}

impl AdmissionReceiptStore {
    #[cfg(test)]
    pub(crate) fn pause_publication_for_test(
        &self,
    ) -> (
        std::sync::MutexGuard<'_, ()>,
        std::sync::Arc<tokio::sync::Notify>,
    ) {
        let guard = self.gate.lock().unwrap();
        let entered = std::sync::Arc::new(tokio::sync::Notify::new());
        *self.publication_entered.lock().unwrap() = Some(entered.clone());
        (guard, entered)
    }

    pub fn open(data_root: &Path) -> io::Result<Self> {
        let directory = data_root.join(".admission-receipts-v1");
        let mut builder = fs::DirBuilder::new();
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            builder.mode(0o700);
        }
        match builder.create(&directory) {
            Ok(()) => File::open(data_root)?.sync_all()?,
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => {
                if !fs::symlink_metadata(&directory)?.file_type().is_dir() {
                    return Err(invalid_data("receipt directory is not a directory"));
                }
            }
            Err(error) => return Err(error),
        }
        let owner_path = directory.join(".owner");
        if let Ok(metadata) = fs::symlink_metadata(&owner_path) {
            if !metadata.file_type().is_file() || metadata.len() != 0 {
                return Err(invalid_data("invalid receipt owner file"));
            }
        }
        let owner = OpenOptions::new()
            .create(true)
            .truncate(false)
            .read(true)
            .write(true)
            .open(owner_path)?;
        owner.try_lock().map_err(io::Error::other)?;
        let store = Self {
            directory,
            gate: Mutex::new(()),
            metadata: Mutex::new(BTreeMap::new()),
            _owner: owner,
            #[cfg(test)]
            publication_entered: Mutex::new(None),
        };
        // A temporary file has never been published as a prepared receipt.
        // Reclaim it only while holding exclusive replacement ownership.
        let mut orphans = Vec::new();
        for entry in fs::read_dir(&store.directory)?.take(MAX_RECEIPTS + 2) {
            let entry = entry?;
            if entry.file_name().to_string_lossy().starts_with(".pending-") {
                if !entry.file_type()?.is_file() {
                    return Err(invalid_data("invalid temporary receipt"));
                }
                orphans.push(entry.path());
            }
        }
        // Validate the bounded directory before removing even orphaned writes.
        store.scan(true)?;
        for orphan in orphans {
            fs::remove_file(orphan)?;
        }
        store.sync_directory()?;
        for (id, phase) in store.scan(false)? {
            let authorization = store.read(&id, phase)?;
            store.metadata()?.insert(
                id,
                ReceiptMetadata {
                    repository_id: authorization.repository_id,
                    phase,
                },
            );
        }
        Ok(store)
    }

    /// The caller must retain the repository's exclusive mutation guard through
    /// public ref commit or prepared-receive recovery, not just this call.
    #[cfg(test)]
    pub fn prepare_fenced(
        &self,
        repository: &gix::Repository,
        authorization: &CallbackAuthorization,
    ) -> anyhow::Result<ReceiptId> {
        let catalog_refs = authorization
            .refs
            .iter()
            .filter(|update| update.ref_name.starts_with("refs/heads/"))
            .map(|update| update.ref_name.clone())
            .collect::<Vec<_>>();
        self.prepare_fenced_with_catalog_refs(repository, authorization, &catalog_refs)
    }

    pub fn prepare_fenced_with_catalog_refs(
        &self,
        repository: &gix::Repository,
        authorization: &CallbackAuthorization,
        catalog_refs: &[String],
    ) -> anyhow::Result<ReceiptId> {
        if authorization.encoded_len() > MAX_RECEIPT_BYTES - MAGIC.len() {
            return Err(invalid_input("receipt payload capacity exceeded").into());
        }
        validate(authorization)?;
        validate_catalog_refs(authorization, catalog_refs)?;
        self.verify_repository(repository, authorization)?;
        #[cfg(test)]
        if let Some(entered) = self.publication_entered.lock().unwrap().take() {
            entered.notify_one();
        }
        let _guard = self.lock()?;
        anyhow::ensure!(
            self.pending_for_repository(&authorization.repository_id)?
                .is_empty(),
            "repository has an unresolved receive"
        );
        let require_absent = |path: PathBuf| -> io::Result<()> {
            match fs::symlink_metadata(path) {
                Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
                Err(error) => Err(error),
                Ok(_) => Err(invalid_input("preexisting ref lock requires recovery")),
            }
        };
        require_absent(repository.path().join("packed-refs.lock"))?;
        for update in &authorization.refs {
            require_absent(repository.path().join(format!("{}.lock", update.ref_name)))?;
            let old = gix::ObjectId::from_hex(update.old_commit_sha.as_bytes())?;
            let current = repository.try_find_reference(&update.ref_name)?;
            let matches = match current {
                None => old.is_null(),
                Some(reference) => {
                    !old.is_null()
                        && reference.target() == gix::refs::TargetRef::Object(old.as_ref())
                }
            };
            anyhow::ensure!(matches, "receive ref preimage changed");
        }
        Ok(self.prepare_locked(authorization, catalog_refs)?)
    }

    /// Requires uninterrupted repository exclusion since fenced preparation,
    /// or replacement ownership before any new repository mutation is served.
    pub fn recover_prepared(
        &self,
        repository: &gix::Repository,
        id: &ReceiptId,
    ) -> anyhow::Result<()> {
        use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};

        let _guard = self.lock()?;
        let phase = self
            .phase(id)?
            .ok_or_else(|| invalid_input("receipt not found"))?;
        let authorization = self.read(id, phase)?;
        self.verify_repository(repository, &authorization)?;
        if phase == ReceiptPhase::Committed {
            self.sync_directory()?;
            self.metadata()?
                .get_mut(id)
                .ok_or_else(|| invalid_data("receipt metadata missing"))?
                .phase = ReceiptPhase::Committed;
            return Ok(());
        }
        anyhow::ensure!(
            phase == ReceiptPhase::Prepared,
            "receive is already acknowledged"
        );
        let pins = receipt_pins(id, &authorization)?;
        let mut edits = Vec::new();
        let mut locks = vec![repository.path().join("packed-refs.lock")];
        for (name, oid) in &pins {
            if let Some(reference) = repository.try_find_reference(name.as_str())? {
                anyhow::ensure!(
                    reference.target() == gix::refs::TargetRef::Object(oid.as_ref()),
                    "retained object pin differs from receipt"
                );
            }
            locks.push(repository.path().join(format!("{name}.lock")));
        }
        for update in &authorization.refs {
            let old = gix::ObjectId::from_hex(update.old_commit_sha.as_bytes())?;
            let new = gix::ObjectId::from_hex(update.new_commit_sha.as_bytes())?;
            for oid in [old, new] {
                if !oid.is_null() {
                    repository.find_object(oid)?;
                }
            }
            let current = match repository.try_find_reference(&update.ref_name)? {
                None => gix::ObjectId::null(gix::hash::Kind::Sha1),
                Some(reference) => reference
                    .target()
                    .try_id()
                    .ok_or_else(|| invalid_data("receive ref became symbolic"))?
                    .to_owned(),
            };
            anyhow::ensure!(
                current == old || current == new,
                "receive ref outcome diverged"
            );
            locks.push(repository.path().join(format!("{}.lock", update.ref_name)));
            if current == new {
                continue;
            }
            let expected = if old.is_null() {
                PreviousValue::MustNotExist
            } else {
                PreviousValue::MustExistAndMatch(gix::refs::Target::Object(old))
            };
            edits.push(RefEdit {
                name: update.ref_name.as_str().try_into()?,
                deref: false,
                change: if new.is_null() {
                    Change::Delete {
                        expected,
                        log: RefLog::AndReference,
                    }
                } else {
                    Change::Update {
                        expected,
                        new: gix::refs::Target::Object(new),
                        log: LogChange::default(),
                    }
                },
            });
        }
        // Fenced preparation required these locks to be absent. Under the
        // caller's exclusion, only this interrupted receive could create them.
        for path in &locks {
            match fs::symlink_metadata(path) {
                Ok(metadata) if metadata.file_type().is_file() => {}
                Ok(_) => return Err(invalid_data("invalid interrupted ref lock").into()),
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(error) => return Err(error.into()),
            }
        }
        for path in locks {
            match fs::remove_file(path) {
                Ok(()) => {}
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(error) => return Err(error.into()),
            }
        }
        for (name, oid) in &pins {
            if repository.try_find_reference(name.as_str())?.is_none() {
                repository.reference(name.as_str(), *oid, PreviousValue::MustNotExist, "")?;
            }
            sync_ref_path(repository.path(), &repository.path().join(name))?;
        }
        sync_ref_path(repository.path(), &repository.path().join("packed-refs"))?;
        if !edits.is_empty() {
            let fail = gix::lock::acquire::Fail::AfterDurationWithBackoff(
                std::time::Duration::from_secs(1),
            );
            repository
                .refs
                .transaction()
                .prepare(edits, fail, fail)?
                .commit(None)?;
        }
        for update in &authorization.refs {
            sync_ref_path(repository.path(), &repository.path().join(&update.ref_name))?;
        }
        sync_ref_path(repository.path(), &repository.path().join("packed-refs"))?;
        self.commit_locked(id)?;
        Ok(())
    }

    #[cfg(test)]
    pub(crate) fn prepare(&self, authorization: &CallbackAuthorization) -> io::Result<ReceiptId> {
        let _guard = self.lock()?;
        let catalog_refs = authorization
            .refs
            .iter()
            .filter(|update| update.ref_name.starts_with("refs/heads/"))
            .map(|update| update.ref_name.clone())
            .collect::<Vec<_>>();
        self.prepare_locked(authorization, &catalog_refs)
    }

    fn prepare_locked(
        &self,
        authorization: &CallbackAuthorization,
        catalog_refs: &[String],
    ) -> io::Result<ReceiptId> {
        if authorization.encoded_len() > MAX_RECEIPT_BYTES - MAGIC.len() {
            return Err(invalid_input("receipt payload capacity exceeded"));
        }
        validate(authorization)?;
        validate_catalog_refs(authorization, catalog_refs)?;
        let snapshot = ReceiptSnapshot {
            authorization: Some(authorization.clone()),
            catalog_refs: catalog_refs.to_vec(),
        };
        if snapshot.encoded_len() > MAX_RECEIPT_BYTES - MAGIC.len() {
            return Err(invalid_input("receipt payload capacity exceeded"));
        }
        let mut bytes = Vec::with_capacity(MAGIC.len() + snapshot.encoded_len());
        bytes.extend_from_slice(MAGIC);
        snapshot.encode(&mut bytes).map_err(io::Error::other)?;
        let id = receipt_id(&bytes);
        let entries = self.scan(false)?;
        if let Some((_, phase)) = entries.iter().find(|(existing, _)| existing == &id) {
            self.read(&id, *phase)?;
            return Ok(id);
        }
        if entries.len() == MAX_RECEIPTS {
            return Err(io::Error::new(
                io::ErrorKind::StorageFull,
                "receipt capacity exhausted",
            ));
        }
        let mut temporary = tempfile::Builder::new()
            .prefix(".pending-")
            .tempfile_in(&self.directory)?;
        temporary.write_all(&bytes)?;
        temporary.as_file().sync_all()?;
        let mut metadata = self.metadata()?;
        if metadata.len() == MAX_RECEIPTS {
            return Err(io::Error::new(
                io::ErrorKind::StorageFull,
                "receipt metadata capacity exhausted",
            ));
        }
        metadata.insert(
            id.clone(),
            ReceiptMetadata {
                repository_id: authorization.repository_id.clone(),
                phase: ReceiptPhase::Prepared,
            },
        );
        temporary
            .persist_noclobber(self.path(&id, ReceiptPhase::Prepared))
            .map_err(|error| error.error)?;
        drop(metadata);
        self.sync_directory()?;
        Ok(id)
    }

    pub fn pending_for_repository(
        &self,
        repository_id: &str,
    ) -> io::Result<Vec<(ReceiptId, ReceiptPhase)>> {
        Ok(self
            .metadata()?
            .iter()
            .filter(|(_, pending)| {
                pending.repository_id == repository_id && pending.phase != ReceiptPhase::Committed
            })
            .map(|(id, pending)| (id.clone(), pending.phase))
            .collect())
    }

    pub fn retains_repository(&self, repository_id: &str) -> io::Result<bool> {
        Ok(self
            .metadata()?
            .values()
            .any(|entry| entry.repository_id == repository_id))
    }

    pub fn repository_id(&self, id: &ReceiptId) -> io::Result<String> {
        self.metadata()?
            .get(id)
            .map(|entry| entry.repository_id.clone())
            .ok_or_else(|| invalid_data("receipt metadata missing"))
    }

    pub fn list(&self, after: Option<&ReceiptId>, limit: usize) -> io::Result<ReceiptPage> {
        if limit == 0 || limit > MAX_PAGE_SIZE {
            return Err(invalid_input("invalid receipt page size"));
        }
        let _guard = self.lock()?;
        let mut entries: Vec<_> = self
            .scan(false)?
            .into_iter()
            .filter(|(id, _)| after.is_none_or(|after| id > after))
            .take(limit + 1)
            .collect();
        let next = if entries.len() > limit {
            entries.truncate(limit);
            entries.last().map(|(id, _)| id.clone())
        } else {
            None
        };
        Ok(ReceiptPage { entries, next })
    }

    pub fn load(&self, id: &ReceiptId) -> io::Result<CallbackAuthorization> {
        let _guard = self.lock()?;
        let phase = self
            .phase(id)?
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "receipt not found"))?;
        self.read(id, phase)
    }

    pub fn load_committed(&self, id: &ReceiptId) -> io::Result<CallbackAuthorization> {
        let _guard = self.lock()?;
        if self.phase(id)? != Some(ReceiptPhase::Committed) {
            return Err(invalid_input("committed receipt required"));
        }
        self.read(id, ReceiptPhase::Committed)
    }

    pub fn load_committed_delivery(
        &self,
        id: &ReceiptId,
    ) -> io::Result<(CallbackAuthorization, Vec<String>)> {
        let _guard = self.lock()?;
        if self.phase(id)? != Some(ReceiptPhase::Committed) {
            return Err(invalid_input("committed receipt required"));
        }
        let snapshot = self.read_snapshot(id, ReceiptPhase::Committed)?;
        Ok((
            snapshot
                .authorization
                .ok_or_else(|| invalid_data("receipt authorization missing"))?,
            snapshot.catalog_refs,
        ))
    }

    #[cfg(test)]
    pub(crate) fn commit(&self, id: &ReceiptId) -> io::Result<()> {
        let _guard = self.lock()?;
        self.commit_locked(id)
    }

    fn commit_locked(&self, id: &ReceiptId) -> io::Result<()> {
        let phase = self
            .phase(id)?
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "receipt not found"))?;
        self.read(id, phase)?;
        if phase == ReceiptPhase::Acknowledged {
            return Err(invalid_input("receive is already acknowledged"));
        }
        if phase == ReceiptPhase::Prepared {
            fs::rename(
                self.path(id, ReceiptPhase::Prepared),
                self.path(id, ReceiptPhase::Committed),
            )?;
        }
        self.sync_directory()?;
        self.metadata()?
            .get_mut(id)
            .ok_or_else(|| invalid_data("receipt metadata missing"))?
            .phase = ReceiptPhase::Committed;
        Ok(())
    }

    fn verify_repository(
        &self,
        repository: &gix::Repository,
        authorization: &CallbackAuthorization,
    ) -> anyhow::Result<()> {
        let expected = crate::git::repo::fanout_path(
            self.directory.parent().expect("receipt data root"),
            &authorization.repository_id,
        )?;
        anyhow::ensure!(
            expected.canonicalize()? == repository.path().canonicalize()?,
            "receipt repository does not match retained storage"
        );
        Ok(())
    }

    /// Begin and finish cleanup under exclusive repository mutation ownership.
    /// An interrupted Acknowledged phase must fence later mutations too.
    pub fn acknowledge_fenced(
        &self,
        repository: &gix::Repository,
        id: &ReceiptId,
    ) -> anyhow::Result<()> {
        let _guard = self.lock()?;
        let Some(phase) = self.phase(id)? else {
            anyhow::ensure!(
                !self.metadata()?.contains_key(id),
                "pending cleanup authority is missing"
            );
            return Ok(());
        };
        anyhow::ensure!(
            phase != ReceiptPhase::Prepared,
            "committed receipt required"
        );
        let authorization = self.read(id, phase)?;
        self.verify_repository(repository, &authorization)?;
        anyhow::ensure!(
            self.pending_for_repository(&authorization.repository_id)?
                .iter()
                .all(|(pending, _)| pending == id),
            "repository has another unresolved receive"
        );
        let pins = receipt_pins(id, &authorization)?;
        let mut locks = vec![repository.path().join("packed-refs.lock")];
        for (name, oid) in &pins {
            if let Some(reference) = repository.try_find_reference(name.as_str())? {
                anyhow::ensure!(
                    reference.target() == gix::refs::TargetRef::Object(oid.as_ref()),
                    "retained object pin differs from receipt"
                );
            }
            locks.push(repository.path().join(format!("{name}.lock")));
        }
        for path in &locks {
            match fs::symlink_metadata(path) {
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(error) => return Err(error.into()),
                Ok(metadata)
                    if phase == ReceiptPhase::Acknowledged && metadata.file_type().is_file() => {}
                Ok(_) => return Err(invalid_data("preexisting pin cleanup lock").into()),
            }
        }
        let mut metadata = self.metadata()?;
        if phase == ReceiptPhase::Committed {
            fs::rename(
                self.path(id, ReceiptPhase::Committed),
                self.path(id, ReceiptPhase::Acknowledged),
            )?;
        }
        metadata.insert(
            id.clone(),
            ReceiptMetadata {
                repository_id: authorization.repository_id,
                phase: ReceiptPhase::Acknowledged,
            },
        );
        drop(metadata);
        self.sync_directory()?;
        for path in locks {
            match fs::remove_file(path) {
                Ok(()) => {}
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(error) => return Err(error.into()),
            }
        }
        for (name, _) in &pins {
            if let Some(reference) = repository.try_find_reference(name.as_str())? {
                reference.delete()?;
            }
            sync_ref_path(repository.path(), &repository.path().join(name))?;
        }
        sync_ref_path(repository.path(), &repository.path().join("packed-refs"))?;
        fs::remove_file(self.path(id, ReceiptPhase::Acknowledged))?;
        self.sync_directory()?;
        self.metadata()?.remove(id);
        Ok(())
    }

    #[cfg(test)]
    pub(crate) fn acknowledge(&self, id: &ReceiptId) -> io::Result<()> {
        self.remove(id, ReceiptPhase::Committed)
    }

    #[cfg(test)]
    pub(crate) fn discard_prepared(&self, id: &ReceiptId) -> io::Result<()> {
        self.remove(id, ReceiptPhase::Prepared)
    }

    #[cfg(test)]
    fn remove(&self, id: &ReceiptId, expected: ReceiptPhase) -> io::Result<()> {
        let _guard = self.lock()?;
        if let Some(phase) = self.phase(id)? {
            if phase != expected {
                return Err(invalid_input("receipt phase does not permit removal"));
            }
            self.read(id, phase)?;
            fs::remove_file(self.path(id, phase))?;
        }
        self.sync_directory()?;
        self.metadata()?.remove(id);
        Ok(())
    }

    fn metadata(&self) -> io::Result<std::sync::MutexGuard<'_, ReceiptIndex>> {
        self.metadata
            .lock()
            .map_err(|_| io::Error::other("receipt barrier lock poisoned"))
    }

    fn lock(&self) -> io::Result<std::sync::MutexGuard<'_, ()>> {
        self.gate
            .lock()
            .map_err(|_| io::Error::other("receipt storage lock poisoned"))
    }

    fn sync_directory(&self) -> io::Result<()> {
        File::open(&self.directory)?.sync_all()
    }

    fn path(&self, id: &ReceiptId, phase: ReceiptPhase) -> PathBuf {
        self.directory.join(format!("{}.{}", id.0, phase.suffix()))
    }

    fn phase(&self, id: &ReceiptId) -> io::Result<Option<ReceiptPhase>> {
        Ok(self
            .scan(false)?
            .into_iter()
            .find_map(|(existing, phase)| (existing == *id).then_some(phase)))
    }

    fn scan(&self, allow_temporary: bool) -> io::Result<Vec<(ReceiptId, ReceiptPhase)>> {
        let mut entries = Vec::new();
        let mut ids = BTreeSet::new();
        let mut count = 0;
        let mut temporary_count = 0;
        for entry in fs::read_dir(&self.directory)? {
            let entry = entry?;
            let name = entry
                .file_name()
                .into_string()
                .map_err(|_| invalid_data("invalid receipt filename"))?;
            if name == ".owner" {
                continue;
            }
            count += 1;
            if count > MAX_RECEIPTS + usize::from(allow_temporary) {
                return Err(invalid_data("receipt directory capacity exceeded"));
            }
            let metadata = fs::symlink_metadata(entry.path())?;
            if !metadata.file_type().is_file() || metadata.len() > MAX_RECEIPT_BYTES as u64 {
                return Err(invalid_data("invalid receipt file"));
            }
            if allow_temporary && name.starts_with(".pending-") {
                temporary_count += 1;
                if temporary_count > 1 {
                    return Err(invalid_data("temporary receipt capacity exceeded"));
                }
                continue;
            }
            let (id, suffix) = name
                .split_once('.')
                .ok_or_else(|| invalid_data("invalid receipt filename"))?;
            if id.len() != 64
                || !id
                    .bytes()
                    .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
            {
                return Err(invalid_data("invalid receipt identity"));
            }
            let phase = match suffix {
                "prepared" => ReceiptPhase::Prepared,
                "committed" => ReceiptPhase::Committed,
                "acknowledged" => ReceiptPhase::Acknowledged,
                _ => return Err(invalid_data("unsupported receipt phase")),
            };
            let id = ReceiptId(id.to_owned());
            if !ids.insert(id.clone()) {
                return Err(invalid_data("contradictory receipt phases"));
            }
            entries.push((id, phase));
            if entries.len() > MAX_RECEIPTS {
                return Err(invalid_data("receipt capacity exceeded"));
            }
        }
        entries.sort_by(|(left, _), (right, _)| left.cmp(right));
        Ok(entries)
    }

    fn read(&self, id: &ReceiptId, phase: ReceiptPhase) -> io::Result<CallbackAuthorization> {
        self.read_snapshot(id, phase)?
            .authorization
            .ok_or_else(|| invalid_data("receipt authorization missing"))
    }

    fn read_snapshot(&self, id: &ReceiptId, phase: ReceiptPhase) -> io::Result<ReceiptSnapshot> {
        let path = self.path(id, phase);
        if !fs::symlink_metadata(&path)?.file_type().is_file() {
            return Err(invalid_data("invalid receipt file"));
        }
        let mut bytes = Vec::new();
        File::open(path)?
            .take(MAX_RECEIPT_BYTES as u64 + 1)
            .read_to_end(&mut bytes)?;
        if bytes.len() > MAX_RECEIPT_BYTES || !bytes.starts_with(MAGIC) || receipt_id(&bytes) != *id
        {
            return Err(invalid_data("receipt integrity check failed"));
        }
        let payload = &bytes[MAGIC.len()..];
        let snapshot = ReceiptSnapshot::decode(payload)
            .map_err(|_| invalid_data("invalid receipt payload"))?;
        let authorization = snapshot
            .authorization
            .as_ref()
            .ok_or_else(|| invalid_data("receipt authorization missing"))?;
        validate(authorization).map_err(|_| invalid_data("invalid receipt context"))?;
        validate_catalog_refs(authorization, &snapshot.catalog_refs)
            .map_err(|_| invalid_data("invalid receipt routing"))?;
        if snapshot.encode_to_vec() != payload {
            return Err(invalid_data("unsupported receipt encoding"));
        }
        Ok(snapshot)
    }
}

fn validate_catalog_refs(
    authorization: &CallbackAuthorization,
    catalog_refs: &[String],
) -> io::Result<()> {
    if catalog_refs.len() > authorization.refs.len() {
        return Err(invalid_input("invalid receipt catalog refs"));
    }
    let mut names = BTreeSet::new();
    for name in catalog_refs {
        if !authorization
            .refs
            .iter()
            .any(|update| update.ref_name == *name)
            || !names.insert(name)
        {
            return Err(invalid_input("invalid receipt catalog refs"));
        }
    }
    Ok(())
}

fn validate(authorization: &CallbackAuthorization) -> io::Result<()> {
    crate::git::repo::fanout_path(Path::new("."), &authorization.repository_id)
        .map_err(|_| invalid_input("invalid receipt repository"))?;
    if authorization.receive_id.is_empty()
        || authorization
            .actor
            .as_ref()
            .is_none_or(|actor| actor.subject.trim().is_empty() || actor.auth_method.is_empty())
        || authorization.refs.is_empty()
        || authorization.refs.len() > MAX_REFS
    {
        return Err(invalid_input("invalid receipt context"));
    }
    let mut names = BTreeSet::new();
    for update in &authorization.refs {
        let _: gix::refs::FullName = update
            .ref_name
            .as_str()
            .try_into()
            .map_err(|_| invalid_input("invalid receipt ref"))?;
        if !update.ref_name.starts_with("refs/")
            || update.ref_name == "refs/gitstore"
            || update.ref_name.starts_with("refs/gitstore/")
            || !names.insert(&update.ref_name)
            || [&update.old_commit_sha, &update.new_commit_sha]
                .iter()
                .any(|oid| {
                    oid.len() != 40
                        || !oid
                            .bytes()
                            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
                })
        {
            return Err(invalid_input("invalid receipt ref vector"));
        }
    }
    Ok(())
}

fn receipt_pins(
    id: &ReceiptId,
    authorization: &CallbackAuthorization,
) -> anyhow::Result<Vec<(String, gix::ObjectId)>> {
    let mut objects = BTreeSet::new();
    for update in &authorization.refs {
        for oid in [&update.old_commit_sha, &update.new_commit_sha] {
            let oid = gix::ObjectId::from_hex(oid.as_bytes())?;
            if !oid.is_null() {
                objects.insert(oid);
            }
        }
    }
    Ok(objects
        .into_iter()
        .map(|oid| (format!("refs/gitstore/admission/{}/{oid}", id.0), oid))
        .collect())
}

fn sync_ref_path(repository: &Path, path: &Path) -> io::Result<()> {
    match File::open(path) {
        Ok(file) => file.sync_all()?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error),
    }
    for parent in path.parent().expect("ref parent").ancestors() {
        match File::open(parent) {
            Ok(directory) => directory.sync_all()?,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
        if parent == repository {
            break;
        }
    }
    Ok(())
}

fn receipt_id(bytes: &[u8]) -> ReceiptId {
    ReceiptId(
        Sha256::digest(bytes)
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect(),
    )
}

fn invalid_input(message: &'static str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, message)
}

fn invalid_data(message: &'static str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::git::hooks::admission_handler::catalog_proto::{
        CallbackActor, CallbackAuthorization, ReceiveRef,
    };

    fn authorization(receive: usize) -> CallbackAuthorization {
        CallbackAuthorization {
            repository_id: "01900000-0000-7000-8000-000000000001".into(),
            receive_id: format!("fixture-{receive}"),
            actor: Some(CallbackActor {
                subject: "original-actor".into(),
                groups: vec!["original-group".into()],
                auth_method: "service-account".into(),
                service_account_uid: "original-service-account".into(),
                issuer: "fixture-issuer".into(),
            }),
            refs: vec![
                ReceiveRef {
                    ref_name: "refs/heads/main".into(),
                    old_commit_sha: "1".repeat(40),
                    new_commit_sha: "2".repeat(40),
                },
                ReceiveRef {
                    ref_name: "refs/heads/catalog".into(),
                    old_commit_sha: "0".repeat(40),
                    new_commit_sha: "3".repeat(40),
                },
            ],
        }
    }

    #[test]
    fn prepared_and_committed_receipts_survive_replacement() {
        let root = tempfile::tempdir().unwrap();
        let original = authorization(1);
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let id = store.prepare(&original).unwrap();
        assert_eq!(store.prepare(&original).unwrap(), id);
        assert!(store.acknowledge(&id).is_err());
        drop(store);

        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let page = store.list(None, 1).unwrap();
        assert_eq!(page.entries, vec![(id.clone(), ReceiptPhase::Prepared)]);
        assert!(page.next.is_none());
        assert_eq!(store.load(&id).unwrap(), original);
        store.commit(&id).unwrap();
        store.commit(&id).unwrap();
        assert!(store.discard_prepared(&id).is_err());
        drop(store);

        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        assert_eq!(
            store.list(None, 1).unwrap().entries,
            vec![(id.clone(), ReceiptPhase::Committed)]
        );
        assert_eq!(store.load(&id).unwrap(), original);
        assert_eq!(store.prepare(&original).unwrap(), id);
        store.acknowledge(&id).unwrap();
        store.acknowledge(&id).unwrap();
        let inherited_owner = store._owner.try_clone().unwrap();
        drop(store);
        assert!(AdmissionReceiptStore::open(root.path())
            .unwrap()
            .list(None, 1)
            .unwrap()
            .entries
            .is_empty());
        drop(inherited_owner);
    }

    fn reference_fixture(root: &Path) -> (gix::Repository, CallbackAuthorization) {
        use gix::refs::transaction::PreviousValue;

        let mut original = authorization(1);
        let path = crate::git::repo::fanout_path(root, &original.repository_id).unwrap();
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        let repo = gix::init_bare(&path).unwrap();
        let tree = repo
            .write_object(&gix::objs::Tree { entries: vec![] })
            .unwrap()
            .detach();
        let signature = gix::actor::Signature {
            name: "fixture".into(),
            email: "fixture@localhost".into(),
            time: gix::date::Time::now_local_or_utc(),
        };
        let mut time = gix::date::parse::TimeBuf::default();
        let signature = signature.to_ref(&mut time);
        let mut commits = Vec::new();
        for name in ["refs/heads/main", "refs/heads/next"] {
            commits.push(
                repo.commit_as(
                    signature,
                    signature,
                    name,
                    name,
                    tree,
                    std::iter::empty::<gix::ObjectId>(),
                )
                .unwrap()
                .detach(),
            );
        }
        original.refs[0].old_commit_sha = commits[0].to_string();
        original.refs[0].new_commit_sha = commits[1].to_string();
        original.refs[1].new_commit_sha = commits[1].to_string();
        repo.reference(
            "refs/heads/remove",
            commits[0],
            PreviousValue::MustNotExist,
            "fixture",
        )
        .unwrap();
        original.refs.push(ReceiveRef {
            ref_name: "refs/heads/remove".to_owned(),
            old_commit_sha: commits[0].to_string(),
            new_commit_sha: "0".repeat(40),
        });
        (repo, original)
    }

    #[test]
    fn fenced_receipt_preserves_exact_catalog_routing_after_replacement() {
        let root = tempfile::tempdir().unwrap();
        let (repo, original) = reference_fixture(root.path());
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let catalog_refs = vec![original.refs[0].ref_name.clone()];
        for invalid in [
            vec!["refs/heads/not-accepted".to_owned()],
            vec![catalog_refs[0].clone(), catalog_refs[0].clone()],
        ] {
            assert!(store
                .prepare_fenced_with_catalog_refs(&repo, &original, &invalid)
                .is_err());
            assert!(store.list(None, 1).unwrap().entries.is_empty());
        }
        let id = store
            .prepare_fenced_with_catalog_refs(&repo, &original, &catalog_refs)
            .unwrap();
        store.recover_prepared(&repo, &id).unwrap();
        drop(store);
        let replacement = AdmissionReceiptStore::open(root.path()).unwrap();
        assert_eq!(
            replacement.load_committed_delivery(&id).unwrap(),
            (original, catalog_refs)
        );
    }

    #[test]
    fn receipts_without_routing_evidence_are_preserved_not_reinterpreted() {
        let root = tempfile::tempdir().unwrap();
        let (_, original) = reference_fixture(root.path());
        drop(AdmissionReceiptStore::open(root.path()).unwrap());
        let mut bytes = b"GSTRCV01".to_vec();
        original.encode(&mut bytes).unwrap();
        let id = receipt_id(&bytes);
        let path = root
            .path()
            .join(".admission-receipts-v1")
            .join(format!("{}.committed", id.0));
        fs::write(&path, &bytes).unwrap();
        assert!(AdmissionReceiptStore::open(root.path()).is_err());
        assert_eq!(fs::read(path).unwrap(), bytes);
    }

    #[test]
    fn fenced_preparation_requires_every_original_ref_preimage() {
        use gix::refs::transaction::PreviousValue;

        let root = tempfile::tempdir().unwrap();
        let (repo, original) = reference_fixture(root.path());
        let commits = [
            gix::ObjectId::from_hex(original.refs[0].old_commit_sha.as_bytes()).unwrap(),
            gix::ObjectId::from_hex(original.refs[0].new_commit_sha.as_bytes()).unwrap(),
        ];
        let store = AdmissionReceiptStore::open(root.path()).unwrap();

        repo.reference(
            "refs/heads/catalog",
            commits[1],
            PreviousValue::MustNotExist,
            "fixture",
        )
        .unwrap();
        assert!(store.prepare_fenced(&repo, &original).is_err());
        assert!(store.list(None, 1).unwrap().entries.is_empty());
        repo.find_reference("refs/heads/catalog")
            .unwrap()
            .delete()
            .unwrap();
        repo.reference("refs/heads/main", commits[1], PreviousValue::Any, "fixture")
            .unwrap();
        assert!(
            store.prepare_fenced(&repo, &original).is_err(),
            "matching the requested new tip is not proof of this receive"
        );
        assert!(store.list(None, 1).unwrap().entries.is_empty());
        repo.reference("refs/heads/main", commits[0], PreviousValue::Any, "fixture")
            .unwrap();

        for lock in ["refs/heads/main.lock", "packed-refs.lock"] {
            let lock = repo.path().join(lock);
            fs::write(&lock, b"unrelated lock").unwrap();
            assert!(store.prepare_fenced(&repo, &original).is_err());
            assert!(store.list(None, 1).unwrap().entries.is_empty());
            assert_eq!(fs::read(&lock).unwrap(), b"unrelated lock");
            fs::remove_file(lock).unwrap();
        }
        let other = tempfile::tempdir().unwrap();
        let other = gix::init_bare(other.path()).unwrap();
        assert!(store.prepare_fenced(&other, &original).is_err());
        let id = store.prepare_fenced(&repo, &original).unwrap();
        assert_eq!(store.load(&id).unwrap(), original);
        let mut overlapping = original.clone();
        overlapping.receive_id = "another-unresolved-receive".to_owned();
        assert!(
            store.prepare_fenced(&repo, &overlapping).is_err(),
            "a second receive cannot pass an unresolved repository barrier"
        );
        assert_eq!(
            repo.find_reference("refs/heads/main").unwrap().target(),
            gix::refs::TargetRef::Object(commits[0].as_ref()),
            "preparation must not mutate public refs"
        );

        repo.reference(
            "refs/heads/catalog",
            commits[0],
            PreviousValue::MustNotExist,
            "fixture",
        )
        .unwrap();
        let retained_lock = repo.path().join("refs/heads/main.lock");
        fs::write(&retained_lock, b"preserve divergent evidence").unwrap();
        assert!(store.recover_prepared(&repo, &id).is_err());
        assert_eq!(
            fs::read(&retained_lock).unwrap(),
            b"preserve divergent evidence"
        );
        fs::remove_file(retained_lock).unwrap();
        assert_eq!(
            repo.find_reference("refs/heads/main").unwrap().target(),
            gix::refs::TargetRef::Object(commits[0].as_ref()),
            "a divergent ref must fail before changing any other ref"
        );
        assert_eq!(store.load(&id).unwrap(), original);
        repo.find_reference("refs/heads/catalog")
            .unwrap()
            .delete()
            .unwrap();

        repo.reference(
            "refs/heads/main",
            commits[1],
            PreviousValue::Any,
            "partial commit",
        )
        .unwrap();
        let ref_lock = repo.path().join("refs/heads/catalog.lock");
        fs::write(&ref_lock, format!("{}\n", commits[1])).unwrap();
        let packed_lock = repo.path().join("packed-refs.lock");
        fs::write(&packed_lock, b"").unwrap();
        drop(store);
        let replacement = AdmissionReceiptStore::open(root.path()).unwrap();
        replacement.recover_prepared(&repo, &id).unwrap();
        for update in &original.refs {
            if update.new_commit_sha == "0".repeat(40) {
                assert!(repo.try_find_reference(&update.ref_name).unwrap().is_none());
            } else {
                assert_eq!(
                    repo.find_reference(&update.ref_name).unwrap().target(),
                    gix::refs::TargetRef::Object(commits[1].as_ref())
                );
            }
        }
        assert!(!ref_lock.exists());
        assert!(!packed_lock.exists());
        assert_eq!(replacement.load_committed(&id).unwrap(), original);
        assert_eq!(
            replacement.list(None, 1).unwrap().entries,
            vec![(id.clone(), ReceiptPhase::Committed)]
        );
        let mut later = overlapping;
        later.refs.truncate(1);
        later.refs[0].old_commit_sha = commits[1].to_string();
        later.refs[0].new_commit_sha = commits[0].to_string();
        let later_id = replacement.prepare_fenced(&repo, &later).unwrap();
        assert!(
            replacement.acknowledge_fenced(&repo, &id).is_err(),
            "older delivery cleanup cannot cross a newer unresolved receive"
        );
        assert_eq!(replacement.load_committed(&id).unwrap(), original);
        replacement.recover_prepared(&repo, &later_id).unwrap();
        replacement.recover_prepared(&repo, &id).unwrap();
        assert_eq!(
            repo.find_reference("refs/heads/main").unwrap().target(),
            gix::refs::TargetRef::Object(commits[0].as_ref()),
            "completed recovery must not undo a later ref change"
        );
    }

    #[test]
    fn receipt_bounds_do_not_evict_or_replace_accepted_context() {
        let root = tempfile::tempdir().unwrap();
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let first = authorization(0);
        let id = store.prepare(&first).unwrap();
        let mut oversized = authorization(1);
        oversized.actor.as_mut().unwrap().groups = vec!["x".repeat(MAX_RECEIPT_BYTES)];
        assert!(store.prepare(&oversized).is_err());
        for receive in 1..MAX_RECEIPTS {
            store.prepare(&authorization(receive)).unwrap();
        }
        assert!(store.prepare(&authorization(MAX_RECEIPTS)).is_err());
        assert_eq!(store.prepare(&first).unwrap(), id);
        assert_eq!(store.load(&id).unwrap(), first);
        let orphan = store.directory.join(".pending-fixture");
        fs::hard_link(store.path(&id, ReceiptPhase::Prepared), &orphan).unwrap();
        drop(store);
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        assert!(!orphan.exists());
        assert_eq!(store.load(&id).unwrap(), first);
        store.discard_prepared(&id).unwrap();
        store.prepare(&authorization(MAX_RECEIPTS)).unwrap();
    }

    #[test]
    fn receipt_objects_survive_gc_until_recoverable_acknowledgement() {
        let root = tempfile::tempdir().unwrap();
        let (repo, original) = reference_fixture(root.path());
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let id = store.prepare_fenced(&repo, &original).unwrap();
        store.recover_prepared(&repo, &id).unwrap();
        let references: Vec<_> = repo
            .references()
            .unwrap()
            .all()
            .unwrap()
            .map(|reference| reference.unwrap().name().as_bstr().to_string())
            .collect();
        let mut pins = Vec::new();
        for name in references {
            if name.starts_with("refs/gitstore/admission/") {
                pins.push(name);
            } else {
                repo.find_reference(name.as_str())
                    .unwrap()
                    .delete()
                    .unwrap();
            }
        }
        assert_eq!(pins.len(), 2);
        let maintenance = |args: &[&str]| {
            let result = std::process::Command::new("git")
                .args(["-c", "core.hooksPath=/dev/null", "--git-dir"])
                .arg(repo.path())
                .env_remove("GIT_DIR")
                .env_remove("GIT_COMMON_DIR")
                .env_remove("GIT_WORK_TREE")
                .env_remove("GIT_INDEX_FILE")
                .env_remove("GIT_OBJECT_DIRECTORY")
                .env_remove("GIT_ALTERNATE_OBJECT_DIRECTORIES")
                .args(args)
                .output()
                .unwrap();
            assert!(
                result.status.success(),
                "{}",
                String::from_utf8_lossy(&result.stderr)
            );
        };
        maintenance(&["pack-refs", "--all"]);
        maintenance(&["prune", "--expire=now"]);
        let retained = crate::git::repo::open_repository(repo.path()).unwrap();
        for oid in [
            &original.refs[0].old_commit_sha,
            &original.refs[0].new_commit_sha,
        ] {
            retained
                .find_commit(gix::ObjectId::from_hex(oid.as_bytes()).unwrap())
                .unwrap();
        }
        let foreign_lock = repo.path().join("packed-refs.lock");
        fs::write(&foreign_lock, b"unrelated writer").unwrap();
        assert!(store.acknowledge_fenced(&retained, &id).is_err());
        assert_eq!(store.load_committed(&id).unwrap(), original);
        assert_eq!(fs::read(&foreign_lock).unwrap(), b"unrelated writer");
        fs::remove_file(foreign_lock).unwrap();

        fs::rename(
            store.path(&id, ReceiptPhase::Committed),
            store.path(&id, ReceiptPhase::Acknowledged),
        )
        .unwrap();
        store.sync_directory().unwrap();
        retained.find_reference(&pins[0]).unwrap().delete().unwrap();
        drop(store);
        let replacement = AdmissionReceiptStore::open(root.path()).unwrap();
        assert!(replacement.load_committed(&id).is_err());
        let mut next = original.clone();
        next.receive_id = "after-cleanup".to_owned();
        next.refs = vec![ReceiveRef {
            ref_name: "refs/heads/absent".to_owned(),
            old_commit_sha: "0".repeat(40),
            new_commit_sha: "0".repeat(40),
        }];
        assert!(
            replacement.prepare_fenced(&retained, &next).is_err(),
            "replacement must restore the interrupted cleanup barrier"
        );
        replacement.acknowledge_fenced(&retained, &id).unwrap();
        assert!(replacement.list(None, 1).unwrap().entries.is_empty());
        let next_id = replacement.prepare_fenced(&retained, &next).unwrap();
        replacement.recover_prepared(&retained, &next_id).unwrap();
        replacement.acknowledge_fenced(&retained, &next_id).unwrap();
        let cleaned = crate::git::repo::open_repository(repo.path()).unwrap();
        for pin in pins {
            assert!(cleaned.try_find_reference(pin.as_str()).unwrap().is_none());
        }
        maintenance(&["prune", "--expire=now"]);
        let cleaned = crate::git::repo::open_repository(repo.path()).unwrap();
        for oid in [
            &original.refs[0].old_commit_sha,
            &original.refs[0].new_commit_sha,
        ] {
            assert!(cleaned
                .find_object(gix::ObjectId::from_hex(oid.as_bytes()).unwrap())
                .is_err());
        }
    }

    #[test]
    fn paged_receipts_resume_without_losing_original_context() {
        let root = tempfile::tempdir().unwrap();
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let mut expected = Vec::new();
        for receive in 0..MAX_PAGE_SIZE + 3 {
            let id = store.prepare(&authorization(receive)).unwrap();
            if receive % 2 == 0 {
                store.commit(&id).unwrap();
            }
            expected.push(id);
        }
        expected.sort();
        assert!(store.list(None, 0).is_err());
        assert!(store.list(None, MAX_PAGE_SIZE + 1).is_err());
        let first = store.list(None, MAX_PAGE_SIZE).unwrap();
        assert_eq!(first.entries.len(), MAX_PAGE_SIZE);
        let cursor = first.next.unwrap();
        drop(store);
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let second = store.list(Some(&cursor), MAX_PAGE_SIZE).unwrap();
        assert!(second.next.is_none());
        let actual: Vec<_> = first
            .entries
            .into_iter()
            .chain(second.entries)
            .map(|(id, _)| id)
            .collect();
        assert_eq!(actual, expected);
    }

    #[test]
    fn receipt_context_rejects_missing_actor_and_ambiguous_ref_vectors() {
        let root = tempfile::tempdir().unwrap();
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let mut missing_actor = authorization(1);
        missing_actor.actor = None;
        let mut duplicate_ref = authorization(2);
        duplicate_ref.refs.push(duplicate_ref.refs[0].clone());
        let mut malformed_ref = authorization(3);
        malformed_ref.refs[0].new_commit_sha = "../untrusted".into();
        for invalid in [missing_actor, duplicate_ref, malformed_ref] {
            assert_eq!(
                store.prepare(&invalid).unwrap_err().kind(),
                io::ErrorKind::InvalidInput
            );
        }
        assert!(store.list(None, 1).unwrap().entries.is_empty());
    }

    struct OwnedChild(std::process::Child);

    impl OwnedChild {
        fn wait_ready(&mut self, root: &Path) {
            let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
            while !root.join("ready").exists() && std::time::Instant::now() < deadline {
                assert!(self.0.try_wait().unwrap().is_none(), "writer exited early");
                std::thread::sleep(std::time::Duration::from_millis(10));
            }
            assert!(root.join("ready").exists(), "writer did not prepare");
        }
    }

    impl Drop for OwnedChild {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    #[test]
    fn receipt_snapshots_survive_killed_writer_replacement() {
        const CHILD_ROOT: &str = "GITSTORE_TEST_RECEIPT_CHILD_ROOT";
        if let Some(root) = std::env::var_os(CHILD_ROOT) {
            let root = Path::new(&root);
            let store = AdmissionReceiptStore::open(root).unwrap();
            store.prepare(&authorization(1)).unwrap();
            let committed = store.prepare(&authorization(2)).unwrap();
            store.commit(&committed).unwrap();
            fs::write(root.join("ready"), b"").unwrap();
            loop {
                std::thread::park();
            }
        }

        let root = tempfile::tempdir().unwrap();
        let mut child = OwnedChild(
            std::process::Command::new(std::env::current_exe().unwrap())
                .args([
                    "--exact",
                    "git::hooks::admission_receipts::tests::receipt_snapshots_survive_killed_writer_replacement",
                    "--nocapture",
                ])
                .env(CHILD_ROOT, root.path())
                .stdout(std::process::Stdio::null())
                .spawn()
                .unwrap(),
        );
        child.wait_ready(root.path());
        assert!(AdmissionReceiptStore::open(root.path()).is_err());
        child.0.kill().unwrap();
        assert!(!child.0.wait().unwrap().success());
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let page = store.list(None, MAX_PAGE_SIZE).unwrap();
        assert_eq!(page.entries.len(), 2);
        for (id, phase) in page.entries {
            let actual = store.load(&id).unwrap();
            if actual.receive_id == "fixture-1" {
                assert_eq!(actual, authorization(1));
                assert_eq!(phase, ReceiptPhase::Prepared);
            } else {
                assert_eq!(actual, authorization(2));
                assert_eq!(phase, ReceiptPhase::Committed);
            }
        }
    }

    #[test]
    fn native_ref_outcomes_recover_after_killed_writer() {
        use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};

        const CHILD_ROOT: &str = "GITSTORE_TEST_REF_RECEIPT_CHILD_ROOT";
        const STAGE: &str = "GITSTORE_TEST_REF_RECEIPT_STAGE";
        if let Some(root) = std::env::var_os(CHILD_ROOT) {
            let root = Path::new(&root);
            let authorization = CallbackAuthorization::decode(
                fs::read(root.join("fixture.pb")).unwrap().as_slice(),
            )
            .unwrap();
            let path = crate::git::repo::fanout_path(root, &authorization.repository_id).unwrap();
            let repo = crate::git::repo::open_repository(&path).unwrap();
            let store = AdmissionReceiptStore::open(root).unwrap();
            let id = store.prepare_fenced(&repo, &authorization).unwrap();
            let stage = std::env::var(STAGE).unwrap();
            let mut pending = None;
            if stage != "prepared" {
                if stage == "partial" {
                    let update = &authorization.refs[0];
                    repo.reference(
                        update.ref_name.as_str(),
                        gix::ObjectId::from_hex(update.new_commit_sha.as_bytes()).unwrap(),
                        PreviousValue::MustExistAndMatch(gix::refs::Target::Object(
                            gix::ObjectId::from_hex(update.old_commit_sha.as_bytes()).unwrap(),
                        )),
                        "partial accepted receive",
                    )
                    .unwrap();
                }
                let edits: Vec<_> = authorization
                    .refs
                    .iter()
                    .skip(usize::from(stage == "partial"))
                    .map(|update| {
                        let old =
                            gix::ObjectId::from_hex(update.old_commit_sha.as_bytes()).unwrap();
                        let new =
                            gix::ObjectId::from_hex(update.new_commit_sha.as_bytes()).unwrap();
                        let expected = if old.is_null() {
                            PreviousValue::MustNotExist
                        } else {
                            PreviousValue::MustExistAndMatch(gix::refs::Target::Object(old))
                        };
                        RefEdit {
                            name: update.ref_name.as_str().try_into().unwrap(),
                            deref: false,
                            change: if new.is_null() {
                                Change::Delete {
                                    expected,
                                    log: RefLog::AndReference,
                                }
                            } else {
                                Change::Update {
                                    expected,
                                    new: gix::refs::Target::Object(new),
                                    log: LogChange::default(),
                                }
                            },
                        }
                    })
                    .collect();
                let transaction = repo
                    .refs
                    .transaction()
                    .prepare(
                        edits,
                        gix::lock::acquire::Fail::Immediately,
                        gix::lock::acquire::Fail::Immediately,
                    )
                    .unwrap();
                if stage == "partial" {
                    pending = Some(transaction);
                } else {
                    transaction.commit(None).unwrap();
                }
            }
            if stage == "acknowledged" {
                store.recover_prepared(&repo, &id).unwrap();
                fs::rename(
                    store.path(&id, ReceiptPhase::Committed),
                    store.path(&id, ReceiptPhase::Acknowledged),
                )
                .unwrap();
                store.sync_directory().unwrap();
                let edits: Vec<_> = receipt_pins(&id, &authorization)
                    .unwrap()
                    .into_iter()
                    .map(|(name, oid)| RefEdit {
                        name: name.as_str().try_into().unwrap(),
                        deref: false,
                        change: Change::Delete {
                            expected: PreviousValue::MustExistAndMatch(gix::refs::Target::Object(
                                oid,
                            )),
                            log: RefLog::AndReference,
                        },
                    })
                    .collect();
                pending = Some(
                    repo.refs
                        .transaction()
                        .prepare(
                            edits,
                            gix::lock::acquire::Fail::Immediately,
                            gix::lock::acquire::Fail::Immediately,
                        )
                        .unwrap(),
                );
            }
            fs::write(root.join("ready"), b"").unwrap();
            loop {
                std::hint::black_box(&pending);
                std::thread::park();
            }
        }

        for stage in ["prepared", "partial", "committed-refs", "acknowledged"] {
            let root = tempfile::tempdir().unwrap();
            let (repo, original) = reference_fixture(root.path());
            drop(repo);
            fs::write(root.path().join("fixture.pb"), original.encode_to_vec()).unwrap();
            let mut child = OwnedChild(std::process::Command::new(std::env::current_exe().unwrap())
                .args(["--exact", "git::hooks::admission_receipts::tests::native_ref_outcomes_recover_after_killed_writer", "--nocapture"])
                .env(CHILD_ROOT, root.path()).env(STAGE, stage)
                .stdout(std::process::Stdio::null()).spawn().unwrap());
            child.wait_ready(root.path());
            assert!(AdmissionReceiptStore::open(root.path()).is_err());
            child.0.kill().unwrap();
            assert!(!child.0.wait().unwrap().success());
            let replacement = AdmissionReceiptStore::open(root.path()).unwrap();
            let page = replacement.list(None, 1).unwrap();
            assert_eq!(page.entries.len(), 1);
            let (id, phase) = &page.entries[0];
            let path = crate::git::repo::fanout_path(root.path(), &original.repository_id).unwrap();
            let repo = crate::git::repo::open_repository(&path).unwrap();
            if stage == "acknowledged" {
                assert_eq!(*phase, ReceiptPhase::Acknowledged);
                assert_eq!(replacement.load(id).unwrap(), original);
                replacement.acknowledge_fenced(&repo, id).unwrap();
                assert!(replacement.list(None, 1).unwrap().entries.is_empty());
                for (name, _) in receipt_pins(id, &original).unwrap() {
                    assert!(repo.try_find_reference(name.as_str()).unwrap().is_none());
                }
            } else {
                assert_eq!(*phase, ReceiptPhase::Prepared);
                replacement.recover_prepared(&repo, id).unwrap();
                assert_eq!(replacement.load_committed(id).unwrap(), original);
            }
            for update in &original.refs {
                let current = repo.try_find_reference(&update.ref_name).unwrap();
                if update.new_commit_sha == "0".repeat(40) {
                    assert!(current.is_none(), "{stage}");
                } else {
                    assert_eq!(
                        current.unwrap().target().try_id().unwrap().to_string(),
                        update.new_commit_sha,
                        "{stage}"
                    );
                }
                assert!(!repo
                    .path()
                    .join(format!("{}.lock", update.ref_name))
                    .exists());
            }
            assert!(!repo.path().join("packed-refs.lock").exists());
        }
    }

    #[test]
    fn receipt_storage_rejects_overlap_corruption_and_unknown_files() {
        let root = tempfile::tempdir().unwrap();
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        assert!(AdmissionReceiptStore::open(root.path()).is_err());
        let id = store.prepare(&authorization(1)).unwrap();
        let path = store.path(&id, ReceiptPhase::Prepared);
        std::fs::write(&path, b"truncated").unwrap();
        assert!(store.load(&id).is_err());
        assert!(store.commit(&id).is_err());
        assert!(path.exists());
        drop(store);
        assert!(AdmissionReceiptStore::open(root.path()).is_err());

        let root = tempfile::tempdir().unwrap();
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        let directory = store.directory.clone();
        drop(store);
        let orphan = directory.join(".pending-fixture");
        std::fs::write(&orphan, b"incomplete uncommitted write").unwrap();
        let store = AdmissionReceiptStore::open(root.path()).unwrap();
        assert!(!orphan.exists());
        drop(store);
        let unknown = directory.join("unrecognized-record");
        std::fs::write(&unknown, b"retain for repair").unwrap();
        assert!(AdmissionReceiptStore::open(root.path()).is_err());
        assert!(unknown.exists());
    }
}
