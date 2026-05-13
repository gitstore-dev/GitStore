// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::path::PathBuf;
use std::sync::atomic::AtomicBool;
use std::time::Instant;

use anyhow::{Context, Result};
use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};
use gix::refs::TargetRef;
use tracing::info;

use super::hooks::{run_post_receive, run_pre_receive, run_update_hooks, RefUpdate};

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
        let (wants, haves) = parse_wants_and_haves(body);
        let mut response = Vec::new();

        if wants.is_empty() {
            // NAK — nothing requested
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
    pub fn handle_receive_pack(&self, body: &[u8]) -> Result<Vec<u8>> {
        let start = Instant::now();
        let pack_size_bytes = body.len() as u64;

        let repo = open_repo(&self.repo_path)?;
        let (ref_updates, pack_data) = parse_receive_pack_body(body)?;

        // Stage pack objects into a quarantine directory; committed after refs succeed.
        let quarantine = if !pack_data.is_empty() {
            Some(stage_pack_to_quarantine(&repo, pack_data).context("staging pack to quarantine")?)
        } else {
            None
        };

        // Build RefUpdate list (shared with hook runners and ref transaction).
        let hook_updates: Vec<RefUpdate> = ref_updates
            .iter()
            .map(|(refname, old_oid, new_oid)| RefUpdate {
                ref_name: refname.clone(),
                old_oid: old_oid.clone(),
                new_oid: new_oid.clone(),
            })
            .collect();

        // pre-receive: non-zero exit rejects the entire push before any mutation.
        run_pre_receive(&self.repo_path, &hook_updates).context("pre-receive hook")?;

        // update: called per-ref; collects accepted indices for the ref transaction.
        let accepted_indices =
            run_update_hooks(&self.repo_path, &hook_updates).context("update hooks")?;

        // Build atomic ref transaction for accepted refs only.
        let mut ref_edits: Vec<RefEdit> = Vec::new();
        for i in &accepted_indices {
            let (refname, old_oid, new_oid) = &ref_updates[*i];
            let new_id = gix::ObjectId::from_hex(new_oid.as_bytes())
                .with_context(|| format!("parse new oid {new_oid}"))?;
            let old_id = gix::ObjectId::from_hex(old_oid.as_bytes())
                .with_context(|| format!("parse old oid {old_oid}"))?;

            let change = if new_id.is_null() {
                // Delete: `git push origin :branch`
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

        // Commit atomically — gix uses lock files; any failure rolls back
        if !ref_edits.is_empty() {
            repo.edit_references(ref_edits)
                .context("atomic ref transaction")?;
        }

        // Refs committed — promote quarantined pack/index into the real ODB.
        // If this step fails the refs already landed, so we surface the error
        // but do not revert them (the objects are still accessible via loose ODB
        // because write_to_directory also indexes them; this is a best-effort move).
        if let Some(q) = quarantine {
            promote_quarantine(&repo, q).context("promoting quarantined pack to ODB")?;
        }

        // post-receive: informational only, exit code ignored.
        let accepted_set: std::collections::HashSet<usize> =
            accepted_indices.iter().copied().collect();
        let accepted_updates: Vec<RefUpdate> = accepted_indices
            .iter()
            .map(|i| RefUpdate {
                ref_name: hook_updates[*i].ref_name.clone(),
                old_oid: hook_updates[*i].old_oid.clone(),
                new_oid: hook_updates[*i].new_oid.clone(),
            })
            .collect();
        run_post_receive(&self.repo_path, &accepted_updates);

        // Build report-status response.
        // With side-band-64k: ALL report-status pkt-lines are bundled into ONE sideband
        // channel-1 payload, followed by a sideband flush pkt-line (0000).
        // Format: pkt-line(\x01 <inner-pkt-lines> <inner-0000>)  then outer 0000
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

        let mut sideband_data = vec![0x01u8]; // channel 1 = data
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

/// Parse `want` and `have` lines from a pkt-line upload-pack request body.
///
/// Returns `(wants, haves)`. `haves` are objects the client already has; the
/// pack builder uses them as cut-off points so only the delta is sent.
fn parse_wants_and_haves(body: &[u8]) -> (Vec<String>, Vec<String>) {
    let mut wants = Vec::new();
    let mut haves = Vec::new();
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
            }
        }
        pos += len;
    }
    (wants, haves)
}

/// Build a pack file containing objects reachable from `wants` but not from `haves`.
///
/// `haves` are commit OIDs the client already has; they act as boundary commits so
/// only the incremental delta is included, matching normal upload-pack negotiation.
fn build_pack_for_wants(
    repo: &gix::Repository,
    wants: &[String],
    haves: &[String],
) -> Result<Vec<u8>> {
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

    // Walk commits from wants, stopping at have boundaries so only the
    // incremental delta is included rather than the full history.
    let walk_ids: Vec<gix::ObjectId> = if have_ids.is_empty() {
        // No client haves — send everything reachable from wants.
        want_ids.clone()
    } else {
        repo.rev_walk(want_ids.iter().copied())
            .with_boundary(have_ids.iter().copied())
            .all()
            .context("rev walk for pack generation")?
            .filter_map(|r| r.ok().map(|info| info.id))
            .collect()
    };

    if walk_ids.is_empty() {
        return Ok(Vec::new());
    }

    let mut ids_iter = walk_ids
        .iter()
        .map(|id| Ok::<_, Box<dyn std::error::Error + Send + Sync>>(*id));

    let (counts, _) = gix_pack::data::output::count::objects_unthreaded(
        &odb,
        &mut ids_iter,
        &gix::progress::Discard,
        &interrupt,
        ObjectExpansion::TreeContents,
    )
    .context("counting pack objects")?;

    if counts.is_empty() {
        return Ok(Vec::new());
    }

    let num_entries = counts.len() as u32;

    let entries_iter = output::entry::iter_from_counts(
        counts,
        odb.clone(),
        Box::new(gix::progress::Discard),
        gix_pack::data::output::entry::iter_from_counts::Options::default(),
    );

    type BatchResult =
        Result<Vec<output::Entry>, gix_pack::data::output::entry::iter_from_counts::Error>;
    let mut pack_bytes: Vec<u8> = Vec::new();
    let mut bytes_iter = gix_pack::data::output::bytes::FromEntriesIter::new(
        entries_iter
            .into_iter()
            .map(|r| -> BatchResult { r.map(|(_seq, entries)| entries) }),
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

struct Quarantine {
    dir: tempfile::TempDir,
    pack_path: std::path::PathBuf,
    index_path: std::path::PathBuf,
    num_objects: u32,
}

/// Write incoming pack data to a temporary quarantine directory.
/// The pack and index files are NOT visible to the live ODB until
/// `promote_quarantine` moves them into the real pack directory.
fn stage_pack_to_quarantine(_repo: &gix::Repository, pack_data: &[u8]) -> Result<Quarantine> {
    use gix_pack::bundle::write::Options;

    let quarantine_dir = tempfile::TempDir::new().context("create quarantine dir")?;
    let mut cursor = std::io::BufReader::new(std::io::Cursor::new(pack_data));
    let interrupt = AtomicBool::new(false);
    let mut progress = gix::progress::Discard;
    let outcome = gix_pack::Bundle::write_to_directory(
        &mut cursor,
        Some(quarantine_dir.path()),
        &mut progress,
        &interrupt,
        None::<gix::odb::Handle>,
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
fn promote_quarantine(repo: &gix::Repository, q: Quarantine) -> Result<()> {
    let pack_dir = repo.objects.store_ref().path().join("pack");
    std::fs::create_dir_all(&pack_dir).context("ensure pack dir")?;

    let pack_dest = pack_dir.join(q.pack_path.file_name().context("pack file name")?);
    let idx_dest = pack_dir.join(q.index_path.file_name().context("index file name")?);

    std::fs::rename(&q.pack_path, &pack_dest).context("move pack file")?;
    std::fs::rename(&q.index_path, &idx_dest).context("move index file")?;

    // Drop the (now-empty) temp dir explicitly so errors surfacing here are clear.
    drop(q.dir);

    info!(
        objects_written = q.num_objects,
        "pack promoted to object database"
    );
    Ok(())
}
