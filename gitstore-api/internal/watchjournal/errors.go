// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package watchjournal

import (
	"errors"
	"fmt"
)

const (
	CodeExpired     = "WATCH_EXPIRED"
	CodeUnavailable = "WATCH_UNAVAILABLE"
)

type TerminalError struct {
	Code   string
	Reason Reason
	Cause  error
}

func (e *TerminalError) Error() string {
	if e.Code == CodeUnavailable {
		return "namespace watch is temporarily unavailable"
	}
	return "namespace watch continuity cannot be guaranteed; re-list"
}

func (e *TerminalError) Unwrap() error { return e.Cause }

func expired(reason Reason, cause error) error {
	return &TerminalError{Code: CodeExpired, Reason: reason, Cause: cause}
}

func unavailable(cause error) error {
	return &TerminalError{Code: CodeUnavailable, Reason: ReasonMaterializerNotReady, Cause: cause}
}

func AsTerminal(err error) (*TerminalError, bool) {
	var terminal *TerminalError
	if !errors.As(err, &terminal) {
		return nil, false
	}
	return terminal, terminal != nil
}

func cursorError(reason Reason, detail string) error {
	return expired(reason, fmt.Errorf("%s", detail))
}
