# GitStore Resource Schemas (v1beta1)

JSON Schema (2020-12) definitions for the YAML frontmatter of GitStore
resource manifests. These schemas exist primarily so that **LLMs and
developers** can reliably author valid resource manifests without needing to
read the Go source — see the top-level `README.md` for GitStore's stated
target users.

## Scope: frontmatter only

Every GitStore resource is a Markdown file: a YAML frontmatter block
(delimited by `---`) followed by a free-form Markdown body (the resource's
"description"). **These schemas validate only the frontmatter object** — the
Markdown body below it is not modeled here, since the Markdown parser/IR for
that body is not yet implemented for most resource kinds.

A minimal manifest looks like:

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: my-product
spec:
  title: My Product
---

# My Product

Free-form Markdown description goes here.
```

Only the YAML object between the two `---` lines is validated against the
corresponding schema in this directory.

## Files

| Schema | Kind | apiVersion | Git-pushable today? |
|---|---|---|---|
| `product.schema.json` | `Product` | `catalog.gitstore.dev/v1beta1` | Yes |
| `variant.schema.json` | `ProductVariant` | `catalog.gitstore.dev/v1beta1` | Yes |
| `category.schema.json` | `CategoryTaxonomy` | `catalog.gitstore.dev/v1beta1` | Yes |
| `collection.schema.json` | `Collection` | `catalog.gitstore.dev/v1beta1` | Yes |
| `file.schema.json` | `File` | `storage.gitstore.dev/v1beta1` | Yes |
| `namespace.schema.json` | `Namespace` | `gitstore.dev/v1beta1` | Yes |
| `repository.schema.json` | `Repository` | `gitstore.dev/v1beta1` | **No** — see below |

Each schema is self-contained (no cross-file `$ref`s) so a single file gives
an LLM or IDE everything it needs, at the cost of duplicating shared
definitions (`ObjectMeta`, `ObjectReference`, `FileReference`,
`MediaDefinition`, etc.) across files. All schemas use `additionalProperties:
false` throughout, so unknown/system-managed fields (e.g. `status`, `uid`,
`resourceVersion`) are rejected — matching the real admission contract.

### Repository is a special case

Unlike the other six kinds, `Repository` resources are **not** currently
created by pushing a manifest through `git push` — the server's
frontmatter parser has no case for `kind: Repository` yet. Repositories are
created via the `createRepository` GraphQL mutation. `repository.schema.json`
documents the shape of a Repository as read back from the API today, and
anticipates a possible future declarative/git-authored form. Don't expect a
file matching this schema to be accepted by `git push`.

## Ground truth

These schemas are derived directly from the Go struct validation tags in
`gitstore-api/internal/catalog/*.go` and the business-rule validators in
`gitstore-api/internal/validate/validator.go` — not from prose documentation,
which occasionally describes aspirational fields not yet implemented (for
example, `docs/namespace/namespace-spec.md` describes several
`pushPolicyDefaults` sub-fields that don't exist on the actual
`NamespacePushPolicyDefaults` struct today). If code and docs ever diverge,
treat the code as authoritative and file an issue to update the docs.

## Editor/tooling caveat

Core [`yaml-language-server`](https://github.com/redhat-developer/yaml-language-server)
(used by most YAML-aware editor extensions) validates plain `.yaml` files
against a `$schema`/`yaml.schemas` mapping, but it does **not** natively
validate YAML frontmatter embedded inside `.md` files. To get live validation
while authoring GitStore manifests in Markdown, use a frontmatter-aware tool
such as [`remark-lint-frontmatter-schema`](https://github.com/JulianCataldo/remark-lint-frontmatter-schema)
or a `validate-md`-style linter that extracts the frontmatter block and
validates it against the appropriate schema file in this directory.

## Validating manifests locally

```bash
pip install jsonschema pyyaml
python3 - <<'EOF'
import json, re, sys, yaml
from jsonschema import Draft202012Validator

path = sys.argv[1] if len(sys.argv) > 1 else "example.md"
raw = open(path).read()
frontmatter = re.match(r"^---\n(.*?)\n---\n", raw, re.S).group(1)
doc = yaml.safe_load(frontmatter)

schema = json.load(open(f"schemas/gitstore/v1beta1/{doc['kind'].lower()}.schema.json"))
Draft202012Validator(schema).validate(doc)
print("valid")
EOF
```

(Adjust the schema filename lookup for `ProductVariant` → `variant.schema.json`
and `CategoryTaxonomy` → `category.schema.json`, since filenames are
shortened relative to `kind`.)
