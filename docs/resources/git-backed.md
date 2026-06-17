# Git-Backed Resources

Git-backed resources are desired-state documents. Git stores the author-written
frontmatter and Markdown body. ScyllaDB or memDB stores hydrated records,
indexes, Git provenance, resource versions, generated metadata, and
system-managed `status`.

## Authoring Shell

Every resource in this page uses this Markdown shell unless noted otherwise.
Replace `apiVersion`, `kind`, and `spec` with the resource-specific shape.

```markdown
---
apiVersion: <group>.gitstore.dev/v1beta1
kind: <Kind>
metadata:
  name: example-name
  namespace: example-store
  labels: {}
  annotations: {}
spec: {}
---

Optional Markdown body content.
```

`status` is not author-writable. Controllers and admission write status to the
datastore.

## Control Plane

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `Namespace` | Core | Tenant or account boundary for repositories and commerce resources. | `spec: {displayName, tier, parentEnterpriseRef, defaults, limits}` |
| `Repository` | Core | Git repository declaration for catalog or configuration storage. | `spec: {name, defaultBranch, visibility, storageClass, description}` |
| `Environment` | Core | Deployment or runtime environment such as dev, staging, prod. | `spec: {type, branchRef, promotionPolicy, variablesRef}` |
| `CatalogRelease` | Core | Immutable publication marker for a catalog Git revision. | `spec: {repositoryRef, gitRef, gitCommitSHA, notes}` |
| `Publication` | Core | Maps a release or branch to a market/channel/storefront target. | `spec: {releaseRef, targetRef, effectiveFromTime, rollbackRef}` |
| `WorkflowPolicy` | Extension/CRD | Review, approval, and automation policy for Git-backed changes. | `spec: {resourceSelector, requiredApprovals, checks, autoMerge}` |

Example:

```markdown
---
apiVersion: core.gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme-store
spec:
  displayName: Acme Store
  tier: enterprise
---

Primary namespace for Acme commerce resources.
```

## Catalog And Merchandising

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `Product` | Core | Non-sellable product descriptor with copy, classification, options, and media references. | `spec: {title, categoryRef, tags, media, options}` |
| `ProductVariant` | Core | Purchasable SKU with selected options, pricing rules, inventory policy, and media. | `spec: {title, sku, productRef, selectedOptions, pricing, inventory, media}` |
| `CategoryTaxonomy` | Core | Hierarchical category node. Products belong to exactly one category in the current model. | `spec: {title, parentRef, media}` |
| `Collection` | Core | Selector-driven product grouping for merchandising. | `spec: {title, targetRef, selector, media}` |
| `Bundle` | Extension/CRD | Sellable or merchandised group of variants with bundle-level pricing and rules. | `spec: {title, componentRefs, pricing, inventoryPolicy, media}` |
| `Kit` | Extension/CRD | Operational kit assembled from required components, often fulfilled as one unit. | `spec: {title, componentRefs, assemblyPolicy, fulfillmentPolicy}` |
| `Brand` | Core | Brand identity, display metadata, and brand-level media. | `spec: {title, slug, media, websiteURL}` |
| `Vendor` | Core | Commercial vendor or supplier shown in catalog data. | `spec: {displayName, accountRef, contact, termsRef}` |
| `Manufacturer` | Extension/CRD | Manufacturer metadata distinct from vendor/seller. | `spec: {displayName, countryCode, identifiers, contact}` |
| `ProductType` | Core | Product type taxonomy used for validation, facets, and default attributes. | `spec: {title, attributeRefs, optionSetRefs, facetRefs}` |
| `AttributeDefinition` | Core | Typed product or variant attribute definition. | `spec: {title, dataType, allowedValues, unit, validation}` |
| `OptionSet` | Core | Reusable product option names and allowed values. | `spec: {options}` |
| `FacetDefinition` | Core | Search/filter facet declaration for storefront discovery. | `spec: {fieldPath, title, type, display, sort}` |

Example:

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: macbook-pro
  namespace: acme-store
spec:
  title: MacBook Pro
  categoryRef:
    kind: CategoryTaxonomy
    name: laptops
  tags: [laptop]
  options:
  - name: color
    values: [silver, space-black]
---

Long-form product description.
```

## Localization And Content

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `Translation` | Core | Locale-specific field patches and translated Markdown body for another resource. | `spec: {locale, targetRef, patches}` |
| `Locale` | Core | Supported locale declaration and fallback behavior. | `spec: {languageTag, fallbackLocaleRef, textDirection, enabled}` |
| `MarketLocalization` | Extension/CRD | Market-specific localization defaults and overrides. | `spec: {marketRef, localeRefs, fallbackPolicy}` |
| `ContentPage` | Extension/CRD | CMS-style page content that belongs in Git review workflows. | `spec: {title, slug, template, seo, publish}` |
| `NavigationMenu` | Extension/CRD | Storefront menu tree with resource links. | `spec: {title, items, channelRefs}` |
| `SearchSynonymSet` | Extension/CRD | Search synonyms and equivalent terms. | `spec: {locale, synonyms}` |
| `SearchRedirect` | Extension/CRD | Search query to destination redirect rules. | `spec: {query, targetURL, locale, channelRefs}` |

Example:

```markdown
---
apiVersion: i18n.gitstore.dev/v1beta1
kind: Translation
metadata:
  name: macbook-pro-de-de
  namespace: acme-store
spec:
  locale: de-DE
  targetRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: Product
    name: macbook-pro
  patches:
  - fieldPath: spec.title
    translation: MacBook Pro
---

Translated Markdown body.
```

## Media Manifests

These resources describe files. They do not require binary payloads to be stored
directly in Git. See [LFS and object storage resources](lfs-object-storage.md).

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `File` | Core | Generic file manifest with content type, source URI, checksum, and processing hints. | `spec: {contentType, type, source, processing}` |
| `MediaAsset` | Core | Image, video, or audio asset with semantic usage and derived variants. | `spec: {fileRef, role, altTextRef, focalPoint, variants}` |
| `DigitalAsset` | Extension/CRD | Downloadable commercial asset such as software, ebook, warranty PDF, or license file. | `spec: {fileRef, entitlementPolicyRef, deliveryPolicy, version}` |

Example:

```markdown
---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: product-hero
  namespace: acme-store
spec:
  contentType: image/jpeg
  type: gitstore.dev/media
  source:
    type: git
    uri: git:///media/product-hero.jpg?ref=main
    checksum:
      algorithm: sha256
      value: example
---

Alt text for the image.
```

## Pricing Configuration

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `PriceList` | Core | Named price list for a market, customer segment, or channel. | `spec: {currencyCode, marketRefs, customerGroupRefs, prices}` |
| `PriceSet` | Core | Reusable set of price templates and eligibility expressions. | `spec: {prices}` |
| `PriceRule` | Core | Independent pricing rule that can be referenced by products, variants, or price lists. | `spec: {selector, amount, strategy, eligibility, priority}` |
| `CurrencyPolicy` | Core | Supported currencies and conversion policy. | `spec: {baseCurrencyCode, allowedCurrencyCodes, conversionProviderRef}` |
| `RoundingPolicy` | Extension/CRD | Currency and market rounding behavior for computed prices. | `spec: {currencyCode, mode, increment}` |
| `TaxInclusivePricingPolicy` | Extension/CRD | Defines where prices are tax-inclusive or tax-exclusive. | `spec: {marketRefs, included, displayMode}` |

Example:

```markdown
---
apiVersion: pricing.gitstore.dev/v1beta1
kind: PriceList
metadata:
  name: eu-retail
  namespace: acme-store
spec:
  currencyCode: EUR
  marketRefs:
  - kind: Market
    name: eu
  prices: []
---

EU retail prices.
```

## Promotions Configuration

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `Campaign` | Core | Time-bounded commercial campaign containing promotions or rules. | `spec: {title, activeFromTime, activeUntilTime, promotionRefs, channelRefs}` |
| `Promotion` | Core | Promotion container with eligibility and benefit rules. | `spec: {title, eligibility, benefits, priority, stackingPolicy}` |
| `DiscountRule` | Core | Reusable discount calculation rule. | `spec: {selector, amount, strategy, eligibility, limits}` |
| `CouponCampaign` | Core | Coupon campaign configuration; generated codes are datastore-only. | `spec: {codePattern, discountRuleRef, usageLimits, activeWindow}` |
| `GiftWithPurchaseRule` | Extension/CRD | Adds a gift item when cart conditions match. | `spec: {eligibility, giftVariantRefs, quantity, limits}` |
| `LoyaltyRule` | Extension/CRD | Loyalty earning or redemption policy. | `spec: {event, points, eligibility, limits}` |

Example:

```markdown
---
apiVersion: promotions.gitstore.dev/v1beta1
kind: Promotion
metadata:
  name: summer-sale
  namespace: acme-store
spec:
  title: Summer Sale
  priority: 100
  eligibility: {}
  benefits: []
---

Promotion notes.
```

## Markets And Channels

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `Market` | Core | Commercial market made of countries, currency, tax, and localization defaults. | `spec: {title, countrySetRef, currencyCode, localeRefs, taxPolicyRefs}` |
| `Region` | Core | Geographic grouping for pricing, tax, shipping, or compliance. | `spec: {title, countryCodes, subdivisionCodes}` |
| `SalesChannel` | Core | Channel such as web, mobile, marketplace, B2B portal, or POS. | `spec: {title, type, marketRefs, storefrontRefs}` |
| `Storefront` | Core | Storefront configuration and routing for a channel. | `spec: {title, domains, defaultLocaleRef, defaultMarketRef, themeRef}` |
| `CountrySet` | Core | Reusable set of countries or subdivisions. | `spec: {countryCodes, subdivisionCodes}` |
| `CurrencyZone` | Extension/CRD | Group of markets sharing currency and conversion behavior. | `spec: {currencyCode, marketRefs, roundingPolicyRef}` |

Example:

```markdown
---
apiVersion: markets.gitstore.dev/v1beta1
kind: Market
metadata:
  name: eu
  namespace: acme-store
spec:
  title: European Union
  countrySetRef:
    kind: CountrySet
    name: eu-countries
  currencyCode: EUR
---

EU market configuration.
```

## Inventory Configuration

Runtime stock quantities and reservations are datastore-only. These resources
declare desired inventory topology and policies.

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `StockLocation` | Core | Sellable stock location reference used by variants and availability. | `spec: {title, type, addressRef, enabled}` |
| `Warehouse` | Core | Physical warehouse configuration and capabilities. | `spec: {title, address, capabilities, operatingHours}` |
| `SupplySource` | Extension/CRD | Supplier, dropship, or upstream inventory source. | `spec: {type, vendorRef, leadTime, capabilities}` |
| `InventoryPolicy` | Core | Availability rules for managed inventory. | `spec: {selector, allocationPolicyRef, reservationPolicyRef, backorderPolicyRef}` |
| `AllocationPolicy` | Core | Rules for assigning demand to stock locations. | `spec: {strategy, locationPriority, splitShipmentAllowed}` |
| `ReservationPolicy` | Core | Hold duration and reservation release behavior. | `spec: {ttlSeconds, refreshAllowed, releaseOnEvents}` |
| `BackorderPolicy` | Core | Backorder eligibility and limits. | `spec: {enabled, maxQuantity, expectedAvailability, customerMessage}` |
| `SafetyStockPolicy` | Extension/CRD | Buffer stock rules to protect operational inventory. | `spec: {selector, quantity, percent, locationRefs}` |

Example:

```markdown
---
apiVersion: inventory.gitstore.dev/v1beta1
kind: StockLocation
metadata:
  name: berlin-warehouse
  namespace: acme-store
spec:
  title: Berlin Warehouse
  type: warehouse
  enabled: true
---

Primary EU stock location.
```

## Fulfillment Configuration

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `FulfillmentLocation` | Core | Location that can fulfill orders, distinct from sellable stock if needed. | `spec: {title, stockLocationRef, address, capabilities}` |
| `ShippingZone` | Core | Destination zone for shipping rates and restrictions. | `spec: {countrySetRef, postalCodeRules, subdivisionRules}` |
| `ShippingMethod` | Core | Customer-selectable shipping method. | `spec: {title, carrierServiceRef, shippingZoneRefs, constraints}` |
| `ShippingRateTable` | Core | Deterministic shipping rates by zone, weight, price, or quantity. | `spec: {currencyCode, rates}` |
| `CarrierService` | Extension/CRD | Carrier integration configuration without secrets. | `spec: {provider, serviceCodes, credentialsRef, sandbox}` |
| `FulfillmentPolicy` | Core | Fulfillment routing, splitting, and SLA policy. | `spec: {selector, routingStrategy, splitPolicy, sla}` |
| `ReturnPolicy` | Core | Return eligibility and refund rules. | `spec: {windowDays, eligibility, restockingFee, refundMethod}` |
| `PackagingProfile` | Extension/CRD | Package sizes, weights, and constraints for rating and fulfillment. | `spec: {packages, defaultPackage, constraints}` |
| `PickupLocation` | Extension/CRD | Customer pickup location metadata and schedule. | `spec: {address, operatingHours, instructions, enabled}` |

Example:

```markdown
---
apiVersion: fulfillment.gitstore.dev/v1beta1
kind: ShippingMethod
metadata:
  name: standard-eu
  namespace: acme-store
spec:
  title: Standard EU
  shippingZoneRefs:
  - kind: ShippingZone
    name: eu
  constraints: {}
---

Standard EU delivery method.
```

## Tax And Compliance Configuration

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `TaxCategory` | Core | Product tax category used by tax rules and providers. | `spec: {title, taxCodeRef, productSelector}` |
| `TaxCode` | Core | Canonical tax code mapping for a provider or jurisdiction. | `spec: {code, provider, description}` |
| `TaxRegion` | Core | Jurisdiction or tax region. | `spec: {countryCode, subdivisionCode, postalCodeRules}` |
| `TaxRate` | Core | Static tax rate for a region and category. | `spec: {taxRegionRef, taxCategoryRef, rate, included}` |
| `TaxRule` | Core | Tax selection or override rule. | `spec: {selector, taxRateRef, eligibility, priority}` |
| `TaxProviderConfig` | Extension/CRD | External tax provider configuration without secrets. | `spec: {provider, accountRef, credentialsRef, nexusRules}` |
| `RestrictedGoodsPolicy` | Extension/CRD | Restricts products by market, customer, or destination. | `spec: {selector, destinationRules, customerRules, action}` |
| `AgeGatePolicy` | Extension/CRD | Age verification requirements for products or markets. | `spec: {selector, minimumAge, verificationProviderRef}` |
| `LegalPolicy` | Core | Terms, privacy, refund, or legal document policy. | `spec: {type, localeRefs, contentRef, effectiveFromTime}` |
| `ConsentPolicy` | Core | Consent collection requirements and retention. | `spec: {purpose, required, localeRefs, retention}` |

Example:

```markdown
---
apiVersion: compliance.gitstore.dev/v1beta1
kind: TaxCategory
metadata:
  name: standard-goods
  namespace: acme-store
spec:
  title: Standard Goods
  taxCodeRef:
    kind: TaxCode
    name: standard
---

Default taxable goods category.
```

## AuthZ Configuration

Authentication runtime records are datastore-only. These resources are
reviewable desired-state authorization and policy configuration.

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `Role` | Core | Named role containing permission references. | `spec: {permissionRefs, description}` |
| `RoleBinding` | Core | Binds users, groups, service accounts, or external subjects to roles. | `spec: {subjects, roleRef, resourceSelector}` |
| `PermissionSet` | Core | Reusable set of actions on resource types. | `spec: {permissions}` |
| `Policy` | Core | Declarative authorization policy for an authz provider such as RBAC, OPA, or OpenFGA. | `spec: {provider, rules}` |
| `PolicyBinding` | Core | Attaches policies to namespaces, repositories, or resource selectors. | `spec: {policyRef, targetRef, selector}` |
| `ServiceAccount` | Core | Non-human principal metadata; secrets and tokens are datastore/secret-manager only. | `spec: {displayName, scopes, ownerRefs}` |
| `ApprovalPolicy` | Extension/CRD | Required approval workflow for sensitive resources or environments. | `spec: {selector, approvers, minApprovals, conditions}` |

Example:

```markdown
---
apiVersion: authz.gitstore.dev/v1beta1
kind: Role
metadata:
  name: catalog-editor
  namespace: acme-store
spec:
  permissionRefs:
  - kind: PermissionSet
    name: catalog-write
---

Catalog editing role.
```

## Integration Configuration

| Resource | Scope | Summary | Initial spec shape |
|---|---|---|---|
| `WebhookEndpoint` | Core | Webhook target and event subscription configuration. | `spec: {url, eventTypes, secretRef, retryPolicy}` |
| `Integration` | Core | External system integration metadata and enabled capabilities. | `spec: {provider, capabilities, config, credentialsRef}` |
| `PaymentGatewayConfig` | Core | Payment provider configuration without customer payment data or credentials. | `spec: {provider, mode, credentialsRef, supportedMethods, capturePolicy}` |
| `NotificationTemplate` | Extension/CRD | Generic notification template for event-driven messages. | `spec: {channel, locale, subjectTemplate, bodyTemplate, variables}` |
| `EmailTemplate` | Extension/CRD | Email-specific template with locale and brand variants. | `spec: {locale, subject, htmlFileRef, textFileRef, variables}` |

Example:

```markdown
---
apiVersion: integrations.gitstore.dev/v1beta1
kind: WebhookEndpoint
metadata:
  name: erp-order-events
  namespace: acme-store
spec:
  url: https://erp.example.com/gitstore/events
  eventTypes:
  - order.created
  retryPolicy:
    maxAttempts: 10
---

ERP webhook subscription.
```
