// Websocket connection manager

use std::collections::HashMap;
use std::net::SocketAddr;
use tokio::sync::mpsc::UnboundedSender;
use tracing::{debug, info};

/// Manages active websocket connections
pub struct ConnectionManager {
    connections: HashMap<SocketAddr, UnboundedSender<String>>,
}

impl ConnectionManager {
    /// Create a new connection manager
    pub fn new() -> Self {
        Self {
            connections: HashMap::new(),
        }
    }

    /// Add a new connection
    pub fn add_connection(&mut self, addr: SocketAddr, sender: UnboundedSender<String>) {
        info!(peer = %addr, "Adding connection");
        self.connections.insert(addr, sender);
    }

    /// Remove a connection
    pub fn remove_connection(&mut self, addr: &SocketAddr) {
        info!(peer = %addr, "Removing connection");
        self.connections.remove(addr);
    }

    /// Get the number of active connections
    pub fn connection_count(&self) -> usize {
        self.connections.len()
    }

    /// Broadcast a message to all connected clients
    pub fn broadcast(&self, message: String) {
        debug!(
            connection_count = self.connections.len(),
            "Broadcasting message"
        );

        for (addr, sender) in &self.connections {
            if let Err(e) = sender.send(message.clone()) {
                debug!(peer = %addr, error = %e, "Failed to send to connection");
            }
        }
    }

    /// Send a message to a specific connection
    pub fn send_to(&self, addr: &SocketAddr, message: String) -> Result<(), String> {
        if let Some(sender) = self.connections.get(addr) {
            sender
                .send(message)
                .map_err(|e| format!("Failed to send: {}", e))
        } else {
            Err(format!("Connection not found: {}", addr))
        }
    }

    /// Get list of all connected addresses
    pub fn connected_addresses(&self) -> Vec<SocketAddr> {
        self.connections.keys().copied().collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_connection_manager_new() {
        let manager = ConnectionManager::new();
        assert_eq!(manager.connection_count(), 0);
    }

    #[test]
    fn test_add_remove_connection() {
        let mut manager = ConnectionManager::new();
        let addr: SocketAddr = "127.0.0.1:8080".parse().unwrap();
        let (tx, _rx) = tokio::sync::mpsc::unbounded_channel();

        manager.add_connection(addr, tx);
        assert_eq!(manager.connection_count(), 1);

        manager.remove_connection(&addr);
        assert_eq!(manager.connection_count(), 0);
    }

    #[test]
    fn test_broadcast() {
        let mut manager = ConnectionManager::new();
        let addr1: SocketAddr = "127.0.0.1:8080".parse().unwrap();
        let addr2: SocketAddr = "127.0.0.1:8081".parse().unwrap();

        let (tx1, mut rx1) = tokio::sync::mpsc::unbounded_channel();
        let (tx2, mut rx2) = tokio::sync::mpsc::unbounded_channel();

        manager.add_connection(addr1, tx1);
        manager.add_connection(addr2, tx2);

        manager.broadcast("test message".to_string());

        // Verify both received the message
        assert_eq!(rx1.try_recv().unwrap(), "test message");
        assert_eq!(rx2.try_recv().unwrap(), "test message");
    }

    #[test]
    fn test_send_to_specific() {
        let mut manager = ConnectionManager::new();
        let addr: SocketAddr = "127.0.0.1:8080".parse().unwrap();
        let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel();

        manager.add_connection(addr, tx);

        let result = manager.send_to(&addr, "direct message".to_string());
        assert!(result.is_ok());

        assert_eq!(rx.try_recv().unwrap(), "direct message");
    }

    #[test]
    fn test_send_to_nonexistent() {
        let manager = ConnectionManager::new();
        let addr: SocketAddr = "127.0.0.1:8080".parse().unwrap();

        let result = manager.send_to(&addr, "message".to_string());
        assert!(result.is_err());
    }

    #[test]
    fn test_connected_addresses() {
        let mut manager = ConnectionManager::new();
        let addr1: SocketAddr = "127.0.0.1:8080".parse().unwrap();
        let addr2: SocketAddr = "127.0.0.1:8081".parse().unwrap();

        let (tx1, _rx1) = tokio::sync::mpsc::unbounded_channel();
        let (tx2, _rx2) = tokio::sync::mpsc::unbounded_channel();

        manager.add_connection(addr1, tx1);
        manager.add_connection(addr2, tx2);

        let addresses = manager.connected_addresses();
        assert_eq!(addresses.len(), 2);
        assert!(addresses.contains(&addr1));
        assert!(addresses.contains(&addr2));
    }
}
