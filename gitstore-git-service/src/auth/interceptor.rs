// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use crate::grpc::metrics::{record_rpc_verification_phase, RPCVerificationPhase};
use hmac::{Hmac, KeyInit, Mac};
use prost::Message;
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeSet, HashMap, HashSet},
    fs::{self, File, OpenOptions},
    path::{Path, PathBuf},
    sync::{Arc, Condvar, Mutex},
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};
use tonic::{metadata::MetadataMap, Request, Status};

pub const SIGNATURE_HEADER: &str = "x-gitstore-rpc";
const MAX_REPLAYS: usize = 100_000;
const REPLAY_CLEANUP_CLOCK_INTERVAL_MS: u64 = 250;
const VERIFICATION_WAITERS: u32 = 256;
const VERIFICATION_WAIT_BYTES: u32 = 128 * 1024 * 1024;
const PREPARED_REPLAY_INODES: usize = 64;

struct ReplayPreparationState {
    ready: Vec<tempfile::NamedTempFile>,
    refilling: bool,
    disabled: bool,
    retry_after: Instant,
}

struct ReplayPreparationPool {
    directory: PathBuf,
    state: Mutex<ReplayPreparationState>,
}

impl ReplayPreparationPool {
    fn open(directory: PathBuf) -> std::io::Result<Arc<Self>> {
        fs::create_dir_all(&directory)?;
        if !fs::symlink_metadata(&directory)?.is_dir() {
            return Err(std::io::Error::other("invalid RPC preparation directory"));
        }
        let stale = fs::read_dir(&directory)?
            .take(PREPARED_REPLAY_INODES + 33)
            .collect::<std::io::Result<Vec<_>>>()?;
        if stale.len() > PREPARED_REPLAY_INODES + 32 {
            return Err(std::io::Error::other("RPC preparation capacity exceeded"));
        }
        for entry in &stale {
            let metadata = fs::symlink_metadata(entry.path())?;
            if !entry.file_name().to_string_lossy().starts_with("nonce-")
                || !metadata.is_file()
                || metadata.len() != 0
            {
                return Err(std::io::Error::other("invalid RPC preparation file"));
            }
        }
        for entry in stale {
            fs::remove_file(entry.path())?;
        }
        let ready = Self::prepare(&directory, PREPARED_REPLAY_INODES, File::sync_all)?;
        File::open(
            directory
                .parent()
                .ok_or_else(|| std::io::Error::other("RPC preparation parent unavailable"))?,
        )?
        .sync_all()?;
        Ok(Arc::new(Self {
            directory,
            state: Mutex::new(ReplayPreparationState {
                ready,
                refilling: false,
                disabled: false,
                retry_after: Instant::now(),
            }),
        }))
    }

    fn prepare(
        directory: &Path,
        count: usize,
        persist: impl Fn(&File) -> std::io::Result<()>,
    ) -> std::io::Result<Vec<tempfile::NamedTempFile>> {
        let files = (0..count)
            .map(|_| {
                tempfile::Builder::new()
                    .prefix("nonce-")
                    .tempfile_in(directory)
            })
            .collect::<std::io::Result<Vec<_>>>()?;
        for file in &files {
            persist(file.as_file())?;
        }
        File::open(directory)?.sync_all()?;
        Ok(files)
    }

    fn take(self: &Arc<Self>) -> Result<Option<tempfile::NamedTempFile>, Status> {
        let mut state = self
            .state
            .lock()
            .map_err(|_| Status::unavailable("RPC preparation state unavailable"))?;
        if state.disabled {
            return Ok(None);
        }
        let file = state.ready.pop();
        if state.ready.len() <= PREPARED_REPLAY_INODES / 2
            && !state.refilling
            && Instant::now() >= state.retry_after
        {
            if let Ok(runtime) = tokio::runtime::Handle::try_current() {
                let count = PREPARED_REPLAY_INODES - state.ready.len();
                state.refilling = true;
                let pool = self.clone();
                runtime.spawn_blocking(move || {
                    let started = Instant::now();
                    let prepared = Self::prepare(&pool.directory, count, File::sync_all);
                    let mut state = match pool.state.lock() {
                        Ok(state) => state,
                        Err(_) => {
                            tracing::error!("RPC preparation state unavailable");
                            return;
                        }
                    };
                    let failure = match prepared {
                        Ok(files) => {
                            state.ready.extend(files);
                            None
                        }
                        Err(error) => {
                            state.retry_after = Instant::now() + Duration::from_millis(250);
                            Some(error)
                        }
                    };
                    drop(state);
                    if let Some(error) = failure {
                        tracing::warn!(
                            error = %error,
                            "RPC preparation failed; standalone inode durability remains enabled"
                        );
                    } else {
                        tracing::info!(
                            prepared_inodes = count,
                            duration_ms = started.elapsed().as_secs_f64() * 1000.0,
                            "RPC replay preparation timing"
                        );
                    }
                    match pool.state.lock() {
                        Ok(mut state) => state.refilling = false,
                        Err(_) => tracing::error!("RPC preparation state unavailable"),
                    }
                });
            }
        }
        Ok(file)
    }

    fn disable(&self) -> Result<(), Status> {
        self.state
            .lock()
            .map_err(|_| Status::unavailable("RPC preparation state unavailable"))?
            .disabled = true;
        Ok(())
    }
}

#[derive(Debug)]
enum ReplayReservation {
    Fresh(File),
    Prepared,
}

impl ReplayReservation {
    fn sync_all(&self) -> std::io::Result<()> {
        match self {
            Self::Fresh(file) => file.sync_all(),
            // The immutable empty inode was flushed before its exclusive move.
            Self::Prepared => Ok(()),
        }
    }
}

fn rename_prepared_inode(from: &Path, to: &Path) -> std::io::Result<()> {
    #[cfg(any(target_os = "linux", target_vendor = "apple"))]
    {
        rustix::fs::renameat_with(
            rustix::fs::CWD,
            from,
            rustix::fs::CWD,
            to,
            rustix::fs::RenameFlags::NOREPLACE,
        )?;
        Ok(())
    }
    #[cfg(not(any(target_os = "linux", target_vendor = "apple")))]
    {
        let _ = (from, to);
        Err(std::io::Error::new(
            std::io::ErrorKind::Unsupported,
            "exclusive RPC preparation rename unsupported",
        ))
    }
}

fn now_millis() -> Result<u64, Status> {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|v| v.as_millis() as u64)
        .map_err(|_| Status::unavailable("RPC clock unavailable"))
}
fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}
fn decode_hex(value: &str, size: usize) -> Result<Vec<u8>, Status> {
    if value.len() != size * 2
        || !value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    {
        return Err(Status::unauthenticated("invalid signed RPC envelope"));
    }
    (0..value.len())
        .step_by(2)
        .map(|i| {
            u8::from_str_radix(&value[i..i + 2], 16)
                .map_err(|_| Status::unauthenticated("invalid signed RPC envelope"))
        })
        .collect()
}
fn mac(secret: &str, audience: &str, method: &str, payload: &str) -> Hmac<Sha256> {
    let mut mac =
        Hmac::<Sha256>::new_from_slice(secret.as_bytes()).expect("HMAC accepts any key size");
    mac.update(format!("gitstore-rpc-v1\n{audience}\n{method}\n{payload}").as_bytes());
    mac
}
pub fn sign_request<T: Message>(
    message: T,
    secret: &str,
    audience: &str,
    method: &str,
) -> Result<Request<T>, Status> {
    let mut nonce = [0u8; 16];
    getrandom::fill(&mut nonce).map_err(|_| Status::unavailable("RPC nonce unavailable"))?;
    signed_at(
        message,
        secret,
        audience,
        method,
        now_millis()?,
        &hex(&nonce),
    )
}
fn signed_at<T: Message>(
    message: T,
    secret: &str,
    audience: &str,
    method: &str,
    now: u64,
    nonce: &str,
) -> Result<Request<T>, Status> {
    if secret.is_empty() {
        return Err(Status::unavailable("RPC signing key required"));
    }
    let digest = hex(&Sha256::digest(message.encode_to_vec()));
    let payload = format!("{now}:{}:{nonce}:{digest}", now + 60_000);
    let signature = hex(&mac(secret, audience, method, &payload)
        .finalize()
        .into_bytes());
    let mut request = Request::new(message);
    request.metadata_mut().insert(
        SIGNATURE_HEADER,
        format!("{payload}:{signature}")
            .parse()
            .map_err(|_| Status::internal("invalid RPC signature metadata"))?,
    );
    crate::inject_trace(request.metadata_mut());
    Ok(request)
}

/// Reverse requests carry only a fresh signature, never an API/user bearer.
pub fn callback_request<T: Message>(
    message: T,
    secret: &str,
    method: &str,
) -> Result<Request<T>, Status> {
    sign_request(message, secret, "gitstore-api", method)
}

/// One active Git process owns this retained directory. A nonce is persisted
/// before effects and survives replacement. No live entry is evicted at the
/// capacity bound: requests fail closed until expired entries can be reclaimed.
struct ReplayState {
    directory: PathBuf,
    preparation: Option<Arc<ReplayPreparationPool>>,
    entries: BTreeSet<(u64, String)>,
    nonces: HashSet<String>,
    pending: HashSet<String>,
    high_water: u64,
    durable_high_water: u64,
    maintenance_in_progress: bool,
}

#[derive(Default)]
struct ReplayDirectoryState {
    next: u64,
    pending: Vec<u64>,
    outcomes: HashMap<u64, Result<(), Status>>,
    in_flight: usize,
    syncing: bool,
}

#[derive(Default)]
struct ReplayDirectoryDurability {
    state: Mutex<ReplayDirectoryState>,
    changed: Condvar,
}

impl ReplayDirectoryDurability {
    fn sync(&self, persist: impl Fn() -> Result<(), Status>) -> Result<(), Status> {
        let unavailable = || Status::unavailable("RPC replay directory barrier unavailable");
        let mut state = self.state.lock().map_err(|_| unavailable())?;
        if state.pending.len() + state.outcomes.len() + state.in_flight >= 32 {
            return Err(Status::resource_exhausted(
                "RPC replay directory barrier exhausted",
            ));
        }
        state.next = state.next.checked_add(1).ok_or_else(unavailable)?;
        let ticket = state.next;
        state.pending.push(ticket);
        loop {
            if let Some(outcome) = state.outcomes.remove(&ticket) {
                return outcome;
            }
            if state.syncing {
                state = self.changed.wait(state).map_err(|_| unavailable())?;
                continue;
            }
            // Only entries whose inode fsync already completed can share this
            // directory barrier. Arrivals during fsync require a later barrier.
            let ready = std::mem::take(&mut state.pending);
            state.in_flight = ready.len();
            state.syncing = true;
            drop(state);
            let started = Instant::now();
            let outcome = persist();
            let duration = started.elapsed();
            let publishing = Instant::now();
            state = self.state.lock().map_err(|_| unavailable())?;
            for &covered in &ready {
                state.outcomes.insert(covered, outcome.clone());
            }
            state.syncing = false;
            state.in_flight = 0;
            self.changed.notify_all();
            drop(state);
            record_rpc_verification_phase(
                RPCVerificationPhase::DirectoryPublish,
                publishing.elapsed().as_secs_f64(),
            );
            let reporting = Instant::now();
            tracing::info!(
                requests = ready.len(),
                duration_ms = duration.as_secs_f64() * 1000.0,
                success = outcome.is_ok(),
                "RPC replay directory barrier timing"
            );
            record_rpc_verification_phase(
                RPCVerificationPhase::DirectoryReport,
                reporting.elapsed().as_secs_f64(),
            );
            state = self.state.lock().map_err(|_| unavailable())?;
        }
    }
}

impl ReplayState {
    fn open(directory: PathBuf) -> Result<Self, Status> {
        fs::create_dir_all(&directory)
            .map_err(|_| Status::unavailable("RPC replay storage unavailable"))?;
        let high_water = match fs::read_to_string(directory.join("clock")) {
            Ok(value) => value
                .parse()
                .map_err(|_| Status::unavailable("invalid RPC replay clock"))?,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => 0,
            Err(_) => return Err(Status::unavailable("RPC replay clock unavailable")),
        };
        let mut state = Self {
            preparation: match ReplayPreparationPool::open(directory.with_extension("prepared-v1"))
            {
                Ok(pool) => Some(pool),
                Err(error) => {
                    tracing::warn!(
                        error = %error,
                        "RPC preparation unavailable; standalone inode durability remains enabled"
                    );
                    None
                }
            },
            directory,
            entries: BTreeSet::new(),
            nonces: HashSet::new(),
            pending: HashSet::new(),
            high_water,
            durable_high_water: high_water,
            maintenance_in_progress: false,
        };
        for entry in fs::read_dir(&state.directory)
            .map_err(|_| Status::unavailable("RPC replay storage unavailable"))?
        {
            let entry = entry.map_err(|_| Status::unavailable("RPC replay storage unavailable"))?;
            let name = entry
                .file_name()
                .into_string()
                .map_err(|_| Status::unavailable("invalid RPC replay record"))?;
            if name == "clock" || name == "clock.tmp" {
                continue;
            }
            if state.entries.len() >= MAX_REPLAYS {
                return Err(Status::resource_exhausted(
                    "RPC replay storage capacity exceeded",
                ));
            }
            let (expiry, nonce) = name
                .split_once('-')
                .ok_or_else(|| Status::unavailable("invalid RPC replay record"))?;
            let expiry = expiry
                .parse()
                .map_err(|_| Status::unavailable("invalid RPC replay expiry"))?;
            decode_hex(nonce, 16)?;
            if !state.nonces.insert(nonce.to_owned()) {
                return Err(Status::unavailable("duplicate RPC replay nonce"));
            }
            state.entries.insert((expiry, nonce.to_owned()));
        }
        Ok(state)
    }
    #[cfg(test)]
    fn sync_directory(&self) -> Result<(), Status> {
        Self::sync_record_directories_at(
            &self.directory,
            self.preparation
                .as_ref()
                .map(|pool| pool.directory.as_path()),
        )
    }
    fn sync_record_directories_at(
        directory: &Path,
        preparation: Option<&Path>,
    ) -> Result<(), Status> {
        if let Some(source) = preparation {
            Self::sync_directory_at(source)?;
        }
        Self::sync_directory_at(directory)
    }
    fn sync_directory_at(directory: &Path) -> Result<(), Status> {
        File::open(directory)
            .and_then(|f| f.sync_all())
            .map_err(|_| Status::unavailable("RPC replay durability failed"))
    }
    fn persist_clock_at(directory: &Path, now: u64) -> Result<(), Status> {
        fs::write(directory.join("clock.tmp"), now.to_string())
            .map_err(|_| Status::unavailable("RPC replay clock unavailable"))?;
        File::open(directory.join("clock.tmp"))
            .and_then(|f| f.sync_all())
            .map_err(|_| Status::unavailable("RPC replay clock durability failed"))?;
        fs::rename(directory.join("clock.tmp"), directory.join("clock"))
            .map_err(|_| Status::unavailable("RPC replay clock unavailable"))?;
        Self::sync_directory_at(directory)
    }
    fn reserve(&mut self, now: u64, expiry: u64, nonce: &str) -> Result<ReplayReservation, Status> {
        if now < self.high_water {
            return Err(Status::unavailable("RPC clock moved backwards"));
        }
        if self.nonces.contains(nonce) {
            return Err(Status::unauthenticated("RPC replay rejected"));
        }
        if self.entries.len() >= MAX_REPLAYS {
            return Err(Status::resource_exhausted("RPC replay capacity exhausted"));
        }
        let path = self.directory.join(format!("{expiry}-{nonce}"));
        let storage_error = |error: std::io::Error| {
            if error.kind() == std::io::ErrorKind::AlreadyExists {
                Status::unauthenticated("RPC replay rejected")
            } else {
                Status::unavailable("RPC replay storage unavailable")
            }
        };
        let prepared = self
            .preparation
            .as_ref()
            .map(|pool| pool.take())
            .transpose()?
            .flatten();
        let file = if let Some(prepared) = prepared {
            match rename_prepared_inode(prepared.path(), &path) {
                Ok(()) => {
                    let (_file, mut source) = prepared.into_parts();
                    source.disable_cleanup(true);
                    ReplayReservation::Prepared
                }
                Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {
                    return Err(storage_error(error));
                }
                Err(error) => {
                    if let Some(pool) = &self.preparation {
                        pool.disable()?;
                    }
                    tracing::warn!(
                        error = %error,
                        "RPC preparation rename unavailable; using standalone inode durability"
                    );
                    ReplayReservation::Fresh(
                        OpenOptions::new()
                            .write(true)
                            .create_new(true)
                            .open(&path)
                            .map_err(storage_error)?,
                    )
                }
            }
        } else {
            ReplayReservation::Fresh(
                OpenOptions::new()
                    .write(true)
                    .create_new(true)
                    .open(&path)
                    .map_err(storage_error)?,
            )
        };
        self.entries.insert((expiry, nonce.to_owned()));
        self.nonces.insert(nonce.to_owned());
        self.pending.insert(nonce.to_owned());
        self.high_water = now;
        Ok(file)
    }
}
#[derive(Clone)]
pub struct RPCVerifier {
    secret: Arc<str>,
    previous: Option<Arc<str>>,
    replay: Arc<Mutex<ReplayState>>,
    directory_durability: Arc<ReplayDirectoryDurability>,
    verification_slots: Arc<tokio::sync::Semaphore>,
    verification_waiters: Arc<tokio::sync::Semaphore>,
    verification_wait_bytes: Arc<tokio::sync::Semaphore>,
}
impl RPCVerifier {
    pub fn new(secret: &str, previous: Option<&str>, data_root: &Path) -> Result<Self, Status> {
        if secret.is_empty() {
            return Err(Status::unavailable("RPC signing key required"));
        }
        Ok(Self {
            secret: Arc::from(secret),
            previous: previous.filter(|s| !s.is_empty()).map(Arc::from),
            replay: Arc::new(Mutex::new(ReplayState::open(
                data_root.join(".rpc-replay-v1"),
            )?)),
            directory_durability: Arc::new(ReplayDirectoryDurability::default()),
            verification_slots: Arc::new(tokio::sync::Semaphore::new(32)),
            verification_waiters: Arc::new(tokio::sync::Semaphore::new(
                VERIFICATION_WAITERS as usize,
            )),
            verification_wait_bytes: Arc::new(tokio::sync::Semaphore::new(
                VERIFICATION_WAIT_BYTES as usize,
            )),
        })
    }
    pub async fn verify<T: Message>(
        &self,
        metadata: &MetadataMap,
        method: &str,
        message: &T,
    ) -> Result<(), Status> {
        let started = Instant::now();
        let capacity_timer =
            crate::grpc::metrics::start_rpc_verification_phase(RPCVerificationPhase::CapacityWait);
        let permit = async {
            match self.verification_slots.clone().try_acquire_owned() {
                Ok(permit) => Ok(permit),
                Err(_) => {
                    let _waiting = self
                        .verification_waiters
                        .clone()
                        .try_acquire_owned()
                        .map_err(|_| {
                            Status::resource_exhausted("RPC verification queue exhausted")
                        })?;
                    let bytes = u32::try_from(message.encoded_len()).map_err(|_| {
                        Status::resource_exhausted(
                            "RPC verification request exceeds queue byte budget",
                        )
                    })?;
                    let _waiting_bytes = self
                        .verification_wait_bytes
                        .clone()
                        .try_acquire_many_owned(bytes.max(1))
                        .map_err(|_| {
                            Status::resource_exhausted(
                                "RPC verification queue byte budget exhausted",
                            )
                        })?;
                    tokio::time::timeout(
                        std::time::Duration::from_secs(1),
                        self.verification_slots.clone().acquire_owned(),
                    )
                    .await
                    .map_err(|_| {
                        Status::resource_exhausted("RPC verification wait budget exhausted")
                    })?
                    .map_err(|_| Status::unavailable("RPC verification unavailable"))
                }
            }
        }
        .await;
        let capacity_wait_ms = started.elapsed().as_secs_f64() * 1000.0;
        drop(capacity_timer);
        if let Err(error) = &permit {
            tracing::info!(
                capacity_wait_ms,
                error_code = error.code() as i32,
                error = error.message(),
                "RPC verification capacity timing"
            );
        }
        let permit = permit?;
        let verifier = self.clone();
        let metadata = metadata.clone();
        let method = method.to_owned();
        let body = message.encode_to_vec();
        let dispatched = Instant::now();
        crate::spawn_blocking(move || {
            let _permit = permit;
            let blocking_wait_ms = dispatched.elapsed().as_secs_f64() * 1000.0;
            record_rpc_verification_phase(
                RPCVerificationPhase::BlockingWait,
                blocking_wait_ms / 1000.0,
            );
            let result = verifier.verify_bytes(&metadata, &method, &body, None);
            tracing::info!(
                method,
                capacity_wait_ms,
                blocking_wait_ms,
                total_ms = started.elapsed().as_secs_f64() * 1000.0,
                success = result.is_ok(),
                error_code = result.as_ref().err().map(|error| error.code() as i32),
                error = result.as_ref().err().map(Status::message),
                "RPC verification dispatch timing"
            );
            result
        })
        .await
        .map_err(|_| Status::unavailable("RPC verification failed"))?
    }
    #[cfg(test)]
    fn verify_at<T: Message>(
        &self,
        metadata: &MetadataMap,
        method: &str,
        message: &T,
        now: u64,
    ) -> Result<(), Status> {
        self.verify_bytes(metadata, method, &message.encode_to_vec(), Some(now))
    }
    fn verify_bytes(
        &self,
        metadata: &MetadataMap,
        method: &str,
        body: &[u8],
        at: Option<u64>,
    ) -> Result<(), Status> {
        self.verify_bytes_with_clock_persistence(
            metadata,
            method,
            body,
            || at.map_or_else(now_millis, Ok),
            ReplayState::persist_clock_at,
        )
    }
    fn verify_bytes_with_clock_persistence(
        &self,
        metadata: &MetadataMap,
        method: &str,
        body: &[u8],
        clock: impl Fn() -> Result<u64, Status>,
        persist_clock: impl FnOnce(&Path, u64) -> Result<(), Status>,
    ) -> Result<(), Status> {
        // Read the clock inside the serialized reservation. Concurrent calls
        // may arrive out of order; that is not a wall-clock rollback.
        let waiting = Instant::now();
        let mut replay = self
            .replay
            .lock()
            .map_err(|_| Status::unavailable("RPC replay state unavailable"))?;
        let replay_lock_wait_ms = waiting.elapsed().as_secs_f64() * 1000.0;
        record_rpc_verification_phase(
            RPCVerificationPhase::ReplayLock,
            replay_lock_wait_ms / 1000.0,
        );
        let checking = Instant::now();
        let mut now = clock()?;
        let invalid = || Status::unauthenticated("invalid signed RPC envelope");
        if metadata.contains_key("authorization") {
            return Err(invalid());
        }
        let values: Vec<_> = metadata.get_all(SIGNATURE_HEADER).iter().collect();
        if values.len() != 1 {
            return Err(invalid());
        }
        let envelope = values[0].to_str().map_err(|_| invalid())?;
        if envelope.len() > 256 {
            return Err(invalid());
        }
        let parts: Vec<_> = envelope.split(':').collect();
        if parts.len() != 5 {
            return Err(invalid());
        }
        let issued: u64 = parts[0].parse().map_err(|_| invalid())?;
        let expiry: u64 = parts[1].parse().map_err(|_| invalid())?;
        if expiry <= issued
            || expiry - issued > 60_000
            || issued > now.saturating_add(5_000)
            || expiry <= now
        {
            return Err(invalid());
        }
        decode_hex(parts[2], 16)?;
        let expected = decode_hex(parts[3], 32)?;
        let signature = decode_hex(parts[4], 32)?;
        let payload = parts[..4].join(":");
        let matches = |key: &str| {
            mac(key, "gitstore-git", method, &payload)
                .verify_slice(&signature)
                .is_ok()
        };
        if !matches(&self.secret) && !self.previous.as_ref().is_some_and(|key| matches(key)) {
            return Err(invalid());
        }
        use subtle::ConstantTimeEq;
        if !bool::from(Sha256::digest(body).as_slice().ct_eq(&expected)) {
            return Err(invalid());
        }
        let envelope_check_ms = checking.elapsed().as_secs_f64() * 1000.0;
        record_rpc_verification_phase(
            RPCVerificationPhase::EnvelopeCheck,
            envelope_check_ms / 1000.0,
        );
        let reserving = Instant::now();
        if now < replay.high_water {
            return Err(Status::unavailable("RPC clock moved backwards"));
        }
        let mut replay_clock_ms = 0.0;
        let mut replay_cleanup_ms = 0.0;
        let mut replay_cleanup_records = 0;
        if !replay.maintenance_in_progress {
            let checkpoint = replay
                .entries
                .first()
                .is_some_and(|(expires, _)| *expires <= now)
                && now.saturating_sub(replay.durable_high_water)
                    >= REPLAY_CLEANUP_CLOCK_INTERVAL_MS;
            let floor = if checkpoint {
                now
            } else {
                replay.durable_high_water
            };
            let expired: Vec<_> = replay
                .entries
                .iter()
                .take_while(|(expires, _)| *expires <= floor)
                .take(256)
                .filter(|(_, nonce)| !replay.pending.contains(nonce))
                .cloned()
                .collect();
            if checkpoint || !expired.is_empty() {
                let directory = replay.directory.clone();
                replay.maintenance_in_progress = true;
                drop(replay);
                // One admitted verifier performs bounded maintenance. Peers can
                // persist fresh nonces, but no old record is forgotten before
                // its clock floor and unlink have both completed.
                let maintenance = (|| {
                    if checkpoint {
                        let started = Instant::now();
                        let result = persist_clock(&directory, now);
                        replay_clock_ms = started.elapsed().as_secs_f64() * 1000.0;
                        record_rpc_verification_phase(
                            RPCVerificationPhase::ReplayClock,
                            replay_clock_ms / 1000.0,
                        );
                        result?;
                        let mut replay = self
                            .replay
                            .lock()
                            .map_err(|_| Status::unavailable("RPC replay state unavailable"))?;
                        replay.high_water = replay.high_water.max(now);
                        replay.durable_high_water = now;
                    }
                    if !expired.is_empty() {
                        let started = Instant::now();
                        let result = (|| {
                            for (expires, nonce) in expired {
                                fs::remove_file(directory.join(format!("{expires}-{nonce}")))
                                    .map_err(|_| {
                                        Status::unavailable("RPC replay cleanup failed")
                                    })?;
                                let mut replay = self.replay.lock().map_err(|_| {
                                    Status::unavailable("RPC replay state unavailable")
                                })?;
                                replay.nonces.remove(&nonce);
                                replay.entries.remove(&(expires, nonce));
                                replay_cleanup_records += 1;
                            }
                            Ok::<_, Status>(())
                        })();
                        replay_cleanup_ms = started.elapsed().as_secs_f64() * 1000.0;
                        record_rpc_verification_phase(
                            RPCVerificationPhase::ReplayCleanup,
                            replay_cleanup_ms / 1000.0,
                        );
                        result?;
                    }
                    Ok::<_, Status>(())
                })();
                replay = self
                    .replay
                    .lock()
                    .map_err(|_| Status::unavailable("RPC replay state unavailable"))?;
                replay.maintenance_in_progress = false;
                maintenance?;
                // Peers may have advanced the clock during maintenance.
                now = clock()?;
                if issued > now.saturating_add(5_000) || expiry <= now {
                    return Err(invalid());
                }
            }
        }
        let file = replay.reserve(now, expiry, parts[2])?;
        let directory = replay.directory.clone();
        let prepared_inode = matches!(file, ReplayReservation::Prepared);
        let preparation_directory = replay
            .preparation
            .as_ref()
            .map(|pool| pool.directory.clone());
        let replay_reserve_ms = reserving.elapsed().as_secs_f64() * 1000.0;
        record_rpc_verification_phase(
            RPCVerificationPhase::ReplayReserve,
            replay_reserve_ms / 1000.0,
        );
        drop(replay);
        // Pending reservations cannot be reclaimed while their durable writes
        // drain, even if the request expires or its caller is cancelled.
        let persisting = Instant::now();
        let inode = file
            .sync_all()
            .map_err(|_| Status::unavailable("RPC replay durability failed"));
        let replay_inode_ms = persisting.elapsed().as_secs_f64() * 1000.0;
        record_rpc_verification_phase(RPCVerificationPhase::ReplayInode, replay_inode_ms / 1000.0);
        let mut replay_directory_ms = 0.0;
        let result = inode.and_then(|_| {
            let directory_started = Instant::now();
            let result = self.directory_durability.sync(|| {
                let started = Instant::now();
                // A prepared inode was synced before its exclusive rename.
                // Both directories must cover that move before effects.
                let result = ReplayState::sync_record_directories_at(
                    &directory,
                    preparation_directory.as_deref(),
                );
                record_rpc_verification_phase(
                    RPCVerificationPhase::DirectoryBarrier,
                    started.elapsed().as_secs_f64(),
                );
                result
            });
            replay_directory_ms = directory_started.elapsed().as_secs_f64() * 1000.0;
            record_rpc_verification_phase(
                RPCVerificationPhase::ReplayDirectory,
                replay_directory_ms / 1000.0,
            );
            result
        });
        let replay_persist_ms = persisting.elapsed().as_secs_f64() * 1000.0;
        record_rpc_verification_phase(
            RPCVerificationPhase::ReplayPersist,
            replay_persist_ms / 1000.0,
        );
        let completing = Instant::now();
        self.replay
            .lock()
            .map_err(|_| Status::unavailable("RPC replay state unavailable"))?
            .pending
            .remove(parts[2]);
        let replay_complete_wait_ms = completing.elapsed().as_secs_f64() * 1000.0;
        record_rpc_verification_phase(
            RPCVerificationPhase::ReplayComplete,
            replay_complete_wait_ms / 1000.0,
        );
        tracing::info!(
            method,
            prepared_inode,
            replay_lock_wait_ms,
            envelope_check_ms,
            replay_reserve_ms,
            replay_clock_ms,
            replay_cleanup_ms,
            replay_cleanup_records,
            replay_persist_ms,
            replay_inode_ms,
            replay_directory_ms,
            replay_complete_wait_ms,
            success = result.is_ok(),
            "RPC replay reservation timing"
        );
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::grpc::server::proto::CreateRepositoryRequest;

    #[cfg(unix)]
    #[test]
    fn replay_reservation_publishes_an_independent_prepared_inode() {
        use std::os::unix::fs::MetadataExt;

        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let pool = root.path().join(".rpc-replay-v1.prepared-v1");
        let prepared: HashSet<_> = fs::read_dir(&pool)
            .expect("durable empty inodes must be prepared before requests")
            .map(|entry| {
                let metadata = entry.unwrap().metadata().unwrap();
                assert!(metadata.is_file());
                assert_eq!(metadata.len(), 0);
                assert_eq!(metadata.nlink(), 1);
                (metadata.dev(), metadata.ino())
            })
            .collect();
        assert_eq!(prepared.len(), 64);
        let now = 1_700_000_000_000;
        let nonce = "a".repeat(32);
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            now,
            &nonce,
        )
        .unwrap();
        verifier
            .verify_at(request.metadata(), method, request.get_ref(), now)
            .unwrap();
        let record = root
            .path()
            .join(".rpc-replay-v1")
            .join(format!("{}-{nonce}", now + 60_000));
        let metadata = record.metadata().unwrap();
        assert!(prepared.contains(&(metadata.dev(), metadata.ino())));
        assert_eq!(metadata.nlink(), 1);
        assert_eq!(fs::read_dir(pool).unwrap().count(), 63);
    }

    #[test]
    fn preparation_failure_does_not_publish_unflushed_inodes() {
        use std::sync::atomic::{AtomicUsize, Ordering};

        let root = tempfile::tempdir().unwrap();
        let persisted = AtomicUsize::new(0);
        let failure = ReplayPreparationPool::prepare(root.path(), 4, |file| {
            if persisted.fetch_add(1, Ordering::SeqCst) == 1 {
                return Err(std::io::Error::other("injected preparation failure"));
            }
            file.sync_all()
        });
        assert!(failure.is_err());
        assert_eq!(persisted.load(Ordering::SeqCst), 2);
        assert_eq!(fs::read_dir(root.path()).unwrap().count(), 0);
    }

    #[test]
    fn prepared_publication_never_overwrites_an_unindexed_nonce_record() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let now = 1_700_000_000_000;
        let nonce = "a".repeat(32);
        let path = root
            .path()
            .join(".rpc-replay-v1")
            .join(format!("{}-{nonce}", now + 60_000));
        fs::write(&path, b"must not replace").unwrap();
        let mut replay = verifier.replay.lock().unwrap();
        assert_eq!(
            replay
                .reserve(now, now + 60_000, &nonce)
                .unwrap_err()
                .code(),
            tonic::Code::Unauthenticated
        );
        assert_eq!(fs::read(path).unwrap(), b"must not replace");
        assert!(replay.entries.is_empty());
        assert!(replay.pending.is_empty());
    }

    #[test]
    fn exhausted_preparation_uses_original_inode_durability() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let mut replay = verifier.replay.lock().unwrap();
        replay
            .preparation
            .as_ref()
            .unwrap()
            .state
            .lock()
            .unwrap()
            .ready
            .clear();
        let nonce = "a".repeat(32);
        let reservation = replay
            .reserve(1_700_000_000_000, 1_700_000_060_000, &nonce)
            .unwrap();
        assert!(matches!(reservation, ReplayReservation::Fresh(_)));
        reservation.sync_all().unwrap();
        replay.sync_directory().unwrap();
        assert!(replay.pending.remove(&nonce));
        let directory = replay.directory.clone();
        drop(replay);
        drop(verifier);
        assert!(ReplayState::open(directory)
            .unwrap()
            .nonces
            .contains(&nonce));
    }

    #[test]
    fn prepared_records_recover_without_the_preparation_pool() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let now = 1_700_000_000_000;
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            now,
            &"a".repeat(32),
        )
        .unwrap();
        verifier
            .verify_at(request.metadata(), method, request.get_ref(), now)
            .unwrap();
        drop(verifier);
        let pool = root.path().join(".rpc-replay-v1.prepared-v1");
        fs::remove_dir(&pool).unwrap();
        fs::write(pool, b"unavailable preparation directory").unwrap();
        let replacement = RPCVerifier::new("key", None, root.path()).unwrap();
        assert!(replacement.replay.lock().unwrap().preparation.is_none());
        assert_eq!(
            replacement
                .verify_at(request.metadata(), method, request.get_ref(), now)
                .unwrap_err()
                .code(),
            tonic::Code::Unauthenticated
        );
    }

    #[test]
    fn stale_preparation_is_bounded_and_does_not_become_nonce_history() {
        let root = tempfile::tempdir().unwrap();
        let pool = root.path().join(".rpc-replay-v1.prepared-v1");
        fs::create_dir(&pool).unwrap();
        let stale = pool.join("nonce-unfinished");
        File::create(&stale).unwrap().sync_all().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        assert!(!stale.exists());
        let replay = verifier.replay.lock().unwrap();
        assert!(replay.entries.is_empty());
        assert!(replay.nonces.is_empty());
        assert_eq!(
            replay
                .preparation
                .as_ref()
                .unwrap()
                .state
                .lock()
                .unwrap()
                .ready
                .len(),
            PREPARED_REPLAY_INODES
        );
        drop(replay);
        drop(verifier);
        for i in 0..PREPARED_REPLAY_INODES + 33 {
            File::create(pool.join(format!("nonce-{i}"))).unwrap();
        }
        let replacement = RPCVerifier::new("key", None, root.path()).unwrap();
        assert!(replacement.replay.lock().unwrap().preparation.is_none());
        assert_eq!(
            fs::read_dir(pool).unwrap().count(),
            PREPARED_REPLAY_INODES + 33
        );
    }

    #[test]
    fn prepared_publication_requires_source_directory_durability() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let mut replay = verifier.replay.lock().unwrap();
        let nonce = "a".repeat(32);
        let record = replay
            .reserve(1_700_000_000_000, 1_700_000_060_000, &nonce)
            .unwrap();
        assert!(matches!(record, ReplayReservation::Prepared));
        record.sync_all().unwrap();
        let source = replay.preparation.as_ref().unwrap().directory.clone();
        let backup = root.path().join("temporarily-unavailable-preparation");
        fs::rename(&source, &backup).unwrap();
        assert_eq!(
            replay.sync_directory().unwrap_err().code(),
            tonic::Code::Unavailable
        );
        assert!(replay.pending.contains(&nonce));
        assert!(replay.nonces.contains(&nonce));
        fs::rename(backup, source).unwrap();
        replay.sync_directory().unwrap();
        assert!(replay.pending.remove(&nonce));
    }

    #[test]
    fn preparation_rename_failure_disables_the_pool_and_retains_failed_nonces() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let now = 1_700_000_000_000;
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let source = root.path().join(".rpc-replay-v1.prepared-v1");
        let backup = root.path().join("unavailable-preparation");
        fs::rename(&source, &backup).unwrap();
        let request = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            now,
            &"a".repeat(32),
        )
        .unwrap();
        assert_eq!(
            verifier
                .verify_at(request.metadata(), method, request.get_ref(), now)
                .unwrap_err()
                .code(),
            tonic::Code::Unavailable,
        );
        fs::rename(backup, source).unwrap();
        {
            let replay = verifier.replay.lock().unwrap();
            assert!(replay.pending.is_empty());
            assert!(
                replay
                    .preparation
                    .as_ref()
                    .unwrap()
                    .state
                    .lock()
                    .unwrap()
                    .disabled
            );
        }
        assert_eq!(
            verifier
                .verify_at(request.metadata(), method, request.get_ref(), now)
                .unwrap_err()
                .code(),
            tonic::Code::Unauthenticated,
        );
        let next = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            now,
            &"b".repeat(32),
        )
        .unwrap();
        verifier
            .verify_at(next.metadata(), method, next.get_ref(), now)
            .unwrap();
    }

    #[test]
    #[ignore = "100,000-record filesystem capacity and restart case; run explicitly"]
    fn prepared_publication_preserves_the_full_retained_nonce_capacity() {
        let root = tempfile::tempdir().unwrap();
        let directory = root.path().join(".rpc-replay-v1");
        let mut replay = ReplayState::open(directory.clone()).unwrap();
        for nonce in 0..MAX_REPLAYS {
            replay
                .reserve(1, 60_001, &format!("{nonce:032x}"))
                .unwrap()
                .sync_all()
                .unwrap();
            if replay.pending.len() == 32 || nonce + 1 == MAX_REPLAYS {
                replay.sync_directory().unwrap();
                replay.pending.clear();
            }
        }
        assert_eq!(replay.entries.len(), MAX_REPLAYS);
        assert_eq!(fs::read_dir(&directory).unwrap().count(), MAX_REPLAYS);
        assert_eq!(
            replay
                .reserve(1, 60_001, &"f".repeat(32))
                .unwrap_err()
                .code(),
            tonic::Code::ResourceExhausted,
        );
        drop(replay);
        let mut recovered = ReplayState::open(directory).unwrap();
        assert_eq!(recovered.entries.len(), MAX_REPLAYS);
        assert_eq!(recovered.nonces.len(), MAX_REPLAYS);
        assert!(recovered.pending.is_empty());
        assert_eq!(
            recovered
                .reserve(1, 60_001, &"f".repeat(32))
                .unwrap_err()
                .code(),
            tonic::Code::ResourceExhausted,
        );
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn prepared_refill_stays_bounded_and_replacement_rejects_every_spent_nonce() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let now = now_millis().unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let mut tasks = tokio::task::JoinSet::new();
        for worker in 0..32 {
            let verifier = verifier.clone();
            tasks.spawn(async move {
                for offset in 0..4 {
                    let nonce = format!("{:032x}", worker * 4 + offset);
                    let request = signed_at(
                        CreateRepositoryRequest::default(),
                        "key",
                        "gitstore-git",
                        method,
                        now,
                        &nonce,
                    )
                    .unwrap();
                    verifier
                        .verify(request.metadata(), method, request.get_ref())
                        .await
                        .unwrap();
                    let replay = verifier.replay.lock().unwrap();
                    let pool = replay.preparation.as_ref().unwrap();
                    assert!(pool.state.lock().unwrap().ready.len() <= PREPARED_REPLAY_INODES);
                    assert!(
                        fs::read_dir(&pool.directory).unwrap().count()
                            <= PREPARED_REPLAY_INODES + 32
                    );
                }
            });
        }
        while let Some(result) = tasks.join_next().await {
            result.unwrap();
        }
        tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                let done = {
                    let replay = verifier.replay.lock().unwrap();
                    let state = replay.preparation.as_ref().unwrap().state.lock().unwrap();
                    !state.refilling && !state.ready.is_empty()
                };
                if done {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(5)).await;
            }
        })
        .await
        .unwrap();
        assert_eq!(verifier.replay.lock().unwrap().entries.len(), 128);
        drop(verifier);
        let replacement = RPCVerifier::new("key", None, root.path()).unwrap();
        for nonce in 0..128 {
            let request = signed_at(
                CreateRepositoryRequest::default(),
                "key",
                "gitstore-git",
                method,
                now,
                &format!("{nonce:032x}"),
            )
            .unwrap();
            assert_eq!(
                replacement
                    .verify_at(request.metadata(), method, request.get_ref(), now)
                    .unwrap_err()
                    .code(),
                tonic::Code::Unauthenticated,
            );
        }
    }

    #[test]
    fn clock_checkpoint_does_not_block_peer_durability_or_lower_the_clock_floor() {
        use std::sync::atomic::{AtomicU64, Ordering};
        use std::sync::mpsc;
        use std::time::Duration;

        struct Release(Option<mpsc::Sender<()>>);
        impl Drop for Release {
            fn drop(&mut self) {
                if let Some(sender) = self.0.take() {
                    let _ = sender.send(());
                }
            }
        }
        for (failed, persisted, raced) in [
            (false, true, false),
            (true, false, false),
            (true, true, false),
            (false, true, true),
        ] {
            let root = tempfile::tempdir().unwrap();
            let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
            let method = "/gitstore.git.v1.GitService/CreateRepository";
            let base = 1_700_000_000_000;
            let checkpoint = base + 60_001;
            let clock = AtomicU64::new(checkpoint);
            let requests: Vec<_> = [base, checkpoint, checkpoint + 1]
                .into_iter()
                .enumerate()
                .map(|(nonce, issued)| {
                    signed_at(
                        CreateRepositoryRequest::default(),
                        "key",
                        "gitstore-git",
                        method,
                        issued,
                        &format!("{nonce:032x}"),
                    )
                    .unwrap()
                })
                .collect();
            verifier
                .verify_at(requests[0].metadata(), method, requests[0].get_ref(), base)
                .unwrap();
            let (entered, entry) = mpsc::channel();
            let (release, wait) = mpsc::channel();
            let wait = Mutex::new(wait);
            let (finished, result) = mpsc::channel();
            std::thread::scope(|scope| {
                let owner = scope.spawn(|| {
                    verifier.verify_bytes_with_clock_persistence(
                        requests[1].metadata(),
                        method,
                        &requests[1].get_ref().encode_to_vec(),
                        || Ok(clock.load(Ordering::SeqCst)),
                        |directory, now| {
                            entered.send(now).unwrap();
                            wait.lock().unwrap().recv().unwrap();
                            if persisted {
                                ReplayState::persist_clock_at(directory, now)?;
                            }
                            if failed {
                                Err(Status::unavailable("injected clock persistence failure"))
                            } else {
                                Ok(())
                            }
                        },
                    )
                });
                let mut release = Release(Some(release));
                assert_eq!(
                    entry.recv_timeout(Duration::from_secs(5)).unwrap(),
                    checkpoint
                );
                clock.store(checkpoint + 1, Ordering::SeqCst);
                scope.spawn(|| {
                    finished
                        .send(verifier.verify_at(
                            requests[2].metadata(),
                            method,
                            requests[2].get_ref(),
                            checkpoint + 1,
                        ))
                        .unwrap();
                });
                result
                    .recv_timeout(Duration::from_secs(5))
                    .expect("a peer must finish while the clock checkpoint is blocked")
                    .unwrap();
                if raced {
                    verifier
                        .verify_at(
                            requests[1].metadata(),
                            method,
                            requests[1].get_ref(),
                            checkpoint + 1,
                        )
                        .unwrap();
                }
                {
                    let replay = verifier.replay.lock().unwrap();
                    assert_eq!(replay.high_water, checkpoint + 1);
                    assert_eq!(replay.durable_high_water, 0);
                    assert!(replay.nonces.contains(&format!("{:032x}", 0)));
                    assert!(replay
                        .directory
                        .join(format!("{}-{:032x}", base + 60_000, 0))
                        .exists());
                    assert!(replay.pending.is_empty());
                }
                release.0.take().unwrap().send(()).unwrap();
                let outcome = owner.join().unwrap();
                if failed {
                    assert_eq!(outcome.unwrap_err().code(), tonic::Code::Unavailable);
                    let replay = verifier.replay.lock().unwrap();
                    assert_eq!(replay.durable_high_water, 0);
                    assert!(replay.nonces.contains(&format!("{:032x}", 0)));
                    assert!(!replay.nonces.contains(&format!("{:032x}", 1)));
                } else {
                    if raced {
                        assert_eq!(outcome.unwrap_err().code(), tonic::Code::Unauthenticated);
                    } else {
                        outcome.unwrap();
                    }
                    let replay = verifier.replay.lock().unwrap();
                    assert_eq!(replay.durable_high_water, checkpoint);
                    assert_eq!(replay.high_water, checkpoint + 1);
                    assert!(!replay.nonces.contains(&format!("{:032x}", 0)));
                }
            });
            if failed {
                verifier
                    .verify_at(
                        requests[1].metadata(),
                        method,
                        requests[1].get_ref(),
                        checkpoint + 1,
                    )
                    .unwrap();
            }
            drop(verifier);
            let replacement = RPCVerifier::new("key", None, root.path()).unwrap();
            for request in &requests[1..] {
                assert_eq!(
                    replacement
                        .verify_at(
                            request.metadata(),
                            method,
                            request.get_ref(),
                            checkpoint + 1,
                        )
                        .unwrap_err()
                        .code(),
                    tonic::Code::Unauthenticated
                );
            }
            assert_eq!(
                replacement
                    .verify_at(requests[0].metadata(), method, requests[0].get_ref(), base)
                    .unwrap_err()
                    .code(),
                tonic::Code::Unavailable
            );
        }
    }

    #[test]
    fn envelope_expiring_during_clock_checkpoint_is_not_reserved() {
        use std::cell::Cell;

        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let base = 1_700_000_000_000;
        for (index, issued) in [base, base + 60_001].into_iter().enumerate() {
            let nonce = format!("{index:032x}");
            let request = signed_at(
                CreateRepositoryRequest::default(),
                "key",
                "gitstore-git",
                method,
                issued,
                &nonce,
            )
            .unwrap();
            if index == 0 {
                verifier
                    .verify_at(request.metadata(), method, request.get_ref(), issued)
                    .unwrap();
                continue;
            }
            let clock = Cell::new(issued);
            let outcome = verifier.verify_bytes_with_clock_persistence(
                request.metadata(),
                method,
                &request.get_ref().encode_to_vec(),
                || Ok(clock.get()),
                |directory, now| {
                    ReplayState::persist_clock_at(directory, now)?;
                    clock.set(issued + 60_000);
                    Ok(())
                },
            );
            assert_eq!(outcome.unwrap_err().code(), tonic::Code::Unauthenticated);
            let replay = verifier.replay.lock().unwrap();
            assert!(!replay.nonces.contains(&nonce));
            assert!(replay.pending.is_empty());
            assert!(!replay
                .directory
                .join(format!("{}-{nonce}", issued + 60_000))
                .exists());
        }
    }

    #[test]
    fn maintenance_preserves_the_cleanup_bound_and_pending_records() {
        use std::cell::Cell;

        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let base = 1_700_000_000_000;
        {
            let mut replay = verifier.replay.lock().unwrap();
            for nonce in 0..257 {
                let nonce = format!("{nonce:032x}");
                replay
                    .reserve(base, base + 1, &nonce)
                    .unwrap()
                    .sync_all()
                    .unwrap();
                if nonce != format!("{:032x}", 0) {
                    assert!(replay.pending.remove(&nonce));
                }
            }
            replay.sync_directory().unwrap();
        }
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let checkpoints = Cell::new(0);
        for (offset, nonce) in [(2, "f".repeat(32)), (3, "e".repeat(32))] {
            let request = signed_at(
                CreateRepositoryRequest::default(),
                "key",
                "gitstore-git",
                method,
                base + offset,
                &nonce,
            )
            .unwrap();
            verifier
                .verify_bytes_with_clock_persistence(
                    request.metadata(),
                    method,
                    &request.get_ref().encode_to_vec(),
                    || Ok(base + offset),
                    |directory, now| {
                        checkpoints.set(checkpoints.get() + 1);
                        ReplayState::persist_clock_at(directory, now)
                    },
                )
                .unwrap();
            let mut replay = verifier.replay.lock().unwrap();
            assert!(!replay.maintenance_in_progress);
            assert_eq!(checkpoints.get(), 1);
            assert_eq!(replay.durable_high_water, base + 2);
            if offset == 2 {
                assert_eq!(replay.entries.len(), 3);
                for remaining in [0, 256] {
                    let nonce = format!("{remaining:032x}");
                    assert!(replay.nonces.contains(&nonce));
                    assert!(replay
                        .directory
                        .join(format!("{}-{nonce}", base + 1))
                        .exists());
                }
                assert!(replay.pending.remove(&format!("{:032x}", 0)));
            } else {
                assert_eq!(replay.entries.len(), 2);
                assert!(replay.entries.iter().all(|(expiry, _)| *expiry > base + 2));
            }
        }
    }

    #[test]
    fn partial_cleanup_failure_retains_unremoved_nonces_and_allows_recovery() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let base = 1_700_000_000_000;
        let broken;
        {
            let mut replay = verifier.replay.lock().unwrap();
            for nonce in 0..3 {
                let nonce = format!("{nonce:032x}");
                replay
                    .reserve(base, base + 1, &nonce)
                    .unwrap()
                    .sync_all()
                    .unwrap();
                assert!(replay.pending.remove(&nonce));
            }
            replay.sync_directory().unwrap();
            broken = replay.directory.join(format!("{}-{:032x}", base + 1, 2));
        }
        fs::remove_file(&broken).unwrap();
        fs::create_dir(&broken).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            base + 2,
            &"f".repeat(32),
        )
        .unwrap();
        assert_eq!(
            verifier
                .verify_at(request.metadata(), method, request.get_ref(), base + 2)
                .unwrap_err()
                .code(),
            tonic::Code::Unavailable
        );
        {
            let replay = verifier.replay.lock().unwrap();
            assert!(!replay.maintenance_in_progress);
            assert_eq!(replay.durable_high_water, base + 2);
            assert_eq!(replay.entries.len(), 1);
            assert!(replay.nonces.contains(&format!("{:032x}", 2)));
            assert!(replay.pending.is_empty());
        }
        fs::remove_dir(&broken).unwrap();
        File::create(&broken).unwrap().sync_all().unwrap();
        verifier
            .verify_at(request.metadata(), method, request.get_ref(), base + 3)
            .unwrap();
        let replay = verifier.replay.lock().unwrap();
        assert!(!replay.maintenance_in_progress);
        assert_eq!(replay.entries.len(), 1);
        assert!(replay.nonces.contains(&"f".repeat(32)));
        assert!(!broken.exists());
        assert!(replay.pending.is_empty());
    }

    #[test]
    fn coalesced_replay_persistence_survives_replacement_for_all_distinct_nonces() {
        let root = tempfile::tempdir().unwrap();
        let verifier = Arc::new(RPCVerifier::new("key", None, root.path()).unwrap());
        let barrier = Arc::new(std::sync::Barrier::new(32));
        let now = 1_700_000_000_000;
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let requests: Vec<_> = (0..32)
            .map(|nonce| {
                signed_at(
                    CreateRepositoryRequest::default(),
                    "key",
                    "gitstore-git",
                    method,
                    now,
                    &format!("{nonce:032x}"),
                )
                .unwrap()
            })
            .collect();
        std::thread::scope(|scope| {
            for request in &requests {
                let verifier = &verifier;
                let barrier = &barrier;
                scope.spawn(move || {
                    barrier.wait();
                    verifier
                        .verify_at(request.metadata(), method, request.get_ref(), now)
                        .unwrap();
                });
            }
        });
        assert!(verifier.replay.lock().unwrap().pending.is_empty());
        drop(verifier);
        let replacement = RPCVerifier::new("key", None, root.path()).unwrap();
        for request in requests {
            assert_eq!(
                replacement
                    .verify_at(request.metadata(), method, request.get_ref(), now + 1)
                    .unwrap_err()
                    .code(),
                tonic::Code::Unauthenticated
            );
        }
    }

    #[test]
    fn directory_barrier_reporting_cannot_block_later_durability() {
        use std::sync::mpsc;
        use std::thread;
        use std::time::Duration;
        use tracing_subscriber::layer::SubscriberExt;

        struct PausedReporting {
            entered: mpsc::Sender<()>,
            release: Mutex<mpsc::Receiver<()>>,
        }
        impl<S: tracing::Subscriber> tracing_subscriber::Layer<S> for PausedReporting {
            fn on_event(
                &self,
                event: &tracing::Event<'_>,
                _: tracing_subscriber::layer::Context<'_, S>,
            ) {
                if event.metadata().fields().field("requests").is_some() {
                    self.entered.send(()).unwrap();
                    self.release
                        .lock()
                        .unwrap()
                        .recv_timeout(Duration::from_secs(5))
                        .unwrap();
                }
            }
        }
        for failed in [false, true] {
            let durability = Arc::new(ReplayDirectoryDurability::default());
            let (entered, reporting) = mpsc::channel();
            let (release, resumed) = mpsc::channel();
            let (completed, completion) = mpsc::channel();
            thread::scope(|scope| {
                let first = durability.clone();
                let owner = scope.spawn(move || {
                    let subscriber = tracing_subscriber::registry().with(PausedReporting {
                        entered,
                        release: Mutex::new(resumed),
                    });
                    tracing::subscriber::with_default(subscriber, || {
                        first.sync(|| {
                            if failed {
                                Err(Status::unavailable("injected directory durability failure"))
                            } else {
                                Ok(())
                            }
                        })
                    })
                });
                reporting.recv_timeout(Duration::from_secs(5)).unwrap();
                let peer = scope.spawn(|| {
                    completed.send(durability.sync(|| Ok(()))).unwrap();
                });
                let while_reporting = completion.recv_timeout(Duration::from_secs(1));
                release.send(()).unwrap();
                let owner_result = owner.join().unwrap();
                peer.join().unwrap();
                assert!(
                    while_reporting.is_ok(),
                    "a completed directory barrier must release the shared gate before reporting"
                );
                assert!(while_reporting.unwrap().is_ok());
                if failed {
                    assert_eq!(owner_result.unwrap_err().code(), tonic::Code::Unavailable);
                } else {
                    assert!(owner_result.is_ok());
                }
            });
            let state = durability.state.lock().unwrap();
            assert!(state.pending.is_empty() && state.outcomes.is_empty());
            assert!(!state.syncing);
            assert_eq!(state.in_flight, 0);
        }
    }

    #[test]
    fn directory_barriers_coalesce_ready_peers_bound_waiters_and_preserve_failures() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        use std::sync::mpsc;
        use std::thread;
        use std::time::Duration;

        struct Release(Option<mpsc::Sender<()>>);
        impl Drop for Release {
            fn drop(&mut self) {
                if let Some(sender) = self.0.take() {
                    let _ = sender.send(());
                }
            }
        }
        for failed in [false, true] {
            let durability = Arc::new(ReplayDirectoryDurability::default());
            let calls = Arc::new(AtomicUsize::new(0));
            let (entered, entries) = mpsc::channel();
            let (first_release, first_wait) = mpsc::channel();
            let (second_release, second_wait) = mpsc::channel();
            let first_wait = Mutex::new(first_wait);
            let second_wait = Mutex::new(second_wait);
            let persist = || {
                let call = calls.fetch_add(1, Ordering::SeqCst);
                entered.send(call).unwrap();
                match call {
                    0 => first_wait.lock().unwrap().recv().unwrap(),
                    1 => second_wait.lock().unwrap().recv().unwrap(),
                    2 => (),
                    _ => panic!("unexpected directory barrier"),
                }
                if call == 1 && failed {
                    Err(Status::unavailable("injected directory durability failure"))
                } else {
                    Ok(())
                }
            };
            thread::scope(|scope| {
                let first = scope.spawn(|| durability.sync(persist));
                let mut first_guard = Release(Some(first_release));
                let mut second_guard = Release(Some(second_release));
                assert_eq!(entries.recv_timeout(Duration::from_secs(5)).unwrap(), 0);
                let (finished, results) = mpsc::channel();
                for _ in 0..31 {
                    let finished = finished.clone();
                    let durability = &durability;
                    let persist = &persist;
                    scope.spawn(move || {
                        finished.send(durability.sync(persist)).unwrap();
                    });
                }
                let deadline = Instant::now() + Duration::from_secs(5);
                while durability.state.lock().unwrap().pending.len() != 31 {
                    assert!(
                        Instant::now() < deadline,
                        "directory waiters did not register"
                    );
                    thread::sleep(Duration::from_millis(1));
                }
                assert_eq!(
                    durability
                        .sync(|| panic!("overflow cannot persist"))
                        .unwrap_err()
                        .code(),
                    tonic::Code::ResourceExhausted
                );
                assert!(results.try_recv().is_err());
                first_guard.0.take().unwrap().send(()).unwrap();
                assert_eq!(entries.recv_timeout(Duration::from_secs(5)).unwrap(), 1);
                assert!(first.join().unwrap().is_ok());
                assert!(
                    results.try_recv().is_err(),
                    "late entries require their own completed barrier"
                );
                let (late_finished, late_result) = mpsc::channel();
                let late_durability = &durability;
                let late_persist = &persist;
                scope.spawn(move || {
                    late_finished
                        .send(late_durability.sync(late_persist))
                        .unwrap();
                });
                let deadline = Instant::now() + Duration::from_secs(5);
                while durability.state.lock().unwrap().pending.len() != 1 {
                    assert!(
                        Instant::now() < deadline,
                        "late directory waiter did not register"
                    );
                    thread::sleep(Duration::from_millis(1));
                }
                assert!(entries.try_recv().is_err());
                assert!(late_result.try_recv().is_err());
                second_guard.0.take().unwrap().send(()).unwrap();
                for _ in 0..31 {
                    let outcome = results.recv_timeout(Duration::from_secs(5)).unwrap();
                    if failed {
                        assert_eq!(outcome.unwrap_err().code(), tonic::Code::Unavailable);
                    } else {
                        assert!(outcome.is_ok());
                    }
                }
                assert_eq!(entries.recv_timeout(Duration::from_secs(5)).unwrap(), 2);
                assert!(late_result
                    .recv_timeout(Duration::from_secs(5))
                    .unwrap()
                    .is_ok());
            });
            assert_eq!(calls.load(Ordering::SeqCst), 3);
            let state = durability.state.lock().unwrap();
            assert!(state.pending.is_empty() && state.outcomes.is_empty());
            assert_eq!(state.in_flight, 0);
            assert!(!state.syncing);
            drop(state);
            assert!(
                durability.sync(|| Ok(())).is_ok(),
                "a failed batch cannot poison later barriers"
            );
        }
    }

    #[test]
    fn cleanup_reuses_only_a_durable_clock_floor_without_relaxing_rollback_checks() {
        let root = tempfile::tempdir().unwrap();
        let now = 1_700_000_000_000;
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let reserve = |at: u64, expiry: u64, nonce: &str| {
            let message = CreateRepositoryRequest::default();
            let digest = hex(&Sha256::digest(message.encode_to_vec()));
            let payload = format!("{at}:{expiry}:{nonce}:{digest}");
            let signature = hex(&mac("key", "gitstore-git", method, &payload)
                .finalize()
                .into_bytes());
            let mut request = Request::new(message);
            request.metadata_mut().insert(
                SIGNATURE_HEADER,
                format!("{payload}:{signature}").parse().unwrap(),
            );
            verifier.verify_at(request.metadata(), method, request.get_ref(), at)
        };
        let a = "a".repeat(32);
        let b = "b".repeat(32);
        for (at, expiry, nonce) in [(now, now + 1, &a), (now + 2, now + 3, &b)] {
            reserve(at, expiry, nonce).unwrap();
        }
        let state = verifier.replay.lock().unwrap();
        assert_eq!(state.durable_high_water, now + 2);
        assert!(!state.nonces.contains(&a));
        drop(state);
        reserve(now + 4, now + 60_000, &"c".repeat(32)).unwrap();
        let state = verifier.replay.lock().unwrap();
        assert!(state.pending.is_empty());
        assert_eq!(state.durable_high_water, now + 2);
        assert!(state.nonces.contains(&b));
        drop(state);
        assert_eq!(
            reserve(now + 3, now + 60_000, &"d".repeat(32))
                .unwrap_err()
                .code(),
            tonic::Code::Unavailable
        );
        let replacement =
            ReplayState::open(verifier.replay.lock().unwrap().directory.clone()).unwrap();
        assert!(replacement.nonces.contains(&b));
        assert_eq!(replacement.high_water, now + 2);
        reserve(now + 252, now + 60_000, &"e".repeat(32)).unwrap();
        let state = verifier.replay.lock().unwrap();
        assert!(state.pending.is_empty());
        assert_eq!(state.durable_high_water, now + 252);
        assert!(!state.nonces.contains(&b));
    }

    #[test]
    fn pending_nonce_excludes_replay_and_expiry_cleanup_until_persistence_drains() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let now = 1_700_000_000_000;
        let nonce = "a".repeat(32);
        let file = verifier
            .replay
            .lock()
            .unwrap()
            .reserve(now, now + 60_000, &nonce)
            .unwrap();
        let duplicate = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            now,
            &nonce,
        )
        .unwrap();
        assert_eq!(
            verifier
                .verify_at(duplicate.metadata(), method, duplicate.get_ref(), now)
                .unwrap_err()
                .code(),
            tonic::Code::Unauthenticated
        );
        for (offset, next_nonce) in [(1, "b".repeat(32)), (60_001, "c".repeat(32))] {
            let next = signed_at(
                CreateRepositoryRequest::default(),
                "key",
                "gitstore-git",
                method,
                now + offset,
                &next_nonce,
            )
            .unwrap();
            verifier
                .verify_at(next.metadata(), method, next.get_ref(), now + offset)
                .unwrap();
            let replay = verifier.replay.lock().unwrap();
            assert!(replay.pending.contains(&nonce));
            assert!(replay.nonces.contains(&nonce));
            assert!(replay
                .directory
                .join(format!("{}-{nonce}", now + 60_000))
                .exists());
        }
        file.sync_all().unwrap();
        {
            let mut replay = verifier.replay.lock().unwrap();
            replay.sync_directory().unwrap();
            assert!(replay.pending.remove(&nonce));
        }
        let next = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            now + 60_002,
            &"d".repeat(32),
        )
        .unwrap();
        verifier
            .verify_at(next.metadata(), method, next.get_ref(), now + 60_002)
            .unwrap();
        assert!(!verifier.replay.lock().unwrap().nonces.contains(&nonce));
        drop(verifier);
        let replacement = RPCVerifier::new("key", None, root.path()).unwrap();
        assert_eq!(
            replacement
                .verify_at(duplicate.metadata(), method, duplicate.get_ref(), now)
                .unwrap_err()
                .code(),
            tonic::Code::Unavailable
        );
    }

    #[tokio::test]
    async fn dispatched_verification_drains_after_caller_cancellation() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = sign_request(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
        )
        .unwrap();
        {
            let (locked_tx, locked_rx) = std::sync::mpsc::channel();
            let (release_tx, release_rx) = std::sync::mpsc::channel();
            let replay = verifier.replay.clone();
            let holder = std::thread::spawn(move || {
                let replay = replay.lock().unwrap();
                locked_tx.send(()).unwrap();
                release_rx.recv().unwrap();
                assert!(replay.nonces.is_empty());
            });
            locked_rx.recv().unwrap();
            let worker = {
                let verifier = verifier.clone();
                let metadata = request.metadata().clone();
                let body = request.get_ref().clone();
                tokio::spawn(async move { verifier.verify(&metadata, method, &body).await })
            };
            tokio::time::timeout(std::time::Duration::from_secs(1), async {
                while verifier.verification_slots.available_permits() == 32 {
                    tokio::task::yield_now().await;
                }
            })
            .await
            .unwrap();
            worker.abort();
            assert!(worker.await.unwrap_err().is_cancelled());
            release_tx.send(()).unwrap();
            holder.join().unwrap();
        }
        tokio::time::timeout(std::time::Duration::from_secs(5), async {
            while verifier.verification_slots.available_permits() != 32 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        {
            let replay = verifier.replay.lock().unwrap();
            assert_eq!(replay.nonces.len(), 1);
            assert!(replay.pending.is_empty());
        }
        let replacement = RPCVerifier::new("key", None, root.path()).unwrap();
        assert_eq!(
            replacement
                .verify(request.metadata(), method, request.get_ref())
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Unauthenticated
        );
    }

    #[test]
    fn go_rust_signed_wire_golden() {
        use crate::grpc::server::proto::{
            AuthContext, InfoRefsRequest, RequestAuthorization, Service,
        };
        let message = InfoRefsRequest {
            repository_id: "repository".into(),
            service: Service::GitUploadPack as i32,
            authorization: Some(RequestAuthorization {
                actor: Some(AuthContext {
                    subject: "alice".into(),
                    auth_method: "static-users".into(),
                    groups: vec!["system:authenticated".into()],
                    ..Default::default()
                }),
                action: "repository.contents.read".into(),
                resource_kind: "repository".into(),
                repository_id: "repository".into(),
                ..Default::default()
            }),
        };
        let request = signed_at(
            message,
            "shared-test-key",
            "gitstore-git",
            "/gitstore.git.v1.GitService/InfoRefs",
            1_700_000_000_000,
            &"a".repeat(32),
        )
        .unwrap();
        assert_eq!(request.metadata().get(SIGNATURE_HEADER).unwrap(), "1700000000000:1700000060000:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:4d4460c75ee3464f10c2597296caab56c7335b9f68f1275d8f90abab79485b26:072605f8daa33d0e3cb6aa7012742936c852b3d2b3495f689e2da3e07b370a76");
    }

    #[test]
    fn rotation_and_clock_rollback_cannot_reopen_spent_envelopes() {
        let root = tempfile::tempdir().unwrap();
        let now = 1_700_000_000_000;
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = |key, at, nonce: &str| {
            signed_at(
                CreateRepositoryRequest::default(),
                key,
                "gitstore-git",
                method,
                at,
                nonce,
            )
            .unwrap()
        };
        let verifier = RPCVerifier::new("new", Some("old"), root.path()).unwrap();
        let old = request("old", now, &"a".repeat(32));
        verifier
            .verify_at(old.metadata(), method, old.get_ref(), now)
            .unwrap();
        let reused_nonce = request("new", now + 1, &"a".repeat(32));
        assert!(verifier
            .verify_at(
                reused_nonce.metadata(),
                method,
                reused_nonce.get_ref(),
                now + 1
            )
            .is_err());
        let next = request("new", now + 61_000, &"b".repeat(32));
        verifier
            .verify_at(next.metadata(), method, next.get_ref(), now + 61_000)
            .unwrap();
        drop(verifier);
        let replacement = RPCVerifier::new("new", None, root.path()).unwrap();
        let retired_key = request("old", now + 61_000, &"c".repeat(32));
        assert!(replacement
            .verify_at(
                retired_key.metadata(),
                method,
                retired_key.get_ref(),
                now + 61_000
            )
            .is_err());
        let rolled_back = request("new", now, &"a".repeat(32));
        assert_eq!(
            replacement
                .verify_at(rolled_back.metadata(), method, rolled_back.get_ref(), now)
                .unwrap_err()
                .code(),
            tonic::Code::Unavailable
        );
        assert!(RPCVerifier::new("", Some("old"), root.path()).is_err());
    }

    #[tokio::test]
    async fn concurrent_replay_executes_once_and_verification_queue_is_bounded() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = sign_request(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
        )
        .unwrap();
        let mut workers = Vec::new();
        for _ in 0..16 {
            let verifier = verifier.clone();
            let md = request.metadata().clone();
            let body = request.get_ref().clone();
            workers.push(tokio::spawn(async move {
                verifier.verify(&md, method, &body).await.is_ok()
            }));
        }
        let mut allowed = 0;
        for worker in workers {
            if worker.await.unwrap() {
                allowed += 1;
            }
        }
        assert_eq!(allowed, 1);
        let _permit = verifier.verification_slots.acquire_many(32).await.unwrap();
        assert_eq!(
            verifier
                .verify(request.metadata(), method, request.get_ref())
                .await
                .unwrap_err()
                .code(),
            tonic::Code::ResourceExhausted
        );
    }

    #[tokio::test]
    #[tracing_test::traced_test]
    async fn verification_timeout_reports_capacity_without_consuming_the_request() {
        let measurements = || {
            prometheus::gather()
                .into_iter()
                .find(|family| family.name() == "gitstore_git_rpc_verification_phase_seconds")
                .and_then(|family| {
                    family
                        .get_metric()
                        .iter()
                        .find(|metric| {
                            metric.get_label().iter().any(|label| {
                                label.name() == "phase" && label.value() == "capacity_wait"
                            })
                        })
                        .map(|metric| {
                            let histogram = metric.get_histogram();
                            (histogram.get_sample_count(), histogram.get_sample_sum())
                        })
                })
                .unwrap_or_default()
        };
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = sign_request(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
        )
        .unwrap();
        let permit = verifier.verification_slots.acquire_many(32).await.unwrap();
        let before = measurements();
        let started = Instant::now();
        let error = verifier
            .verify(request.metadata(), method, request.get_ref())
            .await
            .unwrap_err();
        assert_eq!(error.code(), tonic::Code::ResourceExhausted);
        assert_eq!(error.message(), "RPC verification wait budget exhausted");
        assert!(started.elapsed() >= std::time::Duration::from_secs(1));
        let after = measurements();
        assert!(after.0 > before.0);
        assert!(after.1 - before.1 >= 1.0);
        assert!(logs_contain("RPC verification capacity timing"));
        assert!(logs_contain("capacity_wait_ms="));
        assert!(logs_contain("error_code=8"));
        assert!(logs_contain("RPC verification wait budget exhausted"));
        assert!(!logs_contain(SIGNATURE_HEADER));
        assert!(verifier.replay.lock().unwrap().nonces.is_empty());
        assert_eq!(
            verifier.verification_waiters.available_permits(),
            VERIFICATION_WAITERS as usize
        );
        assert_eq!(
            verifier.verification_wait_bytes.available_permits(),
            VERIFICATION_WAIT_BYTES as usize
        );
        drop(permit);
        assert_eq!(verifier.verification_slots.available_permits(), 32);
        verifier
            .verify(request.metadata(), method, request.get_ref())
            .await
            .unwrap();
        assert_eq!(
            verifier
                .verify(request.metadata(), method, request.get_ref())
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Unauthenticated
        );
    }

    #[tokio::test]
    async fn verification_waits_for_capacity_without_consuming_canceled_requests() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = sign_request(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
        )
        .unwrap();
        let permit = verifier.verification_slots.acquire_many(32).await.unwrap();
        let worker = {
            let verifier = verifier.clone();
            let metadata = request.metadata().clone();
            let body = request.get_ref().clone();
            tokio::spawn(async move { verifier.verify(&metadata, method, &body).await })
        };
        tokio::time::timeout(std::time::Duration::from_secs(1), async {
            while verifier.verification_waiters.available_permits() == VERIFICATION_WAITERS as usize
            {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        worker.abort();
        assert!(worker.await.unwrap_err().is_cancelled());
        assert_eq!(
            verifier.verification_waiters.available_permits(),
            VERIFICATION_WAITERS as usize
        );
        assert_eq!(
            verifier.verification_wait_bytes.available_permits(),
            VERIFICATION_WAIT_BYTES as usize
        );
        assert!(verifier.replay.lock().unwrap().nonces.is_empty());
        drop(permit);
        verifier
            .verify(request.metadata(), method, request.get_ref())
            .await
            .unwrap();
        assert_eq!(
            verifier
                .verify(request.metadata(), method, request.get_ref())
                .await
                .unwrap_err()
                .code(),
            tonic::Code::Unauthenticated
        );
    }

    #[tokio::test]
    async fn verification_queue_releases_and_rejects_excess_waiters() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = sign_request(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
        )
        .unwrap();
        let permit = verifier.verification_slots.acquire_many(32).await.unwrap();
        let waiting = verifier
            .verification_waiters
            .acquire_many(VERIFICATION_WAITERS)
            .await
            .unwrap();
        assert_eq!(
            verifier
                .verify(request.metadata(), method, request.get_ref())
                .await
                .unwrap_err()
                .code(),
            tonic::Code::ResourceExhausted
        );
        assert!(verifier.replay.lock().unwrap().nonces.is_empty());
        drop(waiting);
        let waiting_bytes = verifier
            .verification_wait_bytes
            .acquire_many(VERIFICATION_WAIT_BYTES)
            .await
            .unwrap();
        assert_eq!(
            verifier
                .verify(request.metadata(), method, request.get_ref())
                .await
                .unwrap_err()
                .code(),
            tonic::Code::ResourceExhausted
        );
        assert!(verifier.replay.lock().unwrap().nonces.is_empty());
        drop(waiting_bytes);
        let worker = {
            let verifier = verifier.clone();
            let metadata = request.metadata().clone();
            let body = request.get_ref().clone();
            tokio::spawn(async move { verifier.verify(&metadata, method, &body).await })
        };
        tokio::time::timeout(std::time::Duration::from_secs(1), async {
            while verifier.verification_waiters.available_permits() == VERIFICATION_WAITERS as usize
            {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        drop(permit);
        worker.await.unwrap().unwrap();
        assert_eq!(
            verifier.verification_waiters.available_permits(),
            VERIFICATION_WAITERS as usize
        );
        assert_eq!(
            verifier.verification_wait_bytes.available_permits(),
            VERIFICATION_WAIT_BYTES as usize
        );
        assert_eq!(verifier.verification_slots.available_permits(), 32);
    }

    #[tokio::test]
    async fn verification_rechecks_expiry_after_waiting() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let request = signed_at(
            CreateRepositoryRequest::default(),
            "key",
            "gitstore-git",
            method,
            now_millis().unwrap() - 59_900,
            &"a".repeat(32),
        )
        .unwrap();
        let permit = verifier.verification_slots.acquire_many(32).await.unwrap();
        let worker = {
            let verifier = verifier.clone();
            let metadata = request.metadata().clone();
            let body = request.get_ref().clone();
            tokio::spawn(async move { verifier.verify(&metadata, method, &body).await })
        };
        tokio::time::sleep(std::time::Duration::from_millis(200)).await;
        drop(permit);
        assert_eq!(
            worker.await.unwrap().unwrap_err().code(),
            tonic::Code::Unauthenticated
        );
        assert!(verifier.replay.lock().unwrap().nonces.is_empty());
        assert_eq!(
            verifier.verification_waiters.available_permits(),
            VERIFICATION_WAITERS as usize
        );
        assert_eq!(
            verifier.verification_wait_bytes.available_permits(),
            VERIFICATION_WAIT_BYTES as usize
        );
        assert_eq!(verifier.verification_slots.available_permits(), 32);
    }

    #[test]
    fn replay_capacity_and_storage_failure_fail_closed() {
        let root = tempfile::tempdir().unwrap();
        let directory = root.path().join("replay");
        let mut state = ReplayState::open(directory.clone()).unwrap();
        for i in 0..MAX_REPLAYS {
            state.entries.insert((100_000, format!("{i:032x}")));
        }
        assert_eq!(
            state
                .reserve(1, 100_000, &"f".repeat(32))
                .unwrap_err()
                .code(),
            tonic::Code::ResourceExhausted
        );
        state.entries.clear();
        fs::remove_dir(&directory).unwrap();
        assert_eq!(
            state
                .reserve(1, 100_000, &"f".repeat(32))
                .unwrap_err()
                .code(),
            tonic::Code::Unavailable
        );
    }

    #[test]
    fn signed_requests_reject_tampering_replay_and_legacy_bearer() {
        let root = tempfile::tempdir().unwrap();
        let verifier = RPCVerifier::new("test-key", None, root.path()).unwrap();
        let method = "/gitstore.git.v1.GitService/CreateRepository";
        let now = 1_700_000_000_000;
        let request = signed_at(
            CreateRepositoryRequest {
                repository_id: "repo".into(),
                ..Default::default()
            },
            "test-key",
            "gitstore-git",
            method,
            now,
            &"a".repeat(32),
        )
        .unwrap();
        let mut legacy = MetadataMap::new();
        legacy.insert("authorization", "Bearer test-key".parse().unwrap());
        assert!(verifier
            .verify_at(&legacy, method, request.get_ref(), now)
            .is_err());
        assert!(verifier
            .verify_at(request.metadata(), "/wrong", request.get_ref(), now)
            .is_err());
        let mut tampered = request.get_ref().clone();
        tampered.repository_id = "another".into();
        assert!(verifier
            .verify_at(request.metadata(), method, &tampered, now)
            .is_err());
        assert!(verifier
            .verify_at(request.metadata(), method, request.get_ref(), now + 60_000)
            .is_err());
        verifier
            .verify_at(request.metadata(), method, request.get_ref(), now)
            .unwrap();
        assert!(verifier
            .verify_at(request.metadata(), method, request.get_ref(), now)
            .is_err());
        drop(verifier);
        let replacement = RPCVerifier::new("new-key", Some("test-key"), root.path()).unwrap();
        assert!(
            replacement
                .verify_at(request.metadata(), method, request.get_ref(), now)
                .is_err(),
            "replacement must retain replay state even during rotation"
        );
    }
}
