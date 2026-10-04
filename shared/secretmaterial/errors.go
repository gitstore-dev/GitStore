// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"errors"
	"fmt"
	"io"
)

var (
	ErrInvalidRef          = errors.New("secret: InvalidRef")
	ErrNotFound            = errors.New("secret: NotFound")
	ErrMissingKey          = errors.New("secret: MissingKey")
	ErrForbidden           = errors.New("secret: Forbidden")
	ErrProviderUnavailable = errors.New("secret: ProviderUnavailable")
	ErrUnsupportedType     = errors.New("secret: UnsupportedType")
	ErrValueTooLarge       = errors.New("secret: ValueTooLarge")
)

type resolutionError struct {
	class error
	field string
	cause error
}

func failure(class error, field string, cause error) error {
	return &resolutionError{class: class, field: field, cause: cause}
}

func (e *resolutionError) Error() string { return e.class.Error() + " (" + e.field + ")" }

func (e *resolutionError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, e.Error())
}

// Do not expose unsafe provider causes through Unwrap or generic formatters.
func (e *resolutionError) Is(target error) bool {
	if target == context.Canceled || target == context.DeadlineExceeded {
		return errors.Is(e.cause, target)
	}
	return target == e.class
}

func classified(err error) error {
	for _, class := range []error{
		ErrInvalidRef, ErrNotFound, ErrMissingKey, ErrForbidden,
		ErrProviderUnavailable, ErrUnsupportedType, ErrValueTooLarge,
	} {
		if errors.Is(err, class) {
			return class
		}
	}
	return ErrProviderUnavailable
}

func outcome(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return classified(err).Error()[len("secret: "):]
}
