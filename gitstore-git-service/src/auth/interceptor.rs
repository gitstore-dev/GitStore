// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use hmac::{Hmac, KeyInit, Mac};
use prost::Message;
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeSet, HashSet},
    fs::{self, File, OpenOptions},
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
    time::{SystemTime, UNIX_EPOCH},
};
use tonic::{metadata::MetadataMap, Request, Status};

pub const SIGNATURE_HEADER: &str = "x-gitstore-rpc";
const MAX_REPLAYS: usize = 100_000;

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
    high_water: u64,
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
            high_water,
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
        File::open(&self.directory)
            .and_then(|f| f.sync_all())
            .map_err(|_| Status::unavailable("RPC replay durability failed"))
    }
    fn consume(&mut self, now: u64, expiry: u64, nonce: &str) -> Result<(), Status> {
        if now < self.high_water {
            return Err(Status::unavailable("RPC clock moved backwards"));
        }
        // Persist a high-water mark BEFORE deleting expired records. Otherwise
        // a wall-clock rollback after restart could resurrect spent envelopes.
        if self
            .entries
            .first()
            .is_some_and(|(expires, _)| *expires <= now)
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
            // Bound cleanup work on each request; saturation is fail-closed.
            for _ in 0..256 {
                let Some((expires, nonce)) = self.entries.first().cloned() else {
                    break;
                };
                if expires > now {
                    break;
                }
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
        // If either fsync fails, leave the reservation in place and fail closed.
        self.entries.insert((expiry, nonce.to_owned()));
        self.nonces.insert(nonce.to_owned());
        file.sync_all()
            .map_err(|_| Status::unavailable("RPC replay durability failed"))?;
        self.sync_directory()?;
        self.high_water = now;
        Ok(())
    }
}
#[derive(Clone)]
pub struct RPCVerifier {
    secret: Arc<str>,
    previous: Option<Arc<str>>,
    replay: Arc<Mutex<ReplayState>>,
    verification_slots: Arc<tokio::sync::Semaphore>,
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
            verification_slots: Arc::new(tokio::sync::Semaphore::new(32)),
        })
    }
    pub async fn verify<T: Message>(
        &self,
        metadata: &MetadataMap,
        method: &str,
        message: &T,
    ) -> Result<(), Status> {
        let permit = self
            .verification_slots
            .clone()
            .try_acquire_owned()
            .map_err(|_| Status::resource_exhausted("RPC verification capacity exhausted"))?;
        let verifier = self.clone();
        let metadata = metadata.clone();
        let method = method.to_owned();
        let body = message.encode_to_vec();
        tokio::task::spawn_blocking(move || {
            let _permit = permit;
            verifier.verify_bytes(&metadata, &method, &body, None)
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
        let mut replay = self
            .replay
            .lock()
            .map_err(|_| Status::unavailable("RPC replay state unavailable"))?;
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
        replay.consume(now, expiry, parts[2])
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::grpc::server::proto::CreateRepositoryRequest;

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
                .consume(1, 100_000, &"f".repeat(32))
                .unwrap_err()
                .code(),
            tonic::Code::ResourceExhausted
        );
        state.entries.clear();
        fs::remove_dir(&directory).unwrap();
        assert_eq!(
            state
                .consume(1, 100_000, &"f".repeat(32))
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
