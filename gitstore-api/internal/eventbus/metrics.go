// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package eventbus

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SubscriptionsOpenedTotal counts every successful Subscribe call, labeled
// by kind and whether it was a fresh subscription (resume="false") or a
// resume from a resourceVersion cursor (resume="true"). Distinguishing the
// two lets an operator see resume-rate per kind (spec 040 FR-014).
var SubscriptionsOpenedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "gitstore",
	Subsystem: "eventbus",
	Name:      "subscriptions_opened_total",
	Help:      "Total watch subscriptions opened, by kind and whether resourceVersion resume was requested.",
}, []string{"kind", "resume"})

// WatchExpiredTotal counts Subscribe calls that failed because the
// requested resourceVersion predates the retained window — the signal an
// operator uses to distinguish "cursor expired, controller must re-list"
// from an ordinary transient reconnect (spec 040 FR-014, SC-004).
var WatchExpiredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "gitstore",
	Subsystem: "eventbus",
	Name:      "watch_expired_total",
	Help:      "Total Subscribe calls rejected with ErrWatchExpired, by kind.",
}, []string{"kind"})

// EventsDroppedTotal counts events dropped because a subscriber's buffered
// channel was full (a slow consumer). A sustained non-zero rate for a kind
// indicates that kind's watchers are falling behind (spec 040 FR-014).
var EventsDroppedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "gitstore",
	Subsystem: "eventbus",
	Name:      "events_dropped_total",
	Help:      "Total events dropped due to a full subscriber channel, by kind.",
}, []string{"kind"})
