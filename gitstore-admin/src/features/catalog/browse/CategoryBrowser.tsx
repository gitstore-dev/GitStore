import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { ArrowLeft, ChevronDown, ChevronRight } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import type { DemoSession } from '../../demo/model'
import { ActionButton } from '../../../components/controls'
import { usePageInspector } from '../../../shell/PageInspector'
import { ProductMedia } from '../products/ProductMedia'
import { StatusBadge } from '../../home/overview/StatusBadge'
import { CategoryBreadcrumb } from './CategoryBreadcrumb'
import {
  categoryContext,
  categoryColumns,
  categoryMediaId,
} from './category-navigation'
import { catalogEntries, categoryPath } from './model'
import type { CatalogEntry, CatalogSearch } from './model'
import styles from './category-browser.module.css'

export function CategoryBrowser({
  session,
  namespace,
  search,
  matches,
  renderRow,
  cardEntries,
  cardsHasMore,
  onLoadMore,
  gridClassName = '',
  onSelect,
  onBeforeChange,
}: {
  session: DemoSession
  namespace: NamespaceId
  search: CatalogSearch
  matches: CatalogEntry[]
  renderRow: (item: CatalogEntry) => ReactNode
  cardEntries: CatalogEntry[]
  cardsHasMore: boolean
  onLoadMore: () => void
  gridClassName?: string
  onSelect: (id?: string) => void
  onBeforeChange: () => void
}) {
  const frame = useRef<HTMLDivElement>(null)
  const previousSelected = useRef<string | undefined>(search.selected)
  const [capacity, setCapacity] = useState(3)
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set())
  const [limits, setLimits] = useState<Record<string, number>>({})
  const all = useMemo(() => catalogEntries(session, 'categories'), [session])
  const context = categoryContext(session, matches)
  const filtered =
    !!search.q.trim() || search.status !== 'all' || search.related !== 'all'
  const selectedId = context.entries.some((item) => item.id === search.selected)
    ? search.selected
    : filtered
      ? matches[0]?.id
      : undefined
  const selected = all.find((item) => item.id === selectedId)
  const { path, columns } = categoryColumns(
    session,
    context.entries,
    selectedId,
  )
  const shownColumns = columns.slice(-capacity)
  useLayoutEffect(() => {
    const element = frame.current
    if (!element) return
    const fit = () =>
      setCapacity(
        element.clientWidth >= 900 ? 3 : element.clientWidth >= 560 ? 2 : 1,
      )
    fit()
    const observer = new ResizeObserver(fit)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])
  useLayoutEffect(() => {
    if (
      previousSelected.current !== selectedId &&
      document.activeElement === document.body
    ) {
      frame.current
        ?.querySelector<HTMLElement>(
          '[data-category-columns] section:last-child h2',
        )
        ?.focus({ preventScroll: true })
    }
    previousSelected.current = selectedId
  }, [selectedId])
  const inspector = useMemo(
    () =>
      selected ? (
        <div className={styles.inspector}>
          <h2>{selected.title}</h2>
          <StatusBadge state={selected.status} />
          <ProductMedia
            id={categoryMediaId(session, selected.id)}
            emptyLabel="No category image"
          />
          <p>{selected.resource.diagnostic}</p>
          <p>
            Read-only demo category. Browsing does not change its hierarchy or
            checks.
          </p>
          <Link
            to="/w/$namespace/catalog/categories/$itemId"
            params={{ namespace, itemId: selected.id }}
          >
            Open category
          </Link>
        </div>
      ) : null,
    [selected, session, namespace],
  )
  usePageInspector(inspector)
  function toggle(id: string) {
    onBeforeChange()
    setCollapsed((previous) => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }
  function detail(item: CatalogEntry) {
    return (
      <Link
        className={styles.detailLink}
        to="/w/$namespace/catalog/categories/$itemId"
        params={{ namespace, itemId: item.id }}
        search={search}
      >
        View category <span aria-hidden="true">↗</span>
      </Link>
    )
  }
  const breadcrumbPath =
    search.view === 'columns'
      ? path
      : search.related !== 'all' && search.related !== 'root'
        ? categoryPath(session, search.related)
        : []
  const problem = context.rows.find(
    (row) => row.item.id === selectedId,
  )?.problem
  const crumbs = breadcrumbPath.map((id) => ({
    id,
    title: all.find((item) => item.id === id)!.title,
    link: (
      <Link
        to="/w/$namespace/catalog/categories"
        params={{ namespace }}
        search={{ ...search, selected: id }}
        resetScroll={false}
      >
        {all.find((item) => item.id === id)!.title}
      </Link>
    ),
    onSelect: () => onSelect(id),
  }))
  return (
    <div
      ref={frame}
      className={styles.browser}
      data-category-browser={search.view}
    >
      {crumbs.length > 0 && (
        <div className={styles.path}>
          <CategoryBreadcrumb items={crumbs} incomplete={!!problem} />
        </div>
      )}
      {filtered &&
        context.entries.length > matches.length &&
        search.view !== 'cards' && (
          <p className={styles.note}>
            Ancestors are included for context. Only matching categories count
            as results.
          </p>
        )}
      {search.selected && !all.some((item) => item.id === search.selected) && (
        <p className={styles.note}>
          Selected category unavailable.{' '}
          <ActionButton variant="ghost" onPress={() => onSelect(undefined)}>
            Clear category selection
          </ActionButton>
        </p>
      )}
      {search.view === 'columns' ? (
        <>
          {columns.length > capacity && (
            <ActionButton
              variant="ghost"
              className={styles.back}
              onPress={() =>
                onSelect(
                  columns[Math.max(0, columns.length - capacity - 1)]?.parent ??
                    undefined,
                )
              }
            >
              <ArrowLeft size={15} aria-hidden="true" />
              {columns[Math.max(0, columns.length - capacity - 1)]?.title ??
                'Root categories'}
            </ActionButton>
          )}
          <div
            className={styles.columns}
            style={{ '--column-count': shownColumns.length } as CSSProperties}
            aria-label="Category columns"
            data-category-columns=""
          >
            {shownColumns.map((column) => {
              const key = column.parent ?? 'root'
              const chosenIndex = column.entries.findIndex((item) =>
                path.includes(item.id),
              )
              const limit = Math.max(
                limits[key] ?? 6,
                Math.ceil((chosenIndex + 1) / 6) * 6,
              )
              return (
                <section
                  className={styles.column}
                  aria-label={`${column.title} categories`}
                  key={key}
                >
                  <h2 tabIndex={-1}>{column.title}</h2>
                  {column.entries.slice(0, limit).map((item) => {
                    const hasChildren = context.entries.some((child) => {
                      const value = session.catalog[child.id]
                      return (
                        value?.kind === 'CategoryTaxonomy' &&
                        value.spec.parentId === item.id
                      )
                    })
                    return (
                      <ActionButton
                        className={styles.columnItem}
                        key={item.id}
                        aria-label={`Explore ${item.title}`}
                        aria-pressed={path.includes(item.id)}
                        onPress={() => onSelect(item.id)}
                      >
                        <span aria-hidden="true">
                          <ProductMedia
                            id={categoryMediaId(session, item.id)}
                            compact
                            emptyLabel="No category image"
                          />
                        </span>
                        <span className={styles.columnTitle}>
                          {item.title}
                          {!context.matchIds.has(item.id) && (
                            <small>Ancestor</small>
                          )}
                        </span>
                        {hasChildren && (
                          <ChevronRight size={16} aria-hidden="true" />
                        )}
                      </ActionButton>
                    )
                  })}
                  {column.entries.length > limit && (
                    <ActionButton
                      variant="ghost"
                      onPress={() =>
                        setLimits((previous) => ({
                          ...previous,
                          [key]: limit + 6,
                        }))
                      }
                    >
                      Load more categories
                    </ActionButton>
                  )}
                </section>
              )
            })}
          </div>
          {selected && (
            <section className={styles.preview} aria-label="Selected category">
              <span aria-hidden="true">
                <ProductMedia
                  id={categoryMediaId(session, selected.id)}
                  emptyLabel="No category image"
                />
              </span>
              <div>
                <h2>{selected.title}</h2>
                <StatusBadge state={selected.status} />
              </div>
              {detail(selected)}
            </section>
          )}
        </>
      ) : search.view === 'tree' ? (
        <>
          <div className={styles.treeTools}>
            <ActionButton
              variant="ghost"
              isDisabled={filtered}
              onPress={() => {
                onBeforeChange()
                setCollapsed(new Set())
              }}
            >
              Expand all
            </ActionButton>
            <ActionButton
              variant="ghost"
              isDisabled={filtered}
              onPress={() => {
                onBeforeChange()
                setCollapsed(new Set(context.entries.map((item) => item.id)))
              }}
            >
              Collapse all
            </ActionButton>
          </div>
          <ul className={styles.tree} aria-label="Category hierarchy">
            {context.rows
              .filter(
                ({ item }) =>
                  filtered ||
                  !categoryPath(session, item.id)
                    .slice(0, -1)
                    .some((id) => collapsed.has(id)),
              )
              .map(({ item, depth, problem: issue }) => {
                const expandable =
                  !issue?.includes('cycle') &&
                  context.entries.some((child) => {
                    const value = session.catalog[child.id]
                    return (
                      value?.kind === 'CategoryTaxonomy' &&
                      value.spec.parentId === item.id
                    )
                  })
                return (
                  <li
                    key={item.id}
                    data-category-depth={depth}
                    data-context-only={
                      !context.matchIds.has(item.id) || undefined
                    }
                  >
                    <div
                      className={styles.treeRow}
                      style={{ '--depth': depth } as CSSProperties}
                    >
                      {expandable ? (
                        <ActionButton
                          aria-label={`${collapsed.has(item.id) && !filtered ? 'Expand' : 'Collapse'} ${item.title}`}
                          aria-expanded={filtered || !collapsed.has(item.id)}
                          isDisabled={filtered}
                          onPress={() => toggle(item.id)}
                        >
                          {collapsed.has(item.id) && !filtered ? (
                            <ChevronRight size={15} aria-hidden="true" />
                          ) : (
                            <ChevronDown size={15} aria-hidden="true" />
                          )}
                        </ActionButton>
                      ) : (
                        <span className={styles.spacer} />
                      )}
                      {renderRow(item)}
                    </div>
                    {issue && <p className={styles.note}>{issue}</p>}
                  </li>
                )
              })}
          </ul>
        </>
      ) : (
        <>
          <div className={`${styles.cards} ${gridClassName}`} data-view="grid">
            {cardEntries.map(renderRow)}
          </div>
          {cardsHasMore && (
            <ActionButton className={styles.more} onPress={onLoadMore}>
              Load more
            </ActionButton>
          )}
        </>
      )}
    </div>
  )
}
