import { useLayoutEffect, useState } from 'react'
import {
  Link,
  useRouter,
  useNavigate,
  useLocation,
  useSearch,
} from '@tanstack/react-router'
import {
  Tabs,
  TabList,
  Tab,
  TabPanel,
  SearchField,
  Label,
  Input,
  Tooltip,
  TooltipTrigger,
} from 'react-aria-components'
import {
  List,
  LayoutGrid,
  Search,
  SlidersHorizontal,
  Shapes,
} from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { HomePage, HomePageHeader } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { usePageInspector } from '../../../shell/PageInspector'
import { useGridMotion } from '../../home/shared/motion/useGridMotion'
import {
  useCatalogView,
  rememberCatalogView,
} from '../../../state/catalog-view'
import { ActionButton } from '../../../components/controls'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { FilterBar } from '../../../components/FilterBar'
import { FilterResults } from '../../../components/FilterResults'
import { FilterSheet } from '../../../components/FilterSheet'
import { ActiveFilters } from '../../../components/ActiveFilters'
import {
  structureEntries,
  structureSearch,
  structurePaths,
  filterStructure,
  structureConnection,
  productTitle,
} from './model'
import type { StructureDomain, StructureSearch, StructureTab } from './model'
import { StructureLink } from './StructureLink'
import { DesignNotice } from './DesignNotice'
import { MediaPreview } from '../shared/MediaPreview'
import { CardMediaBackdrop } from '../shared/CardMediaBackdrop'
import catalog from '../browse/catalog.module.css'
import styles from './structure.module.css'
export function StructureListPage({
  namespace,
  domain,
}: {
  namespace: NamespaceId
  domain: StructureDomain
}) {
  const { session, reset, announce } = useHomeDemo(namespace),
    router = useRouter(),
    navigate = useNavigate(),
    location = useLocation()
  const search = structureSearch(useSearch({ strict: false }), domain),
    scenario = router.options.context.structureScenario
  const motion = router.options.context.productNavigation
  const entries = structureEntries(session, search.tab, scenario),
    matches = filterStructure(entries, search, session, scenario)
  const key = JSON.stringify([
    namespace,
    domain,
    search.tab,
    search.q,
    search.related,
    search.sort,
    session.epoch,
  ])
  const saved = motion?.position(location.href, session.epoch, key)
  const [loaded, setLoaded] = useState({
    key: saved?.filterKey ?? '',
    cursors: saved?.cursors ?? ([] as readonly string[]),
  })
  const cursors = loaded.key === key ? loaded.cursors : []
  let connection = structureConnection(matches)
  const visible = connection.edges.map((e) => e.node)
  for (const cursor of cursors) {
    if (
      connection.pageInfo.endCursor !== cursor ||
      !connection.pageInfo.hasNextPage
    )
      break
    connection = structureConnection(matches, cursor)
    visible.push(...connection.edges.map((e) => e.node))
  }
  const { stage, capture, cancel } = useGridMotion()
  const [sheet, setSheet] = useState(false),
    [draft, setDraft] = useState(search)
  const pageTitle =
    domain === 'product-types' ? 'Product types' : 'Attributes & options'
  const noun =
    search.tab === 'options'
      ? 'product options'
      : search.tab === 'definitions'
        ? 'definitions'
        : 'product types'
  useCatalogView(domain, search.view)
  usePageInspector(null)
  useLayoutEffect(() => {
    motion?.bind(stage.current)
    motion?.remember(location.href, {
      epoch: session.epoch,
      filterKey: key,
      cursors,
      search,
      scroll: saved?.scroll ?? 0,
    })
    motion?.mounted(location.href, session.epoch)
  })
  function update(patch: Partial<StructureSearch>) {
    // Tabs replace the dataset rather than rearranging the same records.
    // Finish owned motion before React Aria moves focus to the new panel.
    if (patch.tab && patch.tab !== search.tab) cancel()
    else capture()
    if (patch.view) rememberCatalogView(domain, patch.view)
    void navigate({
      to: structurePaths[domain],
      params: { namespace },
      search: { ...search, ...patch },
      resetScroll: false,
      replace: 'q' in patch,
    })
  }
  const related =
    search.tab === 'options'
      ? [
          ...new Set(
            entries.flatMap((e) =>
              e.source === 'option' ? [e.productId] : [],
            ),
          ),
        ].map((id) => ({
          id,
          label: productTitle(session, id) ?? 'Product unavailable',
        }))
      : structureEntries(
          session,
          search.tab === 'types' ? 'definitions' : 'types',
          scenario,
        ).flatMap((e) =>
          e.source !== 'option' ? [{ id: e.exampleId, label: e.title }] : [],
        )
  const relatedLabel =
    search.tab === 'options'
      ? 'Product'
      : search.tab === 'types'
        ? 'Definition example'
        : 'Type example'
  const relatedOptions = [
    {
      id: 'all',
      label: search.tab === 'options' ? 'All Products' : 'All examples',
    },
    ...related,
  ]
  const sorts = [
    {
      id: 'updated',
      label: search.tab === 'options' ? 'Product last updated' : 'Last updated',
    },
    { id: 'title', label: 'Title A–Z' },
  ]
  function fields(
    value: StructureSearch,
    onChange: (patch: Partial<StructureSearch>) => void,
  ) {
    return (
      <>
        <ChoiceSelect
          label={relatedLabel}
          value={value.related}
          options={relatedOptions}
          onChange={(related) => onChange({ related })}
        />
        <ChoiceSelect
          label="Sort by"
          value={value.sort}
          options={sorts}
          onChange={(sort) =>
            onChange({ sort: sort as StructureSearch['sort'] })
          }
        />
      </>
    )
  }
  const filtered =
    !!search.q || search.related !== 'all' || search.sort !== 'updated'
  const body = (
    <>
      {search.tab !== 'options' && (
        <DesignNotice type={search.tab === 'types'} />
      )}
      {scenario === 'loading' || scenario === 'unavailable' ? (
        <EmptyState
          title={
            scenario === 'loading'
              ? `Loading ${noun}`
              : `${pageTitle} unavailable`
          }
        >
          Presentation specimen. No network request is running.
        </EmptyState>
      ) : entries.length === 0 ? (
        <EmptyState
          title={
            search.tab === 'options'
              ? 'No Product options yet'
              : search.tab === 'definitions'
                ? 'No reusable definition examples'
                : 'No Product type examples'
          }
          actions={
            <Link to="/w/$namespace/catalog/products" params={{ namespace }}>
              Open Products
            </Link>
          }
        >
          {search.tab === 'options'
            ? 'Options saved on your Products will appear here. Edit them from the existing Product editor.'
            : 'Design examples for this store will appear here. No definitions or types are assigned to Products.'}
        </EmptyState>
      ) : (
        <>
          <FilterBar
            search={
              <SearchField
                aria-label={
                  domain === 'product-types'
                    ? 'Search product types'
                    : 'Search attributes and options'
                }
                className={catalog.search}
                data-composite-input=""
                value={search.q}
                onChange={(q) => update({ q })}
              >
                <Label className="sr-only">Search by Title</Label>
                <div className={catalog.inline}>
                  <Search size={15} aria-hidden="true" />
                  <Input
                    placeholder={
                      search.tab === 'options'
                        ? 'Search option or Product Title'
                        : 'Search by Title'
                    }
                  />
                </div>
              </SearchField>
            }
            activeCount={
              Number(search.related !== 'all') +
              Number(search.sort !== 'updated')
            }
            onOpen={() => {
              setDraft(search)
              setSheet(true)
            }}
            tools={
              <div className={catalog.tools}>
                {(
                  [
                    { id: 'table', label: 'Rows view', Icon: List },
                    { id: 'grid', label: 'Cards view', Icon: LayoutGrid },
                  ] as const
                ).map(({ id, label, Icon }) => (
                  <TooltipTrigger key={id} delay={500}>
                    <ActionButton
                      aria-label={label}
                      aria-pressed={search.view === id}
                      onPress={() => update({ view: id })}
                    >
                      <Icon size={16} aria-hidden="true" />
                    </ActionButton>
                    <Tooltip className={catalog.viewTooltip}>{label}</Tooltip>
                  </TooltipTrigger>
                ))}
              </div>
            }
          >
            {fields(search, update)}
          </FilterBar>
          <ActiveFilters
            label="Active catalog structure filters"
            filters={[
              ...(search.related !== 'all'
                ? [
                    {
                      id: 'related',
                      label:
                        related.find((e) => e.id === search.related)?.label ??
                        'Unavailable filter',
                      onRemove: () => update({ related: 'all' }),
                    },
                  ]
                : []),
              ...(search.sort !== 'updated'
                ? [
                    {
                      id: 'sort',
                      label: 'Title A–Z',
                      onRemove: () => update({ sort: 'updated' }),
                    },
                  ]
                : []),
            ]}
          />
          <FilterResults
            filtered={filtered}
            onClear={() => update({ q: '', related: 'all', sort: 'updated' })}
          >
            <p role="status">
              {visible.length} {noun} shown
            </p>
          </FilterResults>
          {matches.length === 0 ? (
            <EmptyState
              title={`No matching ${noun}`}
              actions={
                <ActionButton
                  onPress={() =>
                    update({ q: '', related: 'all', sort: 'updated' })
                  }
                >
                  Clear filters
                </ActionButton>
              }
            >
              Try another Title or remove a filter.
            </EmptyState>
          ) : (
            <div
              className={search.view === 'grid' ? catalog.list : styles.rows}
              data-view={search.view}
              data-structure-list=""
            >
              {visible.map((entry) => {
                const chips =
                  entry.source === 'type'
                    ? entry.definitionIds.map(
                        (id) =>
                          structureEntries(
                            session,
                            'definitions',
                            scenario,
                          ).find(
                            (e) =>
                              e.source === 'definition' && e.exampleId === id,
                          )?.title ?? 'Definition example unavailable',
                      )
                    : entry.values
                return (
                  <StructureLink
                    key={entry.id}
                    entry={entry}
                    namespace={namespace}
                    search={search}
                    className={
                      search.view === 'grid' ? catalog.row : styles.row
                    }
                    data-catalog-card={search.view === 'grid' ? '' : undefined}
                    data-resource-id={entry.id}
                    data-product-surface={entry.id}
                  >
                    {search.view === 'grid' && (
                      <CardMediaBackdrop emptyLabel="No preview image" />
                    )}
                    <div
                      className={
                        search.view === 'grid'
                          ? catalog.rowContents
                          : styles.rowContents
                      }
                      data-product-content="row"
                    >
                      <div
                        className={
                          search.view === 'grid'
                            ? catalog.identity
                            : styles.identity
                        }
                      >
                        {search.view === 'grid' ? (
                          <span
                            className={catalog.cardMedia}
                            aria-hidden="true"
                            data-product-image=""
                          >
                            <MediaPreview emptyLabel="No preview image" />
                          </span>
                        ) : entry.source === 'option' ? (
                          <SlidersHorizontal size={22} aria-hidden="true" />
                        ) : (
                          <Shapes size={22} aria-hidden="true" />
                        )}
                        <div>
                          <strong data-product-title="">{entry.title}</strong>
                          <small>
                            {entry.source === 'option'
                              ? productTitle(session, entry.productId)
                              : entry.source === 'definition'
                                ? 'Reusable choice example'
                                : `${entry.productIds.length} example ${entry.productIds.length === 1 ? 'Product' : 'Products'}`}
                          </small>
                        </div>
                      </div>
                      <div className={styles.values}>
                        {chips.slice(0, 3).map((value, index) => (
                          <span key={`${value}-${index}`}>{value}</span>
                        ))}
                        {chips.length > 3 && (
                          <span>+{chips.length - 3} more</span>
                        )}
                        {chips.length === 0 && (
                          <span>
                            {entry.source === 'type'
                              ? 'No definition examples'
                              : 'No offered values'}
                          </span>
                        )}
                      </div>
                      <span className={styles.badge}>
                        {entry.source === 'option'
                          ? 'Product option'
                          : 'Design preview'}
                      </span>
                    </div>
                  </StructureLink>
                )
              })}
            </div>
          )}
          {connection.pageInfo.hasNextPage && (
            <div className={catalog.load}>
              <ActionButton
                onPress={() => {
                  const cursor = connection.pageInfo.endCursor
                  if (cursor) {
                    capture()
                    setLoaded({ key, cursors: [...cursors, cursor] })
                    announce(`More ${noun} loaded.`)
                  }
                }}
              >
                Load more
              </ActionButton>
            </div>
          )}
        </>
      )}
    </>
  )
  const heading = (
    <HomePageHeader
      actions={
        domain === 'attributes-options' ? (
          <TabList
            aria-label="Attributes and options views"
            className={styles.tabList}
          >
            {(['options', 'definitions'] as const).map((tab) => (
              <Tab key={tab} id={tab} className={styles.tab}>
                {tab === 'options' ? 'Product options' : 'Reusable definitions'}
                <span>{structureEntries(session, tab, scenario).length}</span>
              </Tab>
            ))}
          </TabList>
        ) : undefined
      }
      title={pageTitle}
      namespace={namespaces.find((n) => n.id === namespace)!.name}
      section="Catalog"
      description={
        domain === 'product-types'
          ? 'Explore examples of how your catalog could be organized.'
          : 'Browse the choices behind your Products.'
      }
      onReset={reset}
      demoDetails="Saved Product options follow this store’s demo catalog. Reusable definitions and Product types are separate design examples; no assignments or rules are applied."
    />
  )
  return (
    <HomePage className={`${catalog.page} ${styles.page}`}>
      {domain === 'attributes-options' ? (
        <Tabs
          selectedKey={search.tab}
          onSelectionChange={(key) =>
            update({
              tab: key as StructureTab,
              q: '',
              related: 'all',
              sort: 'updated',
            })
          }
          className={styles.tabs}
        >
          {heading}
          <div ref={stage}>
            {(['options', 'definitions'] as const).map((tab) => (
              <TabPanel key={tab} id={tab}>
                {search.tab === tab ? body : null}
              </TabPanel>
            ))}
          </div>
        </Tabs>
      ) : (
        <>
          {heading}
          <div ref={stage}>{body}</div>
        </>
      )}
      <FilterSheet
        title={`Filter ${noun}`}
        open={sheet}
        onClose={() => setSheet(false)}
        onApply={() => {
          update(draft)
          setSheet(false)
        }}
        onClear={() => setDraft({ ...search, related: 'all', sort: 'updated' })}
        applyLabel="Apply filters"
      >
        {fields(draft, (patch) =>
          setDraft((value) => ({ ...value, ...patch })),
        )}
      </FilterSheet>
    </HomePage>
  )
}
