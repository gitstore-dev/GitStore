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
import { LayoutGrid, List, Search } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { HomePage, HomePageHeader } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { FilterBar } from '../../../components/FilterBar'
import { FilterSheet } from '../../../components/FilterSheet'
import { FilterResults } from '../../../components/FilterResults'
import { ActiveFilters } from '../../../components/ActiveFilters'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { ActionButton } from '../../../components/controls'
import {
  useCatalogView,
  rememberCatalogView,
} from '../../../state/catalog-view'
import { usePageInspector } from '../../../shell/PageInspector'
import { StatusBadge } from '../../home/overview/StatusBadge'
import { useGridMotion } from '../../home/shared/motion/useGridMotion'
import { useResourceLighting } from '../../home/shared/motion/useResourceLighting'
import { MediaPreview } from '../shared/MediaPreview'
import { CardMediaBackdrop } from '../shared/CardMediaBackdrop'
import {
  filterMedia,
  mediaConnection,
  mediaEntries,
  mediaSearch,
  mediaFormat,
} from './model'
import type { MediaSearch } from './model'
import catalog from '../browse/catalog.module.css'
import styles from './media.module.css'
const statuses = [
  { id: 'all', label: 'All statuses' },
  { id: 'attention', label: 'Needs attention' },
  { id: 'ready', label: 'Ready' },
  { id: 'failed', label: 'Has errors' },
  { id: 'blocked', label: 'Needs review' },
  { id: 'stale', label: 'Out of date' },
  { id: 'reconciling', label: 'Processing' },
]
const usages = [
  { id: 'all', label: 'All usage' },
  { id: 'used', label: 'Used in this demo' },
  { id: 'unused', label: 'Not used in this demo' },
]
const sorts = [
  { id: 'updated', label: 'Last updated' },
  { id: 'name', label: 'File name A–Z' },
]
export function MediaListPage({ namespace }: { namespace: NamespaceId }) {
  const { session, reset, announce } = useHomeDemo(namespace)
  const router = useRouter(),
    navigate = useNavigate(),
    location = useLocation()
  const search = mediaSearch(useSearch({ strict: false }))
  const scenario = router.options.context.mediaScenario
  const motion = router.options.context.productNavigation
  const entries = mediaEntries(session, scenario),
    matches = filterMedia(entries, search)
  const key = JSON.stringify([
    namespace,
    search.q,
    search.status,
    search.usage,
    search.sort,
    session.epoch,
  ])
  const saved = motion?.position(location.href, session.epoch, key)
  const [loaded, setLoaded] = useState({
    key: saved?.filterKey ?? '',
    cursors: saved?.cursors ?? ([] as readonly string[]),
  })
  const cursors = loaded.key === key ? loaded.cursors : []
  let connection = mediaConnection(matches)
  const visible = connection.edges.map((e) => e.node)
  for (const cursor of cursors) {
    if (
      connection.pageInfo.endCursor !== cursor ||
      !connection.pageInfo.hasNextPage
    )
      break
    connection = mediaConnection(matches, cursor)
    visible.push(...connection.edges.map((e) => e.node))
  }
  const page = useResourceLighting(search.view === 'grid' ? 'auto' : 'css')
  const { stage, capture } = useGridMotion()
  const [sheet, setSheet] = useState(false),
    [draft, setDraft] = useState(search)
  useCatalogView('media', search.view)
  usePageInspector(null)
  useLayoutEffect(() => {
    motion?.bind(page.current)
    motion?.remember(location.href, {
      epoch: session.epoch,
      filterKey: key,
      cursors,
      search,
      scroll: saved?.scroll ?? 0,
    })
    motion?.mounted(location.href, session.epoch)
  })
  function update(patch: Partial<MediaSearch>) {
    capture()
    if (patch.view) rememberCatalogView('media', patch.view)
    void navigate({
      to: '/w/$namespace/catalog/media',
      params: { namespace },
      search: { ...search, ...patch },
      resetScroll: false,
      replace: 'q' in patch,
    })
  }
  const active = (['status', 'usage', 'sort'] as const).filter(
    (k) => search[k] !== mediaSearch({})[k],
  )
  function fields(
    value: MediaSearch,
    onChange: (patch: Partial<MediaSearch>) => void,
  ) {
    return (
      <>
        <ChoiceSelect
          label="Status"
          value={value.status}
          options={statuses}
          onChange={(status) => onChange({ status })}
        />
        <ChoiceSelect
          label="Usage"
          value={value.usage}
          options={usages}
          onChange={(usage) =>
            onChange({ usage: usage as MediaSearch['usage'] })
          }
        />
        <ChoiceSelect
          label="Sort by"
          value={value.sort}
          options={sorts}
          onChange={(sort) => onChange({ sort: sort as MediaSearch['sort'] })}
        />
      </>
    )
  }
  return (
    <HomePage ref={page} className={`${catalog.page} ${styles.page}`}>
      <HomePageHeader
        title="Media"
        namespace={namespaces.find((n) => n.id === namespace)!.name}
        section="Catalog"
        description="Explore the files behind your catalog."
        onReset={reset}
        demoDetails="Explore example files and their catalog links. Changes elsewhere in this demo are shared here. Uploading and editing files are unavailable."
      />
      {scenario === 'loading' || scenario === 'unavailable' ? (
        <EmptyState
          title={scenario === 'loading' ? 'Loading media' : 'Media unavailable'}
        >
          Presentation specimen. No network request is running.
        </EmptyState>
      ) : entries.length === 0 ? (
        <EmptyState
          title="Your media library starts here"
          actions={
            <Link to="/w/$namespace/catalog/products" params={{ namespace }}>
              Open Products
            </Link>
          }
        >
          Files referenced by your catalog will appear here. Uploading is not
          available in this demo.
        </EmptyState>
      ) : (
        <>
          {scenario === 'stale' && (
            <p className={catalog.warning}>
              Earlier demo snapshot. This is not live media data.
            </p>
          )}
          <FilterBar
            search={
              <SearchField
                aria-label="Search media"
                data-composite-input=""
                className={catalog.search}
                value={search.q}
                onChange={(q) => update({ q })}
              >
                <Label className="sr-only">Search media</Label>
                <div className={catalog.inline}>
                  <Search size={15} aria-hidden="true" />
                  <Input placeholder="Search by file name" />
                </div>
              </SearchField>
            }
            activeCount={active.length}
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
            label="Active media filters"
            filters={active.map((id) => ({
              id,
              label:
                [...statuses, ...usages, ...sorts].find(
                  (o) => o.id === search[id],
                )?.label ?? search[id],
              onRemove: () => update({ [id]: mediaSearch({})[id] }),
            }))}
          />
          <FilterResults
            filtered={active.length > 0 || !!search.q}
            onClear={() => update({ ...mediaSearch({}), view: search.view })}
          >
            <p role="status" className={catalog.summary}>
              {visible.length} media files shown
            </p>
          </FilterResults>
          <div ref={stage}>
            {matches.length === 0 ? (
              <EmptyState
                title="No matching media"
                actions={
                  <ActionButton
                    onPress={() =>
                      update({ ...mediaSearch({}), view: search.view })
                    }
                  >
                    Clear filters
                  </ActionButton>
                }
              >
                Try another file name or remove a filter.
              </EmptyState>
            ) : (
              <div
                className={catalog.list}
                data-view={search.view}
                data-media-list=""
              >
                {visible.map((item) => (
                  <Link
                    key={item.id}
                    to="/w/$namespace/catalog/media/$itemId"
                    params={{ namespace, itemId: item.id }}
                    search={search}
                    className={catalog.row}
                    data-product-surface={item.id}
                    data-resource-id={item.id}
                    data-readiness={item.status}
                    data-catalog-card={search.view === 'grid' ? '' : undefined}
                    data-halo-card={search.view === 'grid' ? '' : undefined}
                  >
                    {search.view === 'grid' && (
                      <CardMediaBackdrop
                        id={item.broken ? undefined : item.details.previewId}
                        emptyLabel="No media preview"
                      />
                    )}
                    <div
                      className={catalog.rowContents}
                      data-product-content="row"
                    >
                      <div className={catalog.identity}>
                        <span
                          className={
                            search.view === 'grid'
                              ? catalog.cardMedia
                              : catalog.rowMedia
                          }
                          aria-hidden="true"
                          data-product-image=""
                        >
                          <MediaPreview
                            id={item.details.previewId}
                            broken={item.broken}
                            compact={search.view === 'table'}
                            emptyLabel="No media preview"
                          />
                        </span>
                        <div>
                          <strong data-product-title="">{item.name}</strong>
                          <small>{mediaFormat(item.details.contentType)}</small>
                        </div>
                      </div>
                      <span className={catalog.secondary}>
                        {item.usage.length
                          ? `Used by ${item.usage.length} items in this demo`
                          : 'Not used in this demo'}
                      </span>
                      <span>
                        <StatusBadge state={item.status} />
                      </span>
                    </div>
                  </Link>
                ))}
              </div>
            )}
          </div>
          {connection.pageInfo.hasNextPage && (
            <div className={catalog.load}>
              <ActionButton
                onPress={() => {
                  const cursor = connection.pageInfo.endCursor
                  if (cursor) {
                    capture()
                    setLoaded({ key, cursors: [...cursors, cursor] })
                    announce('More media files loaded.')
                  }
                }}
              >
                Load more media
              </ActionButton>
            </div>
          )}
        </>
      )}
      <FilterSheet
        title="Filter media"
        open={sheet}
        onClose={() => setSheet(false)}
        onClear={() =>
          setDraft({ ...mediaSearch({}), q: search.q, view: search.view })
        }
        onApply={() => {
          update(draft)
          setSheet(false)
        }}
        applyLabel="Apply filters"
      >
        {fields(draft, (patch) =>
          setDraft((value) => ({ ...value, ...patch })),
        )}
      </FilterSheet>
    </HomePage>
  )
}
