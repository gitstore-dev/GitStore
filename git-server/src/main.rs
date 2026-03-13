// GitStore Server Main Entry Point

use clap::Parser;
use std::env;
use std::net::SocketAddr;
use std::path::PathBuf;
use std::time::Instant;
use tracing::{error, info};

use gitstore::git::repo::init_or_open_repository;
use gitstore::http_git_server::{create_git_routes, GitServerState};
use gitstore::validation::Validator;
use gitstore::websocket::server::WebsocketServer;

#[derive(Parser, Debug)]
#[command(author, version, about, long_about = None)]
struct Args {
    /// Git protocol port
    #[arg(long, default_value = "9418")]
    port: u16,

    /// Websocket notification port
    #[arg(long, default_value = "8080")]
    ws_port: u16,

    /// Data directory for repositories
    #[arg(long, default_value = "/data/repos")]
    data_dir: String,

    /// Log level
    #[arg(long, default_value = "info")]
    log_level: String,
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let mut args = Args::parse();

    // Override with environment variables if present
    if let Ok(port) = env::var("GITSTORE_GIT_PORT") {
        args.port = port.parse().unwrap_or(args.port);
    }
    if let Ok(ws_port) = env::var("GITSTORE_WS_PORT") {
        args.ws_port = ws_port.parse().unwrap_or(args.ws_port);
    }
    if let Ok(data_dir) = env::var("GITSTORE_DATA_DIR") {
        args.data_dir = data_dir;
    }
    if let Ok(log_level) = env::var("GITSTORE_LOG_LEVEL") {
        args.log_level = log_level;
    }

    // Record start time for health checks
    let start_time = Instant::now();

    // Initialize structured logging
    gitstore::init_logging();

    info!(
        git_port = args.port,
        ws_port = args.ws_port,
        data_dir = %args.data_dir,
        "Starting GitStore Server"
    );

    // Create data directory if it doesn't exist
    let data_path = PathBuf::from(&args.data_dir);
    if !data_path.exists() {
        std::fs::create_dir_all(&data_path)?;
        info!(path = %data_path.display(), "Created data directory");
    }

    // Initialize catalog repository
    let catalog_path = data_path.join("catalog.git");
    match init_or_open_repository(&catalog_path) {
        Ok(repo) => {
            info!(path = %catalog_path.display(), "Catalog repository ready");
            drop(repo); // Close repository for now
        }
        Err(e) => {
            error!(error = %e, "Failed to initialize catalog repository");
            return Err(e.into());
        }
    }

    // Start websocket server
    let ws_addr: SocketAddr = format!("0.0.0.0:{}", args.ws_port).parse()?;
    let ws_server = WebsocketServer::new(ws_addr);
    let broadcaster = ws_server.broadcaster();

    info!("Websocket server starting on {}", ws_addr);

    // Spawn websocket server in background
    let ws_handle = tokio::spawn(async move {
        if let Err(e) = ws_server.start().await {
            error!(error = %e, "Websocket server error");
        }
    });

    // Create HTTP Git server state
    let git_state = GitServerState {
        repo_path: data_path.clone(),
        validator: std::sync::Arc::new(Validator::new()),
        broadcaster: std::sync::Arc::new(tokio::sync::RwLock::new(broadcaster)),
        start_time,
    };

    // Create HTTP git server routes
    let app = create_git_routes(git_state);

    // Start HTTP server for git operations
    let http_addr: SocketAddr = format!("0.0.0.0:{}", args.port).parse()?;
    info!(
        http_port = args.port,
        "HTTP Git server starting on http://{}", http_addr
    );

    let listener = tokio::net::TcpListener::bind(http_addr).await?;

    // Serve HTTP git server
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

    Ok(())
}
