// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"strings"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Phase identifies which side of the ref update an admission rejection
// happened on.
type Phase string

const (
	// PhasePreReceive rejections happen before any commit: nothing changed.
	PhasePreReceive Phase = "PRE_RECEIVE"
	// PhasePostReceive rejections happen after the ref moved: the commit
	// exists, the last accepted generation is kept.
	PhasePostReceive Phase = "POST_RECEIVE"
)

// DiagnosticLevel is the severity of one diagnostic entry.
type DiagnosticLevel string

const (
	LevelFailure DiagnosticLevel = "FAILURE"
	LevelWarning DiagnosticLevel = "WARNING"
	LevelNotice  DiagnosticLevel = "NOTICE"
)

// Diagnostic is one machine-readable finding. Reason is required; File and
// Field are set for manifest-level problems.
type Diagnostic struct {
	Reason  string
	Message string
	Level   DiagnosticLevel
	File    string
	Field   string
}

// Code is the kind-neutral mutation error code.
type Code string

const (
	CodeAdmissionRejected  Code = "ADMISSION_REJECTED"
	CodeAlreadyExists      Code = "ALREADY_EXISTS"
	CodeNotFound           Code = "NOT_FOUND"
	CodeConflict           Code = "CONFLICT"
	CodeFailedPrecondition Code = "FAILED_PRECONDITION"
	CodeBadUserInput       Code = "BAD_USER_INPUT"
	CodeForbidden          Code = "FORBIDDEN"
)

// ReasonValidationFailed is the fallback reason for validation errors whose
// constraint has no specific mapping.
const ReasonValidationFailed = "VALIDATION_FAILED"

// Error is the kind-neutral mutation error. Its GraphQL form carries at most
// four extension keys: code (always), diagnostics, phase (only for
// ADMISSION_REJECTED) and commit (only for POST_RECEIVE).
type Error struct {
	Code        Code
	Phase       Phase
	CommitSHA   string
	Diagnostics []Diagnostic
	// Message overrides the error text for codes other than
	// ADMISSION_REJECTED, whose text is always the formatted rejection.
	Message string
}

// NewError builds an error with a single FAILURE diagnostic.
func NewError(code Code, reason, message string) *Error {
	return &Error{Code: code, Diagnostics: []Diagnostic{{Reason: reason, Message: message, Level: LevelFailure}}}
}

// Rejected builds an ADMISSION_REJECTED error for the given phase.
func Rejected(phase Phase, commitSHA string, diagnostics []Diagnostic) *Error {
	return &Error{Code: CodeAdmissionRejected, Phase: phase, CommitSHA: commitSHA, Diagnostics: diagnostics}
}

// Reason returns the first diagnostic's reason, or "" when there is none.
func (e *Error) Reason() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return ""
	}
	return e.Diagnostics[0].Reason
}

// Error returns the rejection text. For ADMISSION_REJECTED it is byte-identical
// to the text a Git push carries for the same manifest.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == CodeForbidden {
		return "forbidden"
	}
	if e.Code == CodeAdmissionRejected {
		return FormatRejection(e.Diagnostics)
	}
	if e.Message != "" {
		return e.Message
	}
	messages := make([]string, 0, len(e.Diagnostics))
	for _, d := range e.Diagnostics {
		if d.Message != "" {
			messages = append(messages, d.Message)
		}
	}
	if len(messages) == 0 {
		return strings.ToLower(strings.ReplaceAll(string(e.Code), "_", " "))
	}
	return strings.Join(messages, "; ")
}

// ToGQLError renders the four-key envelope, omitting absent keys.
func (e *Error) ToGQLError() *gqlerror.Error {
	extensions := map[string]any{"code": string(e.Code)}
	if e.Code != CodeForbidden && len(e.Diagnostics) > 0 {
		diagnostics := make([]map[string]any, 0, len(e.Diagnostics))
		for _, d := range e.Diagnostics {
			entry := map[string]any{"reason": d.Reason, "message": d.Message, "level": string(levelOrFailure(d.Level))}
			if d.File != "" {
				entry["file"] = d.File
			}
			if d.Field != "" {
				entry["field"] = d.Field
			}
			diagnostics = append(diagnostics, entry)
		}
		extensions["diagnostics"] = diagnostics
	}
	if e.Code == CodeAdmissionRejected && e.Phase != "" {
		extensions["phase"] = string(e.Phase)
		if e.Phase == PhasePostReceive && e.CommitSHA != "" {
			extensions["commit"] = e.CommitSHA
		}
	}
	return &gqlerror.Error{Message: e.Error(), Extensions: extensions}
}

func levelOrFailure(level DiagnosticLevel) DiagnosticLevel {
	if level == "" {
		return LevelFailure
	}
	return level
}

// FormatRejection joins diagnostics as "file: message; …" ("message" when
// there is no file), matching the Git service's push rejection text.
func FormatRejection(diagnostics []Diagnostic) string {
	parts := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		if d.File == "" {
			parts = append(parts, d.Message)
		} else {
			parts = append(parts, d.File+": "+d.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// FromValidationErrors converts catalog validation errors into FAILURE
// diagnostics, deriving Reason from the constraint.
func FromValidationErrors(errs []*catalogv1.ValidationError) []Diagnostic {
	if len(errs) == 0 {
		return nil
	}
	diagnostics := make([]Diagnostic, 0, len(errs))
	for _, e := range errs {
		diagnostics = append(diagnostics, Diagnostic{
			Reason:  ReasonForConstraint(e.GetField(), e.GetConstraint()),
			Message: e.GetMessage(),
			Level:   LevelFailure,
			File:    e.GetFilePath(),
			Field:   e.GetField(),
		})
	}
	return diagnostics
}

// ReasonForConstraint maps a validation constraint to a diagnostic reason.
// Unmapped constraints fall back to VALIDATION_FAILED.
func ReasonForConstraint(field, constraint string) string {
	switch constraint {
	case "required":
		return "REQUIRED_FIELD"
	case "envelope":
		return "INVALID_ENVELOPE"
	case "eq":
		switch strings.ToLower(field) {
		case "apiversion", "kind":
			return "INVALID_ENVELOPE"
		}
		return "INVALID_FIELD"
	case "dns-label", "max=200", "read-only", "system-managed":
		return "INVALID_FIELD"
	case "self-parent":
		return "SELF_PARENT"
	case "parent-terminating":
		return "PARENT_TERMINATING"
	case "cross-namespace":
		return "CROSS_NAMESPACE_REFERENCE"
	case "immutable":
		if field == "metadata.namespace" {
			return "IMMUTABLE_NAMESPACE"
		}
		return "IMMUTABLE_NAME"
	}
	if strings.HasPrefix(constraint, "policy/") {
		return "POLICY_DENIED"
	}
	return ReasonValidationFailed
}
