import type { DemoResource } from '../home/overview/model'
import { resourceLabel } from '../home/overview/model'
export interface LabelSelector {
  matchLabels?: Readonly<Record<string, string>>
  matchExpressions?: readonly {
    key: string
    operator: 'In' | 'NotIn' | 'Exists' | 'DoesNotExist'
    values?: readonly string[]
  }[]
}
export type ItemSnapshot =
  | {
      kind: 'Product'
      metadata?: { labels: Readonly<Record<string, string>> }
      body: string
      spec: {
        title: string
        categoryId: string | null
        mediaIds: readonly string[]
        tags: readonly string[]
        options: readonly {
          name: string
          title: string
          values: readonly string[]
        }[]
        lifecycle: { state: 'ACTIVE' | 'RETIRED' }
      }
    }
  | {
      kind: 'CategoryTaxonomy'
      body: string
      spec: {
        title: string
        parentId: string | null
        media?: readonly {
          fileRef: { name: string; kind?: 'File'; optional?: boolean }
        }[]
      }
    }
  | {
      kind: 'Collection'
      body: string
      spec: {
        title: string
        selector?: LabelSelector
      }
    }
  | { kind: 'File'; name: string; altText: string; caption: string }
export type RecoverableKind = ItemSnapshot['kind']
export interface FieldChange {
  label: string
  before: string
  after: string
}
export const snapshotTitle = (value: ItemSnapshot) =>
  value.kind === 'File' ? value.name : value.spec.title
function canonical(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonical)
  if (value && typeof value === 'object')
    return Object.fromEntries(
      Object.entries(value)
        .toSorted(([a], [b]) => a.localeCompare(b))
        .map(([key, entry]) => [key, canonical(entry)]),
    )
  return value
}
export const sameSnapshot = (a: ItemSnapshot, b: ItemSnapshot) =>
  JSON.stringify(canonical(a)) === JSON.stringify(canonical(b))
export function snapshotDescription(value: ItemSnapshot) {
  return value.kind === 'File' ? value.caption : value.body
}
export function snapshotFields(
  value: ItemSnapshot,
): Record<string, string | readonly string[] | null> {
  switch (value.kind) {
    case 'Product':
      return {
        Title: value.spec.title,
        Description: value.body,
        Category: value.spec.categoryId,
        'Product images': value.spec.mediaIds,
        Tags: value.spec.tags,
        Labels: JSON.stringify(value.metadata?.labels ?? {}),
        Options: value.spec.options.map(
          (option) =>
            `Title ${JSON.stringify(option.title)} · Identifier ${JSON.stringify(option.name)} · Values ${JSON.stringify(option.values)}`,
        ),
        Lifecycle: value.spec.lifecycle.state,
      }
    case 'CategoryTaxonomy':
      return {
        Title: value.spec.title,
        Description: value.body,
        'Parent category': value.spec.parentId,
        'Category images': (value.spec.media ?? []).map(
          (media) => media.fileRef.name,
        ),
      }
    case 'Collection':
      return {
        Title: value.spec.title,
        Description: value.body,
        'Product rules': JSON.stringify(value.spec.selector ?? {}),
      }
    case 'File':
      return {
        'Display name': value.name,
        'Alt text': value.altText,
        Caption: value.caption,
      }
  }
}
export function snapshotChanges(
  before: ItemSnapshot | undefined,
  after: ItemSnapshot,
  catalog: Readonly<Record<string, ItemSnapshot>>,
): FieldChange[] {
  const old = before ? snapshotFields(before) : {}
  const current = snapshotFields(after)
  function text(
    label: string,
    value: string | readonly string[] | null | undefined,
  ): string {
    if (value == null || value === '') return 'Empty'
    if (Array.isArray(value))
      return (
        value
          .map((id) => (catalog[id] ? snapshotTitle(catalog[id]) : id))
          .join(' → ') || 'Empty'
      )
    if (label === 'Category' || label === 'Parent category')
      return catalog[String(value)]
        ? snapshotTitle(catalog[String(value)]!)
        : String(value)
    return String(value)
  }
  return Object.entries(current)
    .filter(
      ([key, value]) => JSON.stringify(value) !== JSON.stringify(old[key]),
    )
    .map(([label, value]) => ({
      label,
      before: before ? text(label, old[label]) : 'No earlier version',
      after: text(label, value),
    }))
}
export function seedCatalog(
  resources: readonly DemoResource[],
): Record<string, ItemSnapshot> {
  const catalog: Record<string, ItemSnapshot> = {}
  for (const resource of resources) {
    const title = resourceLabel(resource)
    const description = `Discover ${title.toLowerCase()}, thoughtfully made for everyday use.`
    if (resource.kind === 'Product')
      catalog[resource.id] = {
        kind: 'Product',
        metadata: {
          labels: {
            assortment: ['stone-basin', 'tea-ritual'].includes(resource.id)
              ? 'autumn'
              : 'everyday',
            material: resource.id === 'silk-tunic' ? 'silk' : 'ceramic',
          },
        },
        body: description,
        spec: {
          title,
          categoryId: 'homeware',
          tags: [],
          options:
            resource.id === 'silk-tunic'
              ? [
                  { name: 'size', title: 'Size', values: ['M'] },
                  {
                    name: 'color',
                    title: 'Color',
                    values: ['Natural', 'Black'],
                  },
                ]
              : [],
          lifecycle: { state: 'ACTIVE' },
          mediaIds:
            resource.id === 'stone-basin' ? ['basin-image', 'lookbook'] : [],
        },
      }
    if (resource.kind === 'CategoryTaxonomy')
      catalog[resource.id] = {
        kind: 'CategoryTaxonomy',
        body: description,
        spec: {
          title,
          media:
            resource.id === 'lifestyle'
              ? []
              : [
                  {
                    fileRef: {
                      name:
                        resource.id === 'ceramics' ? 'basin-image' : 'lookbook',
                      kind: 'File',
                    },
                  },
                ],
          parentId:
            (
              { ceramics: 'homeware', tableware: 'ceramics' } as Record<
                string,
                string
              >
            )[resource.id] ?? null,
        },
      }
    if (resource.kind === 'Collection')
      catalog[resource.id] = {
        kind: 'Collection',
        body: description,
        spec: {
          title,
          selector:
            resource.id === 'seasonal-selection'
              ? undefined
              : {
                  matchLabels: {
                    assortment:
                      resource.id === 'autumn-objects' ? 'autumn' : 'everyday',
                  },
                },
        },
      }
    if (resource.kind === 'File')
      catalog[resource.id] = {
        kind: 'File',
        name: title,
        altText: `Studio photograph of ${title}`,
        caption: 'Materials and details from Atelier Kyoto.',
      }
  }
  return catalog
}
export function recoveryProblem(
  id: string,
  target: ItemSnapshot | undefined,
  catalog: Readonly<Record<string, ItemSnapshot>>,
): string | null {
  if (!target)
    return 'This event does not contain an authored item version to restore.'
  const current = catalog[id]
  if (!current || current.kind !== target.kind)
    return 'This item is no longer available in the demo catalog.'
  if (sameSnapshot(current, target))
    return 'This version is already current in this demo.'
  const refs: readonly (readonly [string, RecoverableKind])[] =
    target.kind === 'Product'
      ? [
          ...(target.spec.categoryId
            ? [[target.spec.categoryId, 'CategoryTaxonomy'] as const]
            : []),
          ...target.spec.mediaIds.map((ref) => [ref, 'File'] as const),
        ]
      : target.kind === 'CategoryTaxonomy'
        ? [
            ...(target.spec.parentId
              ? [[target.spec.parentId, 'CategoryTaxonomy'] as const]
              : []),
            ...(target.spec.media ?? [])
              .filter(
                (media) =>
                  !media.fileRef.optional || !!catalog[media.fileRef.name],
              )
              .map((media) => [media.fileRef.name, 'File'] as const),
          ]
        : []
  if (refs.some(([ref, kind]) => catalog[ref]?.kind !== kind))
    return 'A related item is missing or has the wrong type. This version cannot be restored.'
  if (target.kind === 'CategoryTaxonomy') {
    const visited = new Set([id])
    let parent = target.spec.parentId
    while (parent) {
      if (visited.has(parent))
        return 'Restoring this version would create a category cycle.'
      visited.add(parent)
      const ancestor: ItemSnapshot | undefined = catalog[parent]
      parent =
        ancestor?.kind === 'CategoryTaxonomy' ? ancestor.spec.parentId : null
    }
  }
  return null
}
