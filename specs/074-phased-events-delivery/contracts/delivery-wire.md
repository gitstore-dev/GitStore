# Outbound Wire, Retry and Replay Contract (Proposed)

This implements the delivery portion of #506 using #505's immutable facts. It does not add a separate identity/marketing sender, synchronous admission webhook or exactly-once effect promise.

## CloudEvents mapping

CloudEvents **1.0.2** structured JSON, Content-Type `application/cloudevents+json`. JSON specversion is **"1.0"**, not "1.0.2".

```json
{
  "specversion": "1.0",
  "id": "<outbox-uid>",
  "source": "urn:gitstore:<deployment-uid>:resource-journal:<source-epoch>",
  "type": "dev.gitstore.catalog.product.updated.v1",
  "subject": "Product/<resource-uid>",
  "time": "2026-11-21T12:00:00Z",
  "datacontenttype": "application/json",
  "data": {
    "resourceRef": {"kind": "Product", "namespace": "store", "name": "shoe", "uid": "<resource-uid>"},
    "resourceVersion": "<source-version>"
  }
}
```

The example is a wire fixture template, not literal valid URI/UID values or a shipped endpoint. Git source URI additionally binds repository UID: `urn:gitstore:<deployment-uid>:git:<repository-uid>:<epoch>`. Git subject uses repository UID and percent-encoded full ref. Event id is the stable outbox metadata UID; (source,id) identifies duplicates. Type/schema names are versioned; semantic extraction index/version is pinned in source identity. No sequence-only IDs, full raw manifests, arbitrary condition snapshots or recovery/verification tokens.

GitStore owns this approved envelope independently of the [delivery provider](delivery-provider.md). Retain immutable canonical JSON bytes/digest with a versioned encoder. Submit the entire CloudEvent as Svix `payload`, without a provider wrapper, field replacement, template or data expansion. Semantic JSON content, including source/id/type/data, stays identical across adapters/retries/replay. An adapter may encode equivalent JSON differently at initial handoff; signatures cover actual transmitted bytes. Qualify stable retry/replay bytes within the provider; do not promise byte equality between dev and Svix encoders. Content-Type remains application/cloudevents+json and requires adapter conformance. Before accepting a route, validate the subscriber is authorized to receive that selected safe envelope; references do not authorize later API fetches. Sender does not use broad workload permissions to expand data. Each supported kind/type has an allowlisted typed serializer and golden schema fixture. First data shapes are bounded identity/revision, actual Git ref transitions and selected semantic condition changes; no unrestricted object dumps.

## Standard Webhooks HMAC v1

- Require Standard Webhooks-compatible HMAC-SHA256 v1 initially. Signing input is UTF-8 transport ID + `.` + ASCII Unix timestamp + `.` + exact raw transmitted body. Fresh timestamp/signature per attempt; signature format `v1,<standard-base64 HMAC-SHA256>`. Receiver checks timestamp tolerance (GitStore guidance ±300 s), constant-time verification and retained deduplication. Timestamp tolerance alone does not prevent duplicate processing.
- CloudEvent id stays the outbox UID across retries/replay/providers. Dev webhook-id is logical WebhookDelivery metadata UID. Svix message ID is a transport identity mapped privately to the GitStore delivery/run; do not assume eventId overrides the signing header. Require stable transport identity across automatic retries; qualify redelivery identity separately. Use CloudEvent (source,id) for provider-independent semantic deduplication. Svix's svix-id/timestamp/signature headers are Standard Webhooks branded aliases; document the actual emitted names.
- Authorize endpoint-specific signing material through canonical SecretRef/shared/secretmaterial. Dev uses 32-byte raw keys, generic secret-record/v1 current and optional previous (max two), and clears resolved material after bounded attempts. Decode whsec_/base64 wrappers before HMAC; never sign using their textual representation. During rotation emit space-delimited signatures. Svix adapter provisions the approved endpoint key binding/rotation through its verified service capabilities. Unsupported key policies fail activation. No credential/ServiceAccount/AuthN key reuse or cross-endpoint secrets.
- Svix holds endpoint signing material and event payloads under the configured production service's security/retention policy; this external custody is explicit. Provider credentials are separate operator-owned SecretRefs. No plaintext keys, signatures, raw response/body or bearer credentials in Git/telemetry. Never fall back to unsigned delivery. Asymmetric signing is deferred.

## Network, retry and outcome policy

Production destinations require HTTPS/TLS verification and a verified egress policy. Dev validates each dial's address, disallows redirects and binds validated addresses to prevent DNS rebinding; private/test targets require explicit owned-network permission. Production Svix destination validation, redirect/DNS/SSRF protection and egress controls must be qualified against the selected profile before activation. GitStore cannot enforce a per-dial rule inside an external service merely by validating its submission URL. No URL userinfo or caller headers overriding Host/authorization/signing.

Pin destination/configuration and effective provider policy revisions. The API performs bounded asynchronous provider handoff/status reconciliation; Svix owns HTTP retries. Policy profiles declare actual retryable outcomes, finite attempt/age limits, schedule, rate/concurrency, response limits and replay availability. Reject unsupported profiles rather than silently approximating dev behavior. Dev limits/backoff are specified in [delivery-provider.md](delivery-provider.md); they are not claimed as Svix defaults. No strict receiver arrival order is guaranteed in the baseline. Failing destinations must not block unrelated subscribers or primary resource operations.

Provider acceptance is not receipt. Confirmed receiver 2xx is receipt, not completed business processing. Unavailable status/counts remain unknown and cannot become success/zero. Disable/revocation stops new handoffs and reconciles external destination state; accepted/in-flight requests may still arrive until the provider confirms stopping them. Only confirmed outcomes authorize terminal projection; no local age timer fabricates external cancellation. Expose bounded safe reasons/status without secret response snippets.

## Replay and terminal authority

WebhookDelivery.status.resolved.phase is the public terminal projection backed by provider evidence; reconcile design039's EventDeadLetter proposal to it. Preserve original outcomes and separate audited replay runs (<=10 retained/delivery, one active, <=12 normalized latest attempt summaries/run). Replaying requires separate permission/current source/destination authority, original content/configuration and provider retention. Resolve ambiguous replay acceptance before another request. Preserve CloudEvent source/id/content; transport ID behavior is adapter-qualified. Receiver may correctly deduplicate replay; it does not request forced business reprocessing. Missing retained provider message/destination returns REPLAY_EXPIRED rather than creating a replacement with invented historical transport identity.

References: [CloudEvents core](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md), [JSON format](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/formats/json-format.md), [Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md), [Svix verification](https://docs.svix.com/receiving/verifying-payloads/how). Pin and test adapter protocol behavior; do not silently track upstream changes.
