// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use std::collections::{HashSet, VecDeque};
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Instant;

use anyhow::{Context, Result};
use gix::refs::TargetRef;
use tracing::info;
use tracing_opentelemetry::OpenTelemetrySpanExt;

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
const UPLOAD_PACK_SIDEBAND_CHUNK: usize = 65511;

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
        let span = tracing::Span::current();
        span.set_attribute("git.fetch.want_count", request.wants.len() as i64);
        span.set_attribute("git.fetch.have_count", request.haves.len() as i64);
        span.set_attribute("git.fetch.done", request.done_seen);
        span.set_attribute("git.fetch.thin_pack", request.thin_pack);
        if let Some(want) = request.wants.first() {
            span.set_attribute("git.fetch.want_oid", want.clone());
        }
        if let Some(have) = request.haves.first() {
            span.set_attribute("git.fetch.have_oid", have.clone());
        }
        check_upload_pack_interrupt(interrupt)?;
        let mut response = Vec::new();
        let history = if let Some(depth) = request.depth {
            anyhow::ensure!(!request.wants.is_empty(), "deepen requires wanted commits");
            let (commits, boundaries) =
                shallow_history(&repo, &request.wants, depth, self.max_pack_size, interrupt)?;
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
            if history.is_some() && request.haves.is_empty() {
                emit_span("upload-pack-rpc", &self.repo_path, start, "ok", 0);
                return Ok(response);
            }
            write_upload_pack_acknowledgements(&repo, &request, &mut response, interrupt)?;
            emit_span("upload-pack-rpc", &self.repo_path, start, "ok", 0);
            return Ok(response);
        }

        write_upload_pack_acknowledgements(&repo, &request, &mut response, interrupt)?;

        let pack_data = build_pack_with_commits(
            &repo,
            &request.wants,
            &request.haves,
            history.as_deref(),
            request.thin_pack,
            self.max_pack_size,
            interrupt,
        )?;
        let pack_size = pack_data.len();
        let response = frame_upload_pack_response(&response, pack_data, interrupt)?;
        emit_span(
            "upload-pack-rpc",
            &self.repo_path,
            start,
            "ok",
            pack_size as u64,
        );
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

fn frame_upload_pack_response(
    prefix: &[u8],
    mut pack: Vec<u8>,
    interrupt: &AtomicBool,
) -> Result<Vec<u8>> {
    check_upload_pack_interrupt(interrupt)?;
    let pack_len = pack.len();
    let frames = pack_len.div_ceil(UPLOAD_PACK_SIDEBAND_CHUNK);
    let framing_bytes = frames
        .checked_mul(5)
        .context("pack framing size overflow")?;
    let response_len = pack_len
        .checked_add(framing_bytes)
        .and_then(|len| len.checked_add(prefix.len()))
        .and_then(|len| len.checked_add(4))
        .context("pack response size overflow")?;
    pack.try_reserve_exact(response_len - pack_len)
        .context("allocate framed pack response")?;
    pack.resize(response_len, 0);

    // Move backwards so inserted headers and the prefix cannot overwrite unread pack bytes.
    for index in (0..frames).rev() {
        check_upload_pack_interrupt(interrupt)?;
        let source = index * UPLOAD_PACK_SIDEBAND_CHUNK;
        let chunk_len = (pack_len - source).min(UPLOAD_PACK_SIDEBAND_CHUNK);
        let destination = prefix.len() + index * (UPLOAD_PACK_SIDEBAND_CHUNK + 5);
        pack.copy_within(source..source + chunk_len, destination + 5);
        let header = format!("{:04x}", chunk_len + 5);
        pack[destination..destination + 4].copy_from_slice(header.as_bytes());
        pack[destination + 4] = 0x01;
    }
    pack[..prefix.len()].copy_from_slice(prefix);
    pack[response_len - 4..].copy_from_slice(b"0000");
    Ok(pack)
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
        let head_oid = head_id.to_string();
        tracing::Span::current().set_attribute("git.advertised_head_oid", head_oid.clone());
        refs.push(("HEAD".to_string(), head_oid));
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

/// Parsed pkt-line negotiation, including ACK mode and shallow history bounds.
#[derive(Default)]
struct UploadPackNegotiation {
    wants: Vec<String>,
    haves: Vec<String>,
    shallows: Vec<gix::ObjectId>,
    depth: Option<u32>,
    done_seen: bool,
    thin_pack: bool,
    ack_mode: UploadPackAckMode,
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
            let mut fields = rest.split_whitespace();
            let oid = fields.next().context("missing want ID")?;
            gix::ObjectId::from_hex(oid.as_bytes()).context("invalid want ID")?;
            if request.wants.is_empty() {
                for capability in fields {
                    match capability {
                        "thin-pack" => request.thin_pack = true,
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
        // A common commit is not proof that every wanted history is complete.
        write_pkt_line(response, b"NAK\n")?;
    }
    Ok(())
}

fn shallow_history(
    repo: &gix::Repository,
    wants: &[String],
    depth: u32,
    max_pack_size: u64,
    interrupt: &AtomicBool,
) -> Result<(Vec<gix::ObjectId>, Vec<gix::ObjectId>)> {
    check_upload_pack_interrupt(interrupt)?;
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
/// `max_pack_size` includes the pack header and checksum, but not HTTP framing.
pub fn build_pack_for_wants(
    repo: &gix::Repository,
    wants: &[String],
    haves: &[String],
    max_pack_size: u64,
) -> Result<Vec<u8>> {
    build_pack_with_commits(
        repo,
        wants,
        haves,
        None,
        false,
        max_pack_size,
        &AtomicBool::new(false),
    )
}

fn check_upload_pack_interrupt(interrupt: &AtomicBool) -> Result<()> {
    anyhow::ensure!(!interrupt.load(Ordering::Acquire), "upload-pack cancelled");
    Ok(())
}

// Pack selection needs target identities, not paths or a retained change log.
const MIN_TREE_DELTA_BYTES: usize = 4096;
const MAX_TREE_DELTA_BYTES: usize = 1024 * 1024;
const MAX_TREE_DELTA_LITERAL_BYTES: usize = 4096;
const MAX_TREE_DELTA_CANDIDATES: usize = 8;
const MAX_TREE_DELTA_CHAIN: usize = 32;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct TreeDeltaBase {
    base: gix::ObjectId,
    target: gix::ObjectId,
}

fn record_tree_delta(candidates: &mut Vec<TreeDeltaBase>, candidate: TreeDeltaBase) {
    if candidates.len() < MAX_TREE_DELTA_CANDIDATES && !candidates.contains(&candidate) {
        candidates.push(candidate);
    }
}

struct PackTreeObjects<'a> {
    objects: &'a mut HashSet<gix::ObjectId>,
    ordered: &'a mut Vec<gix::ObjectId>,
    tree_deltas: &'a mut Vec<TreeDeltaBase>,
    interrupt: &'a AtomicBool,
}

impl gix::diff::tree::Visit for PackTreeObjects<'_> {
    fn pop_front_tracked_path_and_set_current(&mut self) {}
    fn push_back_tracked_path_component(&mut self, _component: &gix::bstr::BStr) {}
    fn push_path_component(&mut self, _component: &gix::bstr::BStr) {}
    fn pop_path_component(&mut self) {}

    fn visit(&mut self, change: gix::diff::tree::visit::Change) -> gix::diff::tree::visit::Action {
        use gix::diff::tree::visit::Change;
        use std::ops::ControlFlow;

        if self.interrupt.load(Ordering::Acquire) {
            return ControlFlow::Break(());
        }
        if self.tree_deltas.len() < MAX_TREE_DELTA_CANDIDATES {
            if let Change::Modification {
                previous_entry_mode,
                previous_oid,
                entry_mode,
                oid,
            } = &change
            {
                if previous_entry_mode.kind() == gix::object::tree::EntryKind::Tree
                    && entry_mode.kind() == gix::object::tree::EntryKind::Tree
                {
                    record_tree_delta(
                        self.tree_deltas,
                        TreeDeltaBase {
                            base: *previous_oid,
                            target: *oid,
                        },
                    );
                }
            }
        }
        match change {
            Change::Addition {
                entry_mode, oid, ..
            }
            | Change::Modification {
                entry_mode, oid, ..
            } if entry_mode.kind() != gix::object::tree::EntryKind::Commit
                && self.objects.insert(oid) =>
            {
                self.ordered.push(oid);
            }
            _ => {}
        }
        ControlFlow::Continue(())
    }
}

fn pack_objects_since_snapshot(
    repo: &gix::Repository,
    commits: &[gix::ObjectId],
    baseline: &gix::Tree<'_>,
    interrupt: &AtomicBool,
) -> Result<(Vec<gix::ObjectId>, Vec<TreeDeltaBase>)> {
    check_upload_pack_interrupt(interrupt)?;
    let mut tree_deltas = Vec::new();
    let mut objects = HashSet::with_capacity(commits.len());
    let mut ordered = Vec::with_capacity(commits.len());
    let mut state = gix::diff::tree::State::default();
    // Keep nearby loose tree versions together without changing pack object membership.
    for commit in commits.iter().rev() {
        check_upload_pack_interrupt(interrupt)?;
        if objects.insert(*commit) {
            ordered.push(*commit);
        }
        let tree_id = repo.find_commit(*commit)?.tree_id()?.detach();
        if tree_id == baseline.id {
            continue;
        }
        if objects.insert(tree_id) {
            ordered.push(tree_id);
        }
        let tree = repo.find_tree(tree_id)?;
        if baseline.data.len() >= MIN_TREE_DELTA_BYTES {
            record_tree_delta(
                &mut tree_deltas,
                TreeDeltaBase {
                    base: baseline.id,
                    target: tree_id,
                },
            );
        }
        let mut changes = PackTreeObjects {
            objects: &mut objects,
            ordered: &mut ordered,
            tree_deltas: &mut tree_deltas,
            interrupt,
        };
        gix::diff::tree(
            gix::objs::TreeRefIter::from_bytes(&baseline.data, baseline.id.kind()),
            gix::objs::TreeRefIter::from_bytes(&tree.data, tree.id.kind()),
            &mut state,
            &repo.objects,
            &mut changes,
        )
        .context("compare pack tree to client snapshot")?;
    }
    check_upload_pack_interrupt(interrupt)?;
    Ok((ordered, tree_deltas))
}

fn encode_tree_delta(
    base: &[u8],
    target: &[u8],
    interrupt: &AtomicBool,
) -> Result<Option<Vec<u8>>> {
    check_upload_pack_interrupt(interrupt)?;
    if !(MIN_TREE_DELTA_BYTES..=MAX_TREE_DELTA_BYTES).contains(&base.len())
        || !(MIN_TREE_DELTA_BYTES..=MAX_TREE_DELTA_BYTES).contains(&target.len())
    {
        return Ok(None);
    }
    let common = base.len().min(target.len());
    let mut prefix = 0;
    while prefix < common && base[prefix] == target[prefix] {
        if prefix.is_multiple_of(4096) {
            check_upload_pack_interrupt(interrupt)?;
        }
        prefix += 1;
    }
    if prefix == base.len() && prefix == target.len() {
        return Ok(None);
    }
    let mut suffix = 0;
    while suffix < common - prefix
        && base[base.len() - 1 - suffix] == target[target.len() - 1 - suffix]
    {
        if suffix.is_multiple_of(4096) {
            check_upload_pack_interrupt(interrupt)?;
        }
        suffix += 1;
    }
    let literal = &target[prefix..target.len() - suffix];
    if literal.len() > MAX_TREE_DELTA_LITERAL_BYTES || literal.len() > target.len() / 2 {
        return Ok(None);
    }
    let mut delta = Vec::with_capacity(literal.len() + literal.len().div_ceil(127) + 32);
    for mut size in [base.len(), target.len()] {
        loop {
            let byte = (size & 0x7f) as u8;
            size >>= 7;
            delta.push(byte | if size == 0 { 0 } else { 0x80 });
            if size == 0 {
                break;
            }
        }
    }
    // Copy opcodes carry only nonzero little-endian offset and size bytes.
    let copy = |delta: &mut Vec<u8>, offset: usize, size: usize| {
        if size == 0 {
            return;
        }
        let mut opcode = 0x80;
        let mut operands = [0; 7];
        let mut count = 0;
        for (index, byte) in offset.to_le_bytes().into_iter().take(4).enumerate() {
            if byte != 0 {
                opcode |= 1 << index;
                operands[count] = byte;
                count += 1;
            }
        }
        for (index, byte) in size.to_le_bytes().into_iter().take(3).enumerate() {
            if byte != 0 {
                opcode |= 0x10 << index;
                operands[count] = byte;
                count += 1;
            }
        }
        delta.push(opcode);
        delta.extend_from_slice(&operands[..count]);
    };
    copy(&mut delta, 0, prefix);
    for chunk in literal.chunks(127) {
        check_upload_pack_interrupt(interrupt)?;
        delta.push(chunk.len() as u8);
        delta.extend_from_slice(chunk);
    }
    copy(&mut delta, base.len() - suffix, suffix);
    check_upload_pack_interrupt(interrupt)?;
    Ok(Some(delta))
}

fn thin_tree_entry(
    repo: &gix::Repository,
    candidates: &[TreeDeltaBase],
    interrupt: &AtomicBool,
) -> Result<Option<gix_pack::data::output::Entry>> {
    let mut selected: Option<(usize, TreeDeltaBase)> = None;
    'candidate: for candidate in candidates.iter().take(MAX_TREE_DELTA_CANDIDATES) {
        let mut target_size = 0;
        for id in [candidate.base, candidate.target] {
            check_upload_pack_interrupt(interrupt)?;
            let header = repo.find_header(id).context("read thin tree header")?;
            anyhow::ensure!(
                header.kind() == gix::objs::Kind::Tree,
                "thin tree base or target is not a tree"
            );
            let size =
                usize::try_from(header.size()).context("thin tree size is not representable")?;
            if !(MIN_TREE_DELTA_BYTES..=MAX_TREE_DELTA_BYTES).contains(&size) {
                continue 'candidate;
            }
            target_size = size;
        }
        if selected.is_none_or(|(size, _)| target_size > size) {
            selected = Some((target_size, *candidate));
        }
    }
    let Some((_, candidate)) = selected else {
        return Ok(None);
    };
    check_upload_pack_interrupt(interrupt)?;
    let base = repo
        .find_tree(candidate.base)
        .context("read thin tree base")?;
    check_upload_pack_interrupt(interrupt)?;
    let target = repo
        .find_tree(candidate.target)
        .context("read thin tree target")?;
    tree_delta_entry(repo, &base, &target, interrupt)
}

fn tree_delta_entry(
    repo: &gix::Repository,
    base: &gix::Tree<'_>,
    target: &gix::Tree<'_>,
    interrupt: &AtomicBool,
) -> Result<Option<gix_pack::data::output::Entry>> {
    use gix_pack::data::output;

    let Some(delta) = encode_tree_delta(&base.data, &target.data, interrupt)? else {
        return Ok(None);
    };
    let mut entry = output::Entry::from_data(
        &output::Count {
            id: target.id,
            entry_pack_location: output::count::PackLocation::NotLookedUp,
        },
        &gix::objs::Data {
            kind: gix::objs::Kind::Tree,
            data: &delta,
            object_hash: repo.object_hash(),
        },
        gix::zlib::Compression::DEFAULT,
    )
    .context("compress thin tree delta")?;
    entry.kind = output::entry::Kind::DeltaOid { id: base.id };
    tracing::Span::current()
        .set_attribute("git.pack.thin_tree_target_bytes", target.data.len() as i64);
    tracing::Span::current().set_attribute("git.pack.thin_tree_delta_bytes", delta.len() as i64);
    check_upload_pack_interrupt(interrupt)?;
    Ok(Some(entry))
}

fn compact_tree_entry<'repo>(
    repo: &'repo gix::Repository,
    entry: &mut gix_pack::data::output::Entry,
    snapshot: &mut Option<(gix::Tree<'repo>, usize)>,
    interrupt: &AtomicBool,
) -> Result<bool> {
    use gix_pack::data::output::entry::Kind;

    check_upload_pack_interrupt(interrupt)?;
    if entry.kind != Kind::Base(gix::objs::Kind::Tree)
        || !(MIN_TREE_DELTA_BYTES..=MAX_TREE_DELTA_BYTES).contains(&entry.decompressed_size)
    {
        return Ok(false);
    }
    let target = repo
        .find_tree(entry.id)
        .context("read pack tree snapshot")?;
    let mut depth = 0;
    if let Some((base, base_depth)) = snapshot.as_ref() {
        if *base_depth < MAX_TREE_DELTA_CHAIN {
            if let Some(delta) = tree_delta_entry(repo, base, &target, interrupt)? {
                if delta.compressed_data.len() + repo.object_hash().len_in_bytes()
                    < entry.compressed_data.len()
                {
                    *entry = delta;
                    depth = base_depth + 1;
                }
            }
        }
    }
    // At most the previous tree and this target are decoded, as for one thin delta.
    *snapshot = Some((target, depth));
    Ok(depth != 0)
}

fn build_pack_with_commits(
    repo: &gix::Repository,
    wants: &[String],
    haves: &[String],
    commits: Option<&[gix::ObjectId]>,
    thin_pack: bool,
    max_pack_size: u64,
    interrupt: &AtomicBool,
) -> Result<Vec<u8>> {
    use gix::object::Kind;
    use gix_pack::data::output::count::objects::ObjectExpansion;

    check_upload_pack_interrupt(interrupt)?;
    let mut want_ids = Vec::new();
    for want in wants {
        check_upload_pack_interrupt(interrupt)?;
        if let Ok(id) = gix::ObjectId::from_hex(want.as_bytes()) {
            want_ids.push(id);
        }
    }

    if want_ids.is_empty() {
        return Ok(Vec::new());
    }
    let limit_message = if commits.is_some() {
        "shallow pack exceeds configured pack size"
    } else {
        "pack exceeds configured pack size"
    };
    // The SHA-1 pack header and trailing checksum require 32 bytes themselves.
    anyhow::ensure!(max_pack_size >= 32, "{limit_message}");

    // Resolve have OIDs; unknown/invalid ones are silently ignored.
    let mut have_ids = Vec::new();
    for have in haves {
        check_upload_pack_interrupt(interrupt)?;
        if let Ok(id) = gix::ObjectId::from_hex(have.as_bytes()) {
            if repo.find_object(id).is_ok() {
                have_ids.push(id);
            }
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
    check_upload_pack_interrupt(interrupt)?;
    let mut walk_ids = Vec::with_capacity(commits.map_or(0, |ids| ids.len()));
    if let Some(commits) = commits {
        for commit in commits {
            check_upload_pack_interrupt(interrupt)?;
            walk_ids.push(*commit);
        }
    } else {
        let mut walk = repo
            .rev_walk(commit_tips.iter().copied())
            .with_boundary(have_ids.iter().copied())
            // Haves prove ancestry boundaries, not a timestamp cutoff for backdated commits.
            .sorting(gix::revision::walk::Sorting::BreadthFirst)
            .all()
            .context("rev walk for pack generation")?;
        loop {
            check_upload_pack_interrupt(interrupt)?;
            let Some(result) = walk.next() else {
                break;
            };
            walk_ids.push(result.context("walking pack commits")?.id);
        }
    }

    if walk_ids.is_empty() && extra_objects.is_empty() {
        anyhow::bail!(
            "upload-pack: rev_walk produced no objects for {} want(s); \
             repository may be corrupt or wants are not reachable",
            want_ids.len()
        );
    }

    let mut baseline = None;
    for have in &have_ids {
        check_upload_pack_interrupt(interrupt)?;
        let object = repo.find_object(*have)?;
        if object.kind == Kind::Commit {
            baseline = Some(object.try_into_commit()?.tree()?);
            break;
        }
    }
    // Even a shallow have proves its complete tree, not its parent history.
    // Compare every selected commit to that snapshot instead of resending it.
    let (object_ids, tree_deltas, expansion) = match baseline {
        Some(tree) => {
            let (objects, candidate) =
                pack_objects_since_snapshot(repo, &walk_ids, &tree, interrupt)?;
            (objects, candidate, ObjectExpansion::AsIs)
        }
        None => (walk_ids, Vec::new(), ObjectExpansion::TreeContents),
    };
    let mut ids_iter = object_ids.iter().map(|id| Ok::<_, gix::Error>(*id));

    let (mut counts, _) = gix_pack::data::output::count::objects_unthreaded(
        &odb,
        &mut ids_iter,
        &gix::progress::Discard,
        interrupt,
        expansion,
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

    check_upload_pack_interrupt(interrupt)?;
    if counts.is_empty() {
        return Ok(Vec::new());
    }

    let expected_entries = u32::try_from(counts.len()).context("too many pack entries")?;
    let initial_delta = if thin_pack {
        thin_tree_entry(repo, &tree_deltas, interrupt)?
    } else {
        None
    };
    let mut snapshot = match initial_delta {
        Some(gix_pack::data::output::Entry {
            kind: gix_pack::data::output::entry::Kind::DeltaOid { id },
            ..
        }) => Some((
            repo.find_tree(id)
                .context("read negotiated pack tree base")?,
            0,
        )),
        _ => None,
    };
    let entries_iter = gix_pack::data::output::entry::iter_from_counts(
        counts,
        odb.clone(),
        Box::new(gix::progress::Discard),
        gix_pack::data::output::entry::iter_from_counts::Options {
            thread_limit: Some(1),
            ..Default::default()
        },
    )
    .context("resolving pack entry locations")?;
    let mut compacted_trees = 0i64;
    // Replace entries in place so reused in-pack delta indexes remain unchanged.
    let entries_iter = entries_iter.map(|batch| {
        let compacted = (|| -> Result<_> {
            let (sequence, mut entries) = batch?;
            if thin_pack {
                for entry in &mut entries {
                    compacted_trees +=
                        i64::from(compact_tree_entry(repo, entry, &mut snapshot, interrupt)?);
                }
            }
            Ok((sequence, entries))
        })();
        compacted.map_err(|error| gix::Error::from_boxed(error.into_boxed_dyn_error()))
    });

    let pack = write_pack_entries(
        entries_iter,
        expected_entries,
        LimitedPackWriter {
            bytes: Vec::new(),
            limit: max_pack_size,
            limit_message,
            interrupt,
        },
        max_pack_size,
        limit_message,
        interrupt,
    )?;
    let span = tracing::Span::current();
    span.set_attribute("git.pack.thin_tree_delta", compacted_trees != 0);
    span.set_attribute("git.pack.compacted_tree_count", compacted_trees);
    Ok(pack.bytes)
}

fn write_pack_entries<I, W>(
    entries_iter: I,
    expected_entries: u32,
    writer: W,
    max_pack_size: u64,
    limit_message: &'static str,
    interrupt: &AtomicBool,
) -> Result<W>
where
    I: Iterator<
        Item = std::result::Result<(usize, Vec<gix_pack::data::output::Entry>), gix::Error>,
    >,
    W: std::io::Write,
{
    use gix_pack::data::output;

    check_upload_pack_interrupt(interrupt)?;
    // Expansion precedes entry generation: gix emits one entry per Count.
    // Verify that contract without retaining every compressed batch.
    let mut generated_entries = 0u32;
    let mut minimum_pack_bytes = 32u64;
    let mut sequence = 0usize;
    let checked_entries = entries_iter.map(|result| {
        let checked = (|| -> Result<Vec<output::Entry>> {
            check_upload_pack_interrupt(interrupt)?;
            let (batch_sequence, entries) = result.context("pack entry generation error")?;
            anyhow::ensure!(
                batch_sequence == sequence,
                "pack entry batches out of order"
            );
            sequence = sequence
                .checked_add(1)
                .context("too many pack entry batches")?;
            let count = u32::try_from(entries.len()).context("too many pack entries")?;
            generated_entries = generated_entries
                .checked_add(count)
                .context("too many pack entries")?;
            anyhow::ensure!(
                generated_entries <= expected_entries,
                "pack entry count exceeds counted objects"
            );
            for entry in &entries {
                check_upload_pack_interrupt(interrupt)?;
                anyhow::ensure!(!entry.is_invalid(), "counted pack object is unavailable");
                minimum_pack_bytes = minimum_pack_bytes
                    .checked_add(entry.compressed_data.len() as u64)
                    .and_then(|size| size.checked_add(1))
                    .context("pack size accounting overflow")?;
                anyhow::ensure!(minimum_pack_bytes <= max_pack_size, "{limit_message}");
            }
            Ok(entries)
        })();
        checked.map_err(|error| gix::Error::from_boxed(error.into_boxed_dyn_error()))
    });

    let mut bytes_iter = gix_pack::data::output::bytes::FromEntriesIter::new(
        checked_entries,
        writer,
        expected_entries,
        gix_pack::data::Version::V2,
        gix::hash::Kind::Sha1,
    );

    loop {
        check_upload_pack_interrupt(interrupt)?;
        match bytes_iter.next() {
            Some(Ok(_)) => {}
            Some(Err(e)) => {
                let message = format!("pack generation error: {e:#}");
                return Err(anyhow::Error::new(e).context(message));
            }
            None => break,
        }
    }

    let writer = bytes_iter.into_write();
    anyhow::ensure!(
        generated_entries == expected_entries,
        "pack entry count does not match counted objects"
    );
    Ok(writer)
}

struct LimitedPackWriter<'a> {
    bytes: Vec<u8>,
    limit: u64,
    limit_message: &'static str,
    interrupt: &'a AtomicBool,
}

impl std::io::Write for LimitedPackWriter<'_> {
    fn write(&mut self, buffer: &[u8]) -> std::io::Result<usize> {
        check_upload_pack_interrupt(self.interrupt).map_err(|error| {
            std::io::Error::new(std::io::ErrorKind::ConnectionAborted, error.to_string())
        })?;
        if buffer.len() as u64 > self.limit.saturating_sub(self.bytes.len() as u64) {
            return Err(std::io::Error::new(
                std::io::ErrorKind::FileTooLarge,
                self.limit_message,
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

    fn empty_pack_entry() -> gix_pack::data::output::Entry {
        gix_pack::data::output::Entry {
            id: gix::ObjectId::from_hex(b"e69de29bb2d1d6434b8b29ae775ad8c2e48c5391").unwrap(),
            kind: gix_pack::data::output::entry::Kind::Base(gix::object::Kind::Blob),
            decompressed_size: 0,
            compressed_data: vec![0x78, 0x9c, 3, 0, 0, 0, 0, 1],
        }
    }

    #[test]
    fn upload_pack_encodes_before_generating_the_next_entry_batch() {
        use std::sync::atomic::AtomicUsize;

        struct ObservedWriter<'a> {
            bytes: Vec<u8>,
            written: &'a AtomicUsize,
        }
        impl std::io::Write for ObservedWriter<'_> {
            fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
                self.bytes.extend_from_slice(bytes);
                self.written.store(self.bytes.len(), Ordering::Release);
                Ok(bytes.len())
            }

            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }

        let written = AtomicUsize::new(0);
        let mut generated = 0usize;
        let entries = std::iter::from_fn(|| {
            if generated == 2 {
                return None;
            }
            if generated == 1 {
                assert!(
                    written.load(Ordering::Acquire) >= 21,
                    "next batch was generated before the first entry was encoded"
                );
            }
            let sequence = generated;
            generated += 1;
            Some(Ok((sequence, vec![empty_pack_entry()])))
        });
        let writer = write_pack_entries(
            entries,
            2,
            ObservedWriter {
                bytes: Vec::new(),
                written: &written,
            },
            50,
            "pack exceeds configured pack size",
            &AtomicBool::new(false),
        )
        .unwrap();
        assert_eq!(generated, 2);
        assert_eq!(writer.bytes.len(), 50);
        assert_eq!(&writer.bytes[..12], b"PACK\0\0\0\x02\0\0\0\x02");
    }

    #[test]
    fn upload_pack_entry_stream_rejects_count_and_availability_mismatches() {
        for (expected, entries, message) in [
            (1, vec![], "does not match counted objects"),
            (0, vec![empty_pack_entry()], "exceeds counted objects"),
            (
                2,
                vec![empty_pack_entry()],
                "does not match counted objects",
            ),
            (
                1,
                vec![gix_pack::data::output::Entry::invalid()],
                "counted pack object is unavailable",
            ),
        ] {
            let error = write_pack_entries(
                std::iter::once(Ok((0, entries))),
                expected,
                Vec::new(),
                1024,
                "pack exceeds configured pack size",
                &AtomicBool::new(false),
            )
            .unwrap_err();
            assert!(format!("{error:#}").contains(message), "{error:#}");
        }
    }

    #[test]
    fn upload_pack_entry_stream_rejects_bad_order_and_input_errors() {
        for (entry, message) in [
            (Ok((1, vec![empty_pack_entry()])), "batches out of order"),
            (
                Err(gix::Error::from(gix::error::message("entry source failed"))),
                "entry source failed",
            ),
        ] {
            let error = write_pack_entries(
                std::iter::once(entry),
                1,
                Vec::new(),
                1024,
                "pack exceeds configured pack size",
                &AtomicBool::new(false),
            )
            .unwrap_err();
            assert!(format!("{error:#}").contains(message), "{error:#}");
        }
    }

    #[test]
    fn upload_pack_entry_stream_does_not_generate_when_cancelled() {
        let entries =
            std::iter::once_with(|| -> std::result::Result<(usize, Vec<_>), gix::Error> {
                panic!("cancelled generation must not advance the entry source")
            });
        let error = write_pack_entries(
            entries,
            1,
            Vec::new(),
            1024,
            "pack exceeds configured pack size",
            &AtomicBool::new(true),
        )
        .unwrap_err();
        assert!(format!("{error:#}").contains("upload-pack cancelled"));
    }

    #[test]
    fn upload_pack_framing_reuses_pack_allocation() {
        let prefix = b"0008NAK\n";
        let pack_len = UPLOAD_PACK_SIDEBAND_CHUNK * 2 + 1;
        let wire_len = prefix.len() + pack_len + 3 * 5 + 4;
        let mut pack = Vec::with_capacity(wire_len);
        pack.resize(pack_len, 0xa5);
        let allocation = pack.as_ptr();
        let response =
            frame_upload_pack_response(prefix, pack, &AtomicBool::new(false)).expect("frame pack");
        assert_eq!(response.len(), wire_len);
        assert_eq!(
            response.as_ptr(),
            allocation,
            "framing retained a separate whole-pack allocation despite sufficient capacity"
        );
    }

    #[test]
    fn upload_pack_framing_matches_original_wire_bytes() {
        let mut shallow = pkt_lines(&[
            b"shallow 0000000000000000000000000000000000000001\n",
            b"unshallow 0000000000000000000000000000000000000002\n",
        ]);
        flush(&mut shallow);
        shallow.extend(pkt_lines(&[b"NAK\n"]));
        let prefixes = [
            Vec::new(),
            pkt_lines(&[b"NAK\n"]),
            shallow,
            vec![b'p'; UPLOAD_PACK_SIDEBAND_CHUNK * 2 + 3],
        ];
        for pack_len in [
            0,
            1,
            UPLOAD_PACK_SIDEBAND_CHUNK - 1,
            UPLOAD_PACK_SIDEBAND_CHUNK,
            UPLOAD_PACK_SIDEBAND_CHUNK + 1,
            UPLOAD_PACK_SIDEBAND_CHUNK * 2,
            UPLOAD_PACK_SIDEBAND_CHUNK * 2 + 1,
        ] {
            let bytes: Vec<_> = (0u8..=250).cycle().take(pack_len).collect();
            for prefix in &prefixes {
                let mut expected = prefix.clone();
                for chunk in bytes.chunks(UPLOAD_PACK_SIDEBAND_CHUNK) {
                    let mut sideband = vec![0x01];
                    sideband.extend_from_slice(chunk);
                    write_pkt_line(&mut expected, &sideband).expect("reference frame");
                }
                flush(&mut expected);
                for reserve_framing in [false, true] {
                    let capacity = if reserve_framing {
                        expected.len()
                    } else {
                        pack_len
                    };
                    let mut pack = Vec::with_capacity(capacity);
                    pack.extend_from_slice(&bytes);
                    let allocation = pack.as_ptr();
                    let actual = frame_upload_pack_response(prefix, pack, &AtomicBool::new(false))
                        .expect("frame pack");
                    assert_eq!(
                        actual,
                        expected,
                        "pack={pack_len} prefix={} reserve={reserve_framing}",
                        prefix.len()
                    );
                    if reserve_framing {
                        assert_eq!(actual.as_ptr(), allocation);
                    }
                }
            }
        }
    }

    #[test]
    fn upload_pack_framing_rejects_cancelled_empty_and_nonempty_packs() {
        for pack in [Vec::new(), vec![0xa5; UPLOAD_PACK_SIDEBAND_CHUNK * 2 + 1]] {
            let error = frame_upload_pack_response(b"0008NAK\n", pack, &AtomicBool::new(true))
                .expect_err("cancelled framing must not return a response");
            assert!(error.to_string().contains("upload-pack cancelled"));
        }
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
        use std::process::{Command, Stdio};
        use std::sync::atomic::AtomicUsize;
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
        let negotiations = Arc::new(AtomicUsize::new(0));
        let server_negotiations = Arc::clone(&negotiations);
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
                let mut compressed = false;
                loop {
                    line.clear();
                    anyhow::ensure!(reader.read_line(&mut line)? > 0, "incomplete HTTP header");
                    if line == "\r\n" {
                        break;
                    }
                    if let Some(value) = line.to_ascii_lowercase().strip_prefix("content-length:") {
                        length = value.trim().parse::<usize>()?;
                    }
                    if line.trim().eq_ignore_ascii_case("content-encoding: gzip") {
                        compressed = true;
                    }
                    anyhow::ensure!(
                        line.len() <= 65536 && length <= 65536,
                        "oversized fixture request"
                    );
                }
                let mut body = vec![0; length];
                reader.read_exact(&mut body)?;
                drop(reader);
                if compressed {
                    let mut decoder = Command::new("gzip")
                        .arg("-dc")
                        .stdin(Stdio::piped())
                        .stdout(Stdio::piped())
                        .stderr(Stdio::piped())
                        .spawn()
                        .context("fixture gzip decoder")?;
                    decoder
                        .stdin
                        .take()
                        .context("gzip input")?
                        .write_all(&body)?;
                    let decoded = decoder.wait_with_output()?;
                    anyhow::ensure!(
                        decoded.status.success() && decoded.stdout.len() <= 65536,
                        "invalid compressed fixture request"
                    );
                    body = decoded.stdout;
                }
                let response = if advertised {
                    server.advertise_upload_pack_refs()?
                } else {
                    server_negotiations.fetch_add(1, Ordering::Relaxed);
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
        let git_at = |directory: &std::path::Path, args: &[&str]| {
            let output = Command::new("git")
                .env("GIT_CONFIG_COUNT", "0")
                .env("GIT_CONFIG_GLOBAL", "/dev/null")
                .args(["-C"])
                .arg(directory)
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
        let git = |args: &[&str]| git_at(checkout.path(), args);
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

        let mut latest = tip;
        for index in 0..96 {
            latest = make_commit(&repo, &format!("history {index}"), Some(latest));
        }
        git(&["fetch", "--quiet", "origin", "main"]);
        assert_eq!(git(&["rev-parse", "origin/main"]), latest.to_string());
        negotiations.store(0, Ordering::Relaxed);
        latest = make_commit(&repo, "after long common history", Some(latest));
        git(&["fetch", "--quiet", "origin", "main"]);
        assert_eq!(git(&["rev-parse", "origin/main"]), latest.to_string());
        let rounds = negotiations.load(Ordering::Relaxed);
        eprintln!("one-commit fetch after long shared history: {rounds} HTTP negotiation requests");
        assert!(
            (1..=2).contains(&rounds),
            "known ancestry must stop redundant stateless negotiation rounds"
        );

        let shallow = tempfile::TempDir::new().unwrap();
        git_at(
            shallow.path(),
            &["clone", "--quiet", "--depth=1", &remote, "."],
        );
        let sig = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "1 +0000",
        };
        for index in 0..3 {
            let parent = repo.find_commit(latest).unwrap();
            let blob = repo
                .write_blob(format!("new data {index}"))
                .unwrap()
                .detach();
            let mut editor = repo.edit_tree(parent.tree_id().unwrap().detach()).unwrap();
            editor
                .upsert(
                    format!("nested/deep/value{index}.txt"),
                    gix::object::tree::EntryKind::Blob,
                    blob,
                )
                .unwrap();
            if index == 2 {
                editor.remove("file.txt").unwrap();
                editor
                    .upsert(
                        "submodule",
                        gix::object::tree::EntryKind::Commit,
                        gix::ObjectId::from_hex("f".repeat(40).as_bytes()).unwrap(),
                    )
                    .unwrap();
            }
            let tree = editor.write().unwrap().detach();
            latest = repo
                .commit_as(
                    sig,
                    sig,
                    "refs/heads/main",
                    "nested additions",
                    tree,
                    [latest],
                )
                .unwrap()
                .detach();
        }
        git_at(
            shallow.path(),
            &["fetch", "--quiet", "--depth=1", "origin", "main"],
        );
        assert_eq!(
            git_at(shallow.path(), &["rev-parse", "FETCH_HEAD"]),
            latest.to_string()
        );
        for index in 0..3 {
            assert_eq!(
                git_at(
                    shallow.path(),
                    &["show", &format!("FETCH_HEAD:nested/deep/value{index}.txt")],
                ),
                format!("new data {index}")
            );
        }
        git_at(shallow.path(), &["fsck", "--full", "--no-reflogs"]);

        let shallow_boundary = latest;
        let previous_boundaries: HashSet<_> =
            std::fs::read_to_string(shallow.path().join(".git/shallow"))
                .unwrap()
                .lines()
                .map(str::to_owned)
                .collect();
        assert!(previous_boundaries.contains(&shallow_boundary.to_string()));
        let feature_blob = repo
            .write_blob("feature from old history")
            .unwrap()
            .detach();
        let mut feature_tree = repo
            .edit_tree(
                repo.find_commit(parent)
                    .unwrap()
                    .tree_id()
                    .unwrap()
                    .detach(),
            )
            .unwrap();
        feature_tree
            .upsert(
                "branch/feature.txt",
                gix::object::tree::EntryKind::Blob,
                feature_blob,
            )
            .unwrap();
        let feature = repo
            .commit_as(
                sig,
                sig,
                "refs/heads/topic",
                "feature from old history",
                feature_tree.write().unwrap().detach(),
                [parent],
            )
            .unwrap()
            .detach();
        let mut merge_tree = repo
            .edit_tree(
                repo.find_commit(latest)
                    .unwrap()
                    .tree_id()
                    .unwrap()
                    .detach(),
            )
            .unwrap();
        merge_tree
            .upsert(
                "branch/feature.txt",
                gix::object::tree::EntryKind::Blob,
                feature_blob,
            )
            .unwrap();
        latest = repo
            .commit_as(
                sig,
                sig,
                "refs/heads/main",
                "merge old-history feature",
                merge_tree.write().unwrap().detach(),
                [latest, feature],
            )
            .unwrap()
            .detach();
        git_at(shallow.path(), &["fetch", "--quiet", "origin", "main"]);
        assert_eq!(
            git_at(shallow.path(), &["rev-parse", "FETCH_HEAD"]),
            latest.to_string()
        );
        assert_eq!(
            git_at(shallow.path(), &["rev-parse", "FETCH_HEAD^1"]),
            shallow_boundary.to_string()
        );
        assert_eq!(
            git_at(shallow.path(), &["rev-parse", "FETCH_HEAD^2"]),
            feature.to_string()
        );
        assert_eq!(
            git_at(shallow.path(), &["show", "FETCH_HEAD:branch/feature.txt"]),
            "feature from old history"
        );
        assert_eq!(
            git_at(shallow.path(), &["show", "FETCH_HEAD^2:file.txt"]),
            "parent"
        );
        assert_eq!(
            std::fs::read_to_string(shallow.path().join(".git/shallow"))
                .unwrap()
                .lines()
                .map(str::to_owned)
                .collect::<HashSet<_>>(),
            previous_boundaries,
            "ordinary fetch must preserve all existing client shallow boundaries"
        );
        git_at(shallow.path(), &["fsck", "--full", "--no-reflogs"]);
        git(&["fetch", "--quiet", "origin", "main"]);
        git_at(
            shallow.path(),
            &["fetch", "--quiet", "--unshallow", "origin", "main"],
        );
        assert!(!shallow.path().join(".git/shallow").exists());
        assert_eq!(
            git_at(shallow.path(), &["rev-list", "--count", "FETCH_HEAD"]),
            git(&["rev-list", "--count", "origin/main"])
        );
        git_at(shallow.path(), &["fsck", "--full", "--no-reflogs"]);

        git_at(repo.path(), &["gc", "--aggressive", "--prune=now"]);
        let indices: Vec<_> = std::fs::read_dir(repo.path().join("objects/pack"))
            .unwrap()
            .map(|entry| entry.unwrap().path())
            .filter(|path| path.extension().is_some_and(|extension| extension == "idx"))
            .collect();
        assert_eq!(indices.len(), 1);
        let packed = git_at(
            repo.path(),
            &["verify-pack", "-v", indices[0].to_str().unwrap()],
        );
        assert!(
            packed.lines().any(|line| {
                let fields: Vec<_> = line.split_whitespace().collect();
                fields.len() == 7 && fields[5].parse::<u32>().is_ok_and(|depth| depth > 0)
            }),
            "repacked fixture must contain real delta objects"
        );
        let packed_clone = tempfile::TempDir::new().unwrap();
        git_at(packed_clone.path(), &["clone", "--quiet", &remote, "."]);
        assert_eq!(
            git_at(packed_clone.path(), &["rev-parse", "HEAD"]),
            latest.to_string()
        );
        assert_eq!(
            git_at(packed_clone.path(), &["rev-list", "--count", "HEAD"]),
            git(&["rev-list", "--count", "origin/main"])
        );
        git_at(packed_clone.path(), &["fsck", "--full", "--no-reflogs"]);

        let packed_shallow = tempfile::TempDir::new().unwrap();
        git_at(
            packed_shallow.path(),
            &["clone", "--quiet", "--depth=1", &remote, "."],
        );
        latest = make_commit(&repo, "after repacked snapshot", Some(latest));
        for directory in [packed_clone.path(), packed_shallow.path()] {
            git_at(directory, &["fetch", "--quiet", "origin", "main"]);
            assert_eq!(
                git_at(directory, &["rev-parse", "FETCH_HEAD"]),
                latest.to_string()
            );
            assert_eq!(
                git_at(directory, &["show", "FETCH_HEAD:file.txt"]),
                "after repacked snapshot"
            );
            git_at(directory, &["fsck", "--full", "--no-reflogs"]);
        }
        git_at(
            packed_shallow.path(),
            &["fetch", "--quiet", "--unshallow", "origin", "main"],
        );
        assert_eq!(
            git_at(
                packed_shallow.path(),
                &["rev-list", "--count", "FETCH_HEAD"]
            ),
            git_at(packed_clone.path(), &["rev-list", "--count", "FETCH_HEAD"])
        );
        git_at(packed_shallow.path(), &["fsck", "--full", "--no-reflogs"]);
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
        assert_eq!(resp, expected, "NAK terminates this negotiation response");
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
        assert_eq!(
            resp, expected,
            "negotiation NAK must not leave a flush packet for the next ACK/NAK read"
        );
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

    // -----------------------------------------------------------------------
    // T057 – T059: build_pack_for_wants
    // -----------------------------------------------------------------------

    #[test]
    fn t057_build_pack_for_wants_fresh_clone_non_empty() {
        let (_dir, repo) = make_test_repo();
        let commit_oid = make_commit(&repo, "initial", None);

        let wants = vec![commit_oid.to_string()];
        let pack = build_pack_for_wants(&repo, &wants, &[], 64 * 1024 * 1024)
            .expect("build_pack_for_wants");

        assert!(!pack.is_empty(), "fresh clone pack must be non-empty");
        assert!(
            pack.starts_with(b"PACK"),
            "output must be a valid PACK file"
        );
    }

    #[test]
    fn normal_and_shallow_packs_enforce_exact_output_ceiling() {
        let (_dir, repo) = make_test_repo();
        let tip = make_commit(&repo, "bounded output", None);
        let pack = build_pack_for_wants(&repo, &[tip.to_string()], &[], 64 * 1024 * 1024).unwrap();
        let exact = pack.len() as u64;
        for shallow in [false, true] {
            let want = format!("want {tip} side-band-64k shallow\n");
            let mut request = pkt_lines(&[want.as_bytes()]);
            if shallow {
                request.extend_from_slice(&pkt_lines(&[b"deepen 1\n"]));
            }
            flush(&mut request);
            request.extend_from_slice(&pkt_lines(&[b"done\n"]));
            for limit in [0, exact - 1, exact, exact + 1] {
                let server = HttpPackServer::new(repo.path().to_path_buf(), limit);
                let response = server.handle_upload_pack(&request);
                if limit < exact {
                    let error = response.expect_err("oversized pack must not be returned");
                    assert!(
                        error.to_string().contains("configured pack size"),
                        "shallow={shallow} limit={limit}: {error:#}"
                    );
                } else {
                    assert!(
                        response.is_ok(),
                        "exact pack size is inclusive: shallow={shallow} limit={limit}: {response:?}"
                    );
                }
            }
        }
    }

    #[test]
    fn limited_pack_writer_rejects_before_extending_output() {
        use std::io::Write;

        let interrupt = AtomicBool::new(false);
        let mut writer = LimitedPackWriter {
            bytes: Vec::new(),
            limit: 3,
            limit_message: "pack exceeds configured pack size",
            interrupt: &interrupt,
        };
        writer.write_all(b"ab").unwrap();
        assert_eq!(
            writer.write_all(b"cd").unwrap_err().kind(),
            std::io::ErrorKind::FileTooLarge
        );
        assert_eq!(writer.bytes, b"ab");
        writer.write_all(b"c").unwrap();
        writer.write_all(b"").unwrap();
        writer.flush().unwrap();
        assert_eq!(writer.bytes, b"abc");
        assert!(writer.write_all(b"d").is_err());
        assert_eq!(writer.bytes, b"abc");
    }

    #[test]
    fn limited_pack_writer_cancellation_is_terminal_and_preserves_output() {
        use std::io::Write;

        let interrupt = AtomicBool::new(false);
        let mut writer = LimitedPackWriter {
            bytes: Vec::new(),
            limit: 3,
            limit_message: "pack exceeds configured pack size",
            interrupt: &interrupt,
        };
        writer.write_all(b"ab").unwrap();
        interrupt.store(true, Ordering::Release);
        assert_eq!(
            writer.write_all(b"c").unwrap_err().kind(),
            std::io::ErrorKind::ConnectionAborted
        );
        assert!(writer.flush().is_err());
        assert_eq!(writer.bytes, b"ab");
    }

    #[test]
    fn cancelled_upload_pack_stops_before_opening_repository() {
        let root = tempfile::TempDir::new().unwrap();
        let server = HttpPackServer::new(root.path().join("missing.git"), 64 * 1024 * 1024);
        let interrupt = AtomicBool::new(true);
        let error = server
            .handle_upload_pack_with_interrupt(b"invalid request", &interrupt)
            .unwrap_err();
        assert_eq!(error.to_string(), "upload-pack cancelled");
    }

    #[test]
    fn tree_changes_observe_cancellation_after_first_record() {
        use gix::diff::tree::{visit, Visit};

        struct CancelAfterFirst<'a, V> {
            inner: V,
            interrupt: &'a AtomicBool,
            callbacks: usize,
        }
        impl<V: Visit> Visit for CancelAfterFirst<'_, V> {
            fn pop_front_tracked_path_and_set_current(&mut self) {
                self.inner.pop_front_tracked_path_and_set_current();
            }
            fn push_back_tracked_path_component(&mut self, component: &gix::bstr::BStr) {
                self.inner.push_back_tracked_path_component(component);
            }
            fn push_path_component(&mut self, component: &gix::bstr::BStr) {
                self.inner.push_path_component(component);
            }
            fn pop_path_component(&mut self) {
                self.inner.pop_path_component();
            }
            fn visit(&mut self, change: visit::Change) -> visit::Action {
                self.callbacks += 1;
                let action = self.inner.visit(change);
                if self.callbacks == 1 {
                    self.interrupt.store(true, Ordering::Release);
                }
                action
            }
        }

        let (_dir, repo) = make_test_repo();
        let blob = repo.write_blob(b"shared payload").unwrap().detach();
        let tree_id = repo
            .write_object(&gix::objs::Tree {
                entries: (0..1024)
                    .map(|index| gix::objs::tree::Entry {
                        mode: gix::objs::tree::EntryKind::Blob.into(),
                        filename: format!("entry-{index:04}.txt").into(),
                        oid: blob,
                    })
                    .collect(),
            })
            .unwrap()
            .detach();
        let tree = repo.find_tree(tree_id).unwrap();
        let interrupt = AtomicBool::new(false);
        let mut changes = CancelAfterFirst {
            inner: gix::diff::tree::Recorder::default().track_location(None),
            interrupt: &interrupt,
            callbacks: 0,
        };
        let result = gix::diff::tree(
            gix::objs::TreeRefIter::from_bytes(&[], tree.id.kind()),
            gix::objs::TreeRefIter::from_bytes(&tree.data, tree.id.kind()),
            &mut gix::diff::tree::State::default(),
            &repo.objects,
            &mut changes,
        );
        assert!(result.is_ok(), "the original recorder ignores the signal");
        assert_eq!(changes.callbacks, 1024);
        assert_eq!(changes.inner.records.len(), 1024);
        assert!(interrupt.load(Ordering::Acquire));
        drop(changes);

        interrupt.store(false, Ordering::Release);
        let mut objects = HashSet::new();
        let mut changes = CancelAfterFirst {
            inner: PackTreeObjects {
                objects: &mut objects,
                ordered: &mut Vec::new(),
                tree_deltas: &mut Vec::new(),
                interrupt: &interrupt,
            },
            interrupt: &interrupt,
            callbacks: 0,
        };
        let result = gix::diff::tree(
            gix::objs::TreeRefIter::from_bytes(&[], tree.id.kind()),
            gix::objs::TreeRefIter::from_bytes(&tree.data, tree.id.kind()),
            &mut gix::diff::tree::State::default(),
            &repo.objects,
            &mut changes,
        );
        assert!(
            matches!(result, Err(gix::diff::tree::Error::Cancelled)),
            "cancellation must stop tree-change visitation, got {result:?} after {} callbacks",
            changes.callbacks,
        );
        assert_eq!(changes.callbacks, 2);
        assert_eq!(objects, HashSet::from([blob]));
    }

    #[test]
    fn pack_tree_objects_preserve_target_identity_rules() {
        use gix::diff::tree::{visit::Change, Visit};
        use gix::objs::tree::EntryKind;
        use std::ops::ControlFlow;

        let oid = |index: u8| gix::ObjectId::from_hex(format!("{index:040x}").as_bytes()).unwrap();
        let interrupt = AtomicBool::new(false);
        let mut objects = HashSet::new();
        let mut changes = PackTreeObjects {
            objects: &mut objects,
            ordered: &mut Vec::new(),
            tree_deltas: &mut Vec::new(),
            interrupt: &interrupt,
        };
        for change in [
            Change::Addition {
                entry_mode: EntryKind::Blob.into(),
                oid: oid(1),
                relation: None,
            },
            Change::Modification {
                previous_entry_mode: EntryKind::Blob.into(),
                previous_oid: oid(2),
                entry_mode: EntryKind::Blob.into(),
                oid: oid(3),
            },
            Change::Deletion {
                entry_mode: EntryKind::Blob.into(),
                oid: oid(4),
                relation: None,
            },
            Change::Addition {
                entry_mode: EntryKind::Commit.into(),
                oid: oid(5),
                relation: None,
            },
            Change::Modification {
                previous_entry_mode: EntryKind::Tree.into(),
                previous_oid: oid(6),
                entry_mode: EntryKind::Commit.into(),
                oid: oid(7),
            },
            Change::Addition {
                entry_mode: EntryKind::Tree.into(),
                oid: oid(6),
                relation: None,
            },
            Change::Addition {
                entry_mode: EntryKind::Link.into(),
                oid: oid(8),
                relation: None,
            },
            Change::Modification {
                previous_entry_mode: EntryKind::Commit.into(),
                previous_oid: oid(5),
                entry_mode: EntryKind::BlobExecutable.into(),
                oid: oid(9),
            },
            Change::Addition {
                entry_mode: EntryKind::Blob.into(),
                oid: oid(1),
                relation: None,
            },
        ] {
            assert_eq!(changes.visit(change), ControlFlow::Continue(()));
        }
        interrupt.store(true, Ordering::Release);
        assert_eq!(
            changes.visit(Change::Addition {
                entry_mode: EntryKind::Blob.into(),
                oid: oid(10),
                relation: None,
            }),
            ControlFlow::Break(())
        );
        assert_eq!(
            objects,
            HashSet::from([oid(1), oid(3), oid(6), oid(8), oid(9)])
        );
    }

    #[test]
    fn cancelled_pack_stages_reject_before_processing_objects() {
        let (_dir, repo) = make_test_repo();
        let tip = make_commit(&repo, "cancelled snapshot", None);
        let wants = [tip.to_string()];
        let baseline = repo.find_commit(tip).unwrap().tree().unwrap();
        let interrupt = AtomicBool::new(true);
        let limit = 64 * 1024 * 1024;
        assert_eq!(
            shallow_history(&repo, &wants, 1, limit, &interrupt)
                .unwrap_err()
                .to_string(),
            "upload-pack cancelled"
        );
        assert_eq!(
            pack_objects_since_snapshot(&repo, &[tip], &baseline, &interrupt)
                .unwrap_err()
                .to_string(),
            "upload-pack cancelled"
        );
        for commits in [None, Some(vec![tip])] {
            assert_eq!(
                build_pack_with_commits(
                    &repo,
                    &wants,
                    &[],
                    commits.as_deref(),
                    false,
                    limit,
                    &interrupt,
                )
                .unwrap_err()
                .to_string(),
                "upload-pack cancelled"
            );
        }
    }

    #[test]
    fn incremental_pack_does_not_resend_unchanged_snapshot() {
        let (_dir, repo) = make_test_repo();
        let parent = make_commit(&repo, "known snapshot", None);
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
            .commit_as(
                sig,
                sig,
                "refs/heads/main",
                "unchanged tree",
                tree,
                [parent],
            )
            .unwrap()
            .detach();
        let wants = [tip.to_string()];
        let haves = [parent.to_string()];
        for commits in [None, Some(vec![tip])] {
            let pack = build_pack_with_commits(
                &repo,
                &wants,
                &haves,
                commits.as_deref(),
                false,
                64 * 1024 * 1024,
                &AtomicBool::new(false),
            )
            .expect("incremental pack");
            assert_eq!(
                u32::from_be_bytes(pack[8..12].try_into().unwrap()),
                1,
                "only the new commit is needed; the client already has its tree and blob"
            );
        }
    }

    #[test]
    fn incremental_pack_compacts_intervening_large_tree_versions() {
        use std::io::Write;
        use std::process::{Command, Stdio};

        for entry_count in [2000, 10_000] {
            let (_dir, repo) = make_test_repo();
            let mut tree = gix::objs::Tree::default();
            for index in 0..entry_count {
                tree.entries.push(gix::objs::tree::Entry {
                    mode: gix::objs::tree::EntryKind::Blob.into(),
                    filename: format!("category-{index:05}.md").into(),
                    oid: repo
                        .write_blob(format!("original category {index}\n"))
                        .unwrap()
                        .detach(),
                });
            }
            let sig = gix::actor::SignatureRef {
                name: "Test".into(),
                email: "test@example.com".into(),
                time: "1 +0000",
            };
            let parent = repo
                .commit_as(
                    sig,
                    sig,
                    "refs/heads/main",
                    "known catalog snapshot",
                    repo.write_object(&tree).unwrap().detach(),
                    std::iter::empty::<gix::ObjectId>(),
                )
                .unwrap()
                .detach();
            let mut tip = parent;
            for index in 0..48 {
                let blob = repo
                    .write_blob(format!("changed category {index}\n"))
                    .unwrap()
                    .detach();
                if index % 2 == 0 {
                    tree.entries.push(gix::objs::tree::Entry {
                        mode: gix::objs::tree::EntryKind::Blob.into(),
                        filename: format!("capacity-{index:05}.md").into(),
                        oid: blob,
                    });
                    tree.entries
                        .sort_by(|left, right| left.filename.cmp(&right.filename));
                } else {
                    let filename = format!("category-{:05}.md", 1400 + index);
                    tree.entries
                        .iter_mut()
                        .find(|entry| entry.filename == filename.as_str())
                        .unwrap()
                        .oid = blob;
                }
                tip = repo
                    .commit_as(
                        sig,
                        sig,
                        "refs/heads/main",
                        format!("catalog mutation {index}"),
                        repo.write_object(&tree).unwrap().detach(),
                        [tip],
                    )
                    .unwrap()
                    .detach();
            }
            for packed in [false, true] {
                if packed {
                    let repacked = Command::new("git")
                        .arg("--git-dir")
                        .arg(repo.path())
                        .args(["repack", "-ad"])
                        .output()
                        .unwrap();
                    assert!(repacked.status.success(), "{repacked:?}");
                }
                let pack = build_pack_with_commits(
                    &repo,
                    &[tip.to_string()],
                    &[parent.to_string()],
                    None,
                    true,
                    8 * 1024 * 1024,
                    &AtomicBool::new(false),
                )
                .expect("incremental catalog pack");
                eprintln!(
                "48 mixed mutations in a {entry_count}-entry tree (repacked={packed}): {} pack bytes",
                pack.len()
            );
                assert!(
                    pack.len() < 48 * MAX_TREE_DELTA_LITERAL_BYTES + entry_count * 64,
                    "intervening commits must not each resend the complete known catalog tree"
                );

                let (_receiver_dir, receiver) = make_test_repo();
                let fetched = Command::new("git")
                    .arg("--git-dir")
                    .arg(receiver.path())
                    .args(["fetch", "--quiet", "--no-tags"])
                    .arg(repo.path())
                    .arg(parent.to_string())
                    .output()
                    .unwrap();
                assert!(fetched.status.success(), "{fetched:?}");
                let mut child = Command::new("git")
                    .arg("--git-dir")
                    .arg(receiver.path())
                    .args(["index-pack", "--strict", "--stdin", "--fix-thin"])
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .stderr(Stdio::piped())
                    .spawn()
                    .unwrap();
                child.stdin.take().unwrap().write_all(&pack).unwrap();
                let indexed = child.wait_with_output().unwrap();
                assert!(indexed.status.success(), "{indexed:?}");
                if !packed {
                    if entry_count == 10_000 {
                        let exact = build_pack_with_commits(
                            &repo,
                            &[tip.to_string()],
                            &[parent.to_string()],
                            None,
                            true,
                            pack.len() as u64,
                            &AtomicBool::new(false),
                        )
                        .unwrap();
                        assert_eq!(exact, pack);
                        assert!(build_pack_with_commits(
                            &repo,
                            &[tip.to_string()],
                            &[parent.to_string()],
                            None,
                            true,
                            pack.len() as u64 - 1,
                            &AtomicBool::new(false),
                        )
                        .unwrap_err()
                        .to_string()
                        .contains("pack exceeds configured pack size"));
                    }
                    let hash = std::str::from_utf8(&indexed.stdout)
                        .unwrap()
                        .split_whitespace()
                        .last()
                        .unwrap();
                    let verified = Command::new("git")
                        .arg("verify-pack")
                        .arg("-v")
                        .arg(
                            receiver
                                .path()
                                .join("objects/pack")
                                .join(format!("pack-{hash}.idx")),
                        )
                        .output()
                        .unwrap();
                    assert!(verified.status.success(), "{verified:?}");
                    let mut deepest = 0;
                    for line in std::str::from_utf8(&verified.stdout).unwrap().lines() {
                        let fields: Vec<_> = line.split_whitespace().collect();
                        if fields.len() == 7 && fields[0].len() == 40 {
                            deepest = deepest.max(fields[5].parse::<usize>().unwrap());
                        }
                    }
                    assert!(deepest > 0 && deepest <= MAX_TREE_DELTA_CHAIN);
                }
                let count = Command::new("git")
                    .arg("--git-dir")
                    .arg(receiver.path())
                    .args(["rev-list", "--count"])
                    .arg(tip.to_string())
                    .arg(format!("^{parent}"))
                    .output()
                    .unwrap();
                assert!(count.status.success(), "{count:?}");
                assert_eq!(
                    count.stdout, b"48\n",
                    "all intervening commits must remain fetchable"
                );
                let content = Command::new("git")
                    .arg("--git-dir")
                    .arg(receiver.path())
                    .args(["show", &format!("{tip}:category-01447.md")])
                    .output()
                    .unwrap();
                assert!(content.status.success(), "{content:?}");
                assert_eq!(content.stdout, b"changed category 47\n");
            }
        }
    }

    #[test]
    fn tree_compaction_preserves_bounds_cancellation_and_lookup_errors() {
        use gix_pack::data::output;

        let (_dir, repo) = make_test_repo();
        let missing = gix::ObjectId::from_hex(b"1111111111111111111111111111111111111111").unwrap();
        let mut snapshot = None;
        let mut entry = output::Entry {
            id: missing,
            kind: output::entry::Kind::Base(gix::objs::Kind::Tree),
            decompressed_size: MAX_TREE_DELTA_BYTES + 1,
            compressed_data: Vec::new(),
        };
        for size in [MIN_TREE_DELTA_BYTES - 1, MAX_TREE_DELTA_BYTES + 1] {
            entry.decompressed_size = size;
            assert!(
                !compact_tree_entry(&repo, &mut entry, &mut snapshot, &AtomicBool::new(false))
                    .unwrap()
            );
            assert!(snapshot.is_none());
        }
        entry.decompressed_size = MIN_TREE_DELTA_BYTES;
        assert!(
            compact_tree_entry(&repo, &mut entry, &mut snapshot, &AtomicBool::new(true))
                .unwrap_err()
                .to_string()
                .contains("upload-pack cancelled")
        );
        assert!(
            compact_tree_entry(&repo, &mut entry, &mut snapshot, &AtomicBool::new(false))
                .unwrap_err()
                .to_string()
                .contains("read pack tree snapshot")
        );
        assert!(snapshot.is_none());
    }

    #[test]
    fn thin_tree_capability_requires_an_exact_first_want_token() {
        let oid = "1111111111111111111111111111111111111111";
        for (first_caps, later_line, expected) in [
            ("thin-pack", "", true),
            ("side-band-64k thin-pack ofs-delta", "", true),
            ("no-thin-pack", "", false),
            ("", "have", false),
            ("", "want", false),
        ] {
            let mut request = pkt_lines(&[format!("want {oid} {first_caps}\n").as_bytes()]);
            if !later_line.is_empty() {
                request.extend(pkt_lines(&[
                    format!("{later_line} {oid} thin-pack\n").as_bytes()
                ]));
            }
            assert_eq!(
                parse_upload_pack_request(&request).unwrap().thin_pack,
                expected
            );
        }
    }

    #[test]
    fn bounded_tree_delta_copy_and_literal_boundaries_decode_with_real_git() {
        use gix_pack::data::output;
        use std::io::Write;
        use std::process::{Command, Stdio};

        let interrupt = AtomicBool::new(false);
        for (name, length, position, inserted, removed) in [
            ("first byte", 100_000, 0, 1, 1),
            ("last byte", 100_000, 99_999, 1, 1),
            ("64KiB copy", 131_072, 65_536, 1, 1),
            (
                "three byte copy offset",
                MAX_TREE_DELTA_BYTES,
                700_000,
                20,
                20,
            ),
            (
                "maximum copy prefix",
                MAX_TREE_DELTA_BYTES,
                MAX_TREE_DELTA_BYTES - 1,
                1,
                1,
            ),
            ("maximum copy suffix", MAX_TREE_DELTA_BYTES, 0, 1, 1),
            ("literal 127", 9000, 4500, 127, 0),
            ("literal 128", 9000, 4500, 128, 0),
            ("literal limit", 9000, 4500, MAX_TREE_DELTA_LITERAL_BYTES, 0),
            ("delete prefix", 9000, 0, 0, 4000),
            ("delete suffix", 9000, 5000, 0, 4000),
            ("delete middle", 9000, 2500, 0, 4000),
        ] {
            let base: Vec<u8> = (0..length).map(|i| (i % 251) as u8).collect();
            let mut target = base.clone();
            target.splice(
                position..position + removed,
                std::iter::repeat_n(255, inserted),
            );
            let delta = encode_tree_delta(&base, &target, &interrupt)
                .unwrap()
                .expect(name);
            assert!(delta.len() <= MAX_TREE_DELTA_LITERAL_BYTES + 65, "{name}");
            let (_dir, repo) = make_test_repo();
            let base_id = repo.write_blob(&base).unwrap().detach();
            let target_id = repo.write_blob(&target).unwrap().detach();
            let mut entry = output::Entry::from_data(
                &output::Count {
                    id: target_id,
                    entry_pack_location: output::count::PackLocation::NotLookedUp,
                },
                &gix::objs::Data {
                    kind: gix::objs::Kind::Blob,
                    data: &delta,
                    object_hash: repo.object_hash(),
                },
                gix::zlib::Compression::DEFAULT,
            )
            .unwrap();
            entry.kind = output::entry::Kind::DeltaOid { id: base_id };
            let pack = write_pack_entries(
                std::iter::once(Ok((0, vec![entry]))),
                1,
                Vec::new(),
                1024 * 1024,
                "test pack exceeds configured size",
                &interrupt,
            )
            .unwrap();
            let mut child = Command::new("git")
                .arg("--git-dir")
                .arg(repo.path())
                .args(["index-pack", "--stdin", "--fix-thin"])
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .spawn()
                .unwrap();
            child.stdin.take().unwrap().write_all(&pack).unwrap();
            let output = child.wait_with_output().unwrap();
            assert!(output.status.success(), "{name}: {output:?}");
            let hash = std::str::from_utf8(&output.stdout)
                .unwrap()
                .split_whitespace()
                .last()
                .unwrap();
            let index = gix_pack::index::File::at(
                repo.path()
                    .join("objects/pack")
                    .join(format!("pack-{hash}.idx")),
                gix::hash::Kind::Sha1,
            )
            .unwrap();
            assert_eq!(
                index.iter().map(|entry| entry.oid).collect::<HashSet<_>>(),
                HashSet::from([base_id, target_id]),
                "{name}"
            );
        }
    }

    #[test]
    fn thin_tree_delta_bounds_cancellation_and_lookup_errors_are_explicit() {
        let interrupt = AtomicBool::new(false);
        let base = vec![0; 9000];
        assert!(encode_tree_delta(&base, &base, &interrupt)
            .unwrap()
            .is_none());
        assert!(encode_tree_delta(&[], &base, &interrupt).unwrap().is_none());
        assert!(
            encode_tree_delta(&vec![0; MAX_TREE_DELTA_BYTES + 1], &base, &interrupt)
                .unwrap()
                .is_none()
        );
        let mut target = base.clone();
        target.splice(
            4500..4500,
            std::iter::repeat_n(255, MAX_TREE_DELTA_LITERAL_BYTES + 1),
        );
        assert!(encode_tree_delta(&base, &target, &interrupt)
            .unwrap()
            .is_none());
        assert!(encode_tree_delta(&base, &target, &AtomicBool::new(true)).is_err());
        let (_dir, repo) = make_test_repo();
        let blob = repo.write_blob("not a tree").unwrap().detach();
        let missing = gix::ObjectId::from_hex(b"1111111111111111111111111111111111111111").unwrap();
        assert!(thin_tree_entry(
            &repo,
            &[TreeDeltaBase {
                base: blob,
                target: missing
            }],
            &interrupt
        )
        .unwrap_err()
        .to_string()
        .contains("not a tree"));
        assert!(thin_tree_entry(
            &repo,
            &[TreeDeltaBase {
                base: missing,
                target: missing
            }],
            &interrupt
        )
        .unwrap_err()
        .to_string()
        .contains("read thin tree header"));
        let tree = repo
            .write_object(gix::objs::Tree {
                entries: vec![gix::objs::tree::Entry {
                    mode: gix::objs::tree::EntryKind::Blob.into(),
                    filename: "x".repeat(MAX_TREE_DELTA_BYTES + 1).into(),
                    oid: blob,
                }],
            })
            .unwrap()
            .detach();
        assert!(
            thin_tree_entry(
                &repo,
                &[TreeDeltaBase {
                    base: tree,
                    target: missing
                }],
                &interrupt
            )
            .unwrap()
            .is_none(),
            "an oversized base must stop at its header without reading the target"
        );
        let small = repo
            .write_object(gix::objs::Tree::default())
            .unwrap()
            .detach();
        let mut candidates = vec![
            TreeDeltaBase {
                base: small,
                target: missing
            };
            MAX_TREE_DELTA_CANDIDATES
        ];
        candidates.push(TreeDeltaBase {
            base: missing,
            target: missing,
        });
        assert!(
            thin_tree_entry(&repo, &candidates, &interrupt)
                .unwrap()
                .is_none(),
            "candidate header work must stop at the fixed bound"
        );
        let mut recorded = Vec::new();
        for i in 0..32 {
            let candidate = TreeDeltaBase {
                base: small,
                target: gix::ObjectId::from_hex(format!("{i:040x}").as_bytes()).unwrap(),
            };
            record_tree_delta(&mut recorded, candidate);
            record_tree_delta(&mut recorded, candidate);
        }
        assert_eq!(recorded.len(), MAX_TREE_DELTA_CANDIDATES);
    }

    #[test]
    fn negotiated_thin_tree_pack_preserves_real_git_and_exact_limits() {
        use std::io::Write;
        use std::path::Path;
        use std::process::{Command, Stdio};

        let (_dir, repo) = make_test_repo();
        let mut tree = gix::objs::Tree::default();
        for i in 0..1849 {
            tree.entries.push(gix::objs::tree::Entry {
                mode: gix::objs::tree::EntryKind::Blob.into(),
                filename: format!("namespace-fixture-{i:05}.yaml").into(),
                oid: repo
                    .write_blob(format!("namespace fixture {i}\n"))
                    .unwrap()
                    .detach(),
            });
        }
        let previous_tree = repo.write_object(&tree).unwrap().detach();
        let mut root = gix::objs::Tree {
            entries: vec![gix::objs::tree::Entry {
                mode: gix::objs::tree::EntryKind::Tree.into(),
                filename: "namespaces".into(),
                oid: previous_tree,
            }],
        };
        let sig = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "1 +0000",
        };
        let parent = repo
            .commit_as(
                sig,
                sig,
                "refs/heads/main",
                "known snapshot",
                repo.write_object(&root).unwrap().detach(),
                std::iter::empty::<gix::ObjectId>(),
            )
            .unwrap()
            .detach();
        let blob = repo
            .write_blob("namespace fixture 1400 updated\n")
            .unwrap()
            .detach();
        tree.entries[1400].oid = blob;
        let changed_tree = repo.write_object(&tree).unwrap().detach();
        root.entries[0].oid = changed_tree;
        let tip = repo
            .commit_as(
                sig,
                sig,
                "refs/heads/main",
                "updated snapshot",
                repo.write_object(&root).unwrap().detach(),
                [parent],
            )
            .unwrap()
            .detach();
        let request = |thin| {
            pkt_lines(&[
                format!(
                    "want {tip} side-band-64k{}\n",
                    if thin { " thin-pack" } else { "" }
                )
                .as_bytes(),
                b"deepen 1\n",
                format!("have {parent}\n").as_bytes(),
                b"done\n",
            ])
        };
        let unpack = |response: &[u8], common: Option<gix::ObjectId>| {
            let mut pos = 0;
            let mut pack = Vec::new();
            let mut acknowledgements = 0;
            let expected_ack = match common {
                Some(id) => format!("ACK {id}\n"),
                None => "NAK\n".to_owned(),
            };
            while pos < response.len() {
                let len = usize::from_str_radix(
                    std::str::from_utf8(&response[pos..pos + 4]).unwrap(),
                    16,
                )
                .unwrap();
                if len == 0 {
                    pos += 4;
                    continue;
                }
                let line = &response[pos + 4..pos + len];
                if line.first() == Some(&1) {
                    pack.extend_from_slice(&line[1..]);
                } else if line == expected_ack.as_bytes() {
                    acknowledgements += 1;
                } else {
                    assert!(line.starts_with(b"shallow "));
                }
                pos += len;
            }
            assert_eq!(acknowledgements, 1);
            assert!(pack.starts_with(b"PACK"));
            pack
        };
        let server = HttpPackServer::new(repo.path().to_owned(), 32 * 1024 * 1024);
        let full = unpack(
            &server.handle_upload_pack(&request(false)).unwrap(),
            Some(parent),
        );
        let thin = unpack(
            &server.handle_upload_pack(&request(true)).unwrap(),
            Some(parent),
        );
        eprintln!(
            "known-have tree pack: full={} thin={}",
            full.len(),
            thin.len()
        );
        assert!(
            thin.len() * 8 < full.len(),
            "one changed tree must not resend the complete known snapshot"
        );
        assert_eq!(u32::from_be_bytes(thin[8..12].try_into().unwrap()), 4);
        let exact = HttpPackServer::new(repo.path().to_owned(), thin.len() as u64);
        assert_eq!(
            unpack(
                &exact.handle_upload_pack(&request(true)).unwrap(),
                Some(parent)
            ),
            thin
        );
        let too_small = HttpPackServer::new(repo.path().to_owned(), thin.len() as u64 - 1);
        assert!(too_small
            .handle_upload_pack(&request(true))
            .unwrap_err()
            .to_string()
            .contains("shallow pack exceeds"));
        assert!(
            exact.handle_upload_pack(&request(false)).is_err(),
            "non-thin clients retain ordinary encoding and the same ceiling"
        );
        let git = |path: &Path, args: &[&str], input: &[u8]| {
            let mut child = Command::new("git")
                .arg("--git-dir")
                .arg(path)
                .args(args)
                .stdin(Stdio::piped())
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .spawn()
                .unwrap();
            child.stdin.take().unwrap().write_all(input).unwrap();
            child.wait_with_output().unwrap()
        };
        for have in ["".to_owned(), format!("have {}\n", "1".repeat(40))] {
            let mut request = pkt_lines(&[
                format!("want {tip} thin-pack side-band-64k\n").as_bytes(),
                b"deepen 1\n",
            ]);
            if !have.is_empty() {
                request.extend(pkt_lines(&[have.as_bytes()]));
            }
            request.extend(pkt_lines(&[b"done\n"]));
            let snapshot = unpack(&server.handle_upload_pack(&request).unwrap(), None);
            let (_clone_dir, clone) = make_test_repo();
            let indexed = git(clone.path(), &["index-pack", "--stdin"], &snapshot);
            assert!(
                indexed.status.success(),
                "no known have must produce a standalone pack: {indexed:?}"
            );
        }
        let (_receiver_dir, receiver) = make_test_repo();
        assert!(
            !git(
                receiver.path(),
                &["index-pack", "--stdin", "--fix-thin"],
                &thin
            )
            .status
            .success(),
            "a thin pack must require the negotiated known base"
        );
        let fetched = git(
            receiver.path(),
            &[
                "fetch",
                "--no-tags",
                repo.path().to_str().unwrap(),
                &parent.to_string(),
            ],
            &[],
        );
        assert!(fetched.status.success(), "{fetched:?}");
        assert!(
            !git(receiver.path(), &["cat-file", "-e", &tip.to_string()], &[])
                .status
                .success()
        );
        let indexed = git(
            receiver.path(),
            &["index-pack", "--strict", "--stdin", "--fix-thin"],
            &thin,
        );
        assert!(indexed.status.success(), "{indexed:?}");
        assert!(
            git(receiver.path(), &["cat-file", "-e", &tip.to_string()], &[])
                .status
                .success()
        );
        assert!(git(
            receiver.path(),
            &["cat-file", "-e", &changed_tree.to_string()],
            &[]
        )
        .status
        .success());
        let actual = git(
            receiver.path(),
            &["cat-file", "blob", &blob.to_string()],
            &[],
        );
        assert!(actual.status.success(), "{actual:?}");
        assert_eq!(actual.stdout, b"namespace fixture 1400 updated\n");
        let flat_parent = repo
            .commit_as(
                sig,
                sig,
                "refs/heads/flat",
                "known flat snapshot",
                previous_tree,
                std::iter::empty::<gix::ObjectId>(),
            )
            .unwrap()
            .detach();
        let flat_tip = repo
            .commit_as(
                sig,
                sig,
                "refs/heads/flat",
                "updated flat snapshot",
                changed_tree,
                [flat_parent],
            )
            .unwrap()
            .detach();
        let flat = unpack(
            &server
                .handle_upload_pack(&pkt_lines(&[
                    format!("want {flat_tip} thin-pack side-band-64k\n").as_bytes(),
                    b"deepen 1\n",
                    format!("have {flat_parent}\n").as_bytes(),
                    b"done\n",
                ]))
                .unwrap(),
            Some(flat_parent),
        );
        assert!(
            flat.len() * 8 < full.len(),
            "a changed root tree also uses the known snapshot"
        );
        assert_eq!(u32::from_be_bytes(flat[8..12].try_into().unwrap()), 3);
    }

    #[test]
    fn backdated_fetch_keeps_history_until_exact_have_boundary() {
        use std::process::Command;

        let (_dir, repo) = make_test_repo();
        let root = make_commit(&repo, "known root", None);
        let tree = repo.find_commit(root).unwrap().tree_id().unwrap().detach();
        let newer = gix::actor::SignatureRef {
            name: "Test".into(),
            email: "test@example.com".into(),
            time: "1 +0000",
        };
        let known = repo
            .commit_as(
                newer,
                newer,
                "refs/heads/main",
                "known snapshot",
                tree,
                [root],
            )
            .unwrap()
            .detach();
        let middle = make_commit(&repo, "backdated parent", Some(known));
        let tip = make_commit(&repo, "backdated child", Some(middle));
        let expected: HashSet<_> = [
            middle,
            tip,
            repo.find_commit(middle)
                .unwrap()
                .tree_id()
                .unwrap()
                .detach(),
            repo.find_commit(tip).unwrap().tree_id().unwrap().detach(),
            repo.write_blob("backdated parent").unwrap().detach(),
            repo.write_blob("backdated child").unwrap().detach(),
        ]
        .into_iter()
        .collect();
        let git = |args: &[&str]| {
            let output = Command::new("git")
                .arg("--git-dir")
                .arg(repo.path())
                .args(args)
                .output()
                .unwrap();
            assert!(output.status.success(), "{output:?}");
        };
        git(&["config", "core.commitGraph", "true"]);
        for commit_graph in [false, true] {
            if commit_graph {
                git(&["commit-graph", "write", "--reachable"]);
            }
            let reader = gix::open(repo.path()).unwrap();
            assert_eq!(reader.commit_graph().is_ok(), commit_graph);
            let cutoff_walk: Vec<_> = reader
                .rev_walk([tip])
                .with_boundary([known])
                .all()
                .unwrap()
                .map(|entry| entry.unwrap().id)
                .collect();
            assert!(
                cutoff_walk.is_empty(),
                "negative control must reproduce the date cutoff"
            );

            let pack = build_pack_for_wants(
                &reader,
                &[tip.to_string()],
                &[known.to_string()],
                64 * 1024 * 1024,
            )
            .expect("backdated wanted commits must not be discarded by a have timestamp");
            let quarantine = stage_pack_from_reader(&pack[..], Some((*reader.objects).clone()))
                .expect("valid incremental pack");
            let index =
                gix_pack::index::File::at(&quarantine.index_path, gix::hash::Kind::Sha1).unwrap();
            let actual: HashSet<_> = index.iter().map(|entry| entry.oid).collect();
            assert_eq!(actual, expected, "commit_graph={commit_graph}");
            assert!(!actual.contains(&known) && !actual.contains(&root));
        }
    }

    #[test]
    fn t058_build_pack_for_wants_client_has_everything_returns_err() {
        let (_dir, repo) = make_test_repo();
        let commit_oid = make_commit(&repo, "initial", None);

        // Client has the tip — rev_walk with have=tip yields no commits
        let wants = vec![commit_oid.to_string()];
        let haves = vec![commit_oid.to_string()];
        let result = build_pack_for_wants(&repo, &wants, &haves, 64 * 1024 * 1024);

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
        let result = build_pack_for_wants(&repo, &wants, &[], 64 * 1024 * 1024);
        assert!(
            result.is_err(),
            "non-existent want OID must return an error"
        );
    }
}
