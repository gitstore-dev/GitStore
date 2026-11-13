import { CardMediaBackdrop } from './CardMediaBackdrop'
import { ActiveFilters } from '../../../components/ActiveFilters'
import {
  useCatalogView,
  rememberCatalogView,
} from '../../../state/catalog-view'
import { useLayoutEffect, useState } from 'react'
import { FilterBar } from '../../../components/FilterBar'
import { FilterResults } from '../../../components/FilterResults'
import { FilterSheet } from '../../../components/FilterSheet'
import { useGridMotion } from '../../home/shared/motion/useGridMotion'
import { useResourceLighting } from '../../home/shared/motion/useResourceLighting'
import {
  useSearch,
  useNavigate,
  useRouter,
  useLocation,
  Link,
} from '@tanstack/react-router'
import { Input, Label, SearchField } from 'react-aria-components'
import { Plus, Search, LayoutGrid, List } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { snapshotTitle } from '../../demo/snapshots'
import { HomePage, HomePageHeader } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { ActionButton } from '../../../components/controls'
import { StatusBadge } from '../../home/overview/StatusBadge'
import { productList, productSearch, productConnection } from './model'
import type { ProductSearch } from './model'
import styles from './products.module.css'
import { ProductMedia } from './ProductMedia'
export function ProductListPage({ namespace }: { namespace: NamespaceId }) {
  const { session, reset } = useHomeDemo(namespace)
  const motion = useRouter().options.context?.productNavigation
  const location = useLocation()
  const savedPosition = motion?.position(location.href, session.epoch)
  const search = productSearch(useSearch({ strict: false }))
  useCatalogView('products', search.view)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [draftFilters, setDraftFilters] = useState(search)
  const activeFilters = (
    ['status', 'lifecycle', 'category', 'sort'] as const
  ).filter((key) => search[key] !== productSearch({})[key])
  function filterLabel(key: (typeof activeFilters)[number]) {
    const labels: Record<string, string> = {
      attention: 'Needs attention',
      ready: 'Ready',
      reconciling: 'Processing',
      failed: 'Has errors',
      blocked: 'Needs review',
      stale: 'Out of date',
      deleting: 'Deleting',
      ACTIVE: 'Active',
      RETIRED: 'Retired',
      title: 'Title A–Z',
    }
    return key === 'category'
      ? session.catalog[search.category]
        ? snapshotTitle(session.catalog[search.category]!)
        : 'Unavailable category'
      : (labels[search[key]] ?? search[key])
  }
  const navigate = useNavigate()
  const { stage, capture } = useGridMotion()
  const lighting = useResourceLighting()
  const results = productList(session, search)
  const filterKey = JSON.stringify([
    namespace,
    search.q,
    search.status,
    search.lifecycle,
    search.category,
    search.sort,
  ])
  const [loaded, setLoaded] = useState<{
    key: string
    cursors: readonly string[]
  }>(() =>
    savedPosition
      ? { key: savedPosition.filterKey, cursors: savedPosition.cursors }
      : { key: '', cursors: [] },
  )
  const cursors = loaded.key === filterKey ? loaded.cursors : []
  let connection = productConnection(session, search)
  const visible = connection.edges.map((edge) => edge.node)
  const consumed: string[] = []
  for (const cursor of cursors) {
    if (
      !connection.pageInfo.hasNextPage ||
      connection.pageInfo.endCursor !== cursor
    )
      break
    connection = productConnection(session, search, { after: cursor })
    visible.push(...connection.edges.map((edge) => edge.node))
    consumed.push(cursor)
  }
  useLayoutEffect(() => {
    motion?.bind(stage.current)
    motion?.remember(location.href, {
      epoch: session.epoch,
      filterKey,
      cursors,
      search,
      scroll: 0,
    })
    motion?.mounted(location.href, session.epoch)
  })
  const { pageInfo } = connection
  const total = Object.values(session.catalog).filter(
    (p) => p.kind === 'Product',
  ).length
  function update(values: Partial<ProductSearch>) {
    if (values.view) rememberCatalogView('products', values.view)
    capture()
    if (Object.keys(values).some((key) => key !== 'view'))
      setLoaded({ key: '', cursors: [] })
    void navigate({
      to: '/w/$namespace/catalog/products',
      params: { namespace },
      search: { ...search, ...values },
      replace: true,
    })
  }
  function renderFilters(
    values: ProductSearch,
    change: (patch: Partial<ProductSearch>) => void,
  ) {
    return (
      <>
        <ChoiceSelect
          label="Status"
          value={values.status}
          onChange={(status) => change({ status })}
          options={[
            { id: 'all', label: 'All statuses' },
            { id: 'attention', label: 'Needs attention' },
            { id: 'failed', label: 'Has errors' },
            { id: 'blocked', label: 'Needs review' },
            { id: 'stale', label: 'Out of date' },
            { id: 'ready', label: 'Ready' },
            { id: 'reconciling', label: 'Processing' },
            { id: 'deleting', label: 'Deleting' },
          ]}
        />
        <ChoiceSelect
          label="Lifecycle"
          value={values.lifecycle}
          onChange={(lifecycle) => change({ lifecycle })}
          options={[
            { id: 'all', label: 'All products' },
            { id: 'ACTIVE', label: 'Active' },
            { id: 'RETIRED', label: 'Retired' },
          ]}
        />
        <ChoiceSelect
          label="Category"
          value={values.category}
          onChange={(category) => change({ category })}
          options={[
            { id: 'all', label: 'All categories' },
            ...Object.entries(session.catalog)
              .filter(([, p]) => p.kind === 'CategoryTaxonomy')
              .map(([id, p]) => ({ id, label: snapshotTitle(p) })),
          ]}
        />
        <ChoiceSelect
          label="Sort by"
          value={values.sort}
          onChange={(sort) => change({ sort })}
          options={[
            { id: 'updated', label: 'Last updated' },
            { id: 'title', label: 'Title A–Z' },
          ]}
        />
      </>
    )
  }
  return (
    <HomePage ref={lighting}>
      <HomePageHeader
        namespace={namespaces.find((n) => n.id === namespace)!.name}
        section="Catalog"
        title="Products"
        description="Shape your catalog, one thoughtful detail at a time."
        onReset={reset}
        actions={
          <Link
            className={styles.primaryLink}
            to="/w/$namespace/catalog/products/new"
            params={{ namespace }}
          >
            <Plus size={15} aria-hidden="true" />
            New product
          </Link>
        }
      />
      {total === 0 ? (
        <EmptyState
          title="Your catalog starts here"
          actions={
            <Link
              className={styles.primaryLink}
              to="/w/$namespace/catalog/products/new"
              params={{ namespace }}
            >
              Create a demo product <Plus size={15} />
            </Link>
          }
        >
          Add your first product with a Title, description, and images from the
          demo library.
        </EmptyState>
      ) : (
        <>
          <FilterBar
            search={
              <SearchField
                data-composite-input=""
                aria-label="Search products"
                value={search.q}
                onChange={(q) => update({ q })}
                className={styles.search}
              >
                <Label className="sr-only">Search products</Label>
                <Search size={16} aria-hidden="true" />
                <Input placeholder="Search by Title" />
              </SearchField>
            }
            tools={
              <div className={styles.viewControls}>
                <ActionButton
                  aria-label="Table view"
                  aria-pressed={search.view === 'table'}
                  onPress={() => update({ view: 'table' })}
                >
                  <List size={16} />
                </ActionButton>
                <ActionButton
                  aria-label="Grid view"
                  aria-pressed={search.view === 'grid'}
                  onPress={() => update({ view: 'grid' })}
                >
                  <LayoutGrid size={16} />
                </ActionButton>
              </div>
            }
            activeCount={activeFilters.length}
            onOpen={() => {
              setDraftFilters(search)
              setFiltersOpen(true)
            }}
          >
            {renderFilters(search, update)}
          </FilterBar>
          <ActiveFilters
            label="Active product filters"
            filters={activeFilters.map((key) => ({
              id: key,
              label: filterLabel(key),
              onRemove: () => update({ [key]: productSearch({})[key] }),
            }))}
          />
          <FilterSheet
            open={filtersOpen}
            title="Filter products"
            itemLabel={
              productList(session, draftFilters).length === 1
                ? 'product'
                : 'products'
            }
            onClose={() => setFiltersOpen(false)}
            onClear={() =>
              setDraftFilters(productSearch({ view: search.view }))
            }
            onApply={() => {
              update(draftFilters)
              setFiltersOpen(false)
            }}
            count={productList(session, draftFilters).length}
          >
            {renderFilters(draftFilters, (patch) =>
              setDraftFilters((previous) => ({ ...previous, ...patch })),
            )}
          </FilterSheet>
          <FilterResults
            filtered={activeFilters.length > 0 || !!search.q}
            onClear={() => update(productSearch({ view: search.view }))}
          >
            <span role="status">
              {visible.length}{' '}
              {visible.length === 1 ? 'product shown' : 'products shown'}
            </span>
          </FilterResults>
          {!results.length || connection.invalidCursor ? (
            <EmptyState
              title={
                connection.invalidCursor
                  ? 'Product position unavailable'
                  : 'No matching products'
              }
              actions={
                <ActionButton onPress={() => update(productSearch({}))}>
                  {connection.invalidCursor
                    ? 'Return to first products'
                    : 'Clear filters'}
                </ActionButton>
              }
            >
              Try another Title or change the filters.
            </EmptyState>
          ) : (
            <div
              ref={stage}
              className={styles.productList}
              data-view={search.view}
              data-product-epoch={session.epoch}
            >
              {visible.map(([id, product]) => {
                const resource = session.resources.find((r) => r.id === id)
                return (
                  <Link
                    key={id}
                    to="/w/$namespace/catalog/products/$productId"
                    params={{ namespace, productId: id }}
                    className={styles.productRow}
                    data-resource-id={id}
                    data-product-surface={id}
                    data-catalog-card={search.view === 'grid' ? '' : undefined}
                    data-halo-card={search.view === 'grid' ? '' : undefined}
                    data-readiness={resource?.readiness}
                  >
                    {search.view === 'grid' && (
                      <CardMediaBackdrop id={product.spec.mediaIds[0]} />
                    )}
                    <div
                      className={styles.rowContents}
                      data-product-content="row"
                    >
                      <span className="sr-only">View </span>
                      <div className={styles.rowIdentity}>
                        <span aria-hidden="true" data-product-image="">
                          <ProductMedia id={product.spec.mediaIds[0]} compact />
                        </span>
                        <div>
                          <strong
                            className={styles.sharedTitle}
                            data-product-title=""
                          >
                            {product.spec.title}
                          </strong>
                          <small>
                            {product.spec.options.length
                              ? `${product.spec.options.length} options`
                              : 'Product'}
                          </small>
                        </div>
                      </div>
                      <span className={styles.category}>
                        {product.spec.categoryId &&
                        session.catalog[product.spec.categoryId]
                          ? snapshotTitle(
                              session.catalog[product.spec.categoryId]!,
                            )
                          : 'Uncategorized'}
                      </span>
                      <span>
                        {session.deleting.includes(id) ? (
                          <span className={styles.badge}>Deleting</span>
                        ) : (
                          <StatusBadge
                            state={resource?.readiness ?? 'reconciling'}
                          />
                        )}
                      </span>
                      <span className={styles.lifecycle}>
                        {product.spec.lifecycle.state === 'ACTIVE'
                          ? 'Active'
                          : 'Retired'}
                      </span>
                    </div>
                  </Link>
                )
              })}
            </div>
          )}
          {pageInfo.hasNextPage && (
            <div className={styles.loadMore}>
              <ActionButton
                aria-label="Load more products"
                onPress={() => {
                  capture()
                  const firstNew = productConnection(session, search, {
                    after: pageInfo.endCursor!,
                  }).edges[0]?.node[0]
                  setLoaded({
                    key: filterKey,
                    cursors: [...consumed, pageInfo.endCursor!],
                  })
                  if (firstNew)
                    requestAnimationFrame(() =>
                      lighting.current
                        ?.querySelector<HTMLElement>(
                          `[data-resource-id="${CSS.escape(firstNew)}"]`,
                        )
                        ?.focus(),
                    )
                }}
              >
                Load more
              </ActionButton>
            </div>
          )}
        </>
      )}
    </HomePage>
  )
}
