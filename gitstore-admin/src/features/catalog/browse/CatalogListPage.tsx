import { CardMediaBackdrop } from '../products/CardMediaBackdrop'
import { useMediaQuery } from '../../../state/preferences'
import { ActiveFilters } from '../../../components/ActiveFilters'
import {
  useCatalogView,
  rememberCatalogView,
} from '../../../state/catalog-view'
import { ProductMedia } from '../products/ProductMedia'
import {
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import type { CSSProperties } from 'react'
import {
  Link,
  useLocation,
  useNavigate,
  useRouter,
  useSearch,
} from '@tanstack/react-router'
import { Input, Label, SearchField } from 'react-aria-components'
import {
  ArrowUpRight,
  ChevronDown,
  ChevronRight,
  FolderTree,
  LayoutGrid,
  List,
  Network,
  Package,
  Search,
} from 'lucide-react'
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
import {
  usePageInspector,
  PageInspectorContext,
} from '../../../shell/PageInspector'
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
  categoryHierarchy,
  relatedProductTitle,
} from './model'
import type { CatalogDomain, CatalogEntry, CatalogSearch } from './model'
import { CategorySpatial } from './CategorySpatial'
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
  const tablet = useMediaQuery('(max-width: 1100px)')
  const scenario = router.options.context.catalogScenario
  const search = catalogSearch(useSearch({ strict: false }), domain)
  useCatalogView(domain, search.view)
  const { stage: stageRef, capture } = useGridMotion()
  const lightingRef = useResourceLighting(
    search.view === 'grid' ? 'auto' : 'css',
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
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set())
  const trigger = useRef<HTMLElement | undefined>(undefined)
  const available = scenario !== 'loading' && scenario !== 'unavailable'
  const selectedId = search.selected
  const selected = useMemo(
    () =>
      catalogEntries(session, domain).find((item) => item.id === selectedId),
    [session, domain, selectedId],
  )
  const inspectorContext = useContext(PageInspectorContext)
  const content = useMemo(
    () =>
      selected ? (
        <div className={styles.inspector}>
          <h2 id="category-map-heading" tabIndex={-1}>
            {selected.title}
          </h2>
          <StatusBadge state={selected.status} />
          <p>{selected.resource.diagnostic}</p>
          <p>
            Inspect this category and its related products. Browsing the map
            does not change category relationships.
          </p>
          <Link
            to={catalogDetailPaths[domain]}
            params={{ namespace, itemId: selected.id }}
            search={(previous) => ({
              ...catalogSearch(previous, domain),
              selected: undefined,
            })}
            onClick={() => {
              if (inspectorContext) {
                inspectorContext.registerCloseGuard(
                  inspectorContext.owner,
                  null,
                )
                inspectorContext.close()
              }
            }}
          >
            Open category <ArrowUpRight size={13} />
          </Link>
        </div>
      ) : null,
    [selected, domain, namespace, inspectorContext],
  )
  const { owner, open, close, registerCloseGuard } = usePageInspector(content)
  useEffect(() => {
    registerCloseGuard(owner, (proceed) => {
      proceed()
      void navigate({
        to: catalogPaths[domain],
        params: { namespace },
        search: { ...search, selected: undefined },
        replace: true,
        resetScroll: false,
      })
    })
    return () => registerCloseGuard(owner, null)
  }, [owner, registerCloseGuard, navigate, namespace, domain, search])
  const previousSelection = useRef<string | undefined>(undefined)
  useEffect(() => {
    if (
      location.pathname.replace(/\/$/, '') !==
      `/w/${namespace}/catalog/${domain}`
    )
      return
    if (selected && available) {
      if (!tablet) {
        open(trigger.current)
        requestAnimationFrame(() =>
          document
            .getElementById('category-map-heading')
            ?.focus({ preventScroll: true }),
        )
      }
    } else if (previousSelection.current) close()
    previousSelection.current = selected?.id
  }, [
    selected,
    available,
    open,
    close,
    location.pathname,
    namespace,
    domain,
    tablet,
  ])
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
        data-catalog-card={search.view === 'grid' ? '' : undefined}
        data-halo-card={search.view === 'grid' ? '' : undefined}
      >
        {search.view === 'grid' && (
          <CardMediaBackdrop id={variant?.spec.mediaIds[0]} />
        )}
        <div className={styles.rowContents} data-product-content="row">
          <div className={styles.identity}>
            {search.view === 'grid' ? (
              <span
                className={styles.cardMedia}
                aria-hidden="true"
                data-product-image=""
              >
                <ProductMedia id={variant?.spec.mediaIds[0]} />
              </span>
            ) : domain !== 'categories' ? (
              <span
                className={styles.rowMedia}
                aria-hidden="true"
                data-product-image=""
              >
                <ProductMedia id={variant?.spec.mediaIds[0]} compact />
              </span>
            ) : (
              <Package size={23} aria-hidden="true" />
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
  const hierarchy = categoryHierarchy(session, entries)
  const isHidden = (id: string) => {
    if (search.q || search.related !== 'all' || search.status !== 'all')
      return false
    let current = session.catalog[id]
    const visited = new Set<string>()
    while (current?.kind === 'CategoryTaxonomy' && current.spec.parentId) {
      if (visited.has(current.spec.parentId)) return false
      visited.add(current.spec.parentId)
      if (collapsed.has(current.spec.parentId)) return true
      current = session.catalog[current.spec.parentId]
    }
    return false
  }
  const views =
    domain === 'categories'
      ? [
          { id: 'tree', label: 'Tree view', Icon: FolderTree },
          { id: 'table', label: 'Table view', Icon: List },
          { id: 'spatial', label: 'Spatial view', Icon: Network },
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
                  <ActionButton
                    key={id}
                    aria-label={label}
                    aria-pressed={search.view === id}
                    onPress={() =>
                      update({
                        view: id as CatalogSearch['view'],
                        selected: undefined,
                      })
                    }
                  >
                    <Icon size={16} aria-hidden="true" />
                  </ActionButton>
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
            {selected && tablet && (
              <ActionButton
                onPress={() => {
                  open(trigger.current)
                  requestAnimationFrame(() =>
                    document
                      .getElementById('category-map-heading')
                      ?.focus({ preventScroll: true }),
                  )
                }}
              >
                Inspect selected category
              </ActionButton>
            )}
            <p role="status" className={styles.summary}>
              {search.view === 'tree' || search.view === 'spatial'
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
              {search.view === 'spatial' ? (
                <CategorySpatial
                  session={session}
                  entries={entries}
                  selected={search.selected}
                  onSelect={(selected, element) => {
                    trigger.current = element
                    if (selected === selectedId) {
                      open(element)
                      requestAnimationFrame(() =>
                        document
                          .getElementById('category-map-heading')
                          ?.focus({ preventScroll: true }),
                      )
                    } else update({ selected })
                  }}
                />
              ) : search.view === 'tree' ? (
                <ul className={styles.tree} aria-label="Category hierarchy">
                  {hierarchy
                    .filter(({ item }) => !isHidden(item.id))
                    .map(({ item, depth, problem }) => (
                      <li key={item.id}>
                        <div
                          className={styles.treeRow}
                          style={{ '--depth': depth } as CSSProperties}
                        >
                          {!problem?.includes('cycle') &&
                          entries.some((child) => {
                            const value = session.catalog[child.id]
                            return (
                              value?.kind === 'CategoryTaxonomy' &&
                              value.spec.parentId === item.id
                            )
                          }) ? (
                            <ActionButton
                              aria-label={`${collapsed.has(item.id) ? 'Expand' : 'Collapse'} ${item.title}`}
                              aria-expanded={!collapsed.has(item.id)}
                              onPress={() =>
                                setCollapsed((current) => {
                                  const next = new Set(current)
                                  if (next.has(item.id)) next.delete(item.id)
                                  else next.add(item.id)
                                  return next
                                })
                              }
                            >
                              {collapsed.has(item.id) ? (
                                <ChevronRight size={15} />
                              ) : (
                                <ChevronDown size={15} />
                              )}
                            </ActionButton>
                          ) : (
                            <span className={styles.treeSpacer} />
                          )}
                          {row(item)}
                        </div>
                        {problem && <p className={styles.warning}>{problem}</p>}
                      </li>
                    ))}
                </ul>
              ) : (
                <div className={styles.list} data-view={search.view}>
                  {visible.map(row)}
                </div>
              )}
            </div>
          )}
          {search.view !== 'tree' &&
            search.view !== 'spatial' &&
            connection.pageInfo.hasNextPage && (
              <div className={styles.load}>
                <ActionButton
                  onPress={() => {
                    const cursor = connection.pageInfo.endCursor!
                    capture()
                    setLoaded({ key: filterKey, cursors: [...cursors, cursor] })
                    announce(`More ${domain} loaded.`)
                  }}
                >
                  Load more
                </ActionButton>
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
