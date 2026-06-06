// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::path::PathBuf;
use std::sync::atomic::AtomicBool;
use std::time::Instant;

use anyhow::{Context, Result};
use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};
use gix::refs::TargetRef;
use tracing::info;

use super::hooks::RefUpdate;

/// In-process replacement for the four `git upload-pack` / `git receive-pack`
/// shell-out call sites in the HTTP git server.
pub struct HttpPackServer {
    pub repo_path: PathBuf,
    pub max_pack_size: u64,
}

// Protocol v1 capability strings
const UPLOAD_PACK_CAPS: &str =
    "multi_ack_detailed multi_ack thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag";
const RECEIVE_PACK_CAPS: &str = "report-status delete-refs side-band-64k quiet atomic ofs-delta";

impl HttpPackServer {
    pub fn new(repo_path: PathBuf, max_pack_size: u64) -> Self {
        Self {
            repo_path,
            max_pack_size,
        }
    }

    /// Replaces: `git upload-pack --advertise-refs`
    pub fn advertise_upload_pack_refs(&self) -> Result<Vec<u8>> {
        let start = Instant::now();
        let repo = open_repo(&self.repo_path)?;
        let mut body = Vec::new();

        body.extend_from_slice(b"001e# service=git-upload-pack\n0000");

        let refs = collect_refs(&repo)?;
        let caps = build_upload_pack_caps(&repo);
        write_ref_advertisement(&mut body, &refs, &caps)?;

        emit_span("upload-pack-advertise", &self.repo_path, start, "ok", 0);
        Ok(body)
    }

    /// Replaces: `git upload-pack --stateless-rpc`
    pub fn handle_upload_pack(&self, body: &[u8]) -> Result<Vec<u8>> {
        let start = Instant::now();
        let repo = open_repo(&self.repo_path)?;
        let (wants, haves, done_seen) = parse_wants_and_haves(body);
        let mut response = Vec::new();

        if wants.is_empty() || !done_seen {
            // NAK — nothing requested or negotiation still in progress
            write_pkt_line(&mut response, b"NAK\n")?;
            response.extend_from_slice(b"0000");
            emit_span("upload-pack-rpc", &self.repo_path, start, "ok", 0);
            return Ok(response);
        }

        // NAK then pack stream
        write_pkt_line(&mut response, b"NAK\n")?;

        let pack_data = build_pack_for_wants(&repo, &wants, &haves)?;
        // Chunk pack into sideband pkt-lines: pkt-line max data = 65512 bytes,
        // minus 1 sideband-channel byte leaves 65511 bytes of pack per packet.
        const SIDEBAND_CHUNK: usize = 65511;
        for chunk in pack_data.chunks(SIDEBAND_CHUNK) {
            let mut sideband = vec![0x01u8]; // channel 1 = data
            sideband.extend_from_slice(chunk);
            write_pkt_line(&mut response, &sideband)?;
        }

        response.extend_from_slice(b"0000");
        emit_span("upload-pack-rpc", &self.repo_path, start, "ok", 0);
        Ok(response)
    }

    /// Replaces: `git receive-pack --advertise-refs`
    pub fn advertise_receive_pack_refs(&self) -> Result<Vec<u8>> {
        let start = Instant::now();
        let repo = open_repo(&self.repo_path)?;
        let mut body = Vec::new();

        body.extend_from_slice(b"001f# service=git-receive-pack\n0000");

        let refs = collect_refs(&repo)?;
        write_ref_advertisement(&mut body, &refs, RECEIVE_PACK_CAPS)?;

        emit_span("receive-pack-advertise", &self.repo_path, start, "ok", 0);
        Ok(body)
    }

    /// Replaces: `git receive-pack --stateless-rpc`
    ///
    /// Quarantine strategy: pack objects are written to a temp directory first.
    /// Only after the ref transaction commits successfully are the pack/index
    /// files moved into the real ODB. On any failure the temp dir is dropped and
    /// no new objects are left in the repository.
    ///
    /// `pipeline` is called synchronously via `block_in_place` since this method
    /// runs inside a `spawn_blocking` context. The HTTP path uses `NoopAdmissionHandler`
    /// by default; real admission logic runs in the async gRPC path.
    pub fn handle_receive_pack(
        &self,
        body: &[u8],
        pipeline: &crate::git::hooks::HookPipeline,
    ) -> Result<Vec<u8>> {
        let start = Instant::now();
        let pack_size_bytes = body.len() as u64;

        let repo = open_repo(&self.repo_path)?;
        let (ref_updates, pack_data) = parse_receive_pack_body(body)?;

        // Stage pack objects into a quarantine directory; committed after refs succeed.
        let quarantine = if !pack_data.is_empty() {
            let odb = (*repo.objects).clone();
            Some(
                stage_pack_from_reader(std::io::Cursor::new(pack_data), Some(odb))
                    .context("staging pack to quarantine")?,
            )
        } else {
            None
        };

        // Build RefUpdate list.
        let hook_updates: Vec<RefUpdate> = ref_updates
            .iter()
            .map(|(refname, old_oid, new_oid)| RefUpdate {
                ref_name: refname.clone(),
                old_oid: old_oid.clone(),
                new_oid: new_oid.clone(),
            })
            .collect();

        // Run pre-receive → proc-receive → update phases via the pipeline.
        // Pass the quarantine dir so blob extraction can resolve pushed objects
        // that are not yet visible in the live ODB.
        // block_in_place lets us .await inside spawn_blocking.
        let quarantine_path = quarantine.as_ref().map(|q| q.dir.path().to_path_buf());
        let pipeline_result = tokio::task::block_in_place(|| {
            tokio::runtime::Handle::current().block_on(pipeline.run(
                &self.repo_path,
                &hook_updates,
                quarantine_path.as_deref(),
            ))
        });

        let accepted_indices = match pipeline_result {
            Ok(indices) => indices,
            Err(rejection) => {
                // Whole-push rejection (pre-receive or proc-receive).
                let mut inner = Vec::new();
                write_pkt_line(&mut inner, b"unpack ok\n")?;
                for (refname, _, _) in &ref_updates {
                    write_pkt_line(
                        &mut inner,
                        format!("ng {refname} {}\n", rejection.reason).as_bytes(),
                    )?;
                }
                inner.extend_from_slice(b"0000");
                let mut sideband_data = vec![0x01u8];
                sideband_data.extend_from_slice(&inner);
                let mut response = Vec::new();
                write_pkt_line(&mut response, &sideband_data)?;
                response.extend_from_slice(b"0000");
                return Ok(response);
            }
        };

        // Build atomic ref transaction for accepted refs only.
        let mut ref_edits: Vec<RefEdit> = Vec::new();
        for i in &accepted_indices {
            let (refname, old_oid, new_oid) = &ref_updates[*i];
            let new_id = gix::ObjectId::from_hex(new_oid.as_bytes())
                .with_context(|| format!("parse new oid {new_oid}"))?;
            let old_id = gix::ObjectId::from_hex(old_oid.as_bytes())
                .with_context(|| format!("parse old oid {old_oid}"))?;

            let change = if new_id.is_null() {
                let expected = if old_id.is_null() {
                    PreviousValue::Any
                } else {
                    PreviousValue::MustExistAndMatch(gix::refs::Target::Object(old_id))
                };
                Change::Delete {
                    expected,
                    log: RefLog::AndReference,
                }
            } else {
                let previous_value = if old_id.is_null() {
                    PreviousValue::MustNotExist
                } else {
                    PreviousValue::MustExistAndMatch(gix::refs::Target::Object(old_id))
                };
                Change::Update {
                    log: LogChange {
                        mode: RefLog::AndReference,
                        force_create_reflog: false,
                        message: "push".into(),
                    },
                    expected: previous_value,
                    new: gix::refs::Target::Object(new_id),
                }
            };

            ref_edits.push(RefEdit {
                change,
                name: refname
                    .as_str()
                    .try_into()
                    .with_context(|| format!("parse refname {refname}"))?,
                deref: false,
            });
        }

        // Two-phase gix ref transaction: prepare (locks acquired) → reference-transaction hook
        // → commit or rollback.
        let accepted_updates: Vec<RefUpdate> = accepted_indices
            .iter()
            .map(|i| hook_updates[*i].clone())
            .collect();

        if !ref_edits.is_empty() {
            let file_lock_fail = gix::lock::acquire::Fail::AfterDurationWithBackoff(
                std::time::Duration::from_millis(100),
            );
            let packed_lock_fail = gix::lock::acquire::Fail::AfterDurationWithBackoff(
                std::time::Duration::from_millis(1000),
            );
            let txn = repo
                .refs
                .transaction()
                .prepare(ref_edits, file_lock_fail, packed_lock_fail)
                .context("prepare ref transaction")?;

            // reference-transaction/prepared veto check (sync path uses NoopAdmissionHandler).
            let veto_result = tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(
                    pipeline.run_reference_transaction_prepared(&self.repo_path, &accepted_updates),
                )
            });

            match veto_result {
                Ok(()) => {
                    // Promote quarantined pack before committing refs.
                    if let Some(q) = quarantine {
                        promote_quarantine(&repo, q)
                            .context("promoting quarantined pack to ODB")?;
                    }
                    txn.commit(None).context("commit ref transaction")?;
                    pipeline
                        .run_reference_transaction_committed(&self.repo_path, &accepted_updates);
                }
                Err(rejection) => {
                    drop(txn); // releases all lock files
                    pipeline.run_reference_transaction_aborted(&self.repo_path, &accepted_updates);
                    let mut inner = Vec::new();
                    write_pkt_line(&mut inner, b"unpack ok\n")?;
                    for (refname, _, _) in &ref_updates {
                        write_pkt_line(
                            &mut inner,
                            format!("ng {refname} {}\n", rejection.reason).as_bytes(),
                        )?;
                    }
                    inner.extend_from_slice(b"0000");
                    let mut sideband_data = vec![0x01u8];
                    sideband_data.extend_from_slice(&inner);
                    let mut response = Vec::new();
                    write_pkt_line(&mut response, &sideband_data)?;
                    response.extend_from_slice(b"0000");
                    return Ok(response);
                }
            }
        } else {
            // No ref edits: drop quarantine safely.
            drop(quarantine);
        }

        pipeline.run_post_receive(&self.repo_path, &accepted_updates, "");

        // Build report-status response.
        let accepted_set: std::collections::HashSet<usize> =
            accepted_indices.iter().copied().collect();
        let mut inner = Vec::new();
        write_pkt_line(&mut inner, b"unpack ok\n")?;
        for (i, (refname, _, _)) in ref_updates.iter().enumerate() {
            if accepted_set.contains(&i) {
                write_pkt_line(&mut inner, format!("ok {refname}\n").as_bytes())?;
            } else {
                write_pkt_line(
                    &mut inner,
                    format!("ng {refname} rejected by update hook\n").as_bytes(),
                )?;
            }
        }
        inner.extend_from_slice(b"0000");

        let mut sideband_data = vec![0x01u8];
        sideband_data.extend_from_slice(&inner);

        let mut response = Vec::new();
        write_pkt_line(&mut response, &sideband_data)?;
        response.extend_from_slice(b"0000");

        emit_span(
            "receive-pack-rpc",
            &self.repo_path,
            start,
            "ok",
            pack_size_bytes,
        );
        Ok(response)
    }
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

fn open_repo(path: &std::path::Path) -> Result<gix::Repository> {
    gix::open(path).with_context(|| format!("open repo {}", path.display()))
}

fn emit_span(
    operation: &str,
    repo_path: &std::path::Path,
    start: Instant,
    outcome: &str,
    pack_size_bytes: u64,
) {
    let duration_ms = start.elapsed().as_millis() as u64;
    if pack_size_bytes > 0 {
        info!(
            repo = %repo_path.display(),
            operation,
            duration_ms,
            pack_size_bytes,
            outcome,
        );
    } else {
        info!(
            repo = %repo_path.display(),
            operation,
            duration_ms,
            outcome,
        );
    }
}

/// Write pkt-line format: 4-hex-digit length prefix + data.
fn write_pkt_line(out: &mut Vec<u8>, data: &[u8]) -> Result<()> {
    let len = data.len() + 4;
    anyhow::ensure!(len <= 65516, "pkt-line data too long: {} bytes", data.len());
    let hex = format!("{:04x}", len);
    out.extend_from_slice(hex.as_bytes());
    out.extend_from_slice(data);
    Ok(())
}

/// Build upload-pack capability string, including `symref=HEAD:<target>` when HEAD is symbolic.
fn build_upload_pack_caps(repo: &gix::Repository) -> String {
    let symref = repo
        .head_ref()
        .ok()
        .flatten()
        .map(|r| format!(" symref=HEAD:{}", r.name().as_bstr()))
        .unwrap_or_default();
    format!("{}{}", UPLOAD_PACK_CAPS, symref)
}

/// Write protocol v1 ref advertisement (used by both upload-pack and receive-pack).
fn write_ref_advertisement(out: &mut Vec<u8>, refs: &[(String, String)], caps: &str) -> Result<()> {
    if refs.is_empty() {
        let zero = "0000000000000000000000000000000000000000";
        let line = format!("{} capabilities^{{}}\0{}\n", zero, caps);
        write_pkt_line(out, line.as_bytes())?;
    } else {
        let (first_name, first_oid) = &refs[0];
        let line = format!("{} {}\0{}\n", first_oid, first_name, caps);
        write_pkt_line(out, line.as_bytes())?;
        for (name, oid) in refs.iter().skip(1) {
            write_pkt_line(out, format!("{} {}\n", oid, name).as_bytes())?;
        }
    }
    out.extend_from_slice(b"0000");
    Ok(())
}

/// Collect all refs from the repository as sorted (full-name, hex-oid) pairs.
///
/// HEAD is always included explicitly: `platform.all()` does not iterate HEAD
/// on all platforms (notably Linux gix builds omit it), so we add it directly.
fn collect_refs(repo: &gix::Repository) -> Result<Vec<(String, String)>> {
    let mut refs: Vec<(String, String)> = Vec::new();

    // Explicitly resolve HEAD first so it always appears in the advertisement.
    if let Ok(head_id) = repo.head_id() {
        refs.push(("HEAD".to_string(), head_id.to_string()));
    }

    let platform = repo.references().context("access references")?;
    let all = platform.all().context("iterate references")?;

    for r in all {
        let reference = match r {
            Ok(r) => r,
            Err(_) => continue,
        };
        let name = reference.name().as_bstr().to_string();
        if name == "HEAD" {
            continue; // already added above
        }
        let oid = match reference.target() {
            TargetRef::Object(id) => id.to_string(),
            TargetRef::Symbolic(_) => match repo.find_reference(reference.name().as_bstr()) {
                Ok(mut r) => match r.peel_to_id() {
                    Ok(id) => id.to_string(),
                    Err(_) => continue,
                },
                Err(_) => continue,
            },
        };
        refs.push((name, oid));
    }

    // Also advertise peeled tags
    let mut peeled = Vec::new();
    for (name, oid_str) in &refs {
        if name.starts_with("refs/tags/") {
            if let Ok(oid) = gix::ObjectId::from_hex(oid_str.as_bytes()) {
                if let Ok(obj) = repo.find_object(oid) {
                    if let Ok(tag) = obj.try_into_tag() {
                        if let Ok(target_id) = tag.target_id() {
                            peeled.push((format!("{}^{{}}", name), target_id.to_string()));
                        }
                    }
                }
            }
        }
    }
    refs.extend(peeled);

    refs.sort_by(|a, b| {
        if a.0 == "HEAD" {
            std::cmp::Ordering::Less
        } else if b.0 == "HEAD" {
            std::cmp::Ordering::Greater
        } else {
            a.0.cmp(&b.0)
        }
    });

    Ok(refs)
}

/// Parse `want`, `have`, and `done` lines from a pkt-line upload-pack request body.
///
/// Returns `(wants, haves, done_seen)`. `done_seen` is `true` when the client sent
/// `done`, signalling it has finished negotiation and expects a pack in response.
pub fn parse_wants_and_haves(body: &[u8]) -> (Vec<String>, Vec<String>, bool) {
    let mut wants = Vec::new();
    let mut haves = Vec::new();
    let mut done_seen = false;
    let mut pos = 0;

    while pos + 4 <= body.len() {
        let len_str = match std::str::from_utf8(&body[pos..pos + 4]) {
            Ok(s) => s,
            Err(_) => break,
        };
        let len = match usize::from_str_radix(len_str, 16) {
            Ok(l) => l,
            Err(_) => break,
        };
        if len == 0 {
            pos += 4;
            continue;
        }
        if pos + len > body.len() {
            break;
        }

        let line = &body[pos + 4..pos + len];
        if let Ok(s) = std::str::from_utf8(line) {
            let s = s.trim_end_matches('\n').split('\0').next().unwrap_or("");
            if let Some(rest) = s.strip_prefix("want ") {
                let oid = rest.split_whitespace().next().unwrap_or("").to_string();
                if !oid.is_empty() {
                    wants.push(oid);
                }
            } else if let Some(rest) = s.strip_prefix("have ") {
                let oid = rest.split_whitespace().next().unwrap_or("").to_string();
                if !oid.is_empty() {
                    haves.push(oid);
                }
            } else if s == "done" {
                done_seen = true;
            }
        }
        pos += len;
    }
    (wants, haves, done_seen)
}

/// Build a pack file containing objects reachable from `wants` but not from `haves`.
///
/// `haves` are commit OIDs the client already has; they act as boundary commits so
/// only the incremental delta is included, matching normal upload-pack negotiation.
pub fn build_pack_for_wants(
    repo: &gix::Repository,
    wants: &[String],
    haves: &[String],
) -> Result<Vec<u8>> {
    use gix::object::Kind;
    use gix_pack::data::output;
    use gix_pack::data::output::count::objects::ObjectExpansion;

    let want_ids: Vec<gix::ObjectId> = wants
        .iter()
        .filter_map(|h| gix::ObjectId::from_hex(h.as_bytes()).ok())
        .collect();

    if want_ids.is_empty() {
        return Ok(Vec::new());
    }

    // Resolve have OIDs; unknown/invalid ones are silently ignored.
    let have_ids: Vec<gix::ObjectId> = haves
        .iter()
        .filter_map(|h| gix::ObjectId::from_hex(h.as_bytes()).ok())
        .filter(|id| repo.find_object(*id).is_ok())
        .collect();

    let interrupt = AtomicBool::new(false);

    // Clone and prepare ODB handle: prevent_pack_unload ensures pack location data
    // remains valid during the entire pack generation pipeline.
    let mut odb = (*repo.objects).clone();
    odb.prevent_pack_unload();

    // Peel annotated tags: rev_walk traverses commits only, so a want pointing at a
    // tag object would yield no commits and produce an empty pack. We peel each tag
    // to its target commit and include the tag object itself via AsIs expansion.
    let mut commit_tips: Vec<gix::ObjectId> = Vec::new();
    let mut extra_objects: Vec<gix::ObjectId> = Vec::new();
    for oid in &want_ids {
        let obj = repo
            .find_object(*oid)
            .with_context(|| format!("find want object {oid}"))?;
        if obj.kind == Kind::Tag {
            extra_objects.push(*oid);
            let tag = obj
                .try_into_tag()
                .map_err(|_| anyhow::anyhow!("peel tag {oid}"))?;
            let target_id = tag
                .target_id()
                .with_context(|| format!("tag target {oid}"))?;
            commit_tips.push(target_id.detach());
        } else {
            commit_tips.push(*oid);
        }
    }

    // Walk commits from wants, stopping at have boundaries so only the
    // incremental delta is included rather than the full history.
    let walk_ids: Vec<gix::ObjectId> = repo
        .rev_walk(commit_tips.iter().copied())
        .with_boundary(have_ids.iter().copied())
        .all()
        .context("rev walk for pack generation")?
        .filter_map(|r| r.ok().map(|info| info.id))
        .collect();

    if walk_ids.is_empty() && extra_objects.is_empty() {
        anyhow::bail!(
            "upload-pack: rev_walk produced no objects for {} want(s); \
             repository may be corrupt or wants are not reachable",
            want_ids.len()
        );
    }

    // Count commit-reachable objects (trees + blobs via TreeContents expansion).
    let mut ids_iter = walk_ids
        .iter()
        .map(|id| Ok::<_, Box<dyn std::error::Error + Send + Sync>>(*id));

    let (mut counts, _) = gix_pack::data::output::count::objects_unthreaded(
        &odb,
        &mut ids_iter,
        &gix::progress::Discard,
        &interrupt,
        ObjectExpansion::TreeContents,
    )
    .context("counting pack objects")?;

    // Include tag objects themselves (AsIs — no expansion needed for tag blobs).
    if !extra_objects.is_empty() {
        let mut tag_iter = extra_objects
            .iter()
            .map(|id| Ok::<_, Box<dyn std::error::Error + Send + Sync>>(*id));
        let (tag_counts, _) = gix_pack::data::output::count::objects_unthreaded(
            &odb,
            &mut tag_iter,
            &gix::progress::Discard,
            &interrupt,
            ObjectExpansion::AsIs,
        )
        .context("counting tag objects")?;
        counts.extend(tag_counts);
    }

    if counts.is_empty() {
        return Ok(Vec::new());
    }

    let entries_iter = output::entry::iter_from_counts(
        counts,
        odb.clone(),
        Box::new(gix::progress::Discard),
        gix_pack::data::output::entry::iter_from_counts::Options {
            thread_limit: Some(1),
            ..Default::default()
        },
    );

    // Collect all entries first so we know the exact count before creating
    // FromEntriesIter. Passing an incorrect count causes an index-out-of-bounds
    // panic in gix-pack 0.71.0 when delta expansion inflates the entry count
    // beyond counts.len().
    type BatchResult =
        Result<Vec<output::Entry>, gix_pack::data::output::entry::iter_from_counts::Error>;
    let all_entries: Vec<Vec<output::Entry>> = entries_iter
        .into_iter()
        .map(|r| -> BatchResult { r.map(|(_seq, entries)| entries) })
        .collect::<Result<_, _>>()
        .map_err(|e| anyhow::anyhow!("pack entry collection error: {e}"))?;
    let num_entries: u32 = all_entries.iter().map(|b| b.len() as u32).sum();

    if num_entries == 0 {
        return Ok(Vec::new());
    }

    let mut pack_bytes: Vec<u8> = Vec::new();
    type EntryBatch = Vec<output::Entry>;
    let mut bytes_iter = gix_pack::data::output::bytes::FromEntriesIter::new(
        all_entries.into_iter().map(Ok::<EntryBatch, std::convert::Infallible>),
        &mut pack_bytes,
        num_entries,
        gix_pack::data::Version::V2,
        gix::hash::Kind::Sha1,
    );

    loop {
        match bytes_iter.next() {
            Some(Ok(_)) => {}
            Some(Err(e)) => return Err(anyhow::anyhow!("pack generation error: {e}")),
            None => break,
        }
    }

    Ok(pack_bytes)
}

type RefUpdates<'a> = (Vec<(String, String, String)>, &'a [u8]);

/// Parse the pkt-line body of a receive-pack request.
///
/// Returns (ref_updates: Vec<(refname, old-oid, new-oid)>, pack_data slice).
///
/// Body layout: pkt-line ref-updates → flush (0000) → raw PACK bytes (not pkt-line wrapped)
fn parse_receive_pack_body(body: &[u8]) -> Result<RefUpdates<'_>> {
    let mut updates = Vec::new();
    let mut pos = 0;

    while pos + 4 <= body.len() {
        let len_str = std::str::from_utf8(&body[pos..pos + 4]).context("parse pkt-line length")?;

        // If the 4 bytes aren't valid hex, we've reached raw PACK data
        let len = match usize::from_str_radix(len_str, 16) {
            Ok(l) => l,
            Err(_) => break,
        };

        // Flush packet — raw PACK data (if any) follows immediately after
        if len == 0 {
            pos += 4;
            break;
        }

        if pos + len > body.len() {
            break;
        }

        let line = &body[pos + 4..pos + len];

        if let Ok(s) = std::str::from_utf8(line) {
            let s = s.trim_end_matches('\n').split('\0').next().unwrap_or("");
            let parts: Vec<&str> = s.splitn(3, ' ').collect();
            if parts.len() == 3 {
                updates.push((
                    parts[2].to_string(),
                    parts[0].to_string(),
                    parts[1].to_string(),
                ));
            }
        }
        pos += len;
    }

    // Everything after the flush is raw PACK bytes
    let pack_data = if pos < body.len() && body[pos..].starts_with(b"PACK") {
        &body[pos..]
    } else {
        &[]
    };
    Ok((updates, pack_data))
}

pub struct Quarantine {
    pub dir: tempfile::TempDir,
    pub pack_path: std::path::PathBuf,
    pub index_path: std::path::PathBuf,
    pub num_objects: u32,
}

/// A `Read` impl that drains a sync_channel receiver of `Vec<u8>` chunks.
pub struct ChannelReader {
    pub rx: std::sync::mpsc::Receiver<Vec<u8>>,
    buf: Vec<u8>,
    pos: usize,
}

impl ChannelReader {
    pub fn new(rx: std::sync::mpsc::Receiver<Vec<u8>>) -> Self {
        Self {
            rx,
            buf: Vec::new(),
            pos: 0,
        }
    }
}

impl std::io::Read for ChannelReader {
    fn read(&mut self, out: &mut [u8]) -> std::io::Result<usize> {
        loop {
            if self.pos < self.buf.len() {
                let n = std::cmp::min(out.len(), self.buf.len() - self.pos);
                out[..n].copy_from_slice(&self.buf[self.pos..self.pos + n]);
                self.pos += n;
                return Ok(n);
            }
            match self.rx.recv() {
                Ok(chunk) => {
                    self.buf = chunk;
                    self.pos = 0;
                }
                Err(_) => return Ok(0), // channel closed = EOF
            }
        }
    }
}

/// Write incoming pack data to a temporary quarantine directory from any `Read`.
/// The pack and index files are NOT visible to the live ODB until
/// `promote_quarantine` moves them into the real pack directory.
///
/// Pass `odb` to resolve base objects for thin packs sent by incremental pushes.
pub fn stage_pack_from_reader<R: std::io::Read, O: gix_pack::Find + gix::prelude::Find>(
    reader: R,
    odb: Option<O>,
) -> Result<Quarantine> {
    use gix_pack::bundle::write::Options;

    let quarantine_dir = tempfile::TempDir::new().context("create quarantine dir")?;
    let mut reader = std::io::BufReader::new(reader);
    let interrupt = AtomicBool::new(false);
    let mut progress = gix::progress::Discard;
    let outcome = gix_pack::Bundle::write_to_directory(
        &mut reader,
        Some(quarantine_dir.path()),
        &mut progress,
        &interrupt,
        odb,
        Options {
            thread_limit: Some(1),
            iteration_mode: gix_pack::data::input::Mode::Verify,
            index_version: gix_pack::index::Version::V2,
            object_hash: gix::hash::Kind::Sha1,
        },
    )
    .context("write pack to quarantine")?;

    let (pack_path, index_path) = match &outcome.data_path {
        Some(p) => {
            let idx: std::path::PathBuf = p.with_extension("idx");
            (p.clone(), idx)
        }
        None => anyhow::bail!("pack writer produced no output file"),
    };

    Ok(Quarantine {
        dir: quarantine_dir,
        pack_path,
        index_path,
        num_objects: outcome.index.num_objects,
    })
}

/// Move quarantined pack/index files into the repository's live pack directory.
/// Called only after the ref transaction has committed successfully.
/// Falls back to copy+delete when rename fails across filesystem boundaries (e.g. Docker overlay).
pub fn promote_quarantine(repo: &gix::Repository, q: Quarantine) -> Result<()> {
    let pack_dir = repo.objects.store_ref().path().join("pack");
    std::fs::create_dir_all(&pack_dir).context("ensure pack dir")?;

    let pack_dest = pack_dir.join(q.pack_path.file_name().context("pack file name")?);
    let idx_dest = pack_dir.join(q.index_path.file_name().context("index file name")?);

    move_file(&q.pack_path, &pack_dest).context("move pack file")?;
    move_file(&q.index_path, &idx_dest).context("move index file")?;

    drop(q.dir);

    info!(
        objects_written = q.num_objects,
        "pack promoted to object database"
    );
    Ok(())
}

fn move_file(src: &std::path::Path, dst: &std::path::Path) -> Result<()> {
    if std::fs::rename(src, dst).is_ok() {
        return Ok(());
    }
    std::fs::copy(src, dst).context("copy file")?;
    std::fs::remove_file(src).context("remove source file")?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // -----------------------------------------------------------------------
    // Test helpers
    // -----------------------------------------------------------------------

    /// Create an in-memory bare gix repository in a temp directory.
    fn make_test_repo() -> (tempfile::TempDir, gix::Repository) {
        let dir = tempfile::TempDir::new().expect("tmp dir");
        let repo = gix::init_bare(dir.path()).expect("init bare repo");
        (dir, repo)
    }

    /// Write a blob + tree + commit into `repo` and return the commit OID.
    /// Updates `refs/heads/main` so the commit is reachable.
    fn make_commit(
        repo: &gix::Repository,
        message: &str,
        parent: Option<gix::ObjectId>,
    ) -> gix::ObjectId {
        use gix::objs::tree::Entry;
        use gix::objs::{Blob, Tree};

        // Blob
        let blob_oid = repo
            .write_object(Blob {
                data: message.as_bytes().into(),
            })
            .expect("write blob")
            .detach();

        // Tree
        let tree = Tree {
            entries: vec![Entry {
                mode: gix::objs::tree::EntryKind::Blob.into(),
                filename: "file.txt".into(),
                oid: blob_oid,
            }],
        };
        let tree_oid = repo.write_object(tree).expect("write tree").detach();

        // Build a SignatureRef with a raw time string (git format: "seconds offset")
        let sig = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "0 +0000",
        };

        let parents: Vec<gix::ObjectId> = parent.into_iter().collect();
        repo.commit_as(sig, sig, "refs/heads/main", message, tree_oid, parents)
            .expect("write commit")
            .detach()
    }

    /// Encode a slice of lines as pkt-line bytes.
    fn pkt_lines(lines: &[&[u8]]) -> Vec<u8> {
        let mut out = Vec::new();
        for line in lines {
            let len = line.len() + 4;
            out.extend_from_slice(format!("{:04x}", len).as_bytes());
            out.extend_from_slice(line);
        }
        out
    }

    /// Append a flush packet (`0000`) to a byte buffer.
    fn flush(buf: &mut Vec<u8>) {
        buf.extend_from_slice(b"0000");
    }

    // -----------------------------------------------------------------------
    // T050 – T053: parse_wants_and_haves
    // -----------------------------------------------------------------------

    #[test]
    fn t050_parse_wants_and_haves_want_plus_done() {
        let oid = "a".repeat(40);
        let want_line = format!("want {}\n", oid);
        let mut body = pkt_lines(&[want_line.as_bytes(), b"done\n"]);
        flush(&mut body);

        let (wants, haves, done_seen) = parse_wants_and_haves(&body);
        assert_eq!(wants, vec![oid]);
        assert!(haves.is_empty());
        assert!(done_seen, "done_seen must be true when 'done' line present");
    }

    #[test]
    fn t051_parse_wants_and_haves_done_absent() {
        let oid = "b".repeat(40);
        let want_line = format!("want {}\n", oid);
        let mut body = pkt_lines(&[want_line.as_bytes()]);
        flush(&mut body);

        let (wants, haves, done_seen) = parse_wants_and_haves(&body);
        assert_eq!(wants, vec![oid]);
        assert!(haves.is_empty());
        assert!(!done_seen, "done_seen must be false when 'done' absent");
    }

    #[test]
    fn t052_parse_wants_and_haves_empty_body() {
        let (wants, haves, done_seen) = parse_wants_and_haves(b"");
        assert!(wants.is_empty());
        assert!(haves.is_empty());
        assert!(!done_seen);
    }

    #[test]
    fn t053_parse_wants_and_haves_caps_stripped() {
        // capability string after \0 must not bleed into the OID
        let oid = "c".repeat(40);
        let want_line = format!("want {}\0multi_ack side-band-64k\n", oid);
        let mut body = pkt_lines(&[want_line.as_bytes(), b"done\n"]);
        flush(&mut body);

        let (wants, _haves, done_seen) = parse_wants_and_haves(&body);
        assert_eq!(wants, vec![oid], "capability suffix must be stripped");
        assert!(done_seen);
    }

    // -----------------------------------------------------------------------
    // T054 – T056: handle_upload_pack
    // -----------------------------------------------------------------------

    #[test]
    fn t054_handle_upload_pack_empty_wants_returns_nak_flush() {
        let (_dir, repo) = make_test_repo();
        let server = HttpPackServer {
            repo_path: repo.path().to_path_buf(),
            max_pack_size: 64 * 1024 * 1024,
        };

        let body = b""; // no wants, no done
        let resp = server.handle_upload_pack(body).expect("handle_upload_pack");

        // Expected: pkt-line("NAK\n") + "0000"
        let mut expected = Vec::new();
        write_pkt_line(&mut expected, b"NAK\n").unwrap();
        expected.extend_from_slice(b"0000");
        assert_eq!(resp, expected, "empty-wants must return NAK+0000");
    }

    #[test]
    fn t055_handle_upload_pack_wants_plus_done_returns_pack() {
        let (_dir, repo) = make_test_repo();
        let commit_oid = make_commit(&repo, "initial", None);

        // Point HEAD at the commit so the server has a valid repo
        let head_ref = repo
            .refs
            .transaction()
            .prepare(
                vec![gix::refs::transaction::RefEdit {
                    change: gix::refs::transaction::Change::Update {
                        log: gix::refs::transaction::LogChange {
                            mode: gix::refs::transaction::RefLog::AndReference,
                            force_create_reflog: false,
                            message: "init".into(),
                        },
                        expected: gix::refs::transaction::PreviousValue::Any,
                        new: gix::refs::Target::Object(commit_oid),
                    },
                    name: "refs/heads/main".try_into().unwrap(),
                    deref: false,
                }],
                gix::lock::acquire::Fail::Immediately,
                gix::lock::acquire::Fail::Immediately,
            )
            .unwrap();
        let _ = head_ref.commit(None);

        let server = HttpPackServer {
            repo_path: repo.path().to_path_buf(),
            max_pack_size: 64 * 1024 * 1024,
        };

        let want_line = format!("want {}\n", commit_oid);
        let mut body = pkt_lines(&[want_line.as_bytes(), b"done\n"]);
        flush(&mut body);

        let resp = server
            .handle_upload_pack(&body)
            .expect("handle_upload_pack");

        // Response must start with pkt-line("NAK\n") and contain sideband data (0x01 prefix)
        let mut nak = Vec::new();
        write_pkt_line(&mut nak, b"NAK\n").unwrap();
        assert!(
            resp.starts_with(&nak),
            "response must begin with NAK pkt-line"
        );
        assert!(
            resp.len() > nak.len() + 4,
            "response must contain pack data after NAK"
        );
        // Verify sideband channel byte
        let after_nak = &resp[nak.len()..];
        let sideband_len =
            usize::from_str_radix(std::str::from_utf8(&after_nak[..4]).unwrap(), 16).unwrap();
        assert_eq!(
            after_nak[4], 0x01,
            "pack data must be on sideband channel 1"
        );
        let _ = sideband_len;
    }

    #[test]
    fn t056_handle_upload_pack_wants_no_done_returns_nak_flush() {
        let (_dir, repo) = make_test_repo();
        let commit_oid = make_commit(&repo, "initial", None);

        let server = HttpPackServer {
            repo_path: repo.path().to_path_buf(),
            max_pack_size: 64 * 1024 * 1024,
        };

        // wants present but no done line
        let want_line = format!("want {}\n", commit_oid);
        let mut body = pkt_lines(&[want_line.as_bytes()]);
        flush(&mut body);

        let resp = server
            .handle_upload_pack(&body)
            .expect("handle_upload_pack");

        let mut expected = Vec::new();
        write_pkt_line(&mut expected, b"NAK\n").unwrap();
        expected.extend_from_slice(b"0000");
        assert_eq!(resp, expected, "wants without done must return NAK+0000");
    }

    // -----------------------------------------------------------------------
    // T057 – T059: build_pack_for_wants
    // -----------------------------------------------------------------------

    #[test]
    fn t057_build_pack_for_wants_fresh_clone_non_empty() {
        let (_dir, repo) = make_test_repo();
        let commit_oid = make_commit(&repo, "initial", None);

        let wants = vec![commit_oid.to_string()];
        let pack = build_pack_for_wants(&repo, &wants, &[]).expect("build_pack_for_wants");

        assert!(!pack.is_empty(), "fresh clone pack must be non-empty");
        assert!(
            pack.starts_with(b"PACK"),
            "output must be a valid PACK file"
        );
    }

    #[test]
    fn t058_build_pack_for_wants_client_has_everything_returns_err() {
        let (_dir, repo) = make_test_repo();
        let commit_oid = make_commit(&repo, "initial", None);

        // Client has the tip — rev_walk with have=tip yields no commits
        let wants = vec![commit_oid.to_string()];
        let haves = vec![commit_oid.to_string()];
        let result = build_pack_for_wants(&repo, &wants, &haves);

        assert!(
            result.is_err(),
            "should error when rev_walk yields no objects"
        );
    }

    #[test]
    fn t059_build_pack_for_wants_unreachable_want_returns_err() {
        let (_dir, repo) = make_test_repo();
        // A well-formed but non-existent OID
        let fake_oid = "d".repeat(40);
        let wants = vec![fake_oid];
        let result = build_pack_for_wants(&repo, &wants, &[]);
        assert!(
            result.is_err(),
            "non-existent want OID must return an error"
        );
    }
}
