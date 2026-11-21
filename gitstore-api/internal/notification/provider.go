// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package notification

import "context"

type Message struct {
	Recipient    string `json:"recipient"`
	Subject      string `json:"subject"`
	Body         string `json:"body"`
	HTMLBody     string `json:"html_body,omitempty"`
	TemplateType string `json:"template_type"`
	MessageType  string `json:"message_type,omitempty"`
}

type NotificationProvider interface {
	Send(context.Context, Message) error
}
