// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"math"
	"time"
)

type TranslationIndexAuditCell struct {
	Index TranslationIndexKey
	Cell  TranslationIndexCell
}

type TranslationIndexCellAuditPage struct {
	Items   []TranslationIndexAuditCell
	Next    string
	Scanned int
}

type TranslationIndexCellAuditStore interface {
	ListTranslationIndexCellAudit(context.Context, string, string, int) (TranslationIndexCellAuditPage, error)
	RecoverTranslationIndexCellAudit(context.Context, string, TranslationIndexAuditCell) error
}

func ValidateTranslationIndexAuditCell(namespace string, observed TranslationIndexAuditCell) error {
	if !IsTranslationSourceIndex(observed.Index.Kind) || observed.Index.NamespaceUID != namespace ||
		observed.Index.Segment > math.MaxInt64 || observed.Cell.Entry.Slot >= MaxTranslationSegmentRows {
		return ErrInvalidArgument
	}
	index, entry := observed.Index, observed.Cell.Entry
	index.Segment, entry.Slot = 0, 0
	_, _, err := PlanTranslationIndexReservation(nil, index, entry, observed.Cell.Token, 1024)
	return err
}

func TranslationAuditCellMatchesReservation(observed TranslationIndexAuditCell, record TranslationIndexReservation) bool {
	if observed.Index != record.Index || observed.Cell.Token != record.Token {
		return false
	}
	entry := observed.Cell.Entry
	entry.CreationTimestamp = entry.CreationTimestamp.UTC().Truncate(time.Millisecond)
	record.Entry.CreationTimestamp = record.Entry.CreationTimestamp.UTC().Truncate(time.Millisecond)
	return entry == record.Entry
}

func TranslationAuditCellsEqual(a, b TranslationIndexCell) bool {
	a.Entry.CreationTimestamp = a.Entry.CreationTimestamp.UTC().Truncate(time.Millisecond)
	b.Entry.CreationTimestamp = b.Entry.CreationTimestamp.UTC().Truncate(time.Millisecond)
	return a == b
}

type translationCellAuditPosition struct {
	Kind  TranslationIndexKind `json:"kind"`
	State []byte               `json:"state,omitempty"`
}

func DecodeTranslationIndexCellAuditCursor(namespace, backend, cursor string, limit int) (TranslationIndexKind, []byte, error) {
	raw, err := decodeTranslationAuditCursor(namespace, backend, cursor, limit, "IndexCell")
	if err != nil {
		return "", nil, err
	}
	if len(raw) == 0 {
		return TranslationNamespaceIndex, nil, nil
	}
	var position translationCellAuditPosition
	if json.Unmarshal(raw, &position) != nil || !IsTranslationSourceIndex(position.Kind) {
		return "", nil, ErrInvalidArgument
	}
	return position.Kind, position.State, nil
}

func EncodeTranslationIndexCellAuditCursor(namespace, backend string, kind TranslationIndexKind, state []byte) (string, error) {
	if !IsTranslationSourceIndex(kind) {
		return "", ErrInvalidArgument
	}
	raw, err := json.Marshal(translationCellAuditPosition{Kind: kind, State: state})
	if err != nil {
		return "", err
	}
	return encodeTranslationAuditCursor(namespace, backend, raw, "IndexCell")
}
