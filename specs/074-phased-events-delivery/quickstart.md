# Proposed Caller and Receiver Walkthrough

These examples target the proposed Admin/service contracts. No Event/audit/outbox/webhook endpoint or Git notification RPC is currently claimed implemented. Use existing Admin HTTP/WebSocket authentication, canonical subjects and the owning source/configuration boundaries.

## Record and inspect an operational Event

```graphql
mutation Record($input: RecordEventInput!) {
  recordEvent(input: $input) {
    event {
      id apiVersion kind
      metadata { name namespace uid resourceVersion generation }
      spec { type reason regarding { kind name namespace uid } }
      status { conditions { type status } resolved { series { count lastObservedTime } expiresAt } }
      body
    }
  }
}
```

```json
{
  "input": {
    "metadata": {"name": "repository-sync-failed-20261121-1200", "namespace": "store"},
    "spec": {
      "type": "WARNING",
      "reason": "RepositorySyncFailed",
      "action": "Synchronize",
      "regarding": {"apiVersion": "gitstore.dev/v1beta1", "kind": "Repository", "namespace": "store", "name": "catalog", "uid": "019aa5b0-0000-7000-8000-000000000001"},
      "reportingController": "gitstore.dev/repository-controller",
      "reportingInstance": "controller-manager-a"
    },
    "body": "Repository synchronization could not reach the Git service.\n\nA retry is scheduled.",
    "idempotencyKey": "observation-019aa5b0-0000-7000-8000-000000000002"
  }
}
```

The UID/principal/producer binding must exist or be permitted captured-reference evidence in the owned fixture; the example does not grant producer authority. Reuse the key only to retry this exact observation. RECORDING_PENDING means outcome uncertain, not confirmed loss; retry the same key. Each new occurrence gets another key. New explanation/incarnation or series closure requires a new series name. Immutable spec/body plus materialized status avoid parsing Markdown for automation.

```graphql
query Diagnostics($namespace: String!, $after: String) {
  events(namespace: $namespace, first: 50, after: $after) {
    edges { cursor node { id metadata { name } body status { resolved { series { count } expiresAt } } } }
    pageInfo { hasNextPage endCursor }
  }
}

subscription Changes($namespace: String!, $resourceVersion: String) {
  watchEvents(namespace: $namespace, resourceVersion: $resourceVersion) {
    type name namespace resourceVersion
    event { id metadata { name } status { resolved { series { count } expiresAt } } }
  }
}
```

Lists are bounded and expiry-aware. Use existing bookmark-before-list semantics before watch replay; page cursor, entity version and journal cursor are different. Nonblank query terms are available only when the shared parser/index subset is actually registered. Missing/expired history is not an empty successful replay. Diagnostic recording can fail without failing the producer's primary reconciliation.

## Operator audit access

```graphql
query Audit($after: String) {
  auditLogs(first: 50, after: $after) {
    edges { cursor node { id metadata { name } spec { operationId stage outcome actorSubject serviceSubject action reason } } }
    pageInfo { hasNextPage endCursor }
  }
}
```

Requires independent operator audit authority; an Event reader gets no audit access. Default listing fixes a24-hour window on the first page. No public audit append/update/delete/watch. Export is an audited bounded mutation; required evidence failure prevents its protected export operation. Anonymous actors remain null rather than invented users.

## Configure a subscription through Git

```yaml
---
apiVersion: events.gitstore.dev/v1alpha1
kind: EventSubscription
metadata:
  name: erp-products
  namespace: store
spec:
  sources:
    - domain: RESOURCE
      kinds: [Product]
      types: [created, updated, deleted]
  sink:
    url: https://erp.example.com/hooks/gitstore
    signingSecretRef:
      kind: Secret
      name: erp-webhook
  delivery:
    profile: standard
  enabled: true
---
Send the approved product integration envelope to the ERP.
```

Commit in the namespace's existing gitstore-system repository through normal admission. Secret is a configured authorized reference/provider binding, not a new implemented Secret resource API. Do not commit key material. Admission validates source/egress/secret authority; activation remains unready until source watermarks/config history are captured. A later edit cannot retarget older work. Storefront/internal identity notifications are not subscribed automatically.

## Provider, receiver and replay

Operator api.integrations configuration selects dev for local development or svix for production, with an authorized provider credential reference and versioned standard policy profile. These are proposed settings, not shipped CLI flags. Dev is process-local and cannot qualify replica durability. Production activation validates the Svix deployment/account limits, immutable endpoint/key binding, tenant isolation, retention and policy capabilities; unsupported profiles remain unready.

API background processing persists a handoff intent and submits the full approved CloudEvent through the provider. Svix manages queueing/signing/retries; API replicas reconcile uncertain submission and asynchronously materialize provider outcomes. There are no worker GraphQL operations or controller sender. Provider acceptance is not successful delivery. Receiver verifies the [wire contract](contracts/delivery-wire.md), deduplicates CloudEvent (source,id) for semantic effects and may receive duplicate or out-of-order requests. See [provider contract](contracts/delivery-provider.md).

```graphql
mutation Replay($input: ReplayWebhookDeliveryInput!) {
  replayWebhookDelivery(input: $input) {
    delivery { id status { resolved { phase attemptCount lastReason } } }
    replayRunId
  }
}
```

Replay requires separate authority/current policy/audit, original content/configuration and retained provider message/destination. It preserves CloudEvent source/id/content and records another internal run; transport IDs follow the verified adapter contract. A receiver may deduplicate it. Expired material returns REPLAY_EXPIRED; uncertain provider acceptance must reconcile before another request. No promise of forcing business re-execution.

## Implementation/qualification sequence

Use the plan's US1–US5 slices, failing contracts before code, forward migrations/shared generation and existing `make generate TARGET=all`, `make test`, `make check TARGET=all`. Extend existing capacity/chaos selectors only under owned fixtures; preserve retained data and singleton Git storage. Full `make pr-ready` remains required before implementation PRs. Git source-driven admission stays inactive until #501's idempotent accepted-work interface is implemented. Run the agent context helper with `SPECIFY_FEATURE=074-phased-events-delivery` for numeric-prefix-only scripts while retaining the actual codex branch name.
