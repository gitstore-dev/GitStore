# Kratos HTTP Courier Contract

## Configuration

The reference Kratos deployment uses `courier.delivery_strategy=http` and
posts to the GitStore identity-mail endpoint. Configure API-key authentication
in Kratos and a corresponding required, sensitive API setting in GitStore.
Keep endpoint URL, credentials and provider selection in existing config and
secret-injection mechanisms; do not commit secret values.

The opt-in reference hook overlay selects HTTP delivery; standalone OIDC
keeps its SMTP baseline. Set `api.notification.provider = "development"` and
inject `GITSTORE_API__NOTIFICATION__API_KEY` (32+ characters) into both API and
the native Kratos `COURIER_HTTP_REQUEST_CONFIG_AUTH_CONFIG_VALUE` setting.
The pinned request builder passes `ctx` as a Jsonnet top-level argument, not
an external variable. The committed template is `function(ctx)`.

## Request

`POST /internal/notifications/mail` is mounted only on the Admin listener
when the development provider is enabled. Authenticate with
`GitStore-Mail-Key`; require `application/json` (optional charset accepted).
The pinned request template sends `recipient`, `subject`, `body`, optional
`html_body`, `template_type`, `message_type`, `template_data` and empty
`request_headers`. The adapter discards template data and request headers.
Recipient must be a single mailbox, subject must not contain CR/LF and only
email messages are accepted. Do not forward browser credentials.

The body limit is the smaller of the Admin endpoint limit and
`api.notification.max_body_bytes` (default 64 KiB; hard maximum 1 MiB).
`max_concurrent` defaults to 16 (maximum 128), including parsing and delivery.
`timeout` defaults to 2s (maximum 10s); the development transport additionally
caps SMTP connection/handshake/acceptance at 2s and honors earlier cancellation.
The HTTP listener retains its bounded ingress read timeout.

Missing/wrong credentials return 401 before reading the body. Authenticated
wrong methods return 405, unsupported media types 415, malformed messages 400
and oversized bodies 413. Saturation or provider failure returns 503 with no
sensitive response body. Provider acceptance returns 204. The disabled route
returns 404; it is never mounted on Storefront.

## Processing and responses

- Authenticate Kratos before reading or forwarding the message.
- Forward through a `NotificationProvider` interface; ship only the
  development mail-catcher implementation.
- Bound in-flight deliveries and provider deadlines. Do not retain an
  unbounded retry queue.
- Return success only when the configured provider accepted the message.
  Return a retryable status for provider outage, timeout or saturation.
- Reject malformed or oversized payloads. Do not reflect credentials, body,
  recipient or verification/recovery content in the response.
- Treat delivery as at-least-once. Duplicate delivery is possible if Kratos
  retries after an ambiguous response; no exactly-once guarantee is made.

The endpoint records only outcome, template class, duration and a correlation
ID. It never logs recipient, subject, message body, token, password or
verification/recovery code.

The development catcher supports SMTP or implicit SMTPS; certificate
verification is enabled by default. The reference self-signed catcher requires
the explicit development-only `smtps://mailslurper:1025/?skip_ssl_verify=true`
setting. No production notification backend is shipped.

This endpoint's privacy boundary does not redact upstream Kratos logging.
Native courier failure logs can include the native subject, and default
code-flow subjects can contain a verification/recovery code. Restrict upstream
logs and use non-sensitive native subject templates where required.
