// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::collections::{HashSet, VecDeque};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
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
        self.handle_upload_pack_with_interrupt(body, &AtomicBool::new(false))
    }

    pub(crate) fn handle_upload_pack_with_interrupt(
        &self,
        body: &[u8],
        interrupt: &AtomicBool,
    ) -> Result<Vec<u8>> {
        let start = Instant::now();
        check_upload_pack_interrupt(interrupt)?;
        let repo = open_repo(&self.repo_path)?;
        let request = parse_upload_pack_request(body)?;
        // Existing gRPC callers use zero for an unspecified output limit.
        let max_pack_size = if self.max_pack_size == 0 {
            u64::MAX
        } else {
            self.max_pack_size
        };
        let mut response = Vec::new();
        let history = if let Some(depth) = request.depth {
            anyhow::ensure!(!request.wants.is_empty(), "deepen requires wanted commits");
            let (commits, boundaries) =
                shallow_history(&repo, &request.wants, depth, max_pack_size, interrupt)?;
            for boundary in &boundaries {
                check_upload_pack_interrupt(interrupt)?;
                write_pkt_line(&mut response, format!("shallow {boundary}\n").as_bytes())?;
            }
            for shallow in &request.shallows {
                check_upload_pack_interrupt(interrupt)?;
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
            if history.is_none() || !request.haves.is_empty() {
                write_upload_pack_acknowledgements(&repo, &request, &mut response, interrupt)?;
            }
            emit_span("upload-pack-rpc", &self.repo_path, start, "ok", 0);
            return Ok(response);
        }

        write_upload_pack_acknowledgements(&repo, &request, &mut response, interrupt)?;

        let pack_data = build_pack_with_commits(
            &repo,
            &request.wants,
            &request.haves,
            history.as_deref(),
            max_pack_size,
            interrupt,
        )?;
        // Chunk pack into sideband pkt-lines: pkt-line max data = 65512 bytes,
        // minus 1 sideband-channel byte leaves 65511 bytes of pack per packet.
        const SIDEBAND_CHUNK: usize = 65511;
        for chunk in pack_data.chunks(SIDEBAND_CHUNK) {
            check_upload_pack_interrupt(interrupt)?;
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

#[derive(Clone, Copy, Default)]
enum UploadPackAckMode {
    #[default]
    Single,
    Multi,
    Detailed,
}

#[derive(Default)]
struct UploadPackNegotiation {
    wants: Vec<String>,
    haves: Vec<String>,
    shallows: Vec<gix::ObjectId>,
    depth: Option<u32>,
    done_seen: bool,
    ack_mode: UploadPackAckMode,
}

/// Compatibility wrapper; transport handlers use the fallible parser directly.
pub fn parse_wants_and_haves(body: &[u8]) -> (Vec<String>, Vec<String>, bool) {
    let request = parse_upload_pack_request(body).unwrap_or_default();
    (request.wants, request.haves, request.done_seen)
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
            (4..=65520).contains(&len) && len <= body.len() - pos,
            "invalid upload-pack packet bounds"
        );
        let line = std::str::from_utf8(&body[pos + 4..pos + len])?;
        let line = line.trim_end_matches('\n');
        let (line, nul_caps) = line.split_once('\0').unwrap_or((line, ""));
        if let Some(rest) = line.strip_prefix("want ") {
            let mut fields = rest.split_whitespace();
            let oid = fields.next().context("missing want ID")?;
            gix::ObjectId::from_hex(oid.as_bytes()).context("invalid want ID")?;
            if request.wants.is_empty() {
                for capability in fields.chain(nul_caps.split_whitespace()) {
                    match capability {
                        "multi_ack_detailed" => request.ack_mode = UploadPackAckMode::Detailed,
                        "multi_ack" if !matches!(request.ack_mode, UploadPackAckMode::Detailed) => {
                            request.ack_mode = UploadPackAckMode::Multi;
                        }
                        _ => {}
                    }
                }
            }
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

fn write_upload_pack_acknowledgements(
    repo: &gix::Repository,
    request: &UploadPackNegotiation,
    response: &mut Vec<u8>,
    interrupt: &AtomicBool,
) -> Result<()> {
    check_upload_pack_interrupt(interrupt)?;
    let mut common = None;
    if !request.wants.is_empty() {
        for have in &request.haves {
            check_upload_pack_interrupt(interrupt)?;
            let id = gix::ObjectId::from_hex(have.as_bytes())?;
            let object = repo
                .try_find_object(id)
                .context("resolve offered fetch common commit")?;
            if object.is_none_or(|object| object.kind != gix::object::Kind::Commit) {
                continue;
            }
            common = Some(id);
            let status = match request.ack_mode {
                UploadPackAckMode::Single => {
                    write_pkt_line(response, format!("ACK {id}\n").as_bytes())?;
                    return Ok(());
                }
                UploadPackAckMode::Multi => "continue",
                UploadPackAckMode::Detailed => "common",
            };
            if !request.done_seen {
                write_pkt_line(response, format!("ACK {id} {status}\n").as_bytes())?;
            }
        }
    }
    if let Some(id) = common.filter(|_| request.done_seen) {
        write_pkt_line(response, format!("ACK {id}\n").as_bytes())?;
    } else {
        // Common ancestry alone is not proof that every wanted history is complete.
        write_pkt_line(response, b"NAK\n")?;
    }
    Ok(())
}

fn check_upload_pack_interrupt(interrupt: &AtomicBool) -> Result<()> {
    anyhow::ensure!(!interrupt.load(Ordering::Acquire), "upload-pack cancelled");
    Ok(())
}

fn shallow_history(
    repo: &gix::Repository,
    wants: &[String],
    depth: u32,
    max_pack_size: u64,
    interrupt: &AtomicBool,
) -> Result<(Vec<gix::ObjectId>, Vec<gix::ObjectId>)> {
    let mut queue = VecDeque::new();
    for want in wants {
        check_upload_pack_interrupt(interrupt)?;
        let oid = gix::ObjectId::from_hex(want.as_bytes())?;
        let commit = repo.find_object(oid)?.peel_to_commit()?;
        queue.push_back((commit.id, 1));
    }
    let mut seen = HashSet::new();
    let mut commits = Vec::new();
    let mut boundaries = Vec::new();
    while let Some((oid, level)) = queue.pop_front() {
        check_upload_pack_interrupt(interrupt)?;
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
    build_pack_with_commits(repo, wants, haves, None, u64::MAX, &AtomicBool::new(false))
}

fn build_pack_with_commits(
    repo: &gix::Repository,
    wants: &[String],
    haves: &[String],
    commits: Option<&[gix::ObjectId]>,
    max_pack_size: u64,
    interrupt: &AtomicBool,
) -> Result<Vec<u8>> {
    use gix::object::Kind;
    use gix_pack::data::output;
    use gix_pack::data::output::count::objects::ObjectExpansion;

    check_upload_pack_interrupt(interrupt)?;
    let want_ids: Vec<gix::ObjectId> = wants
        .iter()
        .map(|h| gix::ObjectId::from_hex(h.as_bytes()))
        .collect::<Result<_, _>>()?;

    if want_ids.is_empty() {
        return Ok(Vec::new());
    }

    anyhow::ensure!(max_pack_size >= 32, "pack exceeds configured pack size");
    let mut have_ids = Vec::new();
    for have in haves {
        check_upload_pack_interrupt(interrupt)?;
        let id = gix::ObjectId::from_hex(have.as_bytes())?;
        if repo
            .try_find_object(id)
            .context("resolve offered fetch common commit")?
            .is_some_and(|object| object.kind == Kind::Commit)
        {
            have_ids.push(id);
        }
    }

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
        check_upload_pack_interrupt(interrupt)?;
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
    let walk_ids = if let Some(commits) = commits {
        commits.to_vec()
    } else {
        let walk = repo
            .rev_walk(commit_tips.iter().copied())
            .with_boundary(have_ids.iter().copied())
            // A common commit proves an ancestry boundary, not a timestamp cutoff.
            .sorting(gix::revision::walk::Sorting::BreadthFirst)
            .all()
            .context("rev walk for pack generation")?;
        let mut ids = Vec::new();
        for result in walk {
            check_upload_pack_interrupt(interrupt)?;
            ids.push(result.context("walking pack commits")?.id);
            anyhow::ensure!(
                ids.len() as u64 <= max_pack_size / 20,
                "pack traversal exceeds configured pack size"
            );
        }
        ids
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
        interrupt,
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
            interrupt,
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

    // Collect actual entry counts: never assume reused delta expansion matches counts.len().
    type BatchResult = Result<Vec<output::Entry>, gix::Error>;
    let mut minimum_pack_bytes = 32u64;
    let all_entries: Vec<Vec<output::Entry>> = entries_iter
        .into_iter()
        .map(|r| -> BatchResult {
            let checked = (|| -> Result<_> {
                check_upload_pack_interrupt(interrupt)?;
                let (_sequence, entries) = r?;
                for entry in &entries {
                    check_upload_pack_interrupt(interrupt)?;
                    anyhow::ensure!(!entry.is_invalid(), "counted pack object is unavailable");
                    minimum_pack_bytes = minimum_pack_bytes
                        .checked_add(entry.compressed_data.len() as u64)
                        .and_then(|size| size.checked_add(1))
                        .context("pack size accounting overflow")?;
                    anyhow::ensure!(
                        minimum_pack_bytes <= max_pack_size,
                        "pack exceeds configured pack size"
                    );
                }
                Ok(entries)
            })();
            checked.map_err(|error| gix::Error::from_boxed(error.into_boxed_dyn_error()))
        })
        .collect::<Result<_, _>>()
        .map_err(|e| anyhow::anyhow!("pack entry collection error: {e}"))?;
    let num_entries = u32::try_from(all_entries.iter().map(Vec::len).sum::<usize>())
        .context("too many pack entries")?;

    if num_entries == 0 {
        return Ok(Vec::new());
    }

    let mut writer = LimitedPackWriter {
        bytes: Vec::new(),
        limit: max_pack_size,
        interrupt,
    };
    type EntryBatch = Vec<output::Entry>;
    let mut bytes_iter = gix_pack::data::output::bytes::FromEntriesIter::new(
        all_entries.into_iter().map(Ok::<EntryBatch, gix::Error>),
        &mut writer,
        num_entries,
        gix_pack::data::Version::V2,
        gix::hash::Kind::Sha1,
    );

    loop {
        check_upload_pack_interrupt(interrupt)?;
        match bytes_iter.next() {
            Some(Ok(_)) => {}
            Some(Err(e)) => return Err(anyhow::anyhow!("pack generation error: {e}")),
            None => break,
        }
    }

    drop(bytes_iter);
    Ok(writer.bytes)
}

struct LimitedPackWriter<'a> {
    bytes: Vec<u8>,
    limit: u64,
    interrupt: &'a AtomicBool,
}

impl std::io::Write for LimitedPackWriter<'_> {
    fn write(&mut self, buffer: &[u8]) -> std::io::Result<usize> {
        check_upload_pack_interrupt(self.interrupt)
            .map_err(|error| std::io::Error::other(error.to_string()))?;
        if buffer.len() as u64 > self.limit.saturating_sub(self.bytes.len() as u64) {
            return Err(std::io::Error::new(
                std::io::ErrorKind::FileTooLarge,
                "pack exceeds configured pack size",
            ));
        }
        self.bytes.extend_from_slice(buffer);
        Ok(buffer.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        self.write(&[]).map(|_| ())
    }
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
    fn t054_handle_upload_pack_empty_wants_returns_nak() {
        let (_dir, repo) = make_test_repo();
        let server = HttpPackServer {
            repo_path: repo.path().to_path_buf(),
            max_pack_size: 64 * 1024 * 1024,
        };

        let body = b""; // no wants, no done
        let resp = server.handle_upload_pack(body).expect("handle_upload_pack");

        let mut expected = Vec::new();
        write_pkt_line(&mut expected, b"NAK\n").unwrap();
        assert_eq!(resp, expected, "NAK terminates a negotiation response");
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
    fn t056_handle_upload_pack_wants_no_done_returns_nak() {
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
        assert_eq!(resp, expected, "NAK terminates a negotiation response");
    }

    // -----------------------------------------------------------------------
    // T057 – T059: build_pack_for_wants
    // -----------------------------------------------------------------------

    #[test]
    fn shallow_negotiation_rejects_malformed_packets_and_depths() {
        let (_dir, repo) = make_test_repo();
        let server = HttpPackServer::new(repo.path().to_path_buf(), 64 * 1024 * 1024);
        for body in [
            b"0001".to_vec(),
            b"0009x".to_vec(),
            b"000".to_vec(),
            pkt_lines(&[b"deepen 0\n"]),
            pkt_lines(&[b"deepen -1\n"]),
            pkt_lines(&[b"deepen 1\n", b"deepen 2\n"]),
            pkt_lines(&[b"shallow not-an-object-id\n"]),
            pkt_lines(&[b"want not-an-object-id\n"]),
            pkt_lines(&[b"have not-an-object-id\n"]),
        ] {
            assert!(server.handle_upload_pack(&body).is_err(), "{body:?}");
        }
    }

    #[test]
    fn shallow_handshake_precedes_ack_and_bounds_history() {
        let (_dir, repo) = make_test_repo();
        let root = make_commit(&repo, "root", None);
        let parent = make_commit(&repo, "parent", Some(root));
        let tip = make_commit(&repo, "tip", Some(parent));
        let server = HttpPackServer::new(repo.path().to_path_buf(), 64 * 1024 * 1024);
        let want = format!("want {tip} side-band-64k shallow\n");
        let mut first = pkt_lines(&[want.as_bytes(), b"deepen 1\n"]);
        flush(&mut first);
        let mut expected = pkt_lines(&[format!("shallow {tip}\n").as_bytes()]);
        flush(&mut expected);
        assert_eq!(server.handle_upload_pack(&first).unwrap(), expected);
        let interrupt = AtomicBool::new(false);
        let (commits, boundaries) = shallow_history(
            &repo,
            &[tip.to_string()],
            2,
            server.max_pack_size,
            &interrupt,
        )
        .unwrap();
        assert_eq!(commits, vec![tip, parent]);
        assert_eq!(boundaries, vec![parent]);
        let (commits, boundaries) = shallow_history(
            &repo,
            &[tip.to_string()],
            3,
            server.max_pack_size,
            &interrupt,
        )
        .unwrap();
        assert_eq!(commits, vec![tip, parent, root]);
        assert!(boundaries.is_empty());
        assert!(shallow_history(&repo, &[tip.to_string()], 3, 20, &interrupt).is_err());
        first.extend_from_slice(&pkt_lines(&[b"done\n"]));
        let packed = server.handle_upload_pack(&first).unwrap();
        assert!(packed.starts_with(&expected));
        assert!(packed[expected.len()..].starts_with(b"0008NAK\n"));
    }

    #[test]
    fn upload_pack_preserves_output_limits_and_cancellation() {
        let (_dir, repo) = make_test_repo();
        let tip = make_commit(&repo, "snapshot", None);
        for depth in ["", "deepen 1\n"] {
            let mut request =
                pkt_lines(&[format!("want {tip} side-band-64k shallow\n").as_bytes()]);
            if !depth.is_empty() {
                request.extend(pkt_lines(&[depth.as_bytes()]));
            }
            flush(&mut request);
            request.extend(pkt_lines(&[b"done\n"]));
            let pack = build_pack_for_wants(&repo, &[tip.to_string()], &[]).unwrap();
            let exact = HttpPackServer::new(repo.path().to_path_buf(), pack.len() as u64);
            let response = exact.handle_upload_pack(&request).unwrap();
            let too_small = HttpPackServer::new(repo.path().to_path_buf(), pack.len() as u64 - 1);
            let error = too_small.handle_upload_pack(&request).unwrap_err();
            assert!(
                format!("{error:#}").contains("pack exceeds configured pack size"),
                "{error:#}"
            );
            let unspecified = HttpPackServer::new(repo.path().to_path_buf(), 0);
            assert_eq!(unspecified.handle_upload_pack(&request).unwrap(), response);
            assert!(unspecified
                .handle_upload_pack_with_interrupt(&request, &AtomicBool::new(true))
                .unwrap_err()
                .to_string()
                .contains("upload-pack cancelled"));
        }
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn real_git_shallow_clone_deepen_and_common_commit_fetch() {
        use axum::extract::State;
        use axum::http::HeaderMap;
        use axum::routing::{get, post};
        use std::io::Write;
        use std::process::{Command, Stdio};
        use std::sync::atomic::AtomicUsize;
        use std::sync::Arc;

        type Fixture = (PathBuf, Arc<AtomicUsize>);
        async fn advertise(State((path, _)): State<Fixture>) -> impl axum::response::IntoResponse {
            (
                [(
                    "content-type",
                    "application/x-git-upload-pack-advertisement",
                )],
                HttpPackServer::new(path, 64 * 1024 * 1024)
                    .advertise_upload_pack_refs()
                    .unwrap(),
            )
        }
        async fn upload(
            State((path, negotiations)): State<Fixture>,
            headers: HeaderMap,
            body: axum::body::Bytes,
        ) -> impl axum::response::IntoResponse {
            negotiations.fetch_add(1, Ordering::Relaxed);
            let body = if headers
                .get("content-encoding")
                .is_some_and(|value| value == "gzip")
            {
                let mut decoder = Command::new("gzip")
                    .arg("-dc")
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .spawn()
                    .unwrap();
                decoder.stdin.take().unwrap().write_all(&body).unwrap();
                let decoded = decoder.wait_with_output().unwrap();
                assert!(decoded.status.success() && decoded.stdout.len() <= 65536);
                decoded.stdout
            } else {
                body.to_vec()
            };
            (
                [("content-type", "application/x-git-upload-pack-result")],
                HttpPackServer::new(path, 64 * 1024 * 1024)
                    .handle_upload_pack(&body)
                    .unwrap(),
            )
        }
        let git = |directory: &std::path::Path, args: &[&str]| {
            let output = Command::new("git")
                .env("GIT_CONFIG_COUNT", "0")
                .env("GIT_CONFIG_GLOBAL", "/dev/null")
                .env("GIT_CONFIG_NOSYSTEM", "1")
                .args(["-C"])
                .arg(directory)
                .args(args)
                .output()
                .expect("standard Git client");
            assert!(
                output.status.success(),
                "Git {args:?}: {}",
                String::from_utf8_lossy(&output.stderr)
            );
            String::from_utf8(output.stdout).unwrap().trim().to_owned()
        };
        let (_dir, repo) = make_test_repo();
        git(repo.path(), &["symbolic-ref", "HEAD", "refs/heads/main"]);
        let root = make_commit(&repo, "root", None);
        let parent = make_commit(&repo, "parent", Some(root));
        let mut tip = make_commit(&repo, "tip", Some(parent));
        let negotiations = Arc::new(AtomicUsize::new(0));
        let router = axum::Router::new()
            .route("/fixture.git/info/refs", get(advertise))
            .route("/fixture.git/git-upload-pack", post(upload))
            .with_state((repo.path().to_path_buf(), Arc::clone(&negotiations)));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let remote = format!("http://{}/fixture.git", listener.local_addr().unwrap());
        let worker = tokio::spawn(async move { axum::serve(listener, router).await.unwrap() });
        struct StopFixture(tokio::task::JoinHandle<()>);
        impl Drop for StopFixture {
            fn drop(&mut self) {
                self.0.abort();
            }
        }
        let fixture = StopFixture(worker);
        let shallow = tempfile::TempDir::new().unwrap();
        git(
            shallow.path(),
            &["clone", "--quiet", "--depth=1", &remote, "."],
        );
        assert_eq!(git(shallow.path(), &["rev-list", "--count", "HEAD"]), "1");
        assert_eq!(
            std::fs::read_to_string(shallow.path().join(".git/shallow"))
                .unwrap()
                .trim(),
            tip.to_string()
        );
        git(
            shallow.path(),
            &["fetch", "--quiet", "--depth=2", "origin", "main"],
        );
        assert_eq!(git(shallow.path(), &["rev-list", "--count", "HEAD"]), "2");
        assert_eq!(
            std::fs::read_to_string(shallow.path().join(".git/shallow"))
                .unwrap()
                .trim(),
            parent.to_string()
        );
        git(
            shallow.path(),
            &["fetch", "--quiet", "--unshallow", "origin", "main"],
        );
        assert_eq!(git(shallow.path(), &["rev-list", "--count", "HEAD"]), "3");
        assert!(!shallow.path().join(".git/shallow").exists());
        for index in 0..96 {
            tip = make_commit(&repo, &format!("history {index}"), Some(tip));
        }
        git(shallow.path(), &["fetch", "--quiet", "origin", "main"]);
        negotiations.store(0, Ordering::Relaxed);
        tip = make_commit(&repo, "after long common history", Some(tip));
        git(shallow.path(), &["fetch", "--quiet", "origin", "main"]);
        assert_eq!(
            git(shallow.path(), &["rev-parse", "FETCH_HEAD"]),
            tip.to_string()
        );
        let rounds = negotiations.load(Ordering::Relaxed);
        eprintln!("one-commit fetch after long shared history: {rounds} HTTP negotiation requests");
        assert!(
            (1..=2).contains(&rounds),
            "redundant common-commit negotiation rounds"
        );
        git(shallow.path(), &["fsck", "--full", "--no-reflogs"]);

        git(repo.path(), &["gc", "--aggressive", "--prune=now"]);
        let repacked = tempfile::TempDir::new().unwrap();
        git(
            repacked.path(),
            &["clone", "--quiet", "--depth=1", &remote, "."],
        );
        assert_eq!(git(repacked.path(), &["rev-list", "--count", "HEAD"]), "1");
        let tree = repo.find_commit(tip).unwrap().tree_id().unwrap().detach();
        let sig = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "1 +0000",
        };
        tip = repo
            .commit_as(sig, sig, "refs/heads/main", "same snapshot", tree, [tip])
            .unwrap()
            .detach();
        git(
            repacked.path(),
            &["fetch", "--quiet", "--depth=1", "origin", "main"],
        );
        assert_eq!(
            git(repacked.path(), &["rev-parse", "FETCH_HEAD"]),
            tip.to_string()
        );
        assert_eq!(
            git(repacked.path(), &["show", "FETCH_HEAD:file.txt"]),
            "after long common history"
        );
        git(repacked.path(), &["fsck", "--full", "--no-reflogs"]);
        git(
            repacked.path(),
            &["fetch", "--quiet", "--unshallow", "origin", "main"],
        );
        assert_eq!(
            git(repacked.path(), &["rev-list", "--count", "FETCH_HEAD"]),
            "101"
        );
        git(repacked.path(), &["fsck", "--full", "--no-reflogs"]);
        fixture.0.abort();
    }

    #[test]
    fn upload_pack_acknowledges_only_known_commits_in_the_negotiated_mode() {
        let (_dir, repo) = make_test_repo();
        let root = make_commit(&repo, "root", None);
        let parent = make_commit(&repo, "parent", Some(root));
        let tip = make_commit(&repo, "tip", Some(parent));
        let blob = repo.write_blob("not a common commit").unwrap().detach();
        let unknown = "f".repeat(40);
        let server = HttpPackServer::new(repo.path().to_path_buf(), 64 * 1024 * 1024);
        for (capabilities, status, final_common) in [
            ("multi_ack_detailed thin-pack", Some("common"), parent),
            ("multi_ack multi_ack_detailed", Some("common"), parent),
            ("multi_ack_detailed multi_ack", Some("common"), parent),
            ("thin-pack multi_ack", Some("continue"), parent),
            ("thin-pack", None, root),
        ] {
            let want = format!("want {tip} {capabilities}\n");
            let mut request = pkt_lines(&[want.as_bytes()]);
            flush(&mut request);
            for have in [
                &unknown,
                &blob.to_string(),
                &root.to_string(),
                &parent.to_string(),
            ] {
                request.extend_from_slice(&pkt_lines(&[format!("have {have}\n").as_bytes()]));
            }
            let mut negotiation = request.clone();
            flush(&mut negotiation);
            let mut expected = Vec::new();
            if let Some(status) = status {
                for common in [root, parent] {
                    write_pkt_line(&mut expected, format!("ACK {common} {status}\n").as_bytes())
                        .unwrap();
                }
                write_pkt_line(&mut expected, b"NAK\n").unwrap();
            } else {
                write_pkt_line(&mut expected, format!("ACK {root}\n").as_bytes()).unwrap();
            }
            assert_eq!(
                server.handle_upload_pack(&negotiation).unwrap(),
                expected,
                "{capabilities}: known commits must not disappear between stateless rounds"
            );
            request.extend_from_slice(&pkt_lines(&[b"done\n"]));
            let response = server.handle_upload_pack(&request).unwrap();
            let mut final_ack = Vec::new();
            write_pkt_line(&mut final_ack, format!("ACK {final_common}\n").as_bytes()).unwrap();
            assert!(
                response.starts_with(&final_ack),
                "{capabilities}: missing final ACK"
            );
            assert_eq!(
                &response[final_ack.len() + 4..final_ack.len() + 9],
                b"\x01PACK"
            );
        }
        let want = format!("want {tip} multi_ack_detailed\n");
        let mut request = pkt_lines(&[want.as_bytes()]);
        flush(&mut request);
        for have in [unknown, blob.to_string()] {
            request.extend_from_slice(&pkt_lines(&[format!("have {have}\n").as_bytes()]));
        }
        flush(&mut request);
        assert_eq!(server.handle_upload_pack(&request).unwrap(), b"0008NAK\n");
    }

    #[test]
    fn upload_pack_does_not_infer_ack_capabilities_from_later_wants_or_substrings() {
        let (_dir, repo) = make_test_repo();
        let root = make_commit(&repo, "root", None);
        let tip = make_commit(&repo, "tip", Some(root));
        let server = HttpPackServer::new(repo.path().to_path_buf(), 0);
        let request = pkt_lines(&[
            format!("want {tip} no-multi_ack_detailed\n").as_bytes(),
            format!("want {tip} multi_ack_detailed\n").as_bytes(),
            format!("have {root}\n").as_bytes(),
        ]);
        let expected = pkt_lines(&[format!("ACK {root}\n").as_bytes()]);
        assert_eq!(server.handle_upload_pack(&request).unwrap(), expected);
    }

    #[test]
    fn upload_pack_does_not_hide_object_lookup_failures() {
        let (_dir, repo) = make_test_repo();
        let tip = make_commit(&repo, "tip", None);
        let corrupt = "f".repeat(40);
        let object_dir = repo.objects.store_ref().path().join(&corrupt[..2]);
        std::fs::create_dir_all(&object_dir).unwrap();
        std::fs::write(object_dir.join(&corrupt[2..]), b"not a compressed object").unwrap();
        let server = HttpPackServer::new(repo.path().to_path_buf(), 0);
        for done in [false, true] {
            let mut request = pkt_lines(&[
                format!("want {tip} multi_ack_detailed\n").as_bytes(),
                format!("have {corrupt}\n").as_bytes(),
            ]);
            if done {
                request.extend(pkt_lines(&[b"done\n"]));
            } else {
                flush(&mut request);
            }
            assert!(server
                .handle_upload_pack(&request)
                .unwrap_err()
                .to_string()
                .contains("resolve offered fetch common commit"));
        }
    }

    #[test]
    fn shallow_boundary_without_have_still_sends_the_complete_snapshot() {
        let (_dir, repo) = make_test_repo();
        let parent = make_commit(&repo, "known shallow snapshot", None);
        let tree = repo
            .find_commit(parent)
            .unwrap()
            .tree_id()
            .unwrap()
            .detach();
        let sig = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "1 +0000",
        };
        let tip = repo
            .commit_as(sig, sig, "refs/heads/main", "same tree", tree, [parent])
            .unwrap()
            .detach();
        for depth in [1, 2] {
            let want = format!("want {tip} side-band-64k shallow\n");
            let shallow = format!("shallow {parent}\n");
            let deepen = format!("deepen {depth}\n");
            let mut request = pkt_lines(&[want.as_bytes(), shallow.as_bytes(), deepen.as_bytes()]);
            flush(&mut request);
            request.extend_from_slice(&pkt_lines(&[b"done\n"]));
            let server = HttpPackServer::new(repo.path().to_path_buf(), 64 * 1024 * 1024);
            let response = server.handle_upload_pack(&request).unwrap();
            let mut pack = Vec::new();
            let mut position = 0;
            while position < response.len() {
                let length = usize::from_str_radix(
                    std::str::from_utf8(&response[position..position + 4]).unwrap(),
                    16,
                )
                .unwrap();
                if length == 0 {
                    position += 4;
                    continue;
                }
                let payload = &response[position + 4..position + length];
                if payload.first() == Some(&1) {
                    pack.extend_from_slice(&payload[1..]);
                }
                position += length;
            }
            assert!(pack.starts_with(b"PACK"));
            assert_eq!(
                u32::from_be_bytes(pack[8..12].try_into().unwrap()),
                depth + 2,
                "a shallow declaration is not a negotiated have; include the tree and blob"
            );
        }
    }

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
