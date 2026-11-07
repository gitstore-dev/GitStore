import type { ItemSnapshot } from '../../demo/snapshots'
import type { DemoSession, ActivityEvent } from '../../demo/model'
import { sameSnapshot } from '../../demo/snapshots'
import { demoUser } from '../../../data/demo'
import type { Readiness } from '../../home/overview/model'
export type Product = Extract<ItemSnapshot, { kind: 'Product' }>
export interface ProductBuffer {
  value: Product
  base: Product | null
}
export type ProductAction =
  | { type: 'product-buffer'; id: string; buffer: ProductBuffer | null }
  | { type: 'product-save'; id: string; value: Product; base: Product | null }
  | { type: 'product-delete' | 'product-complete-delete'; id: string }
export const emptyProduct = (): Product => ({
  kind: 'Product',
  body: '',
  spec: {
    title: '',
    categoryId: null,
    mediaIds: [],
    tags: [],
    options: [],
    lifecycle: { state: 'ACTIVE' },
  },
})
export function productProblem(
  value: Product,
  session: DemoSession,
): string | null {
  if (!value.spec.title.trim() || value.spec.title.trim().length > 200)
    return 'Enter a Title between 1 and 200 characters.'
  if (
    value.spec.categoryId &&
    session.catalog[value.spec.categoryId]?.kind !== 'CategoryTaxonomy'
  )
    return 'Choose an available category.'
  if (
    new Set(value.spec.mediaIds).size !== value.spec.mediaIds.length ||
    value.spec.mediaIds.some((id) => session.catalog[id]?.kind !== 'File')
  )
    return 'Choose available media without duplicates.'
  const names = new Set<string>()
  for (const option of value.spec.options) {
    if (
      !option.name.trim() ||
      names.has(option.name.trim()) ||
      !option.values.length ||
      option.values.some((v) => !v.trim()) ||
      new Set(option.values).size !== option.values.length
    )
      return 'Options need unique names and nonblank, unique values.'
    names.add(option.name.trim())
  }
  return null
}
export function productAt(
  session: DemoSession,
  id: string,
): Product | undefined {
  const item = session.catalog[id]
  return item?.kind === 'Product' ? item : undefined
}
export function deletionBlockers(session: DemoSession, id: string) {
  return session.resources.filter(
    (resource) =>
      resource.kind === 'ProductVariant' &&
      session.variantOwners[resource.id] === id,
  )
}
export function nextProductId(session: DemoSession, title: string) {
  const base =
    title
      .toLowerCase()
      .normalize('NFKD')
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-|-$/g, '')
      .slice(0, 100) || 'product'
  const used = new Set([
    'new', // Reserved create route.
    ...Object.keys(session.catalog),
    ...session.resources.map((resource) => resource.id),
    ...session.events.map((e) => e.itemId),
  ])
  let id = base
  for (let i = 2; used.has(id); i++) id = `${base}-${i}`
  return id
}
export function staleBuffer(
  session: DemoSession,
  id: string,
  buffer: ProductBuffer,
) {
  const current = productAt(session, id)
  return (
    buffer.base !== null && (!current || !sameSnapshot(buffer.base, current))
  )
}
export function reduceProduct(
  session: DemoSession,
  action: ProductAction,
): DemoSession {
  if (action.type === 'product-buffer') {
    const productBuffers = { ...session.productBuffers }
    if (action.buffer) productBuffers[action.id] = action.buffer
    else delete productBuffers[action.id]
    return { ...session, productBuffers }
  }
  const before = productAt(session, action.id)
  const deleting = session.deleting.includes(action.id)
  const revision = session.revision + 1
  const timestamp = new Date(
    Date.UTC(2026, 10, 2, 12, 0, revision),
  ).toISOString()
  const common = {
    id: `demo-${session.namespace}-${revision}`,
    itemId: action.id,
    kind: 'Product' as const,
    actor: demoUser.name,
    timestamp,
    category: 'catalog' as const,
    local: true,
  }
  if (action.type === 'product-save') {
    if (
      deleting ||
      productProblem(action.value, session) ||
      (action.base
        ? !before || !sameSnapshot(action.base, before)
        : !!before ||
          session.resources.some((resource) => resource.id === action.id) ||
          session.events.some((e) => e.itemId === action.id))
    )
      return session
    const value: Product = {
      ...action.value,
      spec: { ...action.value.spec, title: action.value.spec.title.trim() },
    }
    if (before && sameSnapshot(before, value)) return session
    const productBuffers = { ...session.productBuffers }
    delete productBuffers[action.base ? action.id : 'new']
    const event: ActivityEvent = {
      ...common,
      itemTitle: value.spec.title,
      label: !before
        ? 'Product created'
        : before.spec.lifecycle.state !== value.spec.lifecycle.state
          ? value.spec.lifecycle.state === 'RETIRED'
            ? 'Product retired'
            : 'Product reactivated'
          : 'Product updated',
      description:
        'Saved in this demo only. Processing has not completed; nothing was published.',
      before,
      after: value,
    }
    const resource = {
      id: action.id,
      kind: 'Product' as const,
      spec: { title: value.spec.title },
      readiness: 'reconciling' as const,
      diagnostic:
        'Your demo changes are being processed. Nothing was published.',
      path:
        session.resources.find((r) => r.id === action.id)?.path ??
        `catalog/products/${action.id}.md`,
      references: [],
    }
    return {
      ...session,
      revision,
      catalog: { ...session.catalog, [action.id]: value },
      productBuffers,
      resources: before
        ? session.resources.map((r) => (r.id === action.id ? resource : r))
        : [...session.resources, resource],
      events: [event, ...session.events],
      productUpdated: { ...session.productUpdated, [action.id]: timestamp },
    }
  }
  if (!before || deletionBlockers(session, action.id).length) return session
  if (action.type === 'product-delete') {
    if (deleting) return session
    const event: ActivityEvent = {
      ...common,
      itemTitle: before.spec.title,
      label: 'Product deletion requested',
      description:
        'Deletion is in progress in this demo. Complete demo deletion to remove the item.',
      changes: [{ label: 'Deletion', before: 'Available', after: 'Deleting' }],
    }
    return {
      ...session,
      revision,
      deleting: [...session.deleting, action.id],
      resources: session.resources.map((r) =>
        r.id === action.id
          ? {
              ...r,
              readiness: 'reconciling',
              diagnostic:
                'Deletion is in progress. Open this product to complete demo deletion.',
            }
          : r,
      ),
      events: [event, ...session.events],
    }
  }
  if (!deleting) return session
  const catalog = { ...session.catalog }
  delete catalog[action.id]
  const event: ActivityEvent = {
    ...common,
    itemTitle: before.spec.title,
    label: 'Product deleted',
    description:
      'Removed from this demo catalog. History and personal work are preserved.',
    before,
  }
  return {
    ...session,
    revision,
    catalog,
    deleting: session.deleting.filter((id) => id !== action.id),
    resources: session.resources.filter((r) => r.id !== action.id),
    events: [event, ...session.events],
  }
}
export interface ProductSearch {
  q: string
  status: string
  lifecycle: string
  category: string
  sort: 'title' | 'updated'
  view: 'grid' | 'table'
}
export function productSearch(raw: Record<string, unknown>): ProductSearch {
  return {
    q: typeof raw.q === 'string' ? raw.q.slice(0, 200) : '',
    status: [
      'ready',
      'reconciling',
      'failed',
      'blocked',
      'stale',
      'attention',
      'deleting',
    ].includes(String(raw.status))
      ? String(raw.status)
      : 'all',
    lifecycle: ['ACTIVE', 'RETIRED'].includes(String(raw.lifecycle))
      ? String(raw.lifecycle)
      : 'all',
    category: typeof raw.category === 'string' ? raw.category : 'all',
    sort: raw.sort === 'title' ? 'title' : 'updated',
    view: raw.view === 'grid' ? 'grid' : 'table',
  }
}
export function productList(session: DemoSession, search: ProductSearch) {
  return Object.entries(session.catalog)
    .filter((entry): entry is [string, Product] => entry[1].kind === 'Product')
    .filter(([id, p]) => {
      const status: Readiness =
        session.resources.find((r) => r.id === id)?.readiness ?? 'reconciling'
      return (
        p.spec.title.toLowerCase().includes(search.q.trim().toLowerCase()) &&
        (search.status === 'all' ||
          (search.status === 'deleting'
            ? session.deleting.includes(id)
            : search.status === 'attention'
              ? ['failed', 'blocked', 'stale'].includes(status)
              : status === search.status)) &&
        (search.lifecycle === 'all' ||
          p.spec.lifecycle.state === search.lifecycle) &&
        (search.category === 'all' || p.spec.categoryId === search.category)
      )
    })
    .toSorted(
      ([a, pa], [b, pb]) =>
        (search.sort === 'title'
          ? pa.spec.title.localeCompare(pb.spec.title, 'en')
          : (session.productUpdated[b] ?? '').localeCompare(
              session.productUpdated[a] ?? '',
            )) || a.localeCompare(b),
    )
}
/** Local connection fixture; opaque tokens must be passed through, never decoded by the UI. */
export function productConnection(
  session: DemoSession,
  search: ProductSearch,
  {
    size = 8,
    after = '',
    before = '',
  }: { size?: number; after?: string; before?: string } = {},
) {
  const all = productList(session, search).map((node) => ({
    node,
    cursor: `demo-product:${node[0]}`,
  }))
  const position = after || before
  const index = position
    ? all.findIndex((edge) => edge.cursor === position)
    : -1
  const invalidCursor = !!position && index === -1
  const start = after ? index + 1 : before ? Math.max(0, index - size) : 0
  const end = before ? index : Math.min(all.length, start + size)
  const edges = invalidCursor ? [] : all.slice(start, end)
  return {
    edges,
    invalidCursor,
    pageInfo: {
      startCursor: edges[0]?.cursor ?? null,
      endCursor: edges.at(-1)?.cursor ?? null,
      hasPreviousPage: !invalidCursor && start > 0,
      hasNextPage: !invalidCursor && end < all.length,
    },
  }
}
export function reorderMedia(
  ids: readonly string[],
  index: number,
  direction: -1 | 1,
) {
  const next = [...ids]
  const target = index + direction
  if (target < 0 || target >= ids.length) return next
  ;[next[index], next[target]] = [next[target]!, next[index]!]
  return next
}
