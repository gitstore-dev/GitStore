import { initialSetupReviews } from '../home/setup-guide/model'
import type {
  SetupReviews,
  SetupReviewId,
  SetupScenario,
} from '../home/setup-guide/model'
import { reduceProduct } from '../catalog/products/model'
import type { ProductBuffer, ProductAction } from '../catalog/products/model'
import type { NamespaceId } from '../../data/demo'
import { demoUser } from '../../data/demo'
import { overviewFixture } from '../home/overview/fixtures'
import type {
  DemoResource,
  OverviewScenario,
  ResourceKind,
} from '../home/overview/model'
import { resourceLabel } from '../home/overview/model'
import { workFixture } from '../home/my-work/fixtures'
import { initialWork, workReducer } from '../home/my-work/model'
import type {
  ProductText,
  WorkAction,
  WorkItem,
  WorkScenario,
  WorkState,
} from '../home/my-work/model'
import { recoveryProblem, seedCatalog, snapshotTitle } from './snapshots'
import type { FieldChange, ItemSnapshot } from './snapshots'
export const activityCategories = [
  'catalog',
  'draft',
  'review',
  'processing',
  'recovery',
] as const
export type ActivityCategory = (typeof activityCategories)[number]
export const categoryLabels: Record<ActivityCategory, string> = {
  catalog: 'Catalog changes',
  draft: 'Draft activity',
  review: 'Reviews',
  processing: 'Processing',
  recovery: 'Recovery',
}
export type ActivityScenario =
  | 'populated'
  | 'empty'
  | 'loading'
  | 'unavailable'
  | 'stale'
  | 'restricted'
  | 'recovery-error'
export interface ActivityEvent {
  id: string
  itemId: string
  kind: ResourceKind
  itemTitle: string
  label: string
  description: string
  actor: string
  timestamp: string
  category: ActivityCategory
  local: boolean
  before?: ItemSnapshot
  after?: ItemSnapshot
  changes?: readonly FieldChange[]
  sourceEventId?: string
}
export type ProductScenario =
  'unsupported' | 'stale-edit' | 'deleting' | 'deleted'
export interface DemoSeed {
  setupScenario?: SetupScenario
  productScenario?: ProductScenario
  overviewScenario?: OverviewScenario
  myWorkScenario?: WorkScenario
  activityScenario?: ActivityScenario
}
export interface DemoSession {
  setupReviews: SetupReviews
  namespace: NamespaceId
  catalog: Readonly<Record<string, ItemSnapshot>>
  resources: readonly DemoResource[]
  work: WorkState
  buffers: Readonly<Record<string, ProductText>>
  events: readonly ActivityEvent[]
  revision: number
  epoch: number
  productBuffers: Readonly<Record<string, ProductBuffer>>
  productUpdated: Readonly<Record<string, string>>
  deleting: readonly string[]
  variantOwners: Readonly<Record<string, string>>
}
export type HomeAction =
  | { type: 'setup-review'; step: SetupReviewId; reviewed: boolean }
  | ProductAction
  | { type: 'resolve'; id: string }
  | { type: 'work'; action: Exclude<WorkAction, { type: 'reset' }> }
  | { type: 'buffer'; id: string; text: ProductText | null }
  | { type: 'restore'; eventId: string }
export interface ActivityFilters {
  query: string
  actor: string
  kind: string
  category: string
  date: string
}
export const emptyFilters: ActivityFilters = {
  query: '',
  actor: 'all',
  kind: 'all',
  category: 'all',
  date: 'all',
}
export const eventDate = (event: ActivityEvent) => event.timestamp.slice(0, 10)
export function filterActivity(
  events: readonly ActivityEvent[],
  filters: ActivityFilters,
) {
  const query = filters.query.trim().toLocaleLowerCase('en')
  return events
    .filter(
      (event) =>
        event.itemTitle.toLocaleLowerCase('en').includes(query) &&
        (filters.actor === 'all' || event.actor === filters.actor) &&
        (filters.kind === 'all' || event.kind === filters.kind) &&
        (filters.category === 'all' || event.category === filters.category) &&
        (filters.date === 'all' || eventDate(event) === filters.date),
    )
    .toSorted(
      (a, b) =>
        b.timestamp.localeCompare(a.timestamp) || b.id.localeCompare(a.id),
    )
}
export function groupActivity(events: readonly ActivityEvent[]) {
  const groups = new Map<string, ActivityEvent[]>()
  for (const event of events) {
    const day = eventDate(event)
    groups.set(day, [...(groups.get(day) ?? []), event])
  }
  return [...groups].map(([date, entries]) => ({ date, entries }))
}
function historical(snapshot: ItemSnapshot, stage: number): ItemSnapshot {
  if (snapshot.kind === 'File')
    return {
      ...snapshot,
      name: stage === 0 ? `Original ${snapshot.name}` : snapshot.name,
      altText: stage === 0 ? '' : snapshot.altText,
      caption: 'Earlier studio photography notes.',
    }
  const spec = {
    ...snapshot.spec,
    title:
      stage === 0 ? `${snapshot.spec.title} — Original` : snapshot.spec.title,
  }
  if (snapshot.kind === 'Product')
    return {
      kind: 'Product',
      body: 'Earlier collection notes, before the latest update.',
      spec: { ...snapshot.spec, ...spec, categoryId: null, mediaIds: [] },
    }
  if (snapshot.kind === 'CategoryTaxonomy')
    return {
      kind: 'CategoryTaxonomy',
      body: 'Earlier collection notes, before the latest update.',
      spec: { ...spec, parentId: null },
    }
  return {
    kind: 'Collection',
    body: 'Earlier collection notes, before the latest update.',
    spec: { ...spec, productIds: [...snapshot.spec.productIds].reverse() },
  }
}
export function seedEvents(
  catalog: Readonly<Record<string, ItemSnapshot>>,
): ActivityEvent[] {
  return Object.entries(catalog)
    .flatMap(([id, current], index) => {
      const count = index < 6 ? 3 : 2
      let previous: ItemSnapshot | undefined
      return Array.from({ length: count }, (_, version) => {
        const last = version === count - 1
        const day = last ? 2 : version
        const target = last ? current : historical(current, version)
        const special = version === 1 && !last && index < 3
        const category: ActivityCategory = special
          ? (['review', 'draft', 'processing'] as const)[index]!
          : 'catalog'
        const label = special
          ? ['Review approved', 'Draft submitted', 'Processing completed'][
              index
            ]!
          : version === 0
            ? 'Item added'
            : 'Item updated'
        const event: ActivityEvent = {
          id: `history-${id}-${version}`,
          itemId: id,
          kind: current.kind,
          itemTitle: snapshotTitle(target),
          label,
          description: special
            ? 'Recorded demo activity. This did not publish an item.'
            : 'Authored item data changed in this demo history.',
          actor: [demoUser.name, 'Kenji Sato', 'GitStore'][index % 3]!,
          timestamp: new Date(
            Date.UTC(2026, 9, 31 + day, 10 + (index % 2), index * 5),
          ).toISOString(),
          category,
          local: false,
          ...(!special
            ? { before: previous, after: target }
            : {
                changes: [
                  { label: 'Outcome', before: 'Pending', after: label },
                ],
              }),
        }
        if (!special) previous = target
        return event
      })
    })
    .toSorted(
      (a, b) =>
        b.timestamp.localeCompare(a.timestamp) || b.id.localeCompare(a.id),
    )
}
export function createSession(
  namespace: NamespaceId,
  seed: DemoSeed = {},
): DemoSession {
  const fixture = overviewFixture(namespace, seed.overviewScenario)
  const catalog = seedCatalog(fixture.resources)
  const events = seed.activityScenario === 'empty' ? [] : seedEvents(catalog)
  if (seed.activityScenario === 'recovery-error') {
    const index = events.findIndex(
      (event) => event.id === 'history-stone-basin-0',
    )
    const event = events[index]
    if (event?.after?.kind === 'Product')
      events[index] = {
        ...event,
        after: {
          kind: 'Product',
          body: event.after.body,
          spec: { ...event.after.spec, categoryId: 'missing-category' },
        },
      }
  }
  let session: DemoSession = {
    setupReviews: initialSetupReviews(namespace, seed.setupScenario),
    namespace,
    catalog,
    resources: fixture.resources,
    work: initialWork(
      fixture.resources.length
        ? workFixture(namespace, seed.myWorkScenario)
        : [],
    ),
    buffers: {},
    events,
    revision: 0,
    epoch: 0,
    productBuffers: {},
    productUpdated: Object.fromEntries(
      Object.keys(catalog).map((id) => [id, '2026-11-02T11:00:00.000Z']),
    ),
    deleting: [],
    variantOwners: {
      'tunic-natural-m': 'silk-tunic',
      'tunic-black-m': 'silk-tunic',
      'tea-kit-natural': 'tea-ritual',
    },
  }
  const product = catalog['stone-basin']
  if (product?.kind === 'Product') {
    if (seed.productScenario === 'unsupported')
      session = {
        ...session,
        catalog: {
          ...catalog,
          'stone-basin': {
            ...product,
            body: '<aside>Preserve this authored HTML in Markdown mode.</aside>',
          },
        },
      }
    if (seed.productScenario === 'stale-edit')
      session = {
        ...session,
        productBuffers: {
          'stone-basin': {
            base: product,
            value: { ...product, body: 'Preserved unsaved edits.' },
          },
        },
        catalog: {
          ...catalog,
          'stone-basin': { ...product, body: 'A recovered saved version.' },
        },
      }
    if (
      seed.productScenario === 'deleting' ||
      seed.productScenario === 'deleted'
    )
      session = reduceProduct(session, {
        type: 'product-delete',
        id: 'stone-basin',
      })
    if (seed.productScenario === 'deleted')
      session = reduceProduct(session, {
        type: 'product-complete-delete',
        id: 'stone-basin',
      })
  }
  return session
}
export function resetSession(
  session: DemoSession,
  seed: DemoSeed = {},
): DemoSession {
  return { ...createSession(session.namespace, seed), epoch: session.epoch + 1 }
}
export function projectedResources(
  session: DemoSession,
): readonly DemoResource[] {
  const label = (id: string) =>
    session.catalog[id]
      ? snapshotTitle(session.catalog[id])
      : (() => {
          const resource = session.resources.find(
            (resource) => resource.id === id,
          )
          return resource ? resourceLabel(resource) : 'Unavailable item'
        })()
  return session.resources.map((resource): DemoResource => {
    const snapshot = session.catalog[resource.id]
    let updated = resource
    if (snapshot && 'spec' in snapshot && 'spec' in resource)
      updated = { ...resource, spec: { title: snapshot.spec.title } }
    if (snapshot?.kind === 'File' && resource.kind === 'File')
      updated = { ...resource, name: snapshot.name }
    let references = resource.references
    const ref = (id: string, detail: string) => ({
      resourceId: id,
      label: label(id),
      detail,
    })
    if (snapshot?.kind === 'Product')
      references = [
        ...(snapshot.spec.categoryId
          ? [ref(snapshot.spec.categoryId, 'Assigned category')]
          : []),
        ...snapshot.spec.mediaIds.map((id) => ref(id, 'Product image')),
      ]
    else if (snapshot?.kind === 'CategoryTaxonomy')
      references = snapshot.spec.parentId
        ? [ref(snapshot.spec.parentId, 'Parent category')]
        : []
    else if (snapshot?.kind === 'Collection')
      references = snapshot.spec.productIds.map((id) =>
        ref(id, 'Collection product'),
      )
    else if (snapshot?.kind === 'File')
      references = Object.entries(session.catalog).flatMap(([id, item]) =>
        item.kind === 'Product' && item.spec.mediaIds.includes(resource.id)
          ? [ref(id, 'Used by this product')]
          : [],
      )
    else
      references = references.map((reference) =>
        reference.resourceId
          ? { ...reference, label: label(reference.resourceId) }
          : reference,
      )
    return {
      ...updated,
      references,
      deleting: session.deleting.includes(resource.id),
    }
  })
}
export function restoreProblem(session: DemoSession, eventId: string) {
  const event = session.events.find((entry) => entry.id === eventId)
  if (event && session.deleting.includes(event.itemId))
    return 'This product is being deleted. Recovery is unavailable.'
  return event
    ? recoveryProblem(event.itemId, event.after, session.catalog)
    : 'This event is no longer available.'
}
export function sessionReducer(
  session: DemoSession,
  action: HomeAction,
): DemoSession {
  if (action.type === 'setup-review') {
    if (session.setupReviews[action.step] === action.reviewed) return session
    return {
      ...session,
      setupReviews: { ...session.setupReviews, [action.step]: action.reviewed },
    }
  }
  if (
    action.type === 'product-buffer' ||
    action.type === 'product-save' ||
    action.type === 'product-delete' ||
    action.type === 'product-complete-delete'
  )
    return reduceProduct(session, action)
  if (action.type === 'buffer') {
    const item = session.work.items.find((entry) => entry.id === action.id)
    if (!item || item.status !== 'drafts') return session
    const buffers = { ...session.buffers }
    if (action.text) buffers[action.id] = action.text
    else delete buffers[action.id]
    return { ...session, buffers }
  }
  const revision = session.revision + 1
  const timestamp = new Date(
    Date.UTC(2026, 10, 2, 12, 0, revision),
  ).toISOString()
  const id = `demo-${session.namespace}-${revision}`
  if (action.type === 'restore') {
    if (restoreProblem(session, action.eventId)) return session
    const source = session.events.find((entry) => entry.id === action.eventId)!
    const target = structuredClone(source.after!)
    const before = session.catalog[source.itemId]!
    const event: ActivityEvent = {
      id,
      itemId: source.itemId,
      kind: target.kind,
      itemTitle: snapshotTitle(target),
      label: 'Version restored in demo',
      description:
        'The selected authored version was restored in this tab. Nothing was published.',
      actor: demoUser.name,
      timestamp,
      category: 'recovery',
      local: true,
      before,
      after: target,
      sourceEventId: source.id,
    }
    const recovery: WorkItem = {
      id: `${id}-work`,
      resourceId: source.itemId,
      entryType: 'recovery',
      resourceKind: target.kind,
      status: 'completed',
      outcome: 'restored',
      author: demoUser.name,
      reviewer: 'No review performed',
      path: session.resources.find((resource) => resource.id === source.itemId)!
        .path,
      updatedAt: timestamp,
      recovery: { before, after: target, sourceEventId: source.id },
    }
    return {
      ...session,
      revision,
      catalog: { ...session.catalog, [source.itemId]: target },
      productUpdated:
        target.kind === 'Product'
          ? { ...session.productUpdated, [source.itemId]: timestamp }
          : session.productUpdated,
      events: [event, ...session.events],
      work: { ...session.work, items: [recovery, ...session.work.items] },
    }
  }
  if (action.type === 'resolve') {
    const resource = session.resources.find((entry) => entry.id === action.id)
    if (
      !resource ||
      resource.readiness === 'ready' ||
      session.deleting.includes(action.id)
    )
      return session
    const event: ActivityEvent = {
      id,
      itemId: resource.id,
      kind: resource.kind,
      itemTitle: session.catalog[resource.id]
        ? snapshotTitle(session.catalog[resource.id]!)
        : resourceLabel(resource),
      label: 'Demo processing completed',
      description:
        'The item was marked ready in this demo. Authored data was not changed.',
      actor: demoUser.name,
      timestamp,
      category: 'processing',
      local: true,
      changes: [
        { label: 'Demo status', before: resource.readiness, after: 'Ready' },
      ],
    }
    return {
      ...session,
      revision,
      resources: session.resources.map((entry) =>
        entry.id === resource.id
          ? {
              ...entry,
              readiness: 'ready',
              diagnostic:
                'Demo change applied. Your store has not been changed.',
            }
          : entry,
      ),
      events: [event, ...session.events],
    }
  }
  if (action.type !== 'work') return session
  const old = session.work.items.find((entry) => entry.id === action.action.id)
  if (!old || old.entryType === 'recovery') return session
  const work = workReducer(
    { ...session.work, revision: session.revision },
    action.action,
  )
  if (work.items === session.work.items) return session
  const updated = work.items.find((entry) => entry.id === old.id)
  const nextText =
    updated?.entryType === 'product-text' ? updated.proposed : old.proposed
  const labels = {
    save: 'Draft saved',
    submit: 'Draft submitted',
    discard: 'Draft discarded',
    approve: 'Review approved',
    'request-changes': 'Changes requested',
  }
  const category: ActivityCategory =
    action.action.type === 'approve' || action.action.type === 'request-changes'
      ? 'review'
      : 'draft'
  const changes: FieldChange[] =
    action.action.type === 'save' && updated
      ? (['title', 'body'] as const)
          .filter(
            (field) =>
              (field === 'title'
                ? old.proposed.spec.title
                : old.proposed.body) !==
              (field === 'title' ? nextText.spec.title : nextText.body),
          )
          .map((field) => ({
            label: field === 'title' ? 'Title' : 'Description',
            before:
              (field === 'title'
                ? old.proposed.spec.title
                : old.proposed.body) || 'Empty',
            after:
              (field === 'title' ? nextText.spec.title : nextText.body) ||
              'Empty',
          }))
      : [
          {
            label: 'Outcome',
            before: old.status,
            after:
              action.action.type === 'request-changes'
                ? action.action.note.trim()
                : labels[action.action.type],
          },
        ]
  const event: ActivityEvent = {
    id,
    itemId: old.resourceId,
    kind: old.resourceKind,
    itemTitle: nextText.spec.title,
    label: labels[action.action.type],
    description:
      'Personal work activity in this demo. The catalog item was not published or changed.',
    actor: demoUser.name,
    timestamp,
    category,
    local: true,
    changes,
  }
  const buffers = { ...session.buffers }
  delete buffers[old.id]
  return {
    ...session,
    revision,
    work,
    buffers,
    events: [event, ...session.events],
  }
}
export function recentOverviewActivity(session: DemoSession) {
  return session.events.slice(0, 3).map((event) => ({
    id: event.id,
    resourceId: event.itemId,
    title: `${event.label}: ${event.itemTitle}`,
    time:
      new Intl.DateTimeFormat('en-GB', {
        dateStyle: 'medium',
        timeStyle: 'short',
        timeZone: 'UTC',
      }).format(new Date(event.timestamp)) + ' UTC',
    local: event.local,
  }))
}
