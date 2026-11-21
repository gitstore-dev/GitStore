# Shared Search Capability Projection (Proposed Extension to Spec 072)

Owner remains spec 072 / PR #592's public `GET /.well-known/gitstore` document. Its current proposed search entry is `{kind,supportedTerms}`; the richer fields below are **new work in this plan**. Update that owning schema/model/tests together when integrating branches; never maintain a competing public type here. No extra endpoint, capability resource, runtime metadata envelope or per-vendor discovery API.

Add optional fields to each `search[]` entry and increment discovery schema minor version (from its proposed 1.0.0 to 1.1.0 if no intervening version). Existing kind/supportedTerms remain the conservative compatible projection: terms supported by at least one ready connection/search plan; clients needing combinations/watches must read operation descriptors. Absence of new descriptors never proves watch or ranking support.

```json
{
  "kind": "Product",
  "supportedTerms": ["metadata.name", "metadata.labels"],
  "language": "gitstore-query@1",
  "operations": [
    {
      "name": "connection",
      "endpointId": "admin",
      "availability": "AVAILABLE",
      "plans": [
        {
          "id": "product-label-equality-v1",
          "terms": [{"field": "metadata.labels", "operators": ["EQ", "EXISTS", "IN"], "valueType": "STRING"}],
          "retrievalModes": [],
          "orders": ["metadata.creationTimestamp:DESC,metadata.uid:DESC"],
          "combination": "ONE_LABEL_KEY",
          "pagination": "KEYSET",
          "scope": "NAMESPACE",
          "maxInValues": 8
        }
      ]
    },
    {
      "name": "watch",
      "endpointId": "admin-subscriptions",
      "availability": "AVAILABLE",
      "plans": [{"id": "product-journal-labels-v1", "terms": [{"field": "metadata.labels", "operators": ["EQ", "EXISTS", "IN", "NOT"], "valueType": "STRING"}], "combination": "BOUNDED_BOOLEAN", "pagination": "JOURNAL", "scope": "AUTHORIZED_WATCH"}]
    }
  ],
  "limits": {"maxQueryBytes": 8192, "maxPageSize": 100, "maxRankedWindow": 1000, "maxWindowSeconds": 600}
}
```

The example is a proposed fixture, not implemented support. Operation names are `connection`, `search`, `watch`, qualified by an endpointId referencing the same document's owning endpoint. API surface follows that endpoint, not a separate inferred vendor flag. Admin management search and Storefront public search may expose different terms/kinds/modes from one provider. Recommendation serving is advertised only against an implemented Storefront endpoint; management journal watch stays Admin. Legacy supportedTerms alone never proves endpoint support. Generic search plans additionally declare supported SearchItem resource kinds, allowed kind combinations, common ordering/scoring revision and whether multi-kind paging is supported through the single SearchItemConnection. A union of advertised kinds is not proof that they can be combined. Provider-backed plans use the **same descriptor source** as advanced typed connections; they do not get separate server registration. `retrievalModes` allow LEXICAL/SEMANTIC/HYBRID/SIMILARITY/RECOMMENDATION only when their owning mode is ready. Plan `combination` is a finite versioned enum with executable validators, not arbitrary descriptive text; additions require consumer-safe validation. Additional initial combination IDs are NO_PREDICATES, ONE_NAME, CATEGORY_SUBTREE and PROVIDER_FILTERED, each defined by the execution registry and conformance corpus. Never infer supported conjunctions from independent term lists.

Optional unavailable operations use `availability: UNAVAILABLE` and a safe finite reason (PROVIDER_UNAVAILABLE, INDEX_NOT_READY, REVISION_CHANGED); omit runtime plan data that could leak private setup. Anonymous discovery advertises potential implementation capability, not caller authorization or existence of a private surface/seed. Do not expose private namespace/subject lists, provider URLs, credentials, recommendation schemas containing secrets or raw index metadata.

Generate projection from source registry and validated readiness; include additions in spec 072 canonical digest/ETag, expiry/recompute and publication-floor validation. New floors may require `query.language@1` or a registered operation-plan ID and only validate against implemented local plans. Keep existing bounds <=64 kinds/64 supported terms per kind; add <=3 operation descriptors/endpoint and <=8 descriptors/kind, <=8 plans/operation, <=16 term descriptors/plan and <=256 KiB complete discovery representation. Reject oversized publication rather than truncate; new limits must be implemented in the same owner and reflected in its data model. Routine readiness changes invalidate publication caches under spec 072 rules. Old discovery code cannot enforce new floors; cutover still drains incompatible recipients.

Conformance tests compare registry compilation with published plans, simulate provider/index readiness loss and reject inflated vendor flags, missing combination validators and stale revisions. Avoid copying this example into an independently edited map.
