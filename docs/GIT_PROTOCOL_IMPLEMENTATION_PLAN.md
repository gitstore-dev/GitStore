# Git Protocol Implementation Plan (Native git://)

**Goal**: Implement a full git:// protocol server in Rust for GitStore's built-in git engine.

**Reference**: [Git Protocol Documentation](https://git-scm.com/book/en/v2/Git-Internals-Transfer-Protocols)

---

## Overview

The git:// protocol (also called "git daemon protocol") is a TCP-based protocol running on port 9418. It uses a packet-line format for all communication and supports both read operations (fetch/clone) and write operations (push).

### Architecture

```
Client (git CLI)  ←→  TCP Socket (9418)  ←→  Git Protocol Server  ←→  libgit2  ←→  Filesystem
                           ↓
                    Packet-Line Format
                           ↓
                    upload-pack / receive-pack
                           ↓
                    Pre-receive hooks (validation)
                           ↓
                    Websocket broadcast
```

---

## Phase 1: Packet-Line Protocol Implementation

### What is Packet-Line Format?

Git uses a 4-byte hex length prefix followed by data:
```
0032git-upload-pack /project.git\0host=myserver.com\0
^^^^
length in hex (includes the 4 bytes itself)
```

Special packets:
- `0000` - flush packet (end of data stream)
- `0001` - delimiter packet (separates sections)
- `0002` - response end packet

### Implementation: `src/git/packet_line.rs`

```rust
use std::io::{Read, Write, BufRead, BufReader};
use anyhow::{Context, Result};

const PKT_LINE_SIZE: usize = 4;
const MAX_PACKET_SIZE: usize = 65520; // 65535 - 4 (length) - some overhead

/// Packet types
#[derive(Debug, PartialEq)]
pub enum PacketType {
    Data(Vec<u8>),
    Flush,          // 0000
    Delimiter,      // 0001
    ResponseEnd,    // 0002
}

/// Read a single packet line from a stream
pub fn read_packet<R: Read>(reader: &mut R) -> Result<PacketType> {
    let mut length_buf = [0u8; PKT_LINE_SIZE];
    reader.read_exact(&mut length_buf)
        .context("Failed to read packet length")?;

    let length_str = std::str::from_utf8(&length_buf)
        .context("Invalid UTF-8 in packet length")?;

    let length = u16::from_str_radix(length_str, 16)
        .context("Invalid hex length")?;

    match length {
        0 => Ok(PacketType::Flush),
        1 => Ok(PacketType::Delimiter),
        2 => Ok(PacketType::ResponseEnd),
        n if n >= PKT_LINE_SIZE as u16 => {
            let data_len = (n as usize) - PKT_LINE_SIZE;
            let mut data = vec![0u8; data_len];
            reader.read_exact(&mut data)
                .context("Failed to read packet data")?;
            Ok(PacketType::Data(data))
        }
        _ => anyhow::bail!("Invalid packet length: {}", length),
    }
}

/// Write a packet line to a stream
pub fn write_packet<W: Write>(writer: &mut W, data: &[u8]) -> Result<()> {
    if data.is_empty() {
        // Write flush packet
        writer.write_all(b"0000")?;
        return Ok(());
    }

    let total_length = data.len() + PKT_LINE_SIZE;
    if total_length > MAX_PACKET_SIZE {
        anyhow::bail!("Packet too large: {} bytes", total_length);
    }

    let length_hex = format!("{:04x}", total_length);
    writer.write_all(length_hex.as_bytes())?;
    writer.write_all(data)?;
    Ok(())
}

/// Write multiple packets followed by a flush
pub fn write_packets_with_flush<W: Write>(writer: &mut W, packets: &[&[u8]]) -> Result<()> {
    for packet in packets {
        write_packet(writer, packet)?;
    }
    write_packet(writer, &[])?; // Flush
    Ok(())
}

/// Read packets until flush
pub fn read_until_flush<R: Read>(reader: &mut R) -> Result<Vec<Vec<u8>>> {
    let mut packets = Vec::new();
    loop {
        match read_packet(reader)? {
            PacketType::Flush => break,
            PacketType::Data(data) => packets.push(data),
            _ => {}  // Ignore delimiters
        }
    }
    Ok(packets)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;

    #[test]
    fn test_read_write_data_packet() {
        let data = b"hello world";
        let mut buf = Vec::new();
        write_packet(&mut buf, data).unwrap();

        let expected = b"0010hello world";  // 16 bytes = 0x10
        assert_eq!(&buf, expected);

        let mut reader = Cursor::new(buf);
        match read_packet(&mut reader).unwrap() {
            PacketType::Data(d) => assert_eq!(d, data),
            _ => panic!("Wrong packet type"),
        }
    }

    #[test]
    fn test_flush_packet() {
        let mut buf = Vec::new();
        write_packet(&mut buf, &[]).unwrap();
        assert_eq!(&buf, b"0000");

        let mut reader = Cursor::new(buf);
        assert_eq!(read_packet(&mut reader).unwrap(), PacketType::Flush);
    }
}
```

---

## Phase 2: Git Protocol Server Implementation

### Git Daemon Protocol Flow

**For git-upload-pack (fetch/clone)**:
1. Client connects to port 9418
2. Client sends: `git-upload-pack /path/to/repo.git\0host=hostname\0`
3. Server responds with ref advertisement
4. Client sends wanted refs and capabilities
5. Server streams pack file

**For git-receive-pack (push)**:
1. Client connects to port 9418
2. Client sends: `git-receive-pack /path/to/repo.git\0host=hostname\0`
3. Server responds with ref advertisement
4. Client sends pack file with new objects
5. Server validates (pre-receive hooks)
6. Server accepts or rejects

### Implementation: `src/git/protocol_server.rs`

```rust
use tokio::net::{TcpListener, TcpStream};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use tracing::{debug, error, info};
use anyhow::{Context, Result};

use crate::git::packet_line::{read_packet, write_packet, PacketType};
use crate::validation::Validator;

/// Git protocol server
pub struct GitProtocolServer {
    addr: SocketAddr,
    repo_base_path: PathBuf,
    validator: Arc<Validator>,
}

impl GitProtocolServer {
    pub fn new(addr: SocketAddr, repo_base_path: PathBuf, validator: Arc<Validator>) -> Self {
        Self {
            addr,
            repo_base_path,
            validator,
        }
    }

    /// Start the git protocol server
    pub async fn start(self) -> Result<()> {
        let listener = TcpListener::bind(&self.addr).await?;
        info!(addr = %self.addr, "Git protocol server listening");

        loop {
            match listener.accept().await {
                Ok((stream, peer_addr)) => {
                    debug!(peer = %peer_addr, "New git connection");
                    let repo_base = self.repo_base_path.clone();
                    let validator = Arc::clone(&self.validator);

                    tokio::spawn(async move {
                        if let Err(e) = handle_git_connection(stream, repo_base, validator).await {
                            error!(peer = %peer_addr, error = %e, "Git connection error");
                        }
                    });
                }
                Err(e) => {
                    error!(error = %e, "Failed to accept connection");
                }
            }
        }
    }
}

/// Handle a single git connection
async fn handle_git_connection(
    mut stream: TcpStream,
    repo_base_path: PathBuf,
    validator: Arc<Validator>,
) -> Result<()> {
    // Read initial request
    let mut buf = vec![0u8; 4096];
    let n = stream.read(&mut buf).await?;
    let request = String::from_utf8_lossy(&buf[..n]);

    debug!(request = %request, "Git protocol request");

    // Parse request: "git-upload-pack /repo.git\0host=...\0"
    let parts: Vec<&str> = request.split('\0').collect();
    if parts.is_empty() {
        anyhow::bail!("Invalid git protocol request");
    }

    let command_and_path = parts[0];
    let parts: Vec<&str> = command_and_path.split(' ').collect();
    if parts.len() != 2 {
        anyhow::bail!("Invalid command format");
    }

    let command = parts[0];
    let repo_path = parts[1].trim_start_matches('/');

    match command {
        "git-upload-pack" => {
            handle_upload_pack(&mut stream, &repo_base_path, repo_path).await
        }
        "git-receive-pack" => {
            handle_receive_pack(&mut stream, &repo_base_path, repo_path, validator).await
        }
        _ => anyhow::bail!("Unknown git command: {}", command),
    }
}

/// Handle git-upload-pack (fetch/clone)
async fn handle_upload_pack(
    stream: &mut TcpStream,
    repo_base: &Path,
    repo_path: &str,
) -> Result<()> {
    let full_path = repo_base.join(repo_path);
    info!(repo = %repo_path, "Handling upload-pack request");

    // Open repository with git2
    let repo = git2::Repository::open(&full_path)
        .context("Failed to open repository")?;

    // Send ref advertisement
    advertise_refs(stream, &repo, "upload-pack").await?;

    // Read client wants
    let wants = read_client_wants(stream).await?;
    debug!(wants = ?wants, "Client wants");

    // Stream pack file
    stream_pack_file(stream, &repo, &wants).await?;

    info!("Upload-pack complete");
    Ok(())
}

/// Handle git-receive-pack (push)
async fn handle_receive_pack(
    stream: &mut TcpStream,
    repo_base: &Path,
    repo_path: &str,
    validator: Arc<Validator>,
) -> Result<()> {
    let full_path = repo_base.join(repo_path);
    info!(repo = %repo_path, "Handling receive-pack request");

    // Open repository
    let repo = git2::Repository::open(&full_path)
        .context("Failed to open repository")?;

    // Send ref advertisement
    advertise_refs(stream, &repo, "receive-pack").await?;

    // Read pack file from client
    let pack_data = receive_pack_file(stream).await?;

    // Validate pack (pre-receive hook)
    let old_head = repo.head().ok()
        .and_then(|h| h.target())
        .map(|oid| oid.to_string());

    // Unpack objects
    unpack_objects(&repo, &pack_data)?;

    let new_head = repo.head().ok()
        .and_then(|h| h.target())
        .map(|oid| oid.to_string());

    // Run validation
    if let Some(new_oid) = &new_head {
        validator.validate_push(&repo, old_head.as_deref(), new_oid)?;
    }

    // Send success response
    send_status_report(stream, true).await?;

    info!("Receive-pack complete");
    Ok(())
}

// Helper functions (to implement)
async fn advertise_refs(
    stream: &mut TcpStream,
    repo: &git2::Repository,
    service: &str,
) -> Result<()> {
    // Send ref advertisement in packet-line format
    // Format: "0000" (flush) followed by:
    // "<sha1> <refname>\0<capabilities>\n"
    todo!("Implement ref advertisement")
}

async fn read_client_wants(stream: &mut TcpStream) -> Result<Vec<String>> {
    // Read packets until flush to get list of wanted refs
    todo!("Implement client wants parsing")
}

async fn stream_pack_file(
    stream: &mut TcpStream,
    repo: &git2::Repository,
    wants: &[String],
) -> Result<()> {
    // Use libgit2 packbuilder to stream pack
    todo!("Implement pack streaming")
}

async fn receive_pack_file(stream: &mut TcpStream) -> Result<Vec<u8>> {
    // Read pack file from client
    todo!("Implement pack reception")
}

fn unpack_objects(repo: &git2::Repository, pack_data: &[u8]) -> Result<()> {
    // Unpack received objects into repository
    todo!("Implement object unpacking")
}

async fn send_status_report(stream: &mut TcpStream, success: bool) -> Result<()> {
    // Send status report to client
    // Format: "unpack ok\n" or "unpack error: message\n"
    todo!("Implement status report")
}
```

---

## Phase 3: Detailed Implementation Steps

### Step 3.1: Implement `advertise_refs`

This sends the list of refs (branches, tags) to the client.

```rust
async fn advertise_refs(
    stream: &mut TcpStream,
    repo: &git2::Repository,
    service: &str,
) -> Result<()> {
    use crate::git::packet_line::write_packet;

    // Capabilities
    let capabilities = "multi_ack thin-pack side-band side-band-64k ofs-delta \
                       shallow deepen-since deepen-not deepen-relative \
                       no-progress include-tag multi_ack_detailed \
                       allow-tip-sha1-in-want allow-reachable-sha1-in-want \
                       no-done symref=HEAD:refs/heads/main agent=git/gitstore-1.0";

    // Get HEAD
    let head = repo.head()?;
    let head_oid = head.target().context("No HEAD target")?;
    let head_ref = head.name().context("No HEAD name")?;

    // First ref includes capabilities
    let first_line = format!(
        "{} {}\0{}\n",
        head_oid,
        head_ref,
        capabilities
    );
    write_packet(stream, first_line.as_bytes()).await?;

    // List all refs
    let refs = repo.references()?;
    for reference in refs {
        let r = reference?;
        if let (Some(name), Some(target)) = (r.name(), r.target()) {
            let line = format!("{} {}\n", target, name);
            write_packet(stream, line.as_bytes()).await?;
        }
    }

    // Flush packet
    write_packet(stream, &[]).await?;

    Ok(())
}
```

### Step 3.2: Implement `read_client_wants`

```rust
async fn read_client_wants(stream: &mut TcpStream) -> Result<Vec<String>> {
    use crate::git::packet_line::{read_packet, PacketType};

    let mut wants = Vec::new();
    let mut done = false;

    while !done {
        match read_packet(stream).await? {
            PacketType::Flush => break,
            PacketType::Data(data) => {
                let line = String::from_utf8_lossy(&data);
                if line.starts_with("want ") {
                    // Extract OID: "want <sha1> <capabilities>\n"
                    let parts: Vec<&str> = line.split_whitespace().collect();
                    if parts.len() >= 2 {
                        wants.push(parts[1].to_string());
                    }
                } else if line.starts_with("done") {
                    done = true;
                }
            }
            _ => {}
        }
    }

    Ok(wants)
}
```

### Step 3.3: Implement Pack File Streaming

```rust
async fn stream_pack_file(
    stream: &mut TcpStream,
    repo: &git2::Repository,
    wants: &[String],
) -> Result<()> {
    use git2::{Oid, PackBuilder};

    let mut packbuilder = repo.packbuilder()?;

    // Add wanted objects
    for want_str in wants {
        let oid = Oid::from_str(want_str)?;
        packbuilder.insert_object(oid, None)?;
    }

    // Write pack to stream
    let mut pack_data = Vec::new();
    packbuilder.foreach(|data| {
        pack_data.extend_from_slice(data);
        true
    })?;

    // Send pack in packet-line format
    // Git protocol expects: "0008NAK\n" + pack data
    write_packet(stream, b"NAK\n").await?;
    stream.write_all(&pack_data).await?;

    Ok(())
}
```

---

## Phase 4: Integration with Validation

Update the `handle_receive_pack` to properly integrate with the validator:

```rust
async fn handle_receive_pack(
    stream: &mut TcpStream,
    repo_base: &Path,
    repo_path: &str,
    validator: Arc<Validator>,
    broadcaster: Arc<RwLock<Broadcaster>>,
) -> Result<()> {
    let full_path = repo_base.join(repo_path);
    let repo = git2::Repository::open(&full_path)?;

    advertise_refs(stream, &repo, "receive-pack").await?;

    let updates = read_ref_updates(stream).await?;
    let pack_data = receive_pack_file(stream).await?;

    // Get old HEAD
    let old_head = repo.head().ok()
        .and_then(|h| h.target())
        .map(|oid| oid.to_string());

    // Unpack
    unpack_objects(&repo, &pack_data)?;

    // Get new HEAD
    let new_head = repo.head().ok()
        .and_then(|h| h.target())
        .map(|oid| oid.to_string());

    // VALIDATION
    if let Some(new_oid) = &new_head {
        match validator.validate_push(&repo, old_head.as_deref(), new_oid) {
            Ok(()) => {
                send_status_report(stream, true).await?;

                // Check for tags and broadcast
                if let Ok(tags) = repo.tag_names(None) {
                    for tag_name in tags.iter().flatten() {
                        let broadcaster = broadcaster.read().await;
                        let message = format!(
                            r#"{{"type":"tag","repository":"{}","tag":"{}","commit":"{}"}}"#,
                            repo_path, tag_name, new_oid
                        );
                        broadcaster.broadcast(&message).await;
                    }
                }
            }
            Err(validation_result) => {
                // Send validation errors to client
                send_error_report(stream, &validation_result).await?;
            }
        }
    }

    Ok(())
}

async fn send_error_report(
    stream: &mut TcpStream,
    validation_result: &ValidationResult,
) -> Result<()> {
    let error_msg = validation_result.format_for_git();
    write_packet(stream, format!("unpack {}\n", error_msg).as_bytes()).await?;
    write_packet(stream, &[]).await?;  // Flush
    Ok(())
}
```

---

## Phase 5: Testing

### Unit Tests

```rust
#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_packet_line_roundtrip() {
        let data = b"test data";
        let mut buf = Vec::new();
        write_packet(&mut buf, data).unwrap();

        let mut reader = Cursor::new(buf);
        match read_packet(&mut reader).await.unwrap() {
            PacketType::Data(d) => assert_eq!(d, data),
            _ => panic!("Wrong packet type"),
        }
    }

    #[tokio::test]
    async fn test_ref_advertisement() {
        // Create test repository
        let temp_dir = tempdir().unwrap();
        let repo = git2::Repository::init(&temp_dir).unwrap();

        // Create test commit
        let sig = git2::Signature::now("Test", "test@example.com").unwrap();
        let tree_id = {
            let mut index = repo.index().unwrap();
            index.write_tree().unwrap()
        };
        let tree = repo.find_tree(tree_id).unwrap();
        repo.commit(Some("HEAD"), &sig, &sig, "Initial", &tree, &[]).unwrap();

        // Test advertisement
        let mut stream = Vec::new();
        advertise_refs(&mut stream, &repo, "upload-pack").await.unwrap();

        // Verify packet format
        assert!(stream.len() > 0);
        let response = String::from_utf8_lossy(&stream);
        assert!(response.contains("HEAD"));
    }
}
```

### Integration Tests

```rust
#[tokio::test]
async fn test_full_clone_flow() {
    // Start test server
    let server = GitProtocolServer::new(
        "127.0.0.1:9418".parse().unwrap(),
        PathBuf::from("/tmp/test-repos"),
        Arc::new(Validator::new()),
    );

    tokio::spawn(async move {
        server.start().await.unwrap();
    });

    // Wait for server to start
    tokio::time::sleep(Duration::from_millis(100)).await;

    // Test with real git client
    let output = Command::new("git")
        .args(["clone", "git://127.0.0.1:9418/test.git"])
        .output()
        .expect("Failed to run git clone");

    assert!(output.status.success());
}
```

---

## Phase 6: Performance Optimization

### Considerations

1. **Connection Pooling**: Reuse TCP connections
2. **Pack File Compression**: Use zlib efficiently
3. **Concurrent Pushes**: Handle multiple simultaneous pushes
4. **Large Repository Support**: Stream packs instead of loading into memory
5. **Delta Compression**: Use git's delta algorithm for efficient transfer

### Benchmark Targets

- Clone 1000-file repo: < 2 seconds
- Push 100 file changes: < 5 seconds
- Validate push: < 3 seconds
- Memory usage: < 100MB per connection

---

## Phase 7: Security Considerations

1. **Authentication**: Add SSH-like authentication layer
2. **Authorization**: Per-repository access control
3. **Rate Limiting**: Prevent DOS attacks
4. **Input Validation**: Sanitize all packet data
5. **Resource Limits**: Max pack size, connection timeouts

---

## Estimated Implementation Timeline

- **Phase 1** (Packet-Line): 1-2 days
- **Phase 2** (Protocol Server): 2-3 days
- **Phase 3** (Ref Advertisement): 1 day
- **Phase 4** (Pack Streaming): 2-3 days
- **Phase 5** (Testing): 2 days
- **Phase 6** (Optimization): 2-3 days
- **Phase 7** (Security): 1-2 days

**Total**: 11-17 days of focused development

---

## References

1. [Git Protocol V2 Documentation](https://git-scm.com/docs/protocol-v2)
2. [Git Internals - Transfer Protocols](https://git-scm.com/book/en/v2/Git-Internals-Transfer-Protocols)
3. [libgit2 Documentation](https://libgit2.org/docs/)
4. [Git Packet Protocol](https://github.com/git/git/blob/master/Documentation/technical/protocol-common.txt)
5. [Rust git2 Crate](https://docs.rs/git2/latest/git2/)

---

## Next Steps

1. Start with Phase 1 (packet-line implementation)
2. Create comprehensive tests for each phase
3. Profile performance at each milestone
4. Document protocol edge cases
5. Build integration tests with real git clients

This implementation will provide a production-ready, native git:// protocol server that fully complies with the `001-git-backed-ecommerce` specification.
