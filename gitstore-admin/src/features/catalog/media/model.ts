import type { DemoSession } from '../../demo/model'
import { projectedResources } from '../../demo/model'
import type { DemoResource, Readiness } from '../../home/overview/model'
import type { ItemSnapshot } from '../../demo/snapshots'
import { snapshotTitle } from '../../demo/snapshots'
export type MediaScenario =
  | 'loading'
  | 'unavailable'
  | 'stale'
  | 'read-only'
  | 'many'
  | 'missing-preview'
  | 'failed-preview'
  | 'document'
  | 'ready'
  | 'unused'
  | 'long-name'
export interface MediaSearch {
  q: string
  status: string
  usage: 'all' | 'used' | 'unused'
  sort: 'updated' | 'name'
  view: 'grid' | 'table'
}
export function mediaSearch(raw: Record<string, unknown>): MediaSearch {
  return {
    q: typeof raw.q === 'string' ? raw.q : '',
    status: [
      'attention',
      'ready',
      'failed',
      'blocked',
      'stale',
      'reconciling',
    ].includes(String(raw.status))
      ? String(raw.status)
      : 'all',
    usage: raw.usage === 'used' || raw.usage === 'unused' ? raw.usage : 'all',
    sort: raw.sort === 'name' ? 'name' : 'updated',
    view: raw.view === 'table' ? 'table' : 'grid',
  }
}
export interface MediaDefinition {
  contentType: string
  provider: string
  sourceUri: string
  checksum?: { algorithm: string; value: string }
  requests: readonly string[]
  outputs: readonly { name: string; previewId?: string }[]
  previewId?: string
  body: string
}
/** Presentation fixtures mirror selected File fields, never generated API responses. */
const details: Record<string, MediaDefinition> = {
  'basin-image': {
    contentType: 'image/webp',
    provider: 'demo',
    sourceUri: 'demo://studio/basin-detail.webp',
    checksum: {
      algorithm: 'sha256',
      value: 'd91a82f6b0d88d96c04c46734d919532b1799ec490973747d464c2d890a7b231',
    },
    requests: ['thumbnail', 'storefront'],
    outputs: [],
    previewId: 'basin-image',
    body: 'Studio photography of the ash-glazed basin.\n\nThis is an example **demo preview**.',
  },
  lookbook: {
    contentType: 'image/webp',
    provider: 'demo',
    sourceUri: 'demo://studio/lookbook-hero.webp',
    requests: ['thumbnail', 'storefront'],
    outputs: [{ name: 'thumbnail', previewId: 'lookbook' }],
    previewId: 'lookbook',
    body: 'Editorial photography for the autumn catalog.\n\nProcessing requests and available demo outputs are shown separately.',
  },
}
export interface MediaUsage {
  id: string
  title: string
  kind: 'Product' | 'CategoryTaxonomy' | 'ProductVariant'
}
export function mediaUsage(session: DemoSession, id: string): MediaUsage[] {
  const uses: MediaUsage[] = []
  for (const [key, item] of Object.entries(session.catalog)) {
    if (
      (item.kind === 'Product' && item.spec.mediaIds.includes(id)) ||
      (item.kind === 'CategoryTaxonomy' &&
        item.spec.media?.some((media) => media.fileRef.name === id))
    )
      uses.push({
        id: key,
        title: snapshotTitle(item),
        kind: item.kind as 'Product' | 'CategoryTaxonomy',
      })
  }
  for (const [key, item] of Object.entries(session.variants))
    if (item.spec.mediaIds.includes(id))
      uses.push({ id: key, title: item.spec.title, kind: 'ProductVariant' })
  return uses.sort(
    (a, b) => a.title.localeCompare(b.title) || a.id.localeCompare(b.id),
  )
}
export interface MediaEntry {
  id: string
  name: string
  status: Readiness
  diagnostic: string
  path: string
  updatedAt: string
  snapshot: Extract<ItemSnapshot, { kind: 'File' }>
  details: MediaDefinition
  usage: MediaUsage[]
  broken: boolean
}
export function mediaEntries(
  session: DemoSession,
  scenario?: MediaScenario,
): MediaEntry[] {
  const resources = projectedResources(session)
  const entries = Object.entries(session.catalog).flatMap(([id, snapshot]) => {
    if (snapshot.kind !== 'File') return []
    const resource = resources.find((r) => r.id === id) as
      DemoResource | undefined
    if (!resource) return []
    const definition = details[id] ?? {
      contentType: 'application/octet-stream',
      provider: 'demo',
      sourceUri: `demo://files/${encodeURIComponent(id)}`,
      requests: [],
      outputs: [],
      body: '',
    }
    return [
      {
        id,
        name: snapshot.name,
        status: resource.readiness,
        diagnostic: resource.diagnostic,
        path: resource.path,
        updatedAt:
          [...session.events]
            .filter((e) => e.itemId === id)
            .map((e) => e.timestamp)
            .sort()
            .at(-1) ?? '2026-11-02T09:00:00Z',
        snapshot,
        details: definition,
        usage: mediaUsage(session, id),
        broken: false,
      },
    ]
  })
  if (scenario === 'many' && entries[0])
    for (let i = 1; i <= 13; i++)
      entries.push({
        ...entries[0],
        id: `media-example-${i}`,
        name: `Studio example ${String(i).padStart(2, '0')}.webp`,
        usage: [],
        path: `media/example-${i}.md`,
      })
  return entries.map((entry) =>
    entry.id !== 'basin-image'
      ? entry
      : {
          ...entry,
          ...(scenario === 'ready'
            ? {
                status: 'ready' as const,
                diagnostic: 'No issues in this demo snapshot.',
              }
            : {}),
          ...(scenario === 'unused' ? { usage: [] } : {}),
          ...(scenario === 'long-name'
            ? {
                name: 'autumn-studio-editorial-homeware-collection-photography-retouched-final-large-format.webp',
              }
            : {}),
          broken: scenario === 'failed-preview',
          details: {
            ...entry.details,
            ...(scenario === 'missing-preview' ? { previewId: undefined } : {}),
            ...(scenario === 'document'
              ? {
                  previewId: undefined,
                  contentType: 'application/pdf',
                  requests: [],
                  outputs: [],
                }
              : {}),
          },
        },
  )
}
export function filterMedia(
  entries: readonly MediaEntry[],
  search: MediaSearch,
) {
  return entries
    .filter(
      (item) =>
        item.name
          .toLocaleLowerCase()
          .includes(search.q.trim().toLocaleLowerCase()) &&
        (search.status === 'all' ||
          (search.status === 'attention'
            ? ['failed', 'blocked', 'stale'].includes(item.status)
            : item.status === search.status)) &&
        (search.usage === 'all' ||
          (search.usage === 'used'
            ? item.usage.length > 0
            : item.usage.length === 0)),
    )
    .toSorted(
      (a, b) =>
        (search.sort === 'name'
          ? a.name.localeCompare(b.name)
          : b.updatedAt.localeCompare(a.updatedAt)) || a.id.localeCompare(b.id),
    )
}
export function mediaConnection(
  entries: readonly MediaEntry[],
  after?: string,
  limit = 6,
) {
  const start = after
    ? entries.findIndex(
        (item) => `media:${encodeURIComponent(item.id)}` === after,
      ) + 1
    : 0
  if (after && !start)
    return { edges: [], pageInfo: { hasNextPage: false, endCursor: undefined } }
  const nodes = entries.slice(start, start + limit)
  return {
    edges: nodes.map((node) => ({
      node,
      cursor: `media:${encodeURIComponent(node.id)}`,
    })),
    pageInfo: {
      hasNextPage: start + nodes.length < entries.length,
      endCursor: nodes.length
        ? `media:${encodeURIComponent(nodes.at(-1)!.id)}`
        : undefined,
    },
  }
}

export function mediaFormat(contentType: string) {
  return (
    (
      {
        'image/webp': 'WebP image',
        'image/jpeg': 'JPEG image',
        'image/png': 'PNG image',
        'application/pdf': 'PDF document',
      } as Record<string, string>
    )[contentType] ?? (contentType.startsWith('image/') ? 'Image' : 'File')
  )
}
