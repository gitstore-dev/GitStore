// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package integration_test exercises the controller manager runtime
// end-to-end: a real manager.Manager, listwatch.Runner, and checkpoint.Store
// wired together exactly as cmd/controller/main.go wires them in production,
// against fake ListWatcher/Reconciler/StatusClient doubles that simulate the
// remote API. Unlike tests/contract, which verifies each component in
// isolation, this package proves the pieces integrate correctly.
package integration_test
