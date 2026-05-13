// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GitStore Server Main Entry Point

use clap::Parser;
use std::net::SocketAddr;
use std::path::PathBuf;
use std::time::Instant;
use tracing::{error, info};

use gitstore::grpc::server::{proto::git_service_server::GitServiceServer, GitServiceImpl};
use gitstore::http_git_server::{create_git_routes, GitServerState};
use gitstore::websocket::server::WebsocketServer;

#[derive(Parser, Debug)]
#[command(author, version, about, long_about = None)]
struct Args {
    /// Path to a custom config file (default: gitstore.toml in working directory)
    #[arg(long)]
    config_file: Option<String>,

    /// Override log level (highest priority — overrides all other sources)
    #[arg(long)]
    log_level: Option<String>,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    dotenvy::dotenv().ok();

    let args = Args::parse();

    let mut cfg = gitstore::config::load_config_from(args.config_file.as_deref())
        .map_err(|e| format!("Configuration error: {e}"))?;

    if let Some(level) = args.log_level {
        cfg.log.level = level;
    }

    if let Err(e) = cfg.validate() {
        eprintln!("{e}");
        std::process::exit(1);
    }

    let start_time = Instant::now();

    gitstore::init_logging();

    info!(
        http_port = cfg.http.port,
        ws_port = cfg.ws.port,
        grpc_port = cfg.grpc.port,
        data_dir = %cfg.git.data_dir,
        "Starting GitStore Server"
    );

    // Create data directory if it doesn't exist (no default repo provisioned)
    let data_path = PathBuf::from(&cfg.git.data_dir);
    if !data_path.exists() {
        std::fs::create_dir_all(&data_path)?;
        info!(path = %data_path.display(), "Created data directory");
    }

    // Start websocket server
    let ws_addr: SocketAddr = format!("0.0.0.0:{}", cfg.ws.port).parse()?;
    let ws_server = WebsocketServer::new(ws_addr);
    let broadcaster = ws_server.broadcaster();

    info!("Websocket server starting on {}", ws_addr);

    let ws_handle = tokio::spawn(async move {
        if let Err(e) = ws_server.start().await {
            error!(error = %e, "Websocket server error");
        }
    });

    // Start gRPC server
    let grpc_addr: SocketAddr = format!("0.0.0.0:{}", cfg.grpc.port).parse()?;
    let grpc_service = GitServiceImpl::new(data_path.clone());
    info!(
        grpc_port = cfg.grpc.port,
        "gRPC server starting on {}", grpc_addr
    );
    let grpc_handle = tokio::spawn(async move {
        if let Err(e) = tonic::transport::Server::builder()
            .add_service(GitServiceServer::new(grpc_service))
            .serve(grpc_addr)
            .await
        {
            error!(error = %e, "gRPC server error");
        }
    });

    // Create HTTP Git server state
    let git_state = GitServerState {
        data_root: data_path.clone(),
        broadcaster: std::sync::Arc::new(tokio::sync::RwLock::new(broadcaster)),
        start_time,
        max_pack_size: cfg.git.max_pack_size_bytes,
    };

    let app = create_git_routes(git_state);

    let http_addr: SocketAddr = format!("0.0.0.0:{}", cfg.http.port).parse()?;
    info!(
        http_port = cfg.http.port,
        "HTTP Git server starting on http://{}", http_addr
    );

    let listener = tokio::net::TcpListener::bind(http_addr).await?;

    let http_handle = tokio::spawn(async move {
        if let Err(e) = axum::serve(listener, app).await {
            error!(error = %e, "HTTP server error");
        }
    });

    tokio::signal::ctrl_c().await?;
    info!("Shutting down...");

    ws_handle.abort();
    http_handle.abort();
    grpc_handle.abort();

    Ok(())
}
