// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package watchjournal provides the bounded, durable Namespace change journal
// shared by GraphQL watch subscriptions and the Scylla CDC materializer.
//
// Namespace mutation and lifecycle policy stays in the Namespace admission
// service. This package only records committed effects and exposes resumable,
// at-least-once delivery across API replicas.
package watchjournal
