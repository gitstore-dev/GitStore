import type { DemoSession } from '../../demo/model'
import { snapshotTitle } from '../../demo/snapshots'
export type StructureDomain = 'attributes-options' | 'product-types'
export type StructureTab = 'options' | 'definitions' | 'types'
export type StructureScenario =
  | 'loading'
  | 'unavailable'
  | 'long-values'
  | 'missing-relationships'
  | 'many'
  | 'unmatched-selection'
export interface StructureSearch {
  tab: StructureTab
  q: string
  related: string
  sort: 'updated' | 'title'
  view: 'table' | 'grid'
}
export const structurePaths = {
  'attributes-options': '/w/$namespace/catalog/attributes-options',
  'product-types': '/w/$namespace/catalog/product-types',
} as const
export function structureSearch(
  raw: Record<string, unknown>,
  domain: StructureDomain,
): StructureSearch {
  return {
    tab:
      domain === 'product-types'
        ? 'types'
        : raw.tab === 'definitions'
          ? 'definitions'
          : 'options',
    q: typeof raw.q === 'string' ? raw.q.slice(0, 200) : '',
    related: typeof raw.related === 'string' ? raw.related : 'all',
    sort: raw.sort === 'title' ? 'title' : 'updated',
    view: raw.view === 'grid' ? 'grid' : 'table',
  }
}
export function optionIdentity(productId: string, name: string) {
  return `option:${encodeURIComponent(productId)}:${encodeURIComponent(name)}`
}
export function structureRoute(path: string) {
  const match =
    /^\/w\/([^/]+)\/catalog\/(attributes-options|product-types)(?:\/(.*))?\/?$/.exec(
      path,
    )
  if (!match) return
  const domain = match[2] as StructureDomain,
    tail = match[3]?.replace(/\/$/, '')
  if (!tail) return { namespace: match[1]!, domain, id: undefined }
  try {
    const option = /^products\/([^/]+)\/options\/([^/]+)$/.exec(tail)
    if (domain === 'attributes-options' && option)
      return {
        namespace: match[1]!,
        domain,
        id: optionIdentity(
          decodeURIComponent(option[1]!),
          decodeURIComponent(option[2]!),
        ),
      }
    const definition = /^definitions\/([^/]+)$/.exec(tail)
    if (domain === 'attributes-options' && definition)
      return {
        namespace: match[1]!,
        domain,
        id: `definition:${decodeURIComponent(definition[1]!)}`,
      }
    if (domain === 'product-types' && !tail.includes('/'))
      return {
        namespace: match[1]!,
        domain,
        id: `type:${decodeURIComponent(tail)}`,
      }
  } catch {
    return
  }
}
export function structureReturnSearch(
  raw: Record<string, unknown>,
  domain: StructureDomain,
  path: string,
) {
  const search = structureSearch(raw, domain)
  return domain === 'attributes-options' && path.includes('/definitions/')
    ? { ...search, tab: 'definitions' as const }
    : search
}
interface Common {
  id: string
  title: string
  updatedAt: string
  body: string
  values: readonly string[]
}
export interface InlineOption extends Common {
  source: 'option'
  productId: string
  optionName: string
}
export interface DefinitionExample extends Common {
  source: 'definition'
  exampleId: string
}
export interface TypeExample extends Common {
  source: 'type'
  exampleId: string
  definitionIds: readonly string[]
  productIds: readonly string[]
}
export type StructureEntry = InlineOption | DefinitionExample | TypeExample
const definitions: readonly DefinitionExample[] = [
  {
    source: 'definition',
    id: 'definition:color',
    exampleId: 'color',
    title: 'Color',
    updatedAt: '2026-11-02T11:00:00Z',
    body: 'A reusable choice of colors for a future catalog.\n\nThis example does not supply choices to existing Products.',
    values: ['Natural', 'Black'],
  },
  {
    source: 'definition',
    id: 'definition:size',
    exampleId: 'size',
    title: 'Size',
    updatedAt: '2026-11-02T10:30:00Z',
    body: 'A reusable set of size choices. Products would explicitly choose which sizes to offer.\n\nNo existing Product is connected to this definition.',
    values: ['S', 'M', 'L'],
  },
  {
    source: 'definition',
    id: 'definition:material',
    exampleId: 'material',
    title: 'Material',
    updatedAt: '2026-11-02T10:00:00Z',
    body: 'An example of shared material choices for describing products.\n\nThis is a design preview, not an enforced product requirement.',
    values: ['Ceramic', 'Silk'],
  },
]
const types: readonly TypeExample[] = [
  {
    source: 'type',
    id: 'type:apparel',
    exampleId: 'apparel',
    title: 'Apparel',
    updatedAt: '2026-11-02T11:00:00Z',
    body: 'An example of how clothing could share **Color** and **Size** definitions.\n\nThe products below illustrate the idea; no type assignment or validation is performed.',
    values: [],
    definitionIds: ['color', 'size'],
    productIds: ['silk-tunic'],
  },
  {
    source: 'type',
    id: 'type:homeware',
    exampleId: 'homeware',
    title: 'Homeware',
    updatedAt: '2026-11-02T10:30:00Z',
    body: 'An example grouping for objects used at home, with a shared **Material** definition.\n\nThese examples do not configure tax, shipping, inventory or publication.',
    values: [],
    definitionIds: ['material'],
    productIds: ['stone-basin', 'ceramic-cup', 'tea-ritual'],
  },
]
export function structureEntries(
  session: DemoSession,
  tab: StructureTab,
  scenario?: StructureScenario,
): StructureEntry[] {
  if (tab === 'options') {
    return Object.entries(session.catalog)
      .flatMap(([productId, product]) =>
        product.kind !== 'Product'
          ? []
          : product.spec.options.map((option) => ({
              source: 'option' as const,
              id: optionIdentity(productId, option.name),
              productId,
              optionName: option.name,
              title: option.title.trim() || option.name,
              values: option.values,
              body: '',
              updatedAt:
                session.events
                  .filter(
                    (event) =>
                      event.itemId === productId &&
                      ['catalog', 'recovery'].includes(event.category),
                  )
                  .map((event) => event.timestamp)
                  .sort()
                  .at(-1) ?? '2026-11-02T09:00:00Z',
            })),
      )
      .map((item, index) =>
        index === 0 && scenario === 'long-values'
          ? {
              ...item,
              title:
                'An unusually long product option title for handmade objects',
              values: [
                'A very long descriptive choice that must wrap without hiding the complete value',
                'Black',
              ],
            }
          : index === 0 && scenario === 'unmatched-selection'
            ? { ...item, values: [] }
            : item,
      )
  }
  if (session.namespace !== 'atelier-kyoto') return []
  const entries: (DefinitionExample | TypeExample)[] = (
    tab === 'definitions' ? definitions : types
  ).map((item) => ({ ...item }))
  if (scenario === 'many')
    for (let i = 1; i <= 13; i++) {
      const sample = entries[0]!
      entries.push({
        ...sample,
        id: `${sample.source}:example-${i}`,
        exampleId: `example-${i}`,
        title: `Example ${String(i).padStart(2, '0')}`,
      })
    }
  return entries.map((item, index) =>
    index === 0 && scenario === 'long-values'
      ? {
          ...item,
          title:
            'A long descriptive example title for thoughtful catalog organization',
          values: [
            'A choice with a long readable description that should remain visible on narrow screens',
            'Another example choice',
          ],
        }
      : index === 0 &&
          scenario === 'missing-relationships' &&
          item.source === 'type'
        ? {
            ...item,
            definitionIds: [...item.definitionIds, 'missing-definition'],
            productIds: [...item.productIds, 'missing-product'],
          }
        : item,
  )
}
export function productTitle(session: DemoSession, id: string) {
  const item = session.catalog[id]
  return item?.kind === 'Product' ? snapshotTitle(item) : undefined
}
export function filterStructure(
  entries: readonly StructureEntry[],
  search: StructureSearch,
  session: DemoSession,
  scenario?: StructureScenario,
) {
  return entries
    .filter(
      (item) =>
        `${item.title} ${item.source === 'option' ? (productTitle(session, item.productId) ?? '') : ''}`
          .toLocaleLowerCase()
          .includes(search.q.trim().toLocaleLowerCase()) &&
        (search.related === 'all' ||
          (item.source === 'option'
            ? item.productId === search.related
            : item.source === 'type'
              ? item.definitionIds.includes(search.related)
              : structureEntries(session, 'types', scenario).some(
                  (type) =>
                    type.source === 'type' &&
                    type.exampleId === search.related &&
                    type.definitionIds.includes(item.exampleId),
                ))),
    )
    .toSorted(
      (a, b) =>
        (search.sort === 'title'
          ? a.title.localeCompare(b.title)
          : b.updatedAt.localeCompare(a.updatedAt)) || a.id.localeCompare(b.id),
    )
}
export function structureConnection(
  entries: readonly StructureEntry[],
  after?: string,
  limit = 6,
) {
  const start = after
    ? entries.findIndex(
        (item) => `structure:${encodeURIComponent(item.id)}` === after,
      ) + 1
    : 0
  if (after && !start)
    return { edges: [], pageInfo: { hasNextPage: false, endCursor: undefined } }
  const selected = entries.slice(start, start + limit)
  return {
    edges: selected.map((node) => ({
      node,
      cursor: `structure:${encodeURIComponent(node.id)}`,
    })),
    pageInfo: {
      hasNextPage: start + selected.length < entries.length,
      endCursor: selected.length
        ? `structure:${encodeURIComponent(selected.at(-1)!.id)}`
        : undefined,
    },
  }
}
export function optionSelections(session: DemoSession, entry: InlineOption) {
  return Object.entries(session.variants)
    .filter(([, variant]) => variant.spec.productId === entry.productId)
    .map(([id, variant]) => {
      const value = variant.spec.selectedOptions.find(
        (choice) => choice.name === entry.optionName,
      )?.value
      return {
        id,
        title: variant.spec.title,
        value,
        offered: value !== undefined && entry.values.includes(value),
      }
    })
}
export function structureEntryForPath(
  session: DemoSession,
  path: string,
  scenario?: StructureScenario,
) {
  const route = structureRoute(path)
  return route?.id
    ? (['options', 'definitions', 'types'] as const)
        .flatMap((tab) => structureEntries(session, tab, scenario))
        .find((entry) => entry.id === route.id)
    : undefined
}
