# Transient Resources

Transient resources use Kubernetes-style shapes for consistency, but they are
not stored as durable resources. They are request/response contracts for checks,
quotes, previews, authorization decisions, signed URL creation, or dry runs.

Durable traces can be emitted separately as datastore-only events such as
`AuditLog`, `OrderEvent`, `OutboxEvent`, `WebhookDelivery`, or provider-specific
logs.

## Request/Response Shape

```yaml
apiVersion: checkout.gitstore.dev/v1beta1
kind: PriceQuote
spec:
  marketRef:
    kind: Market
    name: eu
  lines:
  - variantRef:
      kind: ProductVariant
      name: example-sku
    quantity: 1
status:
  result:
    currencyCode: EUR
    total: "19.99"
```

`metadata.name` is usually omitted unless idempotency or correlation requires it.

## AuthN And AuthZ Checks

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `Login` | Core | Authenticates a principal and returns a session. | `spec: {credentials, provider}` |
| `Logout` | Core | Invalidates the current session or token. | `spec: {sessionRef, token}` |
| `RefreshToken` | Core | Exchanges a refresh token for a new session. | `spec: {refreshToken}` |
| `TokenReview` | Core | Authenticates or introspects a token. | `spec: {token, audiences}` |
| `SubjectAccessReview` | Core | Checks whether a principal can perform an action on a resource. | `spec: {principal, action, resourceRef, context}` |

Example:

```yaml
apiVersion: authz.gitstore.dev/v1beta1
kind: SubjectAccessReview
spec:
  principal:
    subject: user-123
    issuer: oidc
  action: repository.write
  resourceRef:
    kind: Repository
    name: catalog
status:
  allowed: true
  reason: RoleBindingMatched
```

## Admission And Validation

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `AdmissionReview` | Core | Reviews a proposed resource write before persistence. | `spec: {operation, resource, oldResource, dryRun}` |
| `ValidationReview` | Core | Performs structural validation without writing anything. | `spec: {resource, schemaRef, strict}` |
| `CatalogDiff` | Core | Computes differences between two Git refs or releases. | `spec: {baseRef, targetRef, resourceSelector}` |
| `ImportDryRun` | Core | Validates an import file and reports proposed changes. | `spec: {fileRef, format, mapping, options}` |

Example:

```yaml
apiVersion: admission.gitstore.dev/v1beta1
kind: ValidationReview
spec:
  strict: true
  resource:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: Product
    metadata:
      name: example-product
    spec:
      title: Example Product
status:
  valid: true
  errors: []
```

## Pricing, Tax, Shipping, And Inventory Calculations

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `PriceQuote` | Core | Computes applicable prices for lines and context. | `spec: {lines, marketRef, channelRef, customerRef, currencyCode}` |
| `TaxQuote` | Core | Computes taxes for checkout/order context. | `spec: {lines, addresses, taxCategoryRefs, providerRef}` |
| `ShippingRateQuote` | Core | Returns eligible shipping methods and rates. | `spec: {destination, lines, packageHints, marketRef}` |
| `InventoryAvailabilityReview` | Core | Checks availability for variants and quantities. | `spec: {lines, marketRef, channelRef, locationRefs}` |
| `PromotionEvaluation` | Core | Evaluates promotion eligibility and benefits. | `spec: {cartRef, lines, customerRef, couponCodes}` |
| `CheckoutValidation` | Core | Validates checkout readiness before order creation. | `spec: {checkoutRef, checks}` |
| `RefundCalculation` | Core | Computes refundable amounts and adjustments. | `spec: {orderRef, lineRefs, reason, includeShipping}` |

Example:

```yaml
apiVersion: inventory.gitstore.dev/v1beta1
kind: InventoryAvailabilityReview
spec:
  lines:
  - variantRef:
      kind: ProductVariant
      name: macbook-pro-silver
    quantity: 1
  marketRef:
    kind: Market
    name: eu
status:
  available: true
  lines:
  - availableQuantity: 18
```

## Payments And Fraud

Payment provider calls are transient at the API boundary. Durable outcomes are
stored as datastore-only payment resources.

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `FraudScoreRequest` | Core | Requests risk scoring from internal or external systems. | `spec: {checkoutRef, orderRef, paymentIntentRef, signals}` |
| `PaymentAuthorizeRequest` | Core | Requests gateway authorization. | `spec: {paymentIntentRef, paymentMethodRef, amount, idempotencyKey}` |
| `PaymentCaptureRequest` | Core | Requests capture of an authorization. | `spec: {authorizationRef, amount, idempotencyKey}` |

Example:

```yaml
apiVersion: payments.gitstore.dev/v1beta1
kind: PaymentAuthorizeRequest
spec:
  paymentIntentRef:
    kind: PaymentIntent
    name: pi-10001
  amount: "49.99"
  currencyCode: EUR
  idempotencyKey: checkout-10001-authorize
status:
  accepted: true
  authorizationRef:
    kind: PaymentAuthorization
    name: pa-10001
```

## Search, Preview, And Upload

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `SearchQuery` | Core | Executes a search request. | `spec: {query, filters, sort, pagination, context}` |
| `ProductPreview` | Core | Renders or resolves a product preview from a Git ref. | `spec: {productRef, gitRef, locale, marketRef}` |
| `UploadSession` | Core | Starts a resumable or multipart upload session. | `spec: {purpose, contentType, sizeBytes, checksum}` |
| `PresignedUploadURL` | Core | Returns a signed URL for direct upload. | `spec: {uploadSessionRef, partNumber, expiresInSeconds}` |
| `WebhookTest` | Core | Sends a synthetic event to a webhook endpoint. | `spec: {endpointRef, eventType, payload}` |

Example:

```yaml
apiVersion: storage.gitstore.dev/v1beta1
kind: PresignedUploadURL
spec:
  uploadSessionRef:
    kind: UploadSession
    name: upload-10001
  expiresInSeconds: 900
status:
  url: https://storage.example.com/signed-upload
  expiresAt: "2026-06-17T12:15:00Z"
```
