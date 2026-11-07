import type { DemoResource } from '../overview/model'
import { resourceLabel } from '../overview/model'
export type ItemSnapshot =
  | {
      kind: 'Product'
      spec: {
        title: string
        description: string
        categoryId: string | null
        mediaIds: readonly string[]
      }
    }
  | {
      kind: 'CategoryTaxonomy'
      spec: { title: string; description: string; parentId: string | null }
    }
  | {
      kind: 'Collection'
      spec: {
        title: string
        description: string
        productIds: readonly string[]
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
export const sameSnapshot = (a: ItemSnapshot, b: ItemSnapshot) =>
  a.kind === b.kind &&
  JSON.stringify(snapshotFields(a)) === JSON.stringify(snapshotFields(b))
export function snapshotDescription(value: ItemSnapshot) {
  return value.kind === 'File' ? value.caption : value.spec.description
}
export function snapshotFields(
  value: ItemSnapshot,
): Record<string, string | readonly string[] | null> {
  switch (value.kind) {
    case 'Product':
      return {
        Title: value.spec.title,
        Description: value.spec.description,
        Category: value.spec.categoryId,
        'Product images': value.spec.mediaIds,
      }
    case 'CategoryTaxonomy':
      return {
        Title: value.spec.title,
        Description: value.spec.description,
        'Parent category': value.spec.parentId,
      }
    case 'Collection':
      return {
        Title: value.spec.title,
        Description: value.spec.description,
        'Collection products': value.spec.productIds,
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
        spec: {
          title,
          description,
          categoryId: 'homeware',
          mediaIds:
            resource.id === 'stone-basin' ? ['basin-image'] : ['lookbook'],
        },
      }
    if (resource.kind === 'CategoryTaxonomy')
      catalog[resource.id] = {
        kind: 'CategoryTaxonomy',
        spec: { title, description, parentId: null },
      }
    if (resource.kind === 'Collection')
      catalog[resource.id] = {
        kind: 'Collection',
        spec: {
          title,
          description,
          productIds:
            resource.id === 'autumn-objects'
              ? ['stone-basin', 'tea-ritual']
              : ['ceramic-cup', 'silk-tunic'],
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
      : target.kind === 'Collection'
        ? target.spec.productIds.map((ref) => [ref, 'Product'] as const)
        : target.kind === 'CategoryTaxonomy' && target.spec.parentId
          ? [[target.spec.parentId, 'CategoryTaxonomy']]
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
