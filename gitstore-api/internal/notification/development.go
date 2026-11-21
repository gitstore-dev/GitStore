// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package notification

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"time"
)

type Development struct {
	address, host, sender string
	tls, skipTLSVerify    bool
	requests              chan struct{}
}

func NewDevelopment(uri, sender string, limits ...int) (*Development, error) {
	limit := 16
	if len(limits) > 1 {
		return nil, errors.New("invalid development notification concurrency")
	}
	if len(limits) == 1 {
		limit = limits[0]
	}
	if limit < 1 || limit > 128 {
		return nil, errors.New("invalid development notification concurrency")
	}
	endpoint, err := url.Parse(uri)
	if err != nil || (endpoint.Scheme != "smtp" && endpoint.Scheme != "smtps") || endpoint.Hostname() == "" || endpoint.Port() == "" ||
		endpoint.User != nil || (endpoint.Path != "" && endpoint.Path != "/") || endpoint.Fragment != "" {
		return nil, errors.New("development notification provider requires a mail-catcher SMTP URI")
	}
	query, err := url.ParseQuery(endpoint.RawQuery)
	if err != nil || len(query) > 1 || (len(query) != 0 && (endpoint.Scheme != "smtps" ||
		(query.Get("skip_ssl_verify") != "true" && query.Get("skip_ssl_verify") != "false"))) {
		return nil, errors.New("invalid development mail-catcher TLS setting")
	}
	address, err := mail.ParseAddress(sender)
	if err != nil || address.Address != sender {
		return nil, errors.New("invalid development notification sender")
	}
	return &Development{address: endpoint.Host, host: endpoint.Hostname(), sender: sender,
		tls: endpoint.Scheme == "smtps", skipTLSVerify: query.Get("skip_ssl_verify") == "true",
		requests: make(chan struct{}, limit)}, nil
}

func (p *Development) Send(ctx context.Context, message Message) error {
	if !validMessage(message) || len(message.Body)+len(message.HTMLBody) > 1<<20 {
		return errors.New("invalid notification message")
	}
	select {
	case p.requests <- struct{}{}:
		defer func() { <-p.requests }()
	default:
		return errors.New("development notification provider saturated")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", p.address)
	if err != nil {
		return errors.New("development notification provider unavailable")
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return errors.New("development notification provider unavailable")
	}
	rawConnection := connection
	stop := context.AfterFunc(ctx, func() { _ = rawConnection.Close() })
	defer stop()
	if p.tls {
		tlsConnection := tls.Client(connection, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: p.host,
			InsecureSkipVerify: p.skipTLSVerify}) // Explicitly opt-in for the development catcher's self-signed certificate.
		if tlsConnection.HandshakeContext(ctx) != nil {
			return errors.New("development notification provider unavailable")
		}
		connection = tlsConnection
	}
	client, err := smtp.NewClient(connection, p.host)
	if err != nil {
		return errors.New("development notification provider unavailable")
	}
	defer client.Close()
	if client.Mail(p.sender) != nil || client.Rcpt(message.Recipient) != nil {
		return errors.New("development notification provider unavailable")
	}
	body, err := encodeMessage(p.sender, message)
	if err != nil {
		return errors.New("invalid notification message")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("development notification provider unavailable")
	}
	if _, err := writer.Write(body); err != nil {
		return errors.New("development notification provider unavailable")
	}
	if writer.Close() != nil {
		return errors.New("development notification provider unavailable")
	}
	_ = client.Quit()
	return nil
}

func encodeMessage(sender string, message Message) ([]byte, error) {
	var content bytes.Buffer
	writer := multipart.NewWriter(&content)
	fmt.Fprintf(&content, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n",
		sender, message.Recipient, mime.QEncoding.Encode("utf-8", message.Subject), writer.Boundary())
	for _, part := range []struct{ kind, body string }{{"text/plain", message.Body}, {"text/html", message.HTMLBody}} {
		if part.body == "" {
			continue
		}
		partWriter, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.kind + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		encoded := quotedprintable.NewWriter(partWriter)
		if _, err := encoded.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := encoded.Close(); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return content.Bytes(), nil
}
