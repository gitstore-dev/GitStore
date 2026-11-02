import type { NamespaceId } from '../../../data/demo'

export const resourceKinds = [
  'Product',
  'ProductVariant',
  'CategoryTaxonomy',
  'Collection',
  'File',
  'Namespace',
  'Repository',
] as const
export type ResourceKind = (typeof resourceKinds)[number]
export const readinessStates = [
  'failed',
  'blocked',
  'stale',
  'reconciling',
  'ready',
] as const
export type Readiness = (typeof readinessStates)[number]
export type QueueFilter = 'all' | Exclude<Readiness, 'ready'>
export type SortOrder = 'severity' | 'name'
export type OverviewScenario =
  | 'populated'
  | 'first-use'
  | 'all-clear'
  | 'filtered-empty'
  | 'loading'
  | 'unavailable'
  | 'stale'
  | 'restricted'
export interface ResourceReference {
  name: string
  detail: string
  resourceId?: string
}
export interface DemoResource {
  id: string
  kind: ResourceKind
  name: string
  readiness: Readiness
  diagnostic: string
  path: string
  references: readonly ResourceReference[]
}
export interface DemoActivity {
  id: string
  resourceId: string
  title: string
  time: string
  local?: boolean
}
export interface SetupStep {
  label: string
  complete: boolean
  destinationId: string
}
export interface OverviewFixture {
  namespace: NamespaceId
  resources: readonly DemoResource[]
  activity: readonly DemoActivity[]
  setup: readonly SetupStep[]
}
export interface OverviewState {
  resources: readonly DemoResource[]
  activity: readonly DemoActivity[]
  revision: number
}
export type OverviewAction =
  { type: 'resolve'; id: string } | { type: 'reset'; fixture: OverviewFixture }

export const resourceDestinations: Record<ResourceKind, string> = {
  Product: 'catalog.products',
  ProductVariant: 'catalog.variants',
  CategoryTaxonomy: 'catalog.categories',
  Collection: 'catalog.collections',
  File: 'catalog.media',
  Namespace: 'control-plane.namespaces',
  Repository: 'control-plane.repositories',
}
export function demoAction(resource: DemoResource) {
  return resource.readiness === 'stale'
    ? 'Refresh demo status'
    : resource.readiness === 'reconciling'
      ? 'Complete demo processing'
      : 'Apply demo fix'
}
export function initialOverview(fixture: OverviewFixture): OverviewState {
  return {
    resources: fixture.resources,
    activity: fixture.activity,
    revision: 0,
  }
}
export function overviewReducer(
  state: OverviewState,
  action: OverviewAction,
): OverviewState {
  if (action.type === 'reset') return initialOverview(action.fixture)
  const resource = state.resources.find((item) => item.id === action.id)
  if (!resource || resource.readiness === 'ready') return state
  return {
    resources: state.resources.map((item) =>
      item.id === action.id
        ? {
            ...item,
            readiness: 'ready',
            diagnostic: 'Demo change applied. Your store has not been changed.',
          }
        : item,
    ),
    activity: [
      {
        id: `local-${state.revision + 1}`,
        resourceId: resource.id,
        title: `Demo change applied to ${resource.name}`,
        time: 'This demo session',
        local: true,
      },
      ...state.activity,
    ],
    revision: state.revision + 1,
  }
}
export function overviewSummary(resources: readonly DemoResource[]) {
  return {
    total: resources.length,
    attention: resources.filter((r) =>
      ['failed', 'blocked', 'stale'].includes(r.readiness),
    ).length,
    reconciling: resources.filter((r) => r.readiness === 'reconciling').length,
    ready: resources.filter((r) => r.readiness === 'ready').length,
  }
}
export function queueCounts(resources: readonly DemoResource[]) {
  const counts: Record<QueueFilter, number> = {
    all: 0,
    failed: 0,
    blocked: 0,
    stale: 0,
    reconciling: 0,
  }
  for (const resource of resources)
    if (resource.readiness !== 'ready') {
      counts.all++
      counts[resource.readiness]++
    }
  return counts
}
export function workQueue(
  resources: readonly DemoResource[],
  filter: QueueFilter,
  sort: SortOrder,
) {
  return resources
    .filter(
      (resource) =>
        resource.readiness !== 'ready' &&
        (filter === 'all' || resource.readiness === filter),
    )
    .toSorted((a, b) =>
      sort === 'name'
        ? a.name.localeCompare(b.name, 'en')
        : readinessStates.indexOf(a.readiness) -
            readinessStates.indexOf(b.readiness) ||
          a.name.localeCompare(b.name, 'en'),
    )
}
export function healthMatrix(resources: readonly DemoResource[]) {
  return resourceKinds.map((kind) => {
    const items = resources.filter((resource) => resource.kind === kind)
    const summary = overviewSummary(items)
    return { kind, ...summary }
  })
}
