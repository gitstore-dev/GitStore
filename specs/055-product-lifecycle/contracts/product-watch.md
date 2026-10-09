# Contract: Durable Product Resource Watch

```graphql
extend type Subscription {
  watchProducts(
    namespace: String
    selector: LabelSelectorInput
    resourceVersion: String
  ): ProductWatchEvent!
}

type ProductWatchEvent {
  type: WatchEventType!
  namespace: String!
  name: String!
  resourceVersion: String!
  product: Product
}
```

`watchProducts` and `watchResources(kind: "Product", ...)` are two views of
the same durable Resource Watch journal and share the same opaque cursor
semantics. The typed event carries a Product postimage for `ADDED` and
`MODIFIED`; `DELETED` and `BOOKMARK` carry no Product payload. The generic
event carries the equivalent Product envelope/object.

## Bootstrap and resume

1. A controller requests the private bootstrap cursor/sentinel and receives a
   durable `BOOKMARK` at the journal high-water.
2. It lists fully paginated authorized Products.
3. It drains events strictly after that bookmark, then continues live.

A retained cursor resumes strictly after its recorded event from any healthy
API replica. Invalid/expired/too-old/future/over-replay-limit/overflow cursor
conditions terminate with `WATCH_EXPIRED`; unavailable or discontinuous
materialization terminates with `WATCH_UNAVAILABLE`. Consumers discard cached
state and bootstrap again. Authorization is checked before the cursor is
parsed or any failure detail/replay is disclosed.

## Ordering and event coverage

The journal append is the Product watch linearisation point. Events are
at-least-once across restart but never omit an acknowledged Product transition:
admitted create/spec update, status write, deletion marker/finalizer changes,
and final removal. Rejected, failed, rolled-back, and no-op requests do not
emit successful Product transitions. The stream has one durable order; callers
must not infer order with other kinds from Product resource versions.

## Bounds

The Product projection uses the shipped Resource Watch bounds: seven-day
journal retention, 14-day source CDC retention, 4,096-event buckets,
256-event reads, 100,000-event maximum replay, 64-event subscriber buffers,
30-second delivery/backpressure bound, 30-second durable bookmarks, and a
30-second lease renewed every 10 seconds. Product adds no per-subscriber
datastore polling and no unbounded queue.
