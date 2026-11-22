// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

func runTranslationTargetMaintenance(ctx context.Context, store datastore.TranslationTargetMaintenanceStore, log *zap.Logger) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		turn, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := store.RecoverTranslationTargetMaintenanceTurn(turn)
		cancel()
		if ctx.Err() != nil {
			return
		}
		delay := 100 * time.Millisecond
		if err != nil && !errors.Is(err, datastore.ErrConflict) {
			log.Warn("Translation target maintenance turn failed", zap.Error(err))
			delay = time.Second
		}
		timer.Reset(delay)
	}
}
