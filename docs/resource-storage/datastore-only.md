# Datastore-Only Resources

Datastore-only resources are durable records but are not Git-authored. They are
created by APIs, checkout flows, controllers, workers, identity providers,
payment gateways, inventory systems, or webhooks.

These resources may still use `apiVersion`, `kind`, `metadata`, `spec`, and
`status` internally. The key difference is source of truth: ScyllaDB or memDB
owns the record, not Git.

## API Shape

```yaml
apiVersion: checkout.gitstore.dev/v1beta1
kind: Order
metadata:
  name: ord-10001
  namespace: example-store
  labels: {}
  annotations: {}
spec:
  customerRef:
    kind: Customer
    name: cus-10001
status:
  observedGeneration: 1
  conditions: []
```

Use generated IDs where names are not human-authored. Do not write these
resources back to Git unless a separate export or audit feature is explicitly
designed.

## Customer And B2B

| Resource                  | Scope | Summary                                                      | Initial spec shape                                                                 |
|---------------------------|-------|--------------------------------------------------------------|------------------------------------------------------------------------------------|
| `Customer`                | Core  | Customer account identity within a namespace.                | `spec: {externalIds, emailHash, profileRef, defaultAddressRef, customerGroupRefs}` |
| `CustomerProfile`         | Core  | Customer profile data subject to privacy and deletion rules. | `spec: {name, email, phone, locale, preferences}`                                  |
| `Address`                 | Core  | Customer, company, billing, or shipping address.             | `spec: {recipient, lines, city, region, postalCode, countryCode, phone}`           |
| `CustomerGroupMembership` | Core  | Runtime membership of customers in groups or segments.       | `spec: {customerRef, groupRef, source, validUntilTime}`                            |
| `Company`                 | Core  | B2B company account.                                         | `spec: {displayName, identifiers, billingAddressRef, creditLimit}`                 |
| `CompanyLocation`         | Core  | B2B location, branch, or buying unit.                        | `spec: {companyRef, addressRef, taxIds, buyerPolicy}`                              |
| `BuyerRoleAssignment`     | Core  | Assigns customer users to B2B buyer roles.                   | `spec: {companyRef, customerRef, role, spendingLimit}`                             |
| `ConsentRecord`           | Core  | Record of consent captured from a subject.                   | `spec: {subjectRef, policyRef, granted, evidence, capturedAt}`                     |
| `PrivacyRequest`          | Core  | GDPR/CCPA access, deletion, export, or correction request.   | `spec: {subjectRef, type, requestedAt, verification}`                              |

## Authentication Runtime

| Resource        | Scope         | Summary                                                          | Initial spec shape                                                |
|-----------------|---------------|------------------------------------------------------------------|-------------------------------------------------------------------|
| `Session`       | Core          | Authenticated session and expiry metadata.                       | `spec: {principal, issuedAt, expiresAt, authMethod}`              |
| `RefreshToken`  | Core          | Refresh token record or hash.                                    | `spec: {subject, tokenHash, issuedAt, expiresAt, revokedAt}`      |
| `TokenJTI`      | Core          | JWT ID allow/deny or replay tracking record.                     | `spec: {issuer, subject, jti, expiresAt, revoked}`                |
| `APIKeyHash`    | Core          | API key metadata and hash, never the raw key.                    | `spec: {subject, keyHash, scopes, expiresAt, lastUsedAt}`         |
| `MFADevice`     | Extension/CRD | Multi-factor authentication device registration.                 | `spec: {subject, type, provider, verifiedAt, disabledAt}`         |
| `IdentityLink`  | Core          | Mapping from external `(issuer, subject)` to GitStore principal. | `spec: {issuer, subject, principalRef, claims}`                   |
| `Invite`        | Core          | User or collaborator invitation.                                 | `spec: {emailHash, roleRefs, tokenHash, expiresAt}`               |
| `PasswordReset` | Extension/CRD | Password reset challenge for local identity providers.           | `spec: {subject, tokenHash, expiresAt, usedAt}`                   |
| `LoginAttempt`  | Core          | Login attempt, risk, and rate-limit record.                      | `spec: {subjectHint, ipHash, userAgentHash, outcome, occurredAt}` |

## Authorization Runtime

| Resource            | Scope | Summary                                                 | Initial spec shape                                                      |
|---------------------|-------|---------------------------------------------------------|-------------------------------------------------------------------------|
| `PolicyDecisionLog` | Core  | Durable authorization decision log when configured.     | `spec: {principal, action, resourceRef, decision, reason, evaluatedAt}` |
| `SubjectCache`      | Core  | Cached provider groups, roles, or claims for a subject. | `spec: {issuer, subject, groups, roles, scopes, expiresAt}`             |

## Cart And Checkout

| Resource            | Scope | Summary                                                      | Initial spec shape                                                           |
|---------------------|-------|--------------------------------------------------------------|------------------------------------------------------------------------------|
| `Cart`              | Core  | Mutable shopping cart.                                       | `spec: {customerRef, anonymousId, currencyCode, marketRef, channelRef}`      |
| `CartLine`          | Core  | Cart line item.                                              | `spec: {cartRef, variantRef, quantity, customAttributes}`                    |
| `Checkout`          | Core  | Checkout aggregate derived from a cart.                      | `spec: {cartRef, customerRef, email, shippingAddressRef, billingAddressRef}` |
| `CheckoutSession`   | Core  | Short-lived checkout session, idempotency, and client state. | `spec: {checkoutRef, idempotencyKey, expiresAt, state}`                      |
| `PriceSnapshot`     | Core  | Applied price details frozen for checkout/order integrity.   | `spec: {sourceRef, lines, currencyCode, computedAt}`                         |
| `TaxSnapshot`       | Core  | Applied tax calculation frozen for checkout/order integrity. | `spec: {sourceRef, taxLines, provider, computedAt}`                          |
| `ShippingSelection` | Core  | Selected shipping method/rate for checkout.                  | `spec: {checkoutRef, shippingMethodRef, rate, deliveryEstimate}`             |

## Orders

Orders are datastore-only by default. Git-backed `Order` resources are not
recommended for the main purchase lifecycle because orders are high-churn, often
contain PII, and require privacy-aware retention.

| Resource       | Scope         | Summary                                                         | Initial spec shape                                                                    |
|----------------|---------------|-----------------------------------------------------------------|---------------------------------------------------------------------------------------|
| `Order`        | Core          | Purchase lifecycle aggregate from checkout through fulfillment. | `spec: {customerRef, lineItems, currencyCode, shippingAddressRef, billingAddressRef}` |
| `OrderLine`    | Core          | Purchased line item with price, tax, and fulfillment state.     | `spec: {orderRef, variantRef, quantity, priceSnapshotRef, taxSnapshotRef}`            |
| `OrderEvent`   | Core          | Append-only order timeline event.                               | `spec: {orderRef, type, payload, occurredAt, actor}`                                  |
| `OrderEdit`    | Core          | Proposed or applied order modification.                         | `spec: {orderRef, changes, reason, requestedBy}`                                      |
| `DraftOrder`   | Core          | Merchant-created draft order or invoice workflow.               | `spec: {customerRef, lineItems, discounts, expiresAt}`                                |
| `Quote`        | Extension/CRD | B2B quote with negotiated prices and validity window.           | `spec: {customerRef, companyRef, lineItems, validUntilTime}`                          |
| `Cancellation` | Core          | Order or line cancellation request and result.                  | `spec: {orderRef, lineRefs, reason, requestedBy}`                                     |
| `OrderNote`    | Core          | Internal or customer-visible order note.                        | `spec: {orderRef, visibility, body, author}`                                          |

## Payments

Payment resources are datastore-only. Payment credentials, card data, and gateway
secrets must never be committed to Git.

| Resource               | Scope         | Summary                                                    | Initial spec shape                                                    |
|------------------------|---------------|------------------------------------------------------------|-----------------------------------------------------------------------|
| `PaymentIntent`        | Core          | Intent to authorize or collect payment for checkout/order. | `spec: {amount, currencyCode, gatewayRef, orderRef, idempotencyKey}`  |
| `PaymentAuthorization` | Core          | Gateway authorization result.                              | `spec: {paymentIntentRef, gatewayAuthorizationId, amount, expiresAt}` |
| `Capture`              | Core          | Payment capture operation.                                 | `spec: {authorizationRef, amount, gatewayCaptureId, capturedAt}`      |
| `Refund`               | Core          | Refund request/result.                                     | `spec: {paymentRef, amount, reason, gatewayRefundId}`                 |
| `Dispute`              | Core          | Payment dispute or chargeback record.                      | `spec: {paymentRef, gatewayDisputeId, amount, reason, evidenceRefs}`  |
| `Payout`               | Extension/CRD | Settlement payout from payment provider.                   | `spec: {gatewayRef, amount, currencyCode, arrivalDate, entries}`      |
| `PaymentMethodRef`     | Core          | Tokenized reference to a customer payment method.          | `spec: {customerRef, gatewayRef, tokenRef, brand, expires}`           |
| `FraudReview`          | Core          | Manual or automated fraud review state.                    | `spec: {orderRef, riskAssessmentRef, decision, reviewer}`             |
| `RiskAssessment`       | Core          | Risk signals and score for checkout/order/payment.         | `spec: {subjectRef, score, signals, provider, assessedAt}`            |

## Inventory Runtime

| Resource               | Scope         | Summary                                                  | Initial spec shape                                                         |
|------------------------|---------------|----------------------------------------------------------|----------------------------------------------------------------------------|
| `StockLevel`           | Core          | Current quantity at a stock location.                    | `spec: {variantRef, stockLocationRef, onHand, available}`                  |
| `InventoryLedgerEntry` | Core          | Append-only stock movement ledger.                       | `spec: {variantRef, stockLocationRef, delta, reason, sourceRef}`           |
| `Reservation`          | Core          | Temporary inventory hold for cart or checkout.           | `spec: {variantRef, stockLocationRef, quantity, ownerRef, expiresAt}`      |
| `Allocation`           | Core          | Assigned inventory for an order or fulfillment.          | `spec: {orderLineRef, stockLocationRef, quantity}`                         |
| `Transfer`             | Core          | Movement of stock between locations.                     | `spec: {fromLocationRef, toLocationRef, variantRefs, status}`              |
| `Adjustment`           | Core          | Manual or system stock correction.                       | `spec: {variantRef, stockLocationRef, delta, reason, evidenceRef}`         |
| `StockCount`           | Extension/CRD | Cycle count or physical stock count.                     | `spec: {stockLocationRef, countedItems, countedAt, countedBy}`             |
| `Receiving`            | Extension/CRD | Inbound receiving event from purchase order or supplier. | `spec: {stockLocationRef, sourceRef, receivedItems, receivedAt}`           |
| `AvailabilitySnapshot` | Core          | Cached availability projection for storefront reads.     | `spec: {variantRef, marketRef, channelRef, availableQuantity, computedAt}` |

## Fulfillment Runtime

| Resource           | Scope         | Summary                                                   | Initial spec shape                                              |
|--------------------|---------------|-----------------------------------------------------------|-----------------------------------------------------------------|
| `FulfillmentOrder` | Core          | Fulfillment work item derived from an order.              | `spec: {orderRef, lines, assignedLocationRef, serviceLevel}`    |
| `Fulfillment`      | Core          | Fulfilled subset of an order.                             | `spec: {fulfillmentOrderRef, lines, shipmentRefs, fulfilledAt}` |
| `Shipment`         | Core          | Carrier shipment and tracking.                            | `spec: {fulfillmentRef, carrier, trackingNumber, packages}`     |
| `Package`          | Core          | Physical package contents and dimensions.                 | `spec: {shipmentRef, lineRefs, weight, dimensions}`             |
| `TrackingEvent`    | Core          | Carrier tracking event.                                   | `spec: {shipmentRef, status, message, location, occurredAt}`    |
| `DeliveryAttempt`  | Extension/CRD | Delivery attempt record.                                  | `spec: {shipmentRef, outcome, occurredAt, evidenceRef}`         |
| `Return`           | Core          | Return request and lifecycle.                             | `spec: {orderRef, lineRefs, reason, requestedAt}`               |
| `Exchange`         | Extension/CRD | Exchange workflow linking returned and replacement items. | `spec: {returnRef, replacementLineItems, priceDifference}`      |
| `RMA`              | Core          | Return merchandise authorization.                         | `spec: {returnRef, number, expiresAt, instructions}`            |

## Value Instruments

Generated codes and balances are datastore-only even when their campaign or
policy is Git-backed.

| Resource         | Scope         | Summary                                                      | Initial spec shape                                         |
|------------------|---------------|--------------------------------------------------------------|------------------------------------------------------------|
| `CouponCode`     | Core          | Individual generated or imported coupon code.                | `spec: {campaignRef, codeHash, usageLimit, expiresAt}`     |
| `Voucher`        | Extension/CRD | Voucher with value or entitlement.                           | `spec: {ownerRef, value, currencyCode, expiresAt}`         |
| `GiftCard`       | Core          | Gift card balance instrument.                                | `spec: {codeHash, initialBalance, currencyCode, ownerRef}` |
| `StoreCredit`    | Core          | Customer store credit balance.                               | `spec: {customerRef, balance, currencyCode, expiresAt}`    |
| `LoyaltyAccount` | Extension/CRD | Customer loyalty account.                                    | `spec: {customerRef, tier, pointsBalance}`                 |
| `PointsLedger`   | Extension/CRD | Loyalty points ledger entry.                                 | `spec: {loyaltyAccountRef, delta, reason, sourceRef}`      |
| `Redemption`     | Extension/CRD | Redemption of coupon, voucher, gift card, credit, or points. | `spec: {instrumentRef, orderRef, amount, redeemedAt}`      |

## Platform Runtime

| Resource          | Scope | Summary                                               | Initial spec shape                                                |
|-------------------|-------|-------------------------------------------------------|-------------------------------------------------------------------|
| `HydrationRecord` | Core  | Records hydration of a Git resource into datastore.   | `spec: {resourceRef, gitRef, gitCommitSHA, result}`               |
| `AdmissionResult` | Core  | Admission outcome for pushed resources.               | `spec: {resourceRef, revision, accepted, errors}`                 |
| `ReconcileJob`    | Core  | Controller reconciliation work item.                  | `spec: {resourceRef, reason, attempt, scheduledAt}`               |
| `WatchEvent`      | Core  | Stored watch event for resume or fanout when enabled. | `spec: {type, resourceRef, resourceVersion, payload}`             |
| `OutboxEvent`     | Core  | Transactional outbox event for integrations.          | `spec: {eventType, aggregateRef, payload, availableAt}`           |
| `WebhookDelivery` | Core  | Delivery attempt to a webhook endpoint.               | `spec: {endpointRef, eventRef, attempt, response, nextAttemptAt}` |
| `AuditLog`        | Core  | Append-only audit event.                              | `spec: {actor, action, resourceRef, before, after, occurredAt}`   |
| `IndexState`      | Core  | Search or projection index cursor and health.         | `spec: {indexName, cursor, lastIndexedAt, status}`                |
