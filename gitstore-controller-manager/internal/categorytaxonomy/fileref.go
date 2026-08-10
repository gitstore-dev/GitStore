// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
)

// computeFileRefCondition implements FR-010/FR-011 (US3): every optional:false
// media entry in self.Media produces a FileRefConfirmed condition with status
// Unknown, since File (#79) is not yet a queryable datastore entity
// (research.md R5) — the check can neither confirm nor deny existence.
// optional:true entries never contribute, and an empty/all-optional Media
// list produces no condition at all, per FR-011 and the spec's Edge Cases.
func computeFileRefCondition(self CategoryTaxonomy) *status.Condition {
	var required []string
	for _, m := range self.Media {
		if !m.Optional {
			required = append(required, m.Name)
		}
	}
	if len(required) == 0 {
		return nil
	}
	return &status.Condition{
		Type:               conditionFileRefPrefix,
		Status:             statusUnknown,
		LastTransitionTime: time.Now(),
		Reason:             "FileNotQueryable",
		Message:            "required file reference(s) could not be confirmed: File resources are not yet queryable (issue #79)",
	}
}
