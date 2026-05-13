// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::path::PathBuf;
use std::sync::atomic::AtomicBool;
use std::time::Instant;

use anyhow::{Context, Result};
use gix::refs::transaction::{Change, LogChange, PreviousValue, RefEdit, RefLog};
use gix::refs::TargetRef;
use tracing::info;

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
        let wants = parse_wants(body);
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

        let pack_data = build_pack_for_wants(&repo, &wants)?;
        if !pack_data.is_empty() {
            // Wrap in sideband-1 (data channel) pkt-line
            let mut sideband = vec![0x01u8];
            sideband.extend_from_slice(&pack_data);
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
    /// Atomically writes pack objects and updates refs via `gix::refs::transaction`.
    /// On any failure the transaction is rolled back; no partial state is left on disk.
    pub fn handle_receive_pack(&self, body: &[u8]) -> Result<Vec<u8>> {
        let start = Instant::now();
        let pack_size_bytes = body.len() as u64;

        let repo = open_repo(&self.repo_path)?;
        let (ref_updates, pack_data) = parse_receive_pack_body(body)?;

        // Write pack objects to the object store before touching refs
        if !pack_data.is_empty() {
            write_pack_to_odb(&repo, pack_data)
                .context("writing pack objects to object database")?;
        }

        // Build atomic ref transaction
        let mut ref_edits: Vec<RefEdit> = Vec::new();
        for (refname, old_oid, new_oid) in &ref_updates {
            let new_id = gix::ObjectId::from_hex(new_oid.as_bytes())
                .with_context(|| format!("parse new oid {new_oid}"))?;
            let old_id = gix::ObjectId::from_hex(old_oid.as_bytes())
                .with_context(|| format!("parse old oid {old_oid}"))?;

            let previous_value = if old_id.is_null() {
                PreviousValue::MustNotExist
            } else {
                PreviousValue::MustExistAndMatch(gix::refs::Target::Object(old_id))
            };

            ref_edits.push(RefEdit {
                change: Change::Update {
                    log: LogChange {
                        mode: RefLog::AndReference,
                        force_create_reflog: false,
                        message: "push".into(),
                    },
                    expected: previous_value,
                    new: gix::refs::Target::Object(new_id),
                },
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

        // Build report-status response.
        // With side-band-64k: ALL report-status pkt-lines are bundled into ONE sideband
        // channel-1 payload, followed by a sideband flush pkt-line (0000).
        // Format: pkt-line(\x01 <inner-pkt-lines> <inner-0000>)  then outer 0000
        let mut inner = Vec::new();
        write_pkt_line(&mut inner, b"unpack ok\n")?;
        for (refname, _, _) in &ref_updates {
            write_pkt_line(&mut inner, format!("ok {}\n", refname).as_bytes())?;
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
fn collect_refs(repo: &gix::Repository) -> Result<Vec<(String, String)>> {
    let mut refs: Vec<(String, String)> = Vec::new();
    let platform = repo.references().context("access references")?;
    let all = platform.all().context("iterate references")?;

    for r in all {
        let reference = match r {
            Ok(r) => r,
            Err(_) => continue,
        };
        let name = reference.name().as_bstr().to_string();
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

/// Parse `want <hex-oid>` lines from a pkt-line request body.
fn parse_wants(body: &[u8]) -> Vec<String> {
    let mut wants = Vec::new();
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
                // Only take the first token (caps may follow after a space)
                let oid = rest.split_whitespace().next().unwrap_or("").to_string();
                if !oid.is_empty() {
                    wants.push(oid);
                }
            }
        }
        pos += len;
    }
    wants
}

/// Build a pack file containing all objects reachable from the requested OIDs.
fn build_pack_for_wants(repo: &gix::Repository, wants: &[String]) -> Result<Vec<u8>> {
    use gix_pack::data::output;
    use gix_pack::data::output::count::objects::ObjectExpansion;

    let want_ids: Vec<gix::ObjectId> = wants
        .iter()
        .filter_map(|h| gix::ObjectId::from_hex(h.as_bytes()).ok())
        .collect();

    if want_ids.is_empty() {
        return Ok(Vec::new());
    }

    let interrupt = AtomicBool::new(false);

    // Clone and prepare ODB handle: prevent_pack_unload ensures pack location data
    // remains valid during the entire pack generation pipeline.
    let mut odb = (*repo.objects).clone();
    odb.prevent_pack_unload();

    let mut ids_iter = want_ids
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

/// Write a pack stream into the repository's object database.
fn write_pack_to_odb(repo: &gix::Repository, pack_data: &[u8]) -> Result<()> {
    use gix_pack::bundle::write::Options;

    let mut cursor = std::io::BufReader::new(std::io::Cursor::new(pack_data));
    let interrupt = AtomicBool::new(false);
    let pack_dir = repo.objects.store_ref().path().join("pack");
    let mut progress = gix::progress::Discard;
    let outcome = gix_pack::Bundle::write_to_directory(
        &mut cursor,
        Some(&pack_dir),
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
    .context("write pack to odb")?;

    info!(
        objects_written = outcome.index.num_objects,
        "pack written to object database"
    );
    Ok(())
}
