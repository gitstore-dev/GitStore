import { ActiveFilters } from '../../../components/ActiveFilters'
import { FilterBar } from '../../../components/FilterBar'
import { FilterResults } from '../../../components/FilterResults'
import { useCallback, useMemo, useState } from 'react'
import { Button, Heading, Input, SearchField } from 'react-aria-components'
import { Clock, Search, History, RotateCcw } from 'lucide-react'
import { ActionButton } from '../../../components/controls'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { ConfirmationDialog } from '../../../components/ConfirmationDialog'
import { namespaces } from '../../../data/demo'
import type { NamespaceId } from '../../../data/demo'
import { usePageInspector } from '../../../shell/PageInspector'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import {
  activityCategories,
  categoryLabels,
  emptyFilters,
  filterActivity,
  groupActivity,
  restoreProblem,
} from '../../demo/model'
import type { ActivityFilters, ActivityScenario } from '../../demo/model'
import { snapshotChanges } from '../../demo/snapshots'
import { ChangePreview } from '../../demo/ChangePreview'
import { kindLabels } from '../overview/presentation'
import { formatWorkTime } from '../my-work/WorkInspector'
import { HomePage, HomePageHeader } from '../shared/HomePage'
import { ResourceCard, StatusPill } from '../shared/HomeCards'
import { useGridMotion } from '../shared/motion/useGridMotion'
import { useResourceLighting } from '../shared/motion/useResourceLighting'
import { EmptyState } from '../shared/EmptyState'
import { ActivityFiltersSheet } from './ActivityFiltersSheet'
import { ActivityInspector } from './ActivityInspector'
import styles from './recent-activity.module.css'
import homeStyles from '../shared/home.module.css'
export function RecentActivityPage({
  namespace,
  scenario,
}: {
  namespace: NamespaceId
  scenario?: ActivityScenario
}) {
  const { session, dispatch, reset } = useHomeDemo(namespace)
  const [filters, setFilters] = useState<ActivityFilters>(emptyFilters)
  const [filtersOpen, setFiltersOpen] = useState(false)
  const [draftFilters, setDraftFilters] =
    useState<ActivityFilters>(emptyFilters)
  const [limit, setLimit] = useState(10)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [announcement, setAnnouncement] = useState('')
  const [lastRecovery, setLastRecovery] = useState<string | null>(null)
  const [retried, setRetried] = useState(false)
  const { stage, capture } = useGridMotion()
  const lighting = useResourceLighting()
  const matches = filterActivity(session.events, filters)
  const groups = groupActivity(matches.slice(0, limit))
  const selected = session.events.find((entry) => entry.id === selectedId)
  const readonly = scenario === 'restricted'
  const focusInspector = useCallback(() => {
    requestAnimationFrame(() =>
      document.getElementById('activity-inspector-heading')?.focus(),
    )
  }, [])
  const askRestore = useCallback(() => setConfirming(true), [])
  const content = useMemo(
    () =>
      selected && !readonly ? (
        <ActivityInspector
          event={selected}
          session={session}
          onRestore={askRestore}
        />
      ) : null,
    [selected, readonly, session, askRestore],
  )
  const inspector = usePageInspector(content)
  function select(id: string, trigger?: HTMLElement) {
    setSelectedId(id)
    inspector.open(trigger)
    focusInspector()
  }
  function filter<K extends keyof ActivityFilters>(
    key: K,
    value: ActivityFilters[K],
  ) {
    capture()
    setFilters((previous) => ({ ...previous, [key]: value }))
    setLimit(10)
  }
  function clear() {
    capture()
    setFilters(emptyFilters)
    setLimit(10)
  }
  const current = selected ? session.catalog[selected.itemId] : undefined
  const problem = selected
    ? restoreProblem(session, selected.id)
    : 'Select an event to restore.'
  const option = (id: string, label = id) => ({ id, label })
  const actors = [...new Set(session.events.map((event) => event.actor))].sort()
  const kinds = [...new Set(session.events.map((event) => event.kind))].sort()
  const dates = [
    ...new Set(session.events.map((event) => event.timestamp.slice(0, 10))),
  ]
    .sort()
    .reverse()
  const filtered = Object.entries(filters).some(([key, value]) =>
    key === 'query' ? value.trim() !== '' : value !== 'all',
  )
  const activeFilters = (['actor', 'kind', 'category', 'date'] as const).filter(
    (key) => filters[key] !== 'all',
  )
  function filterLabel(key: 'actor' | 'kind' | 'category' | 'date') {
    return key === 'kind'
      ? kindLabels[filters.kind as keyof typeof kindLabels].singular
      : key === 'category'
        ? categoryLabels[filters.category as keyof typeof categoryLabels]
        : key === 'date'
          ? formatDate(filters.date)
          : filters.actor
  }
  function renderFilters(
    values: ActivityFilters,
    change: (
      key: 'actor' | 'kind' | 'category' | 'date',
      value: string,
    ) => void,
  ) {
    return (
      <>
        <ChoiceSelect
          label="Actor"
          value={values.actor}
          onChange={(value) => change('actor', value)}
          options={[
            option('all', 'All actors'),
            ...actors.map((actor) => option(actor)),
          ]}
        />
        <ChoiceSelect
          label="Item type"
          value={values.kind}
          onChange={(value) => change('kind', value)}
          options={[
            option('all', 'All item types'),
            ...kinds.map((kind) => option(kind, kindLabels[kind].singular)),
          ]}
        />
        <ChoiceSelect
          label="Event category"
          value={values.category}
          onChange={(value) => change('category', value)}
          options={[
            option('all', 'All events'),
            ...activityCategories.map((category) =>
              option(category, categoryLabels[category]),
            ),
          ]}
        />
        <ChoiceSelect
          label="Recorded date"
          value={values.date}
          onChange={(value) => change('date', value)}
          options={[
            option('all', 'All dates'),
            ...dates.map((date) => option(date, formatDate(date))),
          ]}
        />
      </>
    )
  }
  const storeName = namespaces.find((entry) => entry.id === namespace)!.name
  return (
    <HomePage ref={lighting}>
      <HomePageHeader
        namespace={storeName}
        title="Recent activity"
        headingId="activity-heading"
        description="Changes, reviews, and recovery in your store."
        onReset={reset}
        demoDetails="Demo history through 2 Nov 2026, 12:00 UTC. Session actions appear after this snapshot."
      />
      <p role="status" aria-live="polite" className={styles.srOnly}>
        {announcement}
      </p>
      {scenario === 'loading' ? (
        <section className={styles.empty} aria-busy="true">
          <h2>Loading activity</h2>
          <p>Presentation example; no network request is running.</p>
        </section>
      ) : scenario === 'unavailable' && !retried ? (
        <section className={styles.empty}>
          <h2>Activity is unavailable</h2>
          <p>
            This is a presentation example. Your store has not been contacted.
          </p>
          <ActionButton
            onPress={() => {
              setRetried(true)
              setAnnouncement('Demo history restored.')
            }}
          >
            Retry demo
          </ActionButton>
        </section>
      ) : readonly ? (
        <section className={styles.empty}>
          <h2>Activity is restricted in this preview</h2>
          <p>
            This presentation specimen does not implement authentication or
            enforce permissions.
          </p>
        </section>
      ) : (
        <>
          {scenario === 'stale' && (
            <p className={styles.note}>
              This specimen shows an older demo snapshot. It is not a live feed.
            </p>
          )}
          {lastRecovery && (
            <div className={styles.success}>
              <p>
                Demo recovery recorded. It may be hidden by your current
                filters. Existing drafts were preserved.
              </p>
              <ActionButton
                onPress={() => {
                  clear()
                  select(lastRecovery)
                }}
              >
                View recovery event
              </ActionButton>
            </div>
          )}
          {session.events.length > 0 && (
            <>
              <FilterBar
                activeCount={activeFilters.length}
                onOpen={() => {
                  setDraftFilters(filters)
                  setFiltersOpen(true)
                }}
                search={
                  <SearchField
                    data-composite-input=""
                    className={styles.search}
                    aria-label="Search activity"
                    value={filters.query}
                    onChange={(value) => filter('query', value)}
                  >
                    <Search size={16} aria-hidden="true" />
                    <Input placeholder="Search titles and file names" />
                    <Button aria-label="Clear activity search">×</Button>
                  </SearchField>
                }
              >
                {renderFilters(filters, (key, value) => filter(key, value))}
              </FilterBar>
              <ActiveFilters
                label="Active activity filters"
                filters={activeFilters.map((key) => ({
                  id: key,
                  label: filterLabel(key),
                  onRemove: () => filter(key, 'all'),
                }))}
              />
              <FilterResults filtered={filtered} onClear={clear}>
                <p>
                  {Math.min(limit, matches.length)}{' '}
                  {filtered ? 'matching ' : ''}
                  {Math.min(limit, matches.length) === 1
                    ? 'activity'
                    : 'activities'}{' '}
                  shown
                </p>
              </FilterResults>
            </>
          )}
          <div ref={stage} className={homeStyles.resourceStage}>
            {matches.length ? (
              groups.map((group) => (
                <section
                  key={group.date}
                  className={styles.group}
                  aria-label={`Activity on ${formatDate(group.date)}`}
                >
                  <h2 className={styles.date}>{formatDate(group.date)}</h2>
                  <ol className={styles.timeline} data-activity-timeline="">
                    {group.entries.map((event) => (
                      <li key={event.id}>
                        <ResourceCard
                          state={
                            event.category === 'recovery'
                              ? 'ready'
                              : event.category === 'processing'
                                ? 'reconciling'
                                : 'stale'
                          }
                          id={event.id}
                          title={event.itemTitle}
                          kind={kindLabels[event.kind].singular}
                          badge={
                            <StatusPill
                              label={event.label}
                              state={
                                event.category === 'recovery'
                                  ? 'ready'
                                  : event.category === 'processing'
                                    ? 'reconciling'
                                    : 'stale'
                              }
                              Icon={
                                event.category === 'recovery'
                                  ? RotateCcw
                                  : History
                              }
                            />
                          }
                          description={
                            <>
                              <strong>{event.actor}</strong> ·{' '}
                              <span className={styles.eventDescription}>
                                {event.description}
                              </span>
                              <span className={styles.mobileEventLabel}>
                                {event.label}
                              </span>
                              {event.local && (
                                <p className={styles.local}>
                                  DEMO SESSION CHANGE
                                </p>
                              )}
                            </>
                          }
                          meta={
                            <time
                              className={styles.time}
                              dateTime={event.timestamp}
                            >
                              <Clock size={12} aria-hidden="true" />
                              {formatWorkTime(event.timestamp)}
                            </time>
                          }
                          onInspect={(trigger) => select(event.id, trigger)}
                        />
                      </li>
                    ))}
                  </ol>
                </section>
              ))
            ) : (
              <EmptyState
                title={
                  session.events.length
                    ? 'No matching activity'
                    : 'No activity yet'
                }
                actions={
                  filtered ? (
                    <ActionButton onPress={clear}>Clear filters</ActionButton>
                  ) : undefined
                }
              >
                {session.events.length
                  ? 'Try another title, actor, item type, event category, or date.'
                  : 'Changes and reviews will appear here as you work.'}
              </EmptyState>
            )}
          </div>
          {limit < matches.length && (
            <ActionButton
              className={styles.loadMore}
              onPress={() => {
                capture()
                setLimit((value) => value + 10)
                setAnnouncement(
                  `Loaded ${Math.min(10, matches.length - limit)} more activities. ${Math.min(limit + 10, matches.length)} ${filtered ? 'matching activities' : 'activities'} shown.`,
                )
              }}
            >
              Load more
            </ActionButton>
          )}
        </>
      )}
      <ActivityFiltersSheet
        open={filtersOpen}
        onClose={() => setFiltersOpen(false)}
        onClear={() =>
          setDraftFilters({ ...emptyFilters, query: filters.query })
        }
        onApply={() => {
          capture()
          setFilters({ ...draftFilters, query: filters.query })
          setLimit(10)
          setFiltersOpen(false)
        }}
      >
        {renderFilters(draftFilters, (key, value) =>
          setDraftFilters((previous) => ({ ...previous, [key]: value })),
        )}
      </ActivityFiltersSheet>
      <ConfirmationDialog isOpen={confirming} onOpenChange={setConfirming}>
        <Heading slot="title">Restore this version in demo?</Heading>
        <p>
          This replaces the shared demo item with the authored version recorded
          on {selected && formatWorkTime(selected.timestamp)}. It adds a Recent
          activity event and a Completed recovery entry in My work. Existing
          drafts and unsaved text are preserved.
        </p>
        <p>
          Identity, source paths, readiness, publication state, and media bytes
          are unchanged. Your store and saved files are not changed.
        </p>
        {selected?.after && current && (
          <ChangePreview
            changes={snapshotChanges(current, selected.after, session.catalog)}
            label="Proposed recovery"
          />
        )}
        {problem && (
          <p role="alert" className={styles.note}>
            {problem}
          </p>
        )}
        <div className={styles.actions}>
          <ActionButton autoFocus onPress={() => setConfirming(false)}>
            Cancel
          </ActionButton>
          <ActionButton
            variant="primary"
            isDisabled={!!problem}
            onPress={() => {
              if (!selected || restoreProblem(session, selected.id)) return
              capture()
              dispatch({ type: 'restore', eventId: selected.id })
              setLastRecovery(`demo-${namespace}-${session.revision + 1}`)
              setConfirming(false)
              setAnnouncement(
                'Demo version restored. Overview and My work have been updated. Nothing was published or marked ready.',
              )
              focusInspector()
            }}
          >
            Confirm demo recovery
          </ActionButton>
        </div>
      </ConfirmationDialog>
    </HomePage>
  )
}
function formatDate(date: string) {
  return new Intl.DateTimeFormat('en-GB', {
    dateStyle: 'medium',
    timeZone: 'UTC',
  }).format(new Date(`${date}T12:00:00Z`))
}
