import type { DemoSession } from '../../demo/model'
import { demoMedia } from '../products/ProductMedia'
import type { CatalogEntry } from './model'
import { catalogEntries, categoryHierarchy, categoryPath } from './model'

/** Only a category's own file reference may supply its presentation image. */
export function categoryMediaId(
  session: Pick<DemoSession, 'catalog'>,
  id: string,
) {
  const item = session.catalog[id]
  if (item?.kind !== 'CategoryTaxonomy') return undefined
  const name = item.spec.media?.[0]?.fileRef.name
  return name && session.catalog[name]?.kind === 'File' && demoMedia[name]
    ? name
    : undefined
}

/** Matching records remain distinct from the ancestors included for orientation. */
export function categoryContext(
  session: DemoSession,
  matches: readonly CatalogEntry[],
) {
  const ids = new Set(matches.flatMap((item) => categoryPath(session, item.id)))
  const matchIds = new Set(matches.map((item) => item.id))
  const values = new Map(
    catalogEntries(session, 'categories').map((item) => [item.id, item]),
  )
  const entries = [
    ...matches,
    ...[...ids]
      .filter((id) => !matchIds.has(id))
      .flatMap((id) => values.get(id) ?? []),
  ]
  return { entries, matchIds, rows: categoryHierarchy(session, entries) }
}

export function categoryColumns(
  session: DemoSession,
  entries: readonly CatalogEntry[],
  selected?: string,
) {
  const rows = categoryHierarchy(session, [...entries])
  const ids = new Set(entries.map((item) => item.id))
  const path =
    selected && ids.has(selected)
      ? categoryPath(session, selected).filter((id) => ids.has(id))
      : []
  const columns: {
    parent: string | null
    title: string
    entries: CatalogEntry[]
  }[] = [
    {
      parent: null,
      title: 'Root categories',
      entries: rows.filter((row) => row.depth === 0).map((row) => row.item),
    },
  ]
  for (const [index, id] of path.entries()) {
    const visited = new Set(path.slice(0, index + 1))
    const children = entries.filter((item) => {
      const value = session.catalog[item.id]
      return (
        value?.kind === 'CategoryTaxonomy' &&
        value.spec.parentId === id &&
        !visited.has(item.id)
      )
    })
    if (children.length)
      columns.push({
        parent: id,
        title: entries.find((item) => item.id === id)!.title,
        entries: children,
      })
  }
  return { path, columns }
}
