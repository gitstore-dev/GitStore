// Websocket broadcast functionality

use crate::git::events::GitEvent;
use crate::websocket::connections::ConnectionManager;
use anyhow::Result;
use std::sync::Arc;
use tokio::sync::RwLock;
use tracing::{debug, info};

/// Broadcaster wraps ConnectionManager for easier sharing
#[derive(Clone)]
pub struct Broadcaster {
    manager: Arc<RwLock<ConnectionManager>>,
}

impl Broadcaster {
    /// Create a new broadcaster
    pub fn new(manager: Arc<RwLock<ConnectionManager>>) -> Self {
        Self { manager }
    }

    /// Broadcast a message to all connected clients
    pub async fn broadcast(&self, message: &str) {
        let manager = self.manager.read().await;
        manager.broadcast(message.to_string());
    }

    /// Broadcast a git event
    pub async fn broadcast_event(&self, event: GitEvent) -> Result<()> {
        let json = event.to_json()?;
        self.broadcast(&json).await;
        Ok(())
    }
}

/// Broadcast a git event to all connected websocket clients
pub async fn broadcast_event(
    manager: &Arc<RwLock<ConnectionManager>>,
    event: GitEvent,
) -> Result<()> {
    let json = event.to_json()?;

    info!(
        event = event.tag_name(),
        connection_count = manager.read().await.connection_count(),
        "Broadcasting git event"
    );

    let manager = manager.read().await;
    manager.broadcast(json);

    debug!("Event broadcast complete");

    Ok(())
}

/// Broadcast a release created event
pub async fn broadcast_release_created(
    manager: &Arc<RwLock<ConnectionManager>>,
    tag: String,
    commit: String,
) -> Result<()> {
    let event = GitEvent::release_created(tag, commit);
    broadcast_event(manager, event).await
}

/// Broadcast a release deleted event
pub async fn broadcast_release_deleted(
    manager: &Arc<RwLock<ConnectionManager>>,
    tag: String,
) -> Result<()> {
    let event = GitEvent::release_deleted(tag);
    broadcast_event(manager, event).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_broadcast_event() {
        let manager = Arc::new(RwLock::new(ConnectionManager::new()));

        // Add a test connection
        let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel();
        let addr = "127.0.0.1:8080".parse().unwrap();

        {
            let mut m = manager.write().await;
            m.add_connection(addr, tx);
        }

        // Broadcast event
        let event = GitEvent::release_created("v1.0.0".to_string(), "abc123".to_string());
        broadcast_event(&manager, event).await.unwrap();

        // Verify message received
        let message = rx.try_recv().unwrap();
        assert!(message.contains("release_created"));
        assert!(message.contains("v1.0.0"));
    }

    #[tokio::test]
    async fn test_broadcast_release_created() {
        let manager = Arc::new(RwLock::new(ConnectionManager::new()));

        let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel();
        let addr = "127.0.0.1:8080".parse().unwrap();

        {
            let mut m = manager.write().await;
            m.add_connection(addr, tx);
        }

        broadcast_release_created(&manager, "v2.0.0".to_string(), "def456".to_string())
            .await
            .unwrap();

        let message = rx.try_recv().unwrap();
        assert!(message.contains("v2.0.0"));
        assert!(message.contains("def456"));
    }

    #[tokio::test]
    async fn test_broadcast_release_deleted() {
        let manager = Arc::new(RwLock::new(ConnectionManager::new()));

        let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel();
        let addr = "127.0.0.1:8080".parse().unwrap();

        {
            let mut m = manager.write().await;
            m.add_connection(addr, tx);
        }

        broadcast_release_deleted(&manager, "v1.0.0".to_string())
            .await
            .unwrap();

        let message = rx.try_recv().unwrap();
        assert!(message.contains("release_deleted"));
        assert!(message.contains("v1.0.0"));
    }

    #[tokio::test]
    async fn test_broadcast_to_multiple_clients() {
        let manager = Arc::new(RwLock::new(ConnectionManager::new()));

        let (tx1, mut rx1) = tokio::sync::mpsc::unbounded_channel();
        let (tx2, mut rx2) = tokio::sync::mpsc::unbounded_channel();

        {
            let mut m = manager.write().await;
            m.add_connection("127.0.0.1:8080".parse().unwrap(), tx1);
            m.add_connection("127.0.0.1:8081".parse().unwrap(), tx2);
        }

        broadcast_release_created(&manager, "v3.0.0".to_string(), "ghi789".to_string())
            .await
            .unwrap();

        // Both clients should receive
        let msg1 = rx1.try_recv().unwrap();
        let msg2 = rx2.try_recv().unwrap();

        assert_eq!(msg1, msg2);
        assert!(msg1.contains("v3.0.0"));
    }
}
