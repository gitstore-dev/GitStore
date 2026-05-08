// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GitStore Server Main Entry Point

use clap::Parser;
use std::net::SocketAddr;
use std::path::PathBuf;
use std::time::Instant;
use tracing::{error, info};

use gitstore::git::metrics::log_repo_metrics;
use gitstore::git::repo::init_or_open_repository;
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
    // Load .env file before anything else so env vars are populated
    dotenvy::dotenv().ok();

    let args = Args::parse();

    // Load structured configuration (--config-file overrides default gitstore.toml)
    let mut cfg = gitstore::config::load_config_from(args.config_file.as_deref())
        .map_err(|e| format!("Configuration error: {e}"))?;

    // Apply CLI overrides (highest priority)
    if let Some(level) = args.log_level {
        cfg.log_level = level;
    }

    // Fail fast if config is invalid
    if let Err(e) = cfg.validate() {
        eprintln!("{e}");
        std::process::exit(1);
    }

    // Record start time for health checks
    let start_time = Instant::now();

    // Initialize structured logging
    gitstore::init_logging();

    info!(
        http_port = cfg.http_port,
        ws_port = cfg.ws_port,
        grpc_port = cfg.grpc_port,
        data_dir = %cfg.data_dir,
        "Starting GitStore Server"
    );

    // Create data directory if it doesn't exist
    let data_path = PathBuf::from(&cfg.data_dir);
    if !data_path.exists() {
        std::fs::create_dir_all(&data_path)?;
        info!(path = %data_path.display(), "Created data directory");
    }

    // Initialize catalog repository
    let catalog_path = data_path.join("catalog.git");
    match init_or_open_repository(&catalog_path) {
        Ok(repo) => {
            info!(path = %catalog_path.display(), "Catalog repository ready");
            drop(repo);
            log_repo_metrics(&catalog_path, 500.0);
        }
        Err(e) => {
            error!(error = %e, "Failed to initialize catalog repository");
            return Err(e.into());
        }
    }

    // Start websocket server
    let ws_addr: SocketAddr = format!("0.0.0.0:{}", cfg.ws_port).parse()?;
    let ws_server = WebsocketServer::new(ws_addr);
    let broadcaster = ws_server.broadcaster();

    info!("Websocket server starting on {}", ws_addr);

    let ws_handle = tokio::spawn(async move {
        if let Err(e) = ws_server.start().await {
            error!(error = %e, "Websocket server error");
        }
    });

    // Start gRPC server
    let grpc_addr: SocketAddr = format!("0.0.0.0:{}", cfg.grpc_port).parse()?;
    let grpc_service = GitServiceImpl::new(data_path.join("catalog.git"));
    info!(
        grpc_port = cfg.grpc_port,
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
        repo_path: data_path.clone(),
        broadcaster: std::sync::Arc::new(tokio::sync::RwLock::new(broadcaster)),
        start_time,
    };

    let app = create_git_routes(git_state);

    // Start HTTP server for git operations
    let http_addr: SocketAddr = format!("0.0.0.0:{}", cfg.http_port).parse()?;
    info!(
        http_port = cfg.http_port,
        "HTTP Git server starting on http://{}", http_addr
    );

    let listener = tokio::net::TcpListener::bind(http_addr).await?;

    let http_handle = tokio::spawn(async move {
        if let Err(e) = axum::serve(listener, app).await {
            error!(error = %e, "HTTP server error");
        }
    });

    // Wait for shutdown signal
    tokio::signal::ctrl_c().await?;
    info!("Shutting down...");

    ws_handle.abort();
    http_handle.abort();
    grpc_handle.abort();

    Ok(())
}
