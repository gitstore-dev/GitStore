import type { DemoSession } from '../../demo/model'
import type { LabelSelector } from '../../demo/snapshots'
import { snapshotTitle } from '../../demo/snapshots'
import type { DemoResource, Readiness } from '../../home/overview/model'
export type CatalogDomain = 'variants' | 'categories' | 'collections'
export type CatalogScenario =
  | 'many'
  | 'loading'
  | 'unavailable'
  | 'stale'
  | 'missing-parent'
  | 'cycle'
  | 'missing-member'
export const catalogKinds = {
  variants: 'ProductVariant',
  categories: 'CategoryTaxonomy',
  collections: 'Collection',
} as const
export const catalogLabels = {
  variants: 'Variants',
  categories: 'Categories',
  collections: 'Collections',
} as const
export interface VariantFixture {
  body: string
  spec: {
    title: string
    sku: string
    productId: string
    selectedOptions: readonly { name: string; value: string }[]
    mediaIds: readonly string[]
    pricing: { currencyCode: string; amount: string } | null
    inventory: { managed: boolean; policy: 'deny' | 'backorder' }
  }
}
export function seedVariants(
  resources: readonly DemoResource[],
): Readonly<Record<string, VariantFixture>> {
  return Object.fromEntries(
    resources
      .filter((r) => r.kind === 'ProductVariant')
      .map((r) => [
        r.id,
        {
          body: 'A sellable option in this demo catalog. Pricing and inventory are authored declarations, not live checkout prices or stock balances.',
          spec: {
            title: 'spec' in r ? r.spec.title : r.id,
            sku:
              r.id === 'tea-kit-natural'
                ? 'TEA-NAT'
                : r.id === 'tunic-black-m'
                  ? 'TUNIC-BLK-M'
                  : 'TUNIC-NAT-M',
            productId: r.id === 'tea-kit-natural' ? 'tea-ritual' : 'silk-tunic',
            selectedOptions:
              r.id === 'tea-kit-natural'
                ? []
                : [
                    {
                      name: 'color',
                      value: r.id === 'tunic-black-m' ? 'Black' : 'Natural',
                    },
                    { name: 'size', value: 'M' },
                  ],
            mediaIds: [],
            pricing: {
              currencyCode: 'EUR',
              amount: r.id === 'tea-kit-natural' ? '48.00' : '120.00',
            },
            inventory: { managed: true, policy: 'deny' as const },
          },
        },
      ]),
  )
}
export function matchesLabels(
  labels: Readonly<Record<string, string>>,
  selector?: LabelSelector,
): boolean {
  if (
    !selector ||
    (!Object.keys(selector.matchLabels ?? {}).length &&
      !selector.matchExpressions?.length)
  )
    return false
  return (
    Object.entries(selector.matchLabels ?? {}).every(
      ([key, value]) => labels[key] === value,
    ) &&
    (selector.matchExpressions ?? []).every((rule) => {
      const present = Object.hasOwn(labels, rule.key)
      switch (rule.operator) {
        case 'In':
          return present && !!rule.values?.includes(labels[rule.key]!)
        case 'NotIn':
          return !present || !rule.values?.includes(labels[rule.key]!)
        case 'Exists':
          return present
        case 'DoesNotExist':
          return !present
      }
    })
  )
}
export function matchingProducts(
  session: Pick<DemoSession, 'catalog' | 'deleting'>,
  selector?: LabelSelector,
) {
  return Object.entries(session.catalog)
    .filter(
      ([id, item]) =>
        item.kind === 'Product' &&
        !session.deleting.includes(id) &&
        matchesLabels(item.metadata?.labels ?? {}, selector),
    )
    .map(([id]) => id)
    .toSorted()
}
export interface CatalogSearch {
  q: string
  status: string
  related: string
  sort: 'title' | 'updated'
  view: 'table' | 'grid' | 'tree' | 'spatial'
  selected?: string
}
export function catalogSearch(
  raw: Record<string, unknown>,
  domain: CatalogDomain,
): CatalogSearch {
  const allowed =
    domain === 'categories' ? ['table', 'tree', 'spatial'] : ['table', 'grid']
  return {
    q: typeof raw.q === 'string' ? raw.q : '',
    status: [
      'ready',
      'failed',
      'blocked',
      'stale',
      'reconciling',
      'attention',
    ].includes(String(raw.status))
      ? String(raw.status)
      : 'all',
    related: typeof raw.related === 'string' ? raw.related : 'all',
    sort: raw.sort === 'title' ? 'title' : 'updated',
    view: allowed.includes(String(raw.view))
      ? (raw.view as CatalogSearch['view'])
      : domain === 'categories'
        ? 'tree'
        : 'table',
    ...(domain === 'categories' && typeof raw.selected === 'string'
      ? { selected: raw.selected }
      : {}),
  }
}
export interface CatalogEntry {
  id: string
  title: string
  body: string
  kind: (typeof catalogKinds)[CatalogDomain]
  resource: DemoResource
  status: Readiness
}
export function catalogEntries(
  session: DemoSession,
  domain: CatalogDomain,
): CatalogEntry[] {
  return session.resources
    .filter((r) => r.kind === catalogKinds[domain])
    .flatMap((resource) => {
      const item = session.catalog[resource.id]
      const variant = session.variants[resource.id]
      const value = domain === 'variants' ? variant : item
      if (!value || !('spec' in value)) return []
      return [
        {
          id: resource.id,
          title: value.spec.title,
          body: 'body' in value ? value.body : '',
          kind: catalogKinds[domain],
          resource,
          status: resource.readiness,
        },
      ]
    })
}
export function filteredEntries(
  session: DemoSession,
  domain: CatalogDomain,
  search: CatalogSearch,
) {
  return catalogEntries(session, domain)
    .filter((item) => {
      const query =
        `${item.title} ${domain === 'variants' ? (session.variants[item.id]?.spec.sku ?? '') : ''}`.toLowerCase()
      if (!query.includes(search.q.trim().toLowerCase())) return false
      if (
        search.status === 'attention'
          ? !['failed', 'blocked', 'stale'].includes(item.status)
          : search.status !== 'all' && search.status !== item.status
      )
        return false
      if (search.related !== 'all') {
        if (
          domain === 'variants' &&
          session.variants[item.id]?.spec.productId !== search.related
        )
          return false
        if (domain === 'categories') {
          const category = session.catalog[item.id]
          if (
            category?.kind !== 'CategoryTaxonomy' ||
            (search.related === 'root'
              ? category.spec.parentId !== null
              : category.spec.parentId !== search.related)
          )
            return false
        }
        if (domain === 'collections') {
          const collection = session.catalog[item.id]
          if (
            collection?.kind !== 'Collection' ||
            (search.related === 'rules') !==
              !!(
                Object.keys(collection.spec.selector?.matchLabels ?? {})
                  .length || collection.spec.selector?.matchExpressions?.length
              )
          )
            return false
        }
      }
      return true
    })
    .toSorted(
      (a, b) =>
        (search.sort === 'updated'
          ? (
              session.productUpdated[b.id] ?? '2026-11-02T11:00:00Z'
            ).localeCompare(
              session.productUpdated[a.id] ?? '2026-11-02T11:00:00Z',
            )
          : 0) ||
        a.title.localeCompare(b.title) ||
        a.id.localeCompare(b.id),
    )
}
export function catalogConnection(
  entries: readonly CatalogEntry[],
  after?: string,
) {
  const index = after
    ? entries.findIndex((item) => `demo:${item.id}` === after) + 1
    : 0
  const page = entries.slice(index, index + 6)
  return {
    edges: page.map((node) => ({ node, cursor: `demo:${node.id}` })),
    pageInfo: {
      hasNextPage: index + page.length < entries.length,
      endCursor: page.length ? `demo:${page.at(-1)!.id}` : undefined,
    },
  }
}
export function categoryHierarchy(
  session: DemoSession,
  entries = catalogEntries(session, 'categories'),
) {
  const map = new Map(entries.map((item) => [item.id, item]))
  const seen = new Set<string>()
  const rows: { item: CatalogEntry; depth: number; problem?: string }[] = []
  const parent = (id: string) => {
    const item = session.catalog[id]
    return item?.kind === 'CategoryTaxonomy' ? item.spec.parentId : null
  }
  const visit = (item: CatalogEntry, depth: number, problem?: string) => {
    if (seen.has(item.id)) return
    seen.add(item.id)
    rows.push({ item, depth, problem })
    entries
      .filter((child) => parent(child.id) === item.id)
      .forEach((child) => visit(child, depth + 1, problem))
  }
  entries
    .filter((item) => !parent(item.id) || !map.has(parent(item.id)!))
    .forEach((item) =>
      visit(
        item,
        0,
        parent(item.id) &&
          session.catalog[parent(item.id)!]?.kind !== 'CategoryTaxonomy'
          ? 'Parent category unavailable'
          : undefined,
      ),
    )
  entries
    .filter((item) => !seen.has(item.id))
    .forEach((item) => visit(item, 0, 'Category hierarchy contains a cycle'))
  return rows
}
export function relatedProductTitle(session: DemoSession, id: string) {
  const item = session.catalog[id]
  return item?.kind === 'Product' ? snapshotTitle(item) : 'Product unavailable'
}

export const catalogPaths = {
  variants: '/w/$namespace/catalog/variants',
  categories: '/w/$namespace/catalog/categories',
  collections: '/w/$namespace/catalog/collections',
} as const
export const catalogDetailPaths = {
  variants: '/w/$namespace/catalog/variants/$itemId',
  categories: '/w/$namespace/catalog/categories/$itemId',
  collections: '/w/$namespace/catalog/collections/$itemId',
} as const

export function categoryPath(
  session: Pick<DemoSession, 'catalog'>,
  id: string,
): string[] {
  const path: string[] = []
  const seen = new Set<string>()
  let next: string | null = id
  while (next && !seen.has(next)) {
    const item: DemoSession['catalog'][string] | undefined =
      session.catalog[next]
    if (item?.kind !== 'CategoryTaxonomy') break
    seen.add(next)
    path.unshift(next)
    next = item.spec.parentId
  }
  return path
}
