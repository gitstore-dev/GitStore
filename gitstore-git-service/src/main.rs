// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GitStore Server Main Entry Point

use clap::Parser;
use std::net::SocketAddr;
use std::path::PathBuf;
use tracing::{error, info, warn};

use std::sync::Arc;

use gitstore::auth::interceptor::RPCVerifier;
use gitstore::git::hooks::{
    admission_handler::AdmissionControlHandler,
    category_taxonomy_deletion_handler::ResourceDeletionHandler,
    validation_handler::SchemaValidationHandler, ChainedValidationHandler, HookPipeline,
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

    let telemetry = gitstore::init_observability(&cfg.log, &cfg.observability.otlp)
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
                    Arc::new(schema.with_service_token(&cfg.grpc_auth.hmac_secret)),
                    Arc::new(deletion.with_service_token(&cfg.grpc_auth.hmac_secret)),
                ]))
            }
            (Err(error), _) | (_, Err(error)) => {
                return Err(error.context("catalog validation is required").into())
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
                Arc::new(h.with_service_token(&cfg.grpc_auth.hmac_secret))
            }
            Err(error) => return Err(error.context("catalog admission is required").into()),
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
    let rpc_auth = RPCVerifier::new(
        &cfg.grpc_auth.hmac_secret,
        cfg.grpc_auth.hmac_secret_previous.as_deref(),
        &data_path,
    )?;
    let grpc_service =
        GitServiceImpl::with_pipeline(data_path.clone(), hook_pipeline, push_limits, rpc_auth);
    info!(
        rotation_window_open = cfg.grpc_auth.hmac_secret_previous.is_some(),
        "gRPC HMAC auth active"
    );
    info!(
        grpc_port = cfg.git_service.grpc_port,
        "gRPC server starting on {}", grpc_addr
    );
    let (shutdown_tx, shutdown_rx) = tokio::sync::oneshot::channel::<()>();
    let mut grpc_handle = tokio::spawn(async move {
        if let Err(e) = tonic::transport::Server::builder()
            .trace_fn(|request| gitstore::rpc_span(request.headers(), request.uri().path()))
            .add_service(gitstore::grpc::metrics::RpcMetricsService::new(
                GitServiceServer::new(grpc_service),
            ))
            .serve_with_shutdown(grpc_addr, async {
                let _ = shutdown_rx.await;
            })
            .await
        {
            error!(error = %e, "gRPC server error");
        }
    });

    shutdown_signal().await?;
    info!("Shutting down...");

    let _ = shutdown_tx.send(());
    if tokio::time::timeout(std::time::Duration::from_secs(10), &mut grpc_handle)
        .await
        .is_err()
    {
        grpc_handle.abort();
        let _ = grpc_handle.await;
    }
    shutdown_telemetry(move || telemetry.shutdown()).await?;

    Ok(())
}

async fn shutdown_telemetry(
    shutdown: impl FnOnce() -> anyhow::Result<()> + Send + 'static,
) -> Result<(), tokio::task::JoinError> {
    if let Err(error) = tokio::task::spawn_blocking(shutdown).await? {
        warn!(error = %error, "Telemetry shutdown failed");
    }
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

    #[tokio::test]
    async fn rejected_final_snapshot_does_not_fail_service_shutdown() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let metrics = Arc::new(AtomicUsize::new(0));
        let received = metrics.clone();
        let app = axum::Router::new().fallback(move |request: axum::extract::Request| {
            let received = received.clone();
            async move {
                if request.uri().path() == "/v1/metrics" {
                    received.fetch_add(1, Ordering::Relaxed);
                }
                axum::http::StatusCode::SERVICE_UNAVAILABLE
            }
        });
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let telemetry = gitstore::init_observability(
            &gitstore::config::LogConfig {
                level: "warn".to_string(),
                format: "json".to_string(),
            },
            &gitstore::config::OtlpConfig {
                uri: format!("http://{address}"),
                trace_sample_ratio: 0.0,
            },
        )
        .unwrap();
        // SDK readers export observed instruments. Queue a real RPC
        // measurement so this test exercises rejection of the final batch.
        gitstore::grpc::metrics::record_rpc("GetFile", "Ok", 0.01);
        let export_failed = Arc::new(AtomicBool::new(false));
        let failure = export_failed.clone();
        tokio::time::timeout(
            std::time::Duration::from_secs(10),
            shutdown_telemetry(move || {
                let result = telemetry.shutdown();
                failure.store(result.is_err(), Ordering::Relaxed);
                result
            }),
        )
        .await
        .unwrap()
        .unwrap();
        assert!(metrics.load(Ordering::Relaxed) > 0);
        assert!(export_failed.load(Ordering::Relaxed));
        server.abort();
        let _ = server.await;
    }

    #[tokio::test]
    async fn telemetry_worker_panics_remain_shutdown_errors() {
        let result = shutdown_telemetry(|| -> anyhow::Result<()> {
            panic!("synthetic shutdown worker failure");
        })
        .await;
        assert!(result.unwrap_err().is_panic());
    }
}
