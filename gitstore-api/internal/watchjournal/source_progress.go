// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

const SourceReadFrontier = "__source_read_frontier__"

func SourceReadFrontierKey(source string) string {
	if source == "Namespace" {
		return SourceReadFrontier
	}
	return source + ":" + SourceReadFrontier
}

// RefreshSourceReadiness requires each source's fully published read frontier,
// not the last event or another source's heartbeat. Check remains cache-only.
func RefreshSourceReadiness(ctx context.Context, journal Store, bounds datastore.ResourceWatchBounds, readiness *Readiness, sources []string) error {
	fail := func() error {
		readiness.Update(MaterializerStatus{})
		return ErrMaterializerNotReady
	}
	if bounds.Epoch == "" || len(sources) == 0 {
		return fail()
	}
	var oldest time.Time
	for _, source := range sources {
		progress, err := journal.LoadProgress(ctx, SourceReadFrontierKey(source))
		if err != nil || string(progress.Position) != bounds.Epoch || progress.UpdatedAt.IsZero() {
			return fail()
		}
		if oldest.IsZero() || progress.UpdatedAt.Before(oldest) {
			oldest = progress.UpdatedAt
		}
	}
	readiness.Update(MaterializerStatus{LastProgressAt: oldest, JournalContinuous: true})
	return nil
}
