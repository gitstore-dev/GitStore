// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GitStore Server Main Entry Point

use clap::Parser;
use std::net::SocketAddr;
use std::path::PathBuf;
use tracing::{error, info};

use std::sync::Arc;

use gitstore::auth::interceptor::HmacInterceptor;
use gitstore::git::hooks::{
    admission_handler::AdmissionControlHandler,
    category_taxonomy_deletion_handler::ResourceDeletionHandler,
    validation_handler::SchemaValidationHandler, ChainedValidationHandler, HookPipeline,
    NoopAdmissionHandler, NoopValidationHandler,
};
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
        grpc_port = cfg.git_service.grpc_port,
        data_dir = %cfg.git_service.data_dir,
        "Starting GitStore Server"
    );
    info!(
        pre_receive = true,
        post_receive = true,
        "hook phases always on"
    );

    // Create data directory if it doesn't exist (no default repo provisioned)
    let data_path = PathBuf::from(&cfg.git_service.data_dir);
    if !data_path.exists() {
        std::fs::create_dir_all(&data_path)?;
        info!(path = %data_path.display(), "Created data directory");
    }

    // Preserve schema validation and add the generic deletion proposed-tree
    // check as a second blocking policy.
    let catalog_url = cfg.git_service.catalog.uri.clone();
    let validation_timeout: std::time::Duration = cfg.git_service.validation.timeout.into();
    let validation_handler: Arc<dyn gitstore::git::hooks::ValidationHandler + Send + Sync> = {
        let schema =
            SchemaValidationHandler::connect(&catalog_url, validation_timeout, "".to_string())
                .await;
        let deletion =
            ResourceDeletionHandler::connect(&catalog_url, validation_timeout, "".to_string())
                .await;
        match (schema, deletion) {
            (Ok(schema), Ok(deletion)) => {
                info!(url = %catalog_url, "schema and resource deletion validators connected");
                Arc::new(ChainedValidationHandler::new(vec![
                    Arc::new(schema),
                    Arc::new(deletion),
                ]))
            }
            (schema, deletion) => {
                tracing::warn!(schema_error = ?schema.err(), deletion_error = ?deletion.err(), "validation handlers unavailable at startup; using noop");
                Arc::new(NoopValidationHandler)
            }
        }
    };

    // Build admission handler.
    let admission_handler: Arc<dyn gitstore::git::hooks::AdmissionHandler + Send + Sync> =
        match AdmissionControlHandler::connect(
            &catalog_url,
            cfg.git_service.admission.branch_pattern.clone(),
        )
        .await
        {
            Ok(h) => {
                info!(url = %catalog_url, "AdmissionControlHandler connected");
                Arc::new(h)
            }
            Err(e) => {
                tracing::warn!(error = %e, "AdmissionControlHandler unavailable at startup; using noop");
                Arc::new(NoopAdmissionHandler)
            }
        };

    // Start gRPC server
    let grpc_addr: SocketAddr = format!("0.0.0.0:{}", cfg.git_service.grpc_port).parse()?;
    let hook_pipeline = Arc::new(HookPipeline::new(
        validation_timeout,
        validation_handler,
        admission_handler,
    ));
    let push_limits = gitstore::grpc::server::PushLimits {
        max_pack_size: cfg.push_limits.max_pack_size.0,
        max_file_size: cfg.push_limits.max_file_size.0,
    };
    let grpc_service = GitServiceImpl::with_pipeline(data_path.clone(), hook_pipeline, push_limits);
    let interceptor = HmacInterceptor::new(
        &cfg.grpc_auth.hmac_secret,
        cfg.grpc_auth.hmac_secret_previous.as_deref(),
    );
    info!(
        rotation_window_open = cfg.grpc_auth.hmac_secret_previous.is_some(),
        "gRPC HMAC auth active"
    );
    info!(
        grpc_port = cfg.git_service.grpc_port,
        "gRPC server starting on {}", grpc_addr
    );
    let grpc_handle = tokio::spawn(async move {
        if let Err(e) = tonic::transport::Server::builder()
            .add_service(GitServiceServer::with_interceptor(
                grpc_service,
                interceptor,
            ))
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
