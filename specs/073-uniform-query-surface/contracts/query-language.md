# Query Language v1 (Proposed Normative Contract)

The server selects language major 1 through its advertised capability. No version argument is added to connections. An incompatible language requires an explicit contract release. This is inspired by AIP-160/GitHub/Shopify; colon terms and pipelines are intentional deviations.

## Grammar

```ebnf
query       = [ expression ], { "|", directive } ;
expression  = conjunction, { "OR", conjunction } ;
conjunction = unary, { [ "AND" ], unary } ;
unary       = [ "NOT" | "-" ], primary ;
primary     = "(", expression, ")" | predicate | string ;
predicate   = field, ":", [ comparison ], value ;
comparison  = "<=" | ">=" | "!=" | "=" | "<" | ">" ;
field       = identifier, { ".", identifier | "[", string, "]" } ;
value       = scalar | "[", scalar, { ",", scalar }, "]" ;
scalar      = string | number | "true" | "false" | "null" | atom ;
directive   = identifier, "(", [ argument, { ",", argument } ], ")" ;
argument    = [ identifier, ":" ], jsonValue ;
jsonValue   = scalar | "[", [ jsonValue, { ",", jsonValue } ], "]"
            | "{", [ string, ":", jsonValue,
                { ",", string, ":", jsonValue } ], "}" ;
```

`identifier` is ASCII `[A-Za-z_][A-Za-z0-9_]*`; `number` follows JSON number syntax. `string` is a JSON double-quoted UTF-8 string, with JSON escapes; invalid UTF-8, invalid escapes and unpaired surrogates fail. `atom` is a nonempty sequence excluding whitespace, control characters and `()[]{}:,|"<>=!`; leading `-` is reserved for negation, so quote negative-looking string values. Quote URLs, IDs containing reserved characters and label keys; label map keys use bracket syntax. Whitespace separates tokens, with no comments. AND/OR/NOT are uppercase reserved words at token boundaries; lowercase values remain literals. Longest token match applies. Unquoted atoms that parse as booleans/null/numbers are typed scalars; quote them to select strings. JSON object keys must be quoted. No executable expressions or arbitrary function calls.

Precedence is parentheses, unary negation, AND (including adjacent predicates), OR. Empty/whitespace query means no additional client restriction. Pipelines occur only after the whole Boolean expression; each directive name appears at most once, and retrieval precedes ordering. Unknown names fail. No nested directives or Boolean retrieval clauses. A bare quoted primary is shorthand for lexical retrieval and must be the sole primary; combine filters using an explicit pipeline instead. Unquoted free text without `field:` is rejected, avoiding accidental unknown-term broadening.

## Canonical terms and directives

| Form | Meaning / constraints |
| --- | --- |
| `metadata.name:"shoe"` | Exact name, never an implicit namespace. |
| `metadata.labels["app.example/tier"]:"gold"` | Exact string equality on one label; canonical owning field path. |
| `metadata.labels["tier"]:["gold","silver"]` | IN; NOT on this predicate implements label NotIn (missing label matches NOT IN). |
| `metadata.labels["tier"]:*` | Exists; `NOT ...:*` means DoesNotExist. Quoted `"*"` is a literal star. |
| `descendantOf:"footwear" includeSelf:true maxDepth:<=2` | Category closure selection; includeSelf defaults false, maxDepth omitted is unbounded within the indexed subtree. Modifiers require exactly one positive descendantOf in a top-level conjunction; OR/NOT placement is rejected. The value is the namespace-local category name from the owning CategoryFilterInput contract; an unknown name gives an empty result. Preserve existing authorization and name-resolution rules; it is not a Relay ID or a new CategoryBy selector. |
| `kind:Product` | Generic search accepts one positive top-level kind term, scalar or IN array, e.g. kind:[Product,CategoryTaxonomy]. Omission selects the registered default kind set. The complete selected set needs a supported common-order/pagination plan; otherwise reject explicitly. No negated/OR kind dispatch in v1. Typed connections may omit kind or provide their exact kind only. |
| `spec.title:lap*` | Prefix instruction only if that field/operator has an eligible registered index. Literal prefix is case-sensitive unless registry declares an explicit analyzer; no implicit substring scan. |
| `\| lexical("red shoes")` | Indexed full-text retrieval; analyzer/index revision is part of the plan. |
| `\| semantic("comfortable shoes")` | Semantic retrieval using configured embedding model/index; never keyword fallback. |
| `\| hybrid("comfortable red shoes")` | Configured lexical/vector fusion with a versioned scoring policy; no caller-defined provider JSON. |
| `\| similarTo("prod_ID")` | Authorized seed-item similarity; registry defines eligible kind/index. |
| `\| recommend(surface:"related", context:{"productId":"prod_ID"})` | Reserved Storefront serving directive; not enabled on Admin. Final syntax must be reviewed against the first Storefront/owning recommendation contract before advertisement. Context must pass that surface's schema/privacy/auth contract; no arbitrary unvalidated context. Unavailable until owning #29 serving/context contract exists. |
| `\| order("metadata.name", "-metadata.creationTimestamp")` | Ascending/descending registered keys. Provider-independent output order with UID tie-breaker. Ranked modes use relevance descending/UID tie-break by default; explicit order allowed only when registered with that retrieval mode. |

At most one of lexical/semantic/hybrid/similarTo/recommend is present. Limits are in [execution](execution.md). Retrieval context JSON is recursively charged to token/node/depth/byte budgets. The parser accepts comparisons syntactically, but registry types/operators decide validity: no implicit numeric/string/date coercion. Dates are quoted RFC3339 values on declared datetime fields. `null` is accepted only on explicitly nullable registered terms; absent and null are distinct. Arrays mean membership only on terms declaring it; empty lists are invalid.

Unquoted `*` is Exists only for label terms; a single trailing unescaped `*` is prefix only on prefix-capable strings. Interior/leading/multiple wildcards are unsupported in v1. Quoting suppresses wildcard meaning. Escaped literal backslashes/quotes retain exact values. Repeated ordinary predicates remain conjunctions; contradictory valid predicates yield no matches if an eligible plan exists. Duplicate singleton modifiers/directives fail rather than last-value-wins.

## Watch subset and contextual meaning

Initial watch registry permits canonical kind, metadata.namespace/name and label predicates with Boolean operations supported by existing durable facts. Namespace terms may narrow an already authorized watch, never authorize cross-namespace access. No spec/status, category hierarchy, ranking, pipeline or time-dependent predicate until complete durable previous/deletion/dependency facts are deliberately implemented. Bookmarks bypass filtering. Blank watch query means all permitted events; blank nested Collection query leaves its owning selector untouched, including empty-selector-means-no-members behavior.

Connection support is narrower for some combinations: labels NOT/DoesNotExist may be watchable yet unservable by an indexed list plan. Discovery distinguishes them. Connection/watch parity is required only for the advertised intersection; no scan to manufacture parity.

## Canonicalization and diagnostics

Normalize typed literals/escapes and explicit AND syntax, preserving string codepoints/case, field identity, grouping and directive/argument meaning. Do not sort arbitrary Boolean nodes, case-fold literals or rewrite provider-dependent expressions. Semantic fingerprints include registry/language versions and ordered sort/retrieval arguments. Equivalent whitespace/escape spelling normalizes identically; merely logically equivalent rearrangements need not share a cursor.

Return the existing `.extensions.code` and `.extensions.diagnostics[]` via `admission.Error`: BAD_USER_INPUT with QUERY_SYNTAX_INVALID, QUERY_TERM_UNSUPPORTED, QUERY_COMBINATION_UNSUPPORTED, QUERY_LIMIT_EXCEEDED or QUERY_CURSOR_INVALID. `field` is `query` (cursor errors use after/before); safe offsets may appear in the message, never the raw private expression. Provider/index/window unavailability uses FAILED_PRECONDITION with QUERY_PROVIDER_UNAVAILABLE, QUERY_INDEX_NOT_READY, QUERY_WINDOW_EXPIRED or QUERY_REVISION_CHANGED. FORBIDDEN remains sanitized without diagnostics. No new extension keys, query Payload, partial term dropping or success-shaped errors.
