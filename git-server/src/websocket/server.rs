// Websocket server setup

use anyhow::Result;
use futures_util::{SinkExt, StreamExt};
use std::net::SocketAddr;
use std::sync::Arc;
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::RwLock;
use tokio_tungstenite::{accept_async, tungstenite::Message};
use tracing::{debug, error, info};

use crate::websocket::connections::ConnectionManager;

/// Websocket server for broadcasting git events
pub struct WebsocketServer {
    addr: SocketAddr,
    connection_manager: Arc<RwLock<ConnectionManager>>,
}

impl WebsocketServer {
    /// Create a new websocket server
    pub fn new(addr: SocketAddr) -> Self {
        Self {
            addr,
            connection_manager: Arc::new(RwLock::new(ConnectionManager::new())),
        }
    }

    /// Get a reference to the connection manager for broadcasting
    pub fn connection_manager(&self) -> Arc<RwLock<ConnectionManager>> {
        Arc::clone(&self.connection_manager)
    }

    /// Start the websocket server
    pub async fn start(self) -> Result<()> {
        let listener = TcpListener::bind(&self.addr).await?;
        info!(addr = %self.addr, "Websocket server listening");

        loop {
            match listener.accept().await {
                Ok((stream, peer_addr)) => {
                    debug!(peer = %peer_addr, "New connection");
                    let manager = Arc::clone(&self.connection_manager);
                    tokio::spawn(async move {
                        if let Err(e) = handle_connection(stream, peer_addr, manager).await {
                            error!(peer = %peer_addr, error = %e, "Connection error");
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

/// Handle a single websocket connection
async fn handle_connection(
    stream: TcpStream,
    peer_addr: SocketAddr,
    manager: Arc<RwLock<ConnectionManager>>,
) -> Result<()> {
    let ws_stream = accept_async(stream).await?;
    info!(peer = %peer_addr, "Websocket connection established");

    let (mut ws_sender, mut ws_receiver) = ws_stream.split();

    // Create a channel for this connection
    let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel::<String>();

    // Register connection
    {
        let mut manager = manager.write().await;
        manager.add_connection(peer_addr, tx);
    }

    // Spawn task to send messages to this client
    let send_task = tokio::spawn(async move {
        while let Some(message) = rx.recv().await {
            if let Err(e) = ws_sender.send(Message::Text(message)).await {
                error!(peer = %peer_addr, error = %e, "Failed to send message");
                break;
            }
        }
    });

    // Handle incoming messages (ping/pong, close)
    while let Some(msg) = ws_receiver.next().await {
        match msg {
            Ok(Message::Text(text)) => {
                debug!(peer = %peer_addr, text = %text, "Received message");
                // Echo back for now (can be used for health checks)
            }
            Ok(Message::Ping(_data)) => {
                debug!(peer = %peer_addr, "Received ping");
                // Pong is automatically handled by tungstenite
            }
            Ok(Message::Close(_)) => {
                info!(peer = %peer_addr, "Client closed connection");
                break;
            }
            Err(e) => {
                error!(peer = %peer_addr, error = %e, "Websocket error");
                break;
            }
            _ => {}
        }
    }

    // Cleanup: remove connection
    {
        let mut manager = manager.write().await;
        manager.remove_connection(&peer_addr);
    }

    info!(peer = %peer_addr, "Connection closed");
    send_task.abort();

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_websocket_server_creation() {
        let addr: SocketAddr = "127.0.0.1:8080".parse().unwrap();
        let server = WebsocketServer::new(addr);
        assert_eq!(server.addr, addr);
    }

    #[tokio::test]
    async fn test_connection_manager_shared() {
        let addr: SocketAddr = "127.0.0.1:8081".parse().unwrap();
        let server = WebsocketServer::new(addr);

        let manager1 = server.connection_manager();
        let manager2 = server.connection_manager();

        // Both should point to the same instance
        assert_eq!(Arc::strong_count(&manager1), Arc::strong_count(&manager2));
    }
}
