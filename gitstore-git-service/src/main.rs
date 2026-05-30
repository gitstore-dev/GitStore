// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GitStore Server Main Entry Point

use clap::Parser;
use std::net::SocketAddr;
use std::path::PathBuf;
use tracing::{error, info};

use gitstore::grpc::server::{proto::git_service_server::GitServiceServer, GitServiceImpl};

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

    gitstore::init_logging(&cfg.log.level, &cfg.log.format)
        .map_err(|e| format!("Failed to initialize logger: {e}"))?;

    info!(
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

    shutdown_signal().await?;
    info!("Shutting down...");

    grpc_handle.abort();

    Ok(())
}

#[cfg(unix)]
async fn shutdown_signal() -> Result<(), Box<dyn std::error::Error>> {
    use tokio::signal::unix::{signal, SignalKind};

    let mut interrupt = signal(SignalKind::interrupt())?;
    let mut terminate = signal(SignalKind::terminate())?;

    tokio::select! {
        _ = interrupt.recv() => info!("Received SIGINT"),
        _ = terminate.recv() => info!("Received SIGTERM"),
    }

    Ok(())
}

#[cfg(not(unix))]
async fn shutdown_signal() -> Result<(), Box<dyn std::error::Error>> {
    tokio::signal::ctrl_c().await?;
    info!("Received Ctrl-C");
    Ok(())
}
