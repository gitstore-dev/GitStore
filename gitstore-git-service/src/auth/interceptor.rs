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
    time::{Instant, SystemTime, UNIX_EPOCH},
};
use tonic::{metadata::MetadataMap, Request, Status};

pub const SIGNATURE_HEADER: &str = "x-gitstore-rpc";
const MAX_REPLAYS: usize = 100_000;
const REPLAY_CLEANUP_CLOCK_INTERVAL_MS: u64 = 250;
const VERIFICATION_WAITERS: u32 = 256;
const VERIFICATION_WAIT_BYTES: u32 = 128 * 1024 * 1024;

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
    entries: BTreeSet<(u64, String)>,
    nonces: HashSet<String>,
    pending: HashSet<String>,
    high_water: u64,
    durable_high_water: u64,
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
            tracing::info!(
                requests = ready.len(),
                duration_ms = started.elapsed().as_secs_f64() * 1000.0,
                success = outcome.is_ok(),
                "RPC replay directory barrier timing"
            );
            state = self.state.lock().map_err(|_| unavailable())?;
            for ticket in ready {
                state.outcomes.insert(ticket, outcome.clone());
            }
            state.syncing = false;
            state.in_flight = 0;
            self.changed.notify_all();
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
            directory,
            entries: BTreeSet::new(),
            nonces: HashSet::new(),
            pending: HashSet::new(),
            high_water,
            durable_high_water: high_water,
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
    fn sync_directory(&self) -> Result<(), Status> {
        Self::sync_directory_at(&self.directory)
    }
    fn sync_directory_at(directory: &Path) -> Result<(), Status> {
        File::open(directory)
            .and_then(|f| f.sync_all())
            .map_err(|_| Status::unavailable("RPC replay durability failed"))
    }
    fn reserve(&mut self, now: u64, expiry: u64, nonce: &str) -> Result<File, Status> {
        if now < self.high_water {
            return Err(Status::unavailable("RPC clock moved backwards"));
        }
        // Persist a high-water mark BEFORE deleting expired records. Otherwise
        // a wall-clock rollback after restart could resurrect spent envelopes.
        if self
            .entries
            .first()
            .is_some_and(|(expires, _)| *expires <= now)
            && now.saturating_sub(self.durable_high_water) >= REPLAY_CLEANUP_CLOCK_INTERVAL_MS
        {
            fs::write(self.directory.join("clock.tmp"), now.to_string())
                .map_err(|_| Status::unavailable("RPC replay clock unavailable"))?;
            File::open(self.directory.join("clock.tmp"))
                .and_then(|f| f.sync_all())
                .map_err(|_| Status::unavailable("RPC replay clock durability failed"))?;
            fs::rename(
                self.directory.join("clock.tmp"),
                self.directory.join("clock"),
            )
            .map_err(|_| Status::unavailable("RPC replay clock unavailable"))?;
            self.sync_directory()?;
            self.high_water = now;
            self.durable_high_water = now;
        }
        if self
            .entries
            .first()
            .is_some_and(|(expires, _)| *expires <= self.durable_high_water)
        {
            // Bound cleanup work on each request; saturation is fail-closed.
            let expired: Vec<_> = self
                .entries
                .iter()
                .take_while(|(expires, _)| *expires <= self.durable_high_water)
                .take(256)
                .filter(|(_, nonce)| !self.pending.contains(nonce))
                .cloned()
                .collect();
            for (expires, nonce) in expired {
                fs::remove_file(self.directory.join(format!("{expires}-{nonce}")))
                    .map_err(|_| Status::unavailable("RPC replay cleanup failed"))?;
                self.nonces.remove(&nonce);
                self.entries.remove(&(expires, nonce));
            }
        }
        if self.nonces.contains(nonce) {
            return Err(Status::unauthenticated("RPC replay rejected"));
        }
        if self.entries.len() >= MAX_REPLAYS {
            return Err(Status::resource_exhausted("RPC replay capacity exhausted"));
        }
        let path = self.directory.join(format!("{expiry}-{nonce}"));
        let file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(path)
            .map_err(|error| {
                if error.kind() == std::io::ErrorKind::AlreadyExists {
                    Status::unauthenticated("RPC replay rejected")
                } else {
                    Status::unavailable("RPC replay storage unavailable")
                }
            })?;
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
        let permit = match self.verification_slots.clone().try_acquire_owned() {
            Ok(permit) => permit,
            Err(_) => {
                let _waiting = self
                    .verification_waiters
                    .clone()
                    .try_acquire_owned()
                    .map_err(|_| Status::resource_exhausted("RPC verification queue exhausted"))?;
                let bytes = u32::try_from(message.encoded_len()).map_err(|_| {
                    Status::resource_exhausted("RPC verification request exceeds queue byte budget")
                })?;
                let _waiting_bytes = self
                    .verification_wait_bytes
                    .clone()
                    .try_acquire_many_owned(bytes.max(1))
                    .map_err(|_| {
                        Status::resource_exhausted("RPC verification queue byte budget exhausted")
                    })?;
                tokio::time::timeout(
                    std::time::Duration::from_secs(1),
                    self.verification_slots.clone().acquire_owned(),
                )
                .await
                .map_err(|_| Status::resource_exhausted("RPC verification wait budget exhausted"))?
                .map_err(|_| Status::unavailable("RPC verification unavailable"))?
            }
        };
        let capacity_wait_ms = started.elapsed().as_secs_f64() * 1000.0;
        record_rpc_verification_phase(
            RPCVerificationPhase::CapacityWait,
            capacity_wait_ms / 1000.0,
        );
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
        let now = match at {
            Some(now) => now,
            None => now_millis()?,
        };
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
        let file = replay.reserve(now, expiry, parts[2])?;
        let directory = replay.directory.clone();
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
                let result = ReplayState::sync_directory_at(&directory);
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
            replay_lock_wait_ms,
            envelope_check_ms,
            replay_reserve_ms,
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
        let mut state = ReplayState::open(root.path().join("replay")).unwrap();
        let a = "a".repeat(32);
        let b = "b".repeat(32);
        for (at, expiry, nonce) in [(now, now + 1, &a), (now + 2, now + 3, &b)] {
            let file = state.reserve(at, expiry, nonce).unwrap();
            file.sync_all().unwrap();
            state.sync_directory().unwrap();
            assert!(state.pending.remove(nonce));
        }
        assert_eq!(state.durable_high_water, now + 2);
        assert!(!state.nonces.contains(&a));
        let file = state
            .reserve(now + 4, now + 60_000, &"c".repeat(32))
            .unwrap();
        file.sync_all().unwrap();
        state.sync_directory().unwrap();
        assert!(state.pending.remove(&"c".repeat(32)));
        assert_eq!(state.durable_high_water, now + 2);
        assert!(state.nonces.contains(&b));
        assert_eq!(
            state
                .reserve(now + 3, now + 60_000, &"d".repeat(32))
                .unwrap_err()
                .code(),
            tonic::Code::Unavailable
        );
        let replacement = ReplayState::open(state.directory.clone()).unwrap();
        assert!(replacement.nonces.contains(&b));
        assert_eq!(replacement.high_water, now + 2);
        let file = state
            .reserve(now + 252, now + 60_000, &"e".repeat(32))
            .unwrap();
        file.sync_all().unwrap();
        state.sync_directory().unwrap();
        assert!(state.pending.remove(&"e".repeat(32)));
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
