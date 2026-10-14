// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::collections::{HashSet, VecDeque};
use std::path::PathBuf;
use std::sync::atomic::AtomicBool;
use std::time::Instant;

use anyhow::{Context, Result};
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
        let request = parse_upload_pack_request(body)?;
        let mut response = Vec::new();
        let history = if let Some(depth) = request.depth {
            anyhow::ensure!(!request.wants.is_empty(), "deepen requires wanted commits");
            let (commits, boundaries) =
                shallow_history(&repo, &request.wants, depth, self.max_pack_size)?;
            for boundary in &boundaries {
                write_pkt_line(&mut response, format!("shallow {boundary}\n").as_bytes())?;
            }
            for shallow in &request.shallows {
                if commits.contains(shallow) && !boundaries.contains(shallow) {
                    write_pkt_line(&mut response, format!("unshallow {shallow}\n").as_bytes())?;
                }
            }
            response.extend_from_slice(b"0000");
            Some(commits)
        } else {
            None
        };

        if request.wants.is_empty() || !request.done_seen {
            if history.is_some() {
                emit_span("upload-pack-rpc", &self.repo_path, start, "ok", 0);
                return Ok(response);
            }
            // NAK — nothing requested or negotiation still in progress
            write_pkt_line(&mut response, b"NAK\n")?;
            response.extend_from_slice(b"0000");
            emit_span("upload-pack-rpc", &self.repo_path, start, "ok", 0);
            return Ok(response);
        }

        // NAK then pack stream
        write_pkt_line(&mut response, b"NAK\n")?;

        let pack_data =
            build_pack_with_commits(&repo, &request.wants, &request.haves, history.as_deref())?;
        if history.is_some() {
            anyhow::ensure!(
                pack_data.len() as u64 <= self.max_pack_size,
                "shallow pack exceeds configured pack size"
            );
        }
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
        if name == "HEAD" || name == "refs/gitstore" || name.starts_with("refs/gitstore/") {
            continue;
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
#[derive(Default)]
struct UploadPackNegotiation {
    wants: Vec<String>,
    haves: Vec<String>,
    shallows: Vec<gix::ObjectId>,
    depth: Option<u32>,
    done_seen: bool,
}

fn parse_upload_pack_request(body: &[u8]) -> Result<UploadPackNegotiation> {
    let mut request = UploadPackNegotiation::default();
    let mut pos = 0;

    while pos < body.len() {
        anyhow::ensure!(pos + 4 <= body.len(), "truncated upload-pack packet");
        let len = usize::from_str_radix(std::str::from_utf8(&body[pos..pos + 4])?, 16)
            .context("invalid upload-pack packet length")?;
        if len == 0 {
            pos += 4;
            continue;
        }
        anyhow::ensure!(
            len >= 4 && pos + len <= body.len(),
            "invalid upload-pack packet bounds"
        );

        let line = std::str::from_utf8(&body[pos + 4..pos + len])?;
        let line = line.trim_end_matches('\n').split('\0').next().unwrap_or("");
        if let Some(rest) = line.strip_prefix("want ") {
            let oid = rest.split_whitespace().next().context("missing want ID")?;
            gix::ObjectId::from_hex(oid.as_bytes()).context("invalid want ID")?;
            request.wants.push(oid.to_owned());
        } else if let Some(rest) = line.strip_prefix("have ") {
            let oid = rest.split_whitespace().next().context("missing have ID")?;
            gix::ObjectId::from_hex(oid.as_bytes()).context("invalid have ID")?;
            request.haves.push(oid.to_owned());
        } else if let Some(rest) = line.strip_prefix("shallow ") {
            request
                .shallows
                .push(gix::ObjectId::from_hex(rest.as_bytes()).context("invalid shallow ID")?);
        } else if let Some(rest) = line.strip_prefix("deepen ") {
            let depth: u32 = rest.parse().context("invalid deepen depth")?;
            anyhow::ensure!(
                depth > 0 && request.depth.is_none(),
                "invalid deepen request"
            );
            request.depth = Some(depth);
        } else if line == "done" {
            request.done_seen = true;
        } else {
            anyhow::bail!("unsupported upload-pack negotiation line");
        }
        pos += len;
    }
    Ok(request)
}

fn shallow_history(
    repo: &gix::Repository,
    wants: &[String],
    depth: u32,
    max_pack_size: u64,
) -> Result<(Vec<gix::ObjectId>, Vec<gix::ObjectId>)> {
    let mut queue = VecDeque::new();
    for want in wants {
        let oid = gix::ObjectId::from_hex(want.as_bytes())?;
        let commit = repo.find_object(oid)?.peel_to_commit()?;
        queue.push_back((commit.id, 1));
    }
    let mut seen = HashSet::new();
    let mut commits = Vec::new();
    let mut boundaries = Vec::new();
    while let Some((oid, level)) = queue.pop_front() {
        if !seen.insert(oid) {
            continue;
        }
        anyhow::ensure!(
            seen.len() as u64 <= max_pack_size / 20,
            "shallow traversal exceeds configured pack size"
        );
        let commit = repo.find_commit(oid)?;
        let mut parents = commit.parent_ids().map(|id| id.detach());
        if level == depth {
            if parents.next().is_some() {
                boundaries.push(oid);
            }
        } else {
            queue.extend(parents.map(|parent| (parent, level + 1)));
        }
        commits.push(oid);
    }
    Ok((commits, boundaries))
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
    build_pack_with_commits(repo, wants, haves, None)
}

fn build_pack_with_commits(
    repo: &gix::Repository,
    wants: &[String],
    haves: &[String],
    commits: Option<&[gix::ObjectId]>,
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
    let walk_ids: Vec<gix::ObjectId> = if let Some(commits) = commits {
        commits.to_vec()
    } else {
        repo.rev_walk(commit_tips.iter().copied())
            .with_boundary(have_ids.iter().copied())
            .all()
            .context("rev walk for pack generation")?
            .map(|result| result.map(|info| info.id))
            .collect::<std::result::Result<_, _>>()
            .context("walking pack commits")?
    };

    if walk_ids.is_empty() && extra_objects.is_empty() {
        anyhow::bail!(
            "upload-pack: rev_walk produced no objects for {} want(s); \
             repository may be corrupt or wants are not reachable",
            want_ids.len()
        );
    }

    // Count commit-reachable objects (trees + blobs via TreeContents expansion).
    let mut ids_iter = walk_ids.iter().map(|id| Ok::<_, gix::Error>(*id));

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
        let mut tag_iter = extra_objects.iter().map(|id| Ok::<_, gix::Error>(*id));
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
    )
    .context("resolving pack entry locations")?;

    // Collect all entries first so we know the exact count before creating
    // FromEntriesIter. Passing an incorrect count causes an index-out-of-bounds
    // panic in gix-pack 0.71.0 when delta expansion inflates the entry count
    // beyond counts.len().
    type BatchResult = Result<Vec<output::Entry>, gix::Error>;
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
        all_entries.into_iter().map(Ok::<EntryBatch, gix::Error>),
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

pub struct Quarantine {
    pub dir: tempfile::TempDir,
    pub pack_path: std::path::PathBuf,
    pub index_path: std::path::PathBuf,
    pub num_objects: u32,
}

/// A `Read` impl that wraps another reader and returns an error when a byte limit is exceeded.
pub struct LimitedReader<R: std::io::Read> {
    inner: R,
    limit: u64,
    read_so_far: u64,
}

impl<R: std::io::Read> LimitedReader<R> {
    pub fn new(inner: R, limit: u64) -> Self {
        Self {
            inner,
            limit,
            read_so_far: 0,
        }
    }
}

impl<R: std::io::Read> std::io::Read for LimitedReader<R> {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        let n = self.inner.read(buf)?;
        self.read_so_far += n as u64;
        if self.read_so_far > self.limit {
            return Err(std::io::Error::other(format!(
                "pack size limit exceeded: read {} bytes, limit is {} bytes",
                self.read_so_far, self.limit
            )));
        }
        Ok(n)
    }
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
        gix::hash::Kind::Sha1,
        Options {
            thread_limit: Some(1),
            iteration_mode: gix_pack::data::input::Mode::Verify,
            index_version: gix_pack::index::Version::V2,
            ..Default::default()
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

/// Check that no blob in the quarantined pack exceeds `max_file_size_bytes`.
/// Returns an error naming the first offending object if the limit is exceeded.
/// A limit of 0 means "no limit enforced".
pub fn check_blob_sizes_in_quarantine(q: &Quarantine, max_file_size_bytes: u64) -> Result<()> {
    check_blob_sizes_in_quarantine_paths(&q.pack_path, &q.index_path, max_file_size_bytes)
}

/// Path-based variant of [`check_blob_sizes_in_quarantine`] — used from async contexts
/// where passing `&Quarantine` across `spawn_blocking` is not possible.
pub fn check_blob_sizes_in_quarantine_paths(
    pack_path: &std::path::Path,
    index_path: &std::path::Path,
    max_file_size_bytes: u64,
) -> Result<()> {
    if max_file_size_bytes == 0 {
        return Ok(());
    }

    let pack_file = gix_pack::data::File::at(pack_path, gix::hash::Kind::Sha1)
        .with_context(|| format!("open quarantine pack {}", pack_path.display()))?;
    let index_file = gix_pack::index::File::at(index_path, gix::hash::Kind::Sha1)
        .with_context(|| format!("open quarantine index {}", index_path.display()))?;

    for entry in index_file.iter() {
        let pack_entry = pack_file
            .entry(entry.pack_offset)
            .with_context(|| format!("read pack entry at offset {}", entry.pack_offset))?;
        // Check non-delta blobs directly. Delta entries (OFS_DELTA / REF_DELTA) that
        // resolve to blobs must be decoded to obtain the true object size; we use
        // decompressed_size only for the base-object (Blob) case where it IS the
        // uncompressed blob size.
        match pack_entry.header {
            gix_pack::data::entry::Header::Blob => {
                if pack_entry.decompressed_size > max_file_size_bytes {
                    anyhow::bail!(
                        "blob {} has size {} bytes which exceeds the {} byte file size limit",
                        entry.oid,
                        pack_entry.decompressed_size,
                        max_file_size_bytes
                    );
                }
            }
            gix_pack::data::entry::Header::OfsDelta { .. }
            | gix_pack::data::entry::Header::RefDelta { .. } => {
                // Delta-encoded entry: decode to resolve the true object size and kind.
                let mut out = Vec::new();
                let mut inflate = gix::zlib::Inflate::default();
                let outcome = pack_file
                    .decode_entry(
                        pack_entry,
                        &mut out,
                        &mut inflate,
                        &|_, _| Ok(None),
                        &mut gix_pack::cache::Never,
                    )
                    .with_context(|| {
                        format!("decode delta entry at offset {}", entry.pack_offset)
                    })?;
                if outcome.kind == gix::object::Kind::Blob
                    && outcome.object_size > max_file_size_bytes
                {
                    anyhow::bail!(
                        "blob {} has size {} bytes which exceeds the {} byte file size limit",
                        entry.oid,
                        outcome.object_size,
                        max_file_size_bytes
                    );
                }
            }
            _ => {}
        }
    }
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
    // T050 – T053: upload-pack negotiation
    // -----------------------------------------------------------------------

    #[test]
    fn t050_parse_wants_and_haves_want_plus_done() {
        let oid = "a".repeat(40);
        let want_line = format!("want {}\n", oid);
        let mut body = pkt_lines(&[want_line.as_bytes(), b"done\n"]);
        flush(&mut body);

        let request = parse_upload_pack_request(&body).expect("parse request");
        assert_eq!(request.wants, vec![oid]);
        assert!(request.haves.is_empty());
        assert!(
            request.done_seen,
            "done_seen must be true when 'done' line present"
        );
    }

    #[test]
    fn t051_parse_wants_and_haves_done_absent() {
        let oid = "b".repeat(40);
        let want_line = format!("want {}\n", oid);
        let mut body = pkt_lines(&[want_line.as_bytes()]);
        flush(&mut body);

        let request = parse_upload_pack_request(&body).expect("parse request");
        assert_eq!(request.wants, vec![oid]);
        assert!(request.haves.is_empty());
        assert!(
            !request.done_seen,
            "done_seen must be false when 'done' absent"
        );
    }

    #[test]
    fn t052_parse_wants_and_haves_empty_body() {
        let request = parse_upload_pack_request(b"").expect("parse empty request");
        assert!(request.wants.is_empty());
        assert!(request.haves.is_empty());
        assert!(!request.done_seen);
    }

    #[test]
    fn t053_parse_wants_and_haves_caps_stripped() {
        // capability string after \0 must not bleed into the OID
        let oid = "c".repeat(40);
        let want_line = format!("want {}\0multi_ack side-band-64k\n", oid);
        let mut body = pkt_lines(&[want_line.as_bytes(), b"done\n"]);
        flush(&mut body);

        let request = parse_upload_pack_request(&body).expect("parse request");
        assert_eq!(
            request.wants,
            vec![oid],
            "capability suffix must be stripped"
        );
        assert!(request.done_seen);
    }

    #[test]
    fn shallow_negotiation_rejects_malformed_packets_and_depths() {
        for body in [
            b"0001".to_vec(),
            b"0009x".to_vec(),
            pkt_lines(&[b"deepen 0\n"]),
            pkt_lines(&[b"deepen -1\n"]),
            pkt_lines(&[b"deepen 1\n", b"deepen 2\n"]),
            pkt_lines(&[b"shallow not-an-object-id\n"]),
        ] {
            assert!(parse_upload_pack_request(&body).is_err());
        }
    }

    #[test]
    fn shallow_handshake_precedes_nak_and_bounds_history() {
        let (_dir, repo) = make_test_repo();
        let root = make_commit(&repo, "root", None);
        let parent = make_commit(&repo, "parent", Some(root));
        let tip = make_commit(&repo, "tip", Some(parent));
        let server = HttpPackServer::new(repo.path().to_path_buf(), 64 * 1024 * 1024);
        let want = format!("want {tip} side-band-64k shallow\n");
        let mut first = pkt_lines(&[want.as_bytes(), b"deepen 1\n"]);
        flush(&mut first);
        let response = server
            .handle_upload_pack(&first)
            .expect("shallow handshake");
        let mut expected = pkt_lines(&[format!("shallow {tip}\n").as_bytes()]);
        flush(&mut expected);
        assert_eq!(
            response, expected,
            "negotiation cannot send NAK where shallow lines are required"
        );
        let (commits, boundaries) =
            shallow_history(&repo, &[tip.to_string()], 2, server.max_pack_size).unwrap();
        assert_eq!(commits, vec![tip, parent]);
        assert_eq!(boundaries, vec![parent]);
        let (commits, boundaries) =
            shallow_history(&repo, &[tip.to_string()], 3, server.max_pack_size).unwrap();
        assert_eq!(commits, vec![tip, parent, root]);
        assert!(boundaries.is_empty());
        assert!(shallow_history(&repo, &[tip.to_string()], 3, 20).is_err());
        let mut final_request = first;
        final_request.extend_from_slice(&pkt_lines(&[b"done\n"]));
        let packed = server
            .handle_upload_pack(&final_request)
            .expect("shallow pack");
        assert!(packed.starts_with(&expected));
        assert!(packed[expected.len()..].starts_with(b"0008NAK\n"));
    }

    #[test]
    fn real_git_shallow_clone_deepen_and_full_fetch() {
        use std::io::{BufRead, BufReader, Read, Write};
        use std::net::TcpListener;
        use std::process::Command;
        use std::sync::Arc;
        use std::thread;
        use std::time::Duration;

        let (_dir, repo) = make_test_repo();
        let root = make_commit(&repo, "root", None);
        let parent = make_commit(&repo, "parent", Some(root));
        let tip = make_commit(&repo, "tip", Some(parent));
        let output = Command::new("git")
            .args(["--git-dir"])
            .arg(repo.path())
            .args(["symbolic-ref", "HEAD", "refs/heads/main"])
            .output()
            .expect("set fixture HEAD");
        assert!(output.status.success());
        let listener = TcpListener::bind("127.0.0.1:0").expect("HTTP fixture");
        listener.set_nonblocking(true).unwrap();
        let remote = format!("http://{}/fixture.git", listener.local_addr().unwrap());
        let stop = Arc::new(AtomicBool::new(false));
        let server_stop = Arc::clone(&stop);
        let path = repo.path().to_path_buf();
        let worker = thread::spawn(move || -> Result<()> {
            let server = HttpPackServer::new(path, 64 * 1024 * 1024);
            while !server_stop.load(std::sync::atomic::Ordering::Acquire) {
                let (mut stream, _) = match listener.accept() {
                    Ok(connection) => connection,
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(5));
                        continue;
                    }
                    Err(error) => return Err(error.into()),
                };
                stream.set_nonblocking(false)?;
                stream.set_read_timeout(Some(Duration::from_secs(2)))?;
                let mut reader = BufReader::new(&mut stream);
                let mut line = String::new();
                reader.read_line(&mut line)?;
                let advertised = line.starts_with("GET ");
                let mut length = 0;
                loop {
                    line.clear();
                    anyhow::ensure!(reader.read_line(&mut line)? > 0, "incomplete HTTP header");
                    if line == "\r\n" {
                        break;
                    }
                    if let Some(value) = line.to_ascii_lowercase().strip_prefix("content-length:") {
                        length = value.trim().parse::<usize>()?;
                    }
                    anyhow::ensure!(
                        line.len() <= 65536 && length <= 65536,
                        "oversized fixture request"
                    );
                }
                let mut body = vec![0; length];
                reader.read_exact(&mut body)?;
                drop(reader);
                let response = if advertised {
                    server.advertise_upload_pack_refs()?
                } else {
                    server.handle_upload_pack(&body)?
                };
                let content_type = if advertised {
                    "advertisement"
                } else {
                    "result"
                };
                write!(
                    stream,
                    "HTTP/1.1 200 OK\r\nContent-Type: application/x-git-upload-pack-{content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                    response.len()
                )?;
                stream.write_all(&response)?;
            }
            Ok(())
        });
        struct StopFixture(Arc<AtomicBool>, Option<thread::JoinHandle<Result<()>>>);
        impl Drop for StopFixture {
            fn drop(&mut self) {
                self.0.store(true, std::sync::atomic::Ordering::Release);
                if let Some(worker) = self.1.take() {
                    let result = worker.join().expect("HTTP fixture thread");
                    if !thread::panicking() {
                        result.expect("HTTP fixture request");
                    } else if let Err(error) = result {
                        eprintln!("HTTP fixture failed: {error:#}");
                    }
                }
            }
        }
        let _fixture = StopFixture(stop, Some(worker));
        let checkout = tempfile::TempDir::new().unwrap();
        let git = |args: &[&str]| {
            let output = Command::new("git")
                .env("GIT_CONFIG_COUNT", "0")
                .env("GIT_CONFIG_GLOBAL", "/dev/null")
                .args(["-C"])
                .arg(checkout.path())
                .args(args)
                .output()
                .expect("standard Git client");
            assert!(
                output.status.success(),
                "Git {:?}: {}",
                args,
                String::from_utf8_lossy(&output.stderr)
            );
            String::from_utf8(output.stdout).unwrap().trim().to_owned()
        };
        git(&["clone", "--quiet", "--depth=1", &remote, "."]);
        assert_eq!(git(&["rev-list", "--count", "HEAD"]), "1");
        let excluded = Command::new("git")
            .args(["-C"])
            .arg(checkout.path())
            .args(["cat-file", "-e", &parent.to_string()])
            .output()
            .expect("check excluded parent");
        assert!(
            !excluded.status.success(),
            "depth-one pack must exclude parent objects"
        );
        assert_eq!(
            std::fs::read_to_string(checkout.path().join(".git/shallow"))
                .unwrap()
                .trim(),
            tip.to_string()
        );
        git(&["fetch", "--quiet", "--depth=2", "origin", "main"]);
        assert_eq!(git(&["rev-list", "--count", "HEAD"]), "2");
        let excluded = Command::new("git")
            .args(["-C"])
            .arg(checkout.path())
            .args(["cat-file", "-e", &root.to_string()])
            .output()
            .expect("check excluded root");
        assert!(
            !excluded.status.success(),
            "depth-two pack must exclude root objects"
        );
        assert_eq!(
            std::fs::read_to_string(checkout.path().join(".git/shallow"))
                .unwrap()
                .trim(),
            parent.to_string()
        );
        git(&["fetch", "--quiet", "--depth=3", "origin", "main"]);
        assert_eq!(git(&["rev-list", "--count", "HEAD"]), "3");
        assert!(!checkout.path().join(".git/shallow").exists());
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
