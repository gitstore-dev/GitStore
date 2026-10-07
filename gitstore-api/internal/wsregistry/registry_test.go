// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package wsregistry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegistryCancelAllCancelsActiveConnections(t *testing.T) {
	registry := New()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	first := registry.Register(firstCtx, "service-account-uid", firstCancel)
	secondCtx, secondCancel := context.WithCancel(context.Background())
	second := registry.Register(secondCtx, "service-account-uid", secondCancel)

	registry.CancelAll("service-account-uid")
	assert.ErrorIs(t, firstCtx.Err(), context.Canceled)
	assert.ErrorIs(t, secondCtx.Err(), context.Canceled)
	registry.Unregister(first)
	registry.Unregister(second)

	newCtx, newCancel := context.WithCancel(context.Background())
	registered := registry.Register(newCtx, "service-account-uid", newCancel)
	assert.NoError(t, newCtx.Err())
	registry.Unregister(registered)
	newCancel()
}
