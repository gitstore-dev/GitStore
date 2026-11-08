// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

const RegistrationHookPath = "/internal/userdir/kratos/registration"
const SettingsHookPath = "/internal/userdir/kratos/settings"
const PasswordSettingsHookPath = "/internal/userdir/kratos/settings-password"

func (p *Provider) WebhookHandler() http.Handler {
	mux := http.NewServeMux()
	active := make(chan struct{}, p.config.MaxConcurrent)
	register := func(path string, registration bool) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			select {
			case active <- struct{}{}:
				defer func() { <-active }()
			default:
				http.Error(w, "directory unavailable", http.StatusServiceUnavailable)
				return
			}
			p.handleHook(w, r, registration)
		})
	}
	register(RegistrationHookPath, true)
	register(SettingsHookPath, false)
	register(PasswordSettingsHookPath, false)
	return mux
}

func hookError(w http.ResponseWriter, pointer, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]any{"messages": []any{map[string]any{
		"instance_ptr": pointer, "messages": []any{map[string]any{
			"id": 4000000, "type": "error", "text": message,
		}},
	}}})
}

func (p *Provider) handleHook(w http.ResponseWriter, r *http.Request, registration bool) {
	var payload struct {
		Identity json.RawMessage `json:"identity"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, p.config.MaxResponseBytes))
	if err := decoder.Decode(&payload); err != nil {
		hookError(w, "", "invalid webhook payload")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		hookError(w, "", "invalid webhook payload")
		return
	}
	var candidate identity
	var raw map[string]any
	if json.Unmarshal(payload.Identity, &candidate) != nil || json.Unmarshal(payload.Identity, &raw) != nil ||
		!validIdentifier(candidate.ID) || !validIdentifier(candidate.Traits.Username) {
		hookError(w, "", "invalid identity")
		return
	}
	metadata, _ := raw["metadata_admin"].(map[string]any)
	authorChanged := true
	if registration {
		if candidate.SchemaID != string(auth.ProfileCustomer) {
			hookError(w, "", "public registration requires a Customer identity")
			return
		}
		if metadata == nil {
			metadata = make(map[string]any)
		}
		metadata["creation_actor"], metadata["update_actor"] = candidate.Traits.Username, candidate.Traits.Username
		metadata["setup_complete"], metadata["lifecycle_state"] = true, auth.ActorPendingVerification
		candidate.MetadataAdmin.Metadata = nil
	} else {
		current, previous, err := p.fetchIdentity(r.Context(), candidate.ID)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, auth.ErrProfileNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, "directory unavailable", status)
			return
		}
		if candidate.SchemaID != current.SchemaID || (current.SchemaID != "staff" && current.SchemaID != "customer") {
			hookError(w, "", "identity kind cannot be changed")
			return
		}
		if candidate.Traits.Username != current.Traits.Username {
			hookError(w, "/traits/username", "username cannot be changed")
			return
		}
		metadata = previous
		if candidate.Traits.Email != current.Traits.Email {
			contact, err := p.profile(current)
			if err != nil {
				http.Error(w, "directory unavailable", http.StatusServiceUnavailable)
				return
			}
			metadata["deliverable_email"], metadata["email_deliverable"] = contact.DeliverableEmail, contact.EmailDeliverable
		}
		candidate.MetadataAdmin.Metadata = current.MetadataAdmin.Metadata
		authorChanged = candidate.Traits.Email != current.Traits.Email || candidate.Traits.FullName != current.Traits.FullName
		if r.URL.Path == PasswordSettingsHookPath {
			metadata["setup_complete"] = true
		}
	}
	updated, err := revisedMetadata(candidate, metadata, candidate.Traits.Username, authorChanged)
	if err != nil {
		hookError(w, "", "invalid identity metadata")
		return
	}
	p.invalidate()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"identity": map[string]any{"metadata_admin": updated}})
}
