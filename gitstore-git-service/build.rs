// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_prost_build::configure()
        .build_server(true)
        .build_client(false)
        .compile_protos(
            &["../shared/proto/gitstore/git/v1/git_service.proto"],
            &["../shared/proto"],
        )?;
    Ok(())
}
