import { CardMediaBackdrop } from '../products/CardMediaBackdrop'
import { ActiveFilters } from '../../../components/ActiveFilters'
import {
  useCatalogView,
  rememberCatalogView,
} from '../../../state/catalog-view'
import { ProductMedia } from '../products/ProductMedia'
import { useLayoutEffect, useState } from 'react'
import {
  Link,
  useLocation,
  useNavigate,
  useRouter,
  useSearch,
} from '@tanstack/react-router'
import {
  Input,
  Label,
  SearchField,
  Tooltip,
  TooltipTrigger,
} from 'react-aria-components'
import { FolderTree, Columns3, LayoutGrid, List, Search } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { snapshotTitle } from '../../demo/snapshots'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { HomePage, HomePageHeader } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { StatusBadge } from '../../home/overview/StatusBadge'
import { FilterBar } from '../../../components/FilterBar'
import { FilterResults } from '../../../components/FilterResults'
import { FilterSheet } from '../../../components/FilterSheet'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { ActionButton } from '../../../components/controls'
import { useGridMotion } from '../../home/shared/motion/useGridMotion'
import { useResourceLighting } from '../../home/shared/motion/useResourceLighting'
import {
  catalogPaths,
  catalogDetailPaths,
  catalogEntries,
  catalogLabels,
  catalogSearch,
  filteredEntries,
  catalogConnection,
  relatedProductTitle,
} from './model'
import type { CatalogDomain, CatalogEntry, CatalogSearch } from './model'
import { CategoryBrowser } from './CategoryBrowser'
import { categoryMediaId } from './category-navigation'
import styles from './catalog.module.css'
const statusOptions = [
  { id: 'all', label: 'All statuses' },
  { id: 'attention', label: 'Needs attention' },
  { id: 'ready', label: 'Ready' },
  { id: 'failed', label: 'Has errors' },
  { id: 'blocked', label: 'Needs review' },
  { id: 'stale', label: 'Out of date' },
  { id: 'reconciling', label: 'Processing' },
]
export function CatalogListPage({
  namespace,
  domain,
}: {
  namespace: NamespaceId
  domain: CatalogDomain
}) {
  const { session, reset, announce } = useHomeDemo(namespace)
  const router = useRouter(),
    location = useLocation(),
    navigate = useNavigate()
  const scenario = router.options.context.catalogScenario
  const search = catalogSearch(useSearch({ strict: false }), domain)
  useCatalogView(domain, search.view)
  const { stage: stageRef, capture } = useGridMotion()
  const lightingRef = useResourceLighting(
    search.view === 'grid' || search.view === 'cards' ? 'auto' : 'css',
  )
  const motion = router.options.context.productNavigation
  const filterKey = JSON.stringify([
    namespace,
    domain,
    search.q,
    search.status,
    search.related,
    search.sort,
    session.epoch,
  ])
  const saved = motion?.position(location.href, session.epoch, filterKey)
  const [loaded, setLoaded] = useState({
    key: saved?.filterKey ?? '',
    cursors: saved?.cursors ?? ([] as readonly string[]),
  })
  const cursors = loaded.key === filterKey ? loaded.cursors : []
  const entries = filteredEntries(session, domain, search)
  let connection = catalogConnection(entries)
  const visible = connection.edges.map((edge) => edge.node)
  for (const cursor of cursors) {
    if (
      connection.pageInfo.endCursor !== cursor ||
      !connection.pageInfo.hasNextPage
    )
      break
    connection = catalogConnection(entries, cursor)
    visible.push(...connection.edges.map((edge) => edge.node))
  }
  useLayoutEffect(() => {
    motion?.bind(stageRef.current)
    motion?.remember(location.href, {
      epoch: session.epoch,
      filterKey,
      cursors,
      search,
      scroll: saved?.scroll ?? 0,
    })
    motion?.mounted(location.href, session.epoch)
  })
  const [filtersOpen, setFiltersOpen] = useState(false),
    [draft, setDraft] = useState(search)
  const available = scenario !== 'loading' && scenario !== 'unavailable'
  const isGrid = search.view === 'grid' || search.view === 'cards'
  function update(patch: Partial<CatalogSearch>) {
    if (patch.view) rememberCatalogView(domain, patch.view)
    capture()
    void navigate({
      to: catalogPaths[domain],
      params: { namespace },
      search: { ...search, ...patch },
      resetScroll: false,
      replace: 'q' in patch,
    })
  }
  function loadMore() {
    const cursor = connection.pageInfo.endCursor
    if (!cursor || !connection.pageInfo.hasNextPage) return
    capture()
    setLoaded({ key: filterKey, cursors: [...cursors, cursor] })
    announce(`More ${domain} loaded.`)
  }
  const activeCount =
    Number(search.status !== 'all') +
    Number(search.related !== 'all') +
    Number(search.sort !== catalogSearch({}, domain).sort)
  const relatedOptions =
    domain === 'variants'
      ? [
          { id: 'all', label: 'All products' },
          ...Object.entries(session.catalog)
            .filter(([, item]) => item.kind === 'Product')
            .map(([id, item]) => ({
              id,
              label: 'spec' in item ? item.spec.title : id,
            })),
        ]
      : domain === 'categories'
        ? [
            { id: 'all', label: 'All parents' },
            { id: 'root', label: 'Top-level categories' },
            ...catalogEntries(session, 'categories').map((item) => ({
              id: item.id,
              label: item.title,
            })),
          ]
        : [
            { id: 'all', label: 'All collections' },
            { id: 'rules', label: 'With product rules' },
            { id: 'empty', label: 'Without rules' },
          ]
  function fields(
    value: CatalogSearch,
    onChange: (patch: Partial<CatalogSearch>) => void,
  ) {
    return (
      <>
        <ChoiceSelect
          label="Status"
          value={value.status}
          options={statusOptions}
          onChange={(status) => onChange({ status })}
        />
        <ChoiceSelect
          label={
            domain === 'variants'
              ? 'Product'
              : domain === 'categories'
                ? 'Parent category'
                : 'Product rules'
          }
          value={value.related}
          options={relatedOptions}
          onChange={(related) => onChange({ related })}
        />
        <ChoiceSelect
          label="Sort by"
          value={value.sort}
          options={[
            { id: 'updated', label: 'Last updated' },
            { id: 'title', label: 'Title A–Z' },
          ]}
          onChange={(sort) => onChange({ sort })}
        />
      </>
    )
  }
  function row(item: CatalogEntry) {
    const variant = session.variants[item.id]
    const category = session.catalog[item.id]
    const mediaId =
      domain === 'categories'
        ? categoryMediaId(session, item.id)
        : variant?.spec.mediaIds[0]
    return (
      <Link
        key={item.id}
        to={catalogDetailPaths[domain]}
        params={{ namespace, itemId: item.id }}
        search={search}
        className={styles.row}
        data-product-surface={item.id}
        data-resource-id={item.id}
        data-readiness={item.status}
        data-catalog-card={isGrid ? '' : undefined}
        data-selected={
          (domain === 'categories' && search.selected === item.id) || undefined
        }
        data-halo-card={isGrid ? '' : undefined}
      >
        {isGrid && (
          <CardMediaBackdrop
            id={mediaId}
            emptyLabel={
              domain === 'categories' ? 'No category image' : undefined
            }
          />
        )}
        <div className={styles.rowContents} data-product-content="row">
          <div className={styles.identity}>
            {isGrid ? (
              <span
                className={styles.cardMedia}
                aria-hidden="true"
                data-product-image=""
              >
                <ProductMedia
                  id={mediaId}
                  emptyLabel={
                    domain === 'categories' ? 'No category image' : undefined
                  }
                />
              </span>
            ) : (
              <span
                className={styles.rowMedia}
                aria-hidden="true"
                data-product-image=""
              >
                <ProductMedia id={mediaId} compact />
              </span>
            )}
            <div>
              <strong data-product-title="">{item.title}</strong>
              <small>
                {domain === 'variants'
                  ? variant?.spec.sku
                  : domain === 'categories'
                    ? 'Category'
                    : 'Collection'}
              </small>
            </div>
          </div>
          <span className={styles.secondary}>
            {domain === 'variants'
              ? relatedProductTitle(session, variant?.spec.productId ?? '')
              : domain === 'categories'
                ? category?.kind === 'CategoryTaxonomy' &&
                  category.spec.parentId
                  ? session.catalog[category.spec.parentId]?.kind ===
                    'CategoryTaxonomy'
                    ? snapshotTitle(session.catalog[category.spec.parentId]!)
                    : 'Parent unavailable'
                  : 'Top-level category'
                : 'Product selection rules'}
          </span>
          <span>
            <StatusBadge state={item.status} />
          </span>
        </div>
      </Link>
    )
  }
  const views =
    domain === 'categories'
      ? [
          { id: 'columns', label: 'Columns view', Icon: Columns3 },
          { id: 'tree', label: 'Tree view', Icon: FolderTree },
          { id: 'cards', label: 'Cards view', Icon: LayoutGrid },
        ]
      : [
          { id: 'table', label: 'Table view', Icon: List },
          { id: 'grid', label: 'Grid view', Icon: LayoutGrid },
        ]
  return (
    <HomePage className={styles.page} ref={lightingRef}>
      <HomePageHeader
        title={catalogLabels[domain]}
        namespace={namespaces.find((n) => n.id === namespace)!.name}
        section="Catalog"
        description={
          domain === 'variants'
            ? 'Explore the sellable options behind your products.'
            : domain === 'categories'
              ? 'Find your way through your catalog.'
              : 'See how products come together.'
        }
        onReset={reset}
        demoDetails="Read-only catalog browsing. Demo changes made in Products and Home are shared in this tab. Reload or a confirmed store reset restores the examples."
      />
      {!available ? (
        <EmptyState
          title={
            scenario === 'loading'
              ? `Loading ${domain}`
              : `${catalogLabels[domain]} unavailable`
          }
        >
          Presentation specimen. No network request is running.
        </EmptyState>
      ) : catalogEntries(session, domain).length === 0 ? (
        <EmptyState
          title={`No ${domain} yet`}
          actions={
            <Link to="/w/$namespace/catalog/products" params={{ namespace }}>
              Open products
            </Link>
          }
        >
          Your store’s {domain} will appear here. Authoring is not available in
          this demo browser.
        </EmptyState>
      ) : (
        <>
          {scenario === 'stale' && (
            <p className={styles.warning}>
              Earlier demo snapshot. This is not live catalog data.
            </p>
          )}
          <FilterBar
            search={
              <SearchField
                data-composite-input=""
                className={styles.search}
                aria-label={`Search ${domain}`}
                value={search.q}
                onChange={(q) => update({ q })}
              >
                <Label className="sr-only">Search {domain}</Label>
                <div className={styles.inline}>
                  <Search size={15} aria-hidden="true" />
                  <Input
                    placeholder={
                      domain === 'variants'
                        ? 'Search Title or SKU'
                        : 'Search by Title'
                    }
                  />
                </div>
              </SearchField>
            }
            activeCount={activeCount}
            onOpen={() => {
              setDraft(search)
              setFiltersOpen(true)
            }}
            tools={
              <div className={styles.tools}>
                {views.map(({ id, label, Icon }) => (
                  <TooltipTrigger key={id} delay={500}>
                    <ActionButton
                      aria-label={label}
                      aria-pressed={search.view === id}
                      onPress={() =>
                        update({
                          view: id as CatalogSearch['view'],
                        })
                      }
                    >
                      <Icon size={16} aria-hidden="true" />
                    </ActionButton>
                    <Tooltip className={styles.viewTooltip}>{label}</Tooltip>
                  </TooltipTrigger>
                ))}
              </div>
            }
          >
            {fields(search, update)}
          </FilterBar>
          <ActiveFilters
            label={`Active ${domain} filters`}
            filters={(['status', 'related', 'sort'] as const)
              .filter((key) => search[key] !== catalogSearch({}, domain)[key])
              .map((key) => ({
                id: key,
                label:
                  key === 'status'
                    ? (statusOptions.find(
                        (option) => option.id === search.status,
                      )?.label ?? search.status)
                    : key === 'related'
                      ? (relatedOptions.find(
                          (option) => option.id === search.related,
                        )?.label ?? search.related)
                      : search.sort === 'updated'
                        ? 'Last updated'
                        : 'Title A–Z',
                onRemove: () =>
                  update({ [key]: catalogSearch({}, domain)[key] }),
              }))}
          />
          <FilterResults
            filtered={activeCount > 0 || !!search.q}
            onClear={() =>
              update({
                q: '',
                status: 'all',
                related: 'all',
                sort: catalogSearch({}, domain).sort,
              })
            }
          >
            <p role="status" className={styles.summary}>
              {domain === 'categories' && search.view !== 'cards'
                ? entries.length
                : visible.length}{' '}
              {domain} shown
              {activeCount || search.q ? ' · Filters applied' : ''}
            </p>
          </FilterResults>
          {!entries.length ? (
            <EmptyState title={`No matching ${domain}`}>
              Try a different search or clear your filters.
            </EmptyState>
          ) : (
            <div ref={stageRef} data-product-epoch={session.epoch}>
              {domain === 'categories' ? (
                <CategoryBrowser
                  key={filterKey}
                  session={session}
                  namespace={namespace}
                  search={search}
                  matches={entries}
                  renderRow={row}
                  cardEntries={visible}
                  cardsHasMore={connection.pageInfo.hasNextPage}
                  onLoadMore={loadMore}
                  onBeforeChange={capture}
                  gridClassName={styles.list}
                  onSelect={(selected) => update({ selected })}
                />
              ) : (
                <div className={styles.list} data-view={search.view}>
                  {visible.map(row)}
                </div>
              )}
            </div>
          )}
          {domain !== 'categories' && connection.pageInfo.hasNextPage && (
            <div className={styles.load}>
              <ActionButton onPress={loadMore}>Load more</ActionButton>
            </div>
          )}
          <FilterSheet
            title={`Filter ${domain}`}
            open={filtersOpen}
            onClose={() => setFiltersOpen(false)}
            onClear={() => setDraft(catalogSearch({}, domain))}
            onApply={() => {
              update(draft)
              setFiltersOpen(false)
            }}
            applyLabel="Apply filters"
          >
            {fields(draft, (patch) =>
              setDraft((current) => ({ ...current, ...patch })),
            )}
          </FilterSheet>
        </>
      )}
    </HomePage>
  )
}
