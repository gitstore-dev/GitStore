import { useCallback, useMemo, useReducer, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Button, Radio, RadioGroup } from 'react-aria-components'
import {
  ArrowUpRight,
  ArrowDownWideNarrow,
  Boxes,
  CircleCheck,
  CircleDot,
  Compass,
  GitBranch,
  Layers,
  RefreshCw,
  TriangleAlert,
} from 'lucide-react'
import { ActionButton } from '../../../components/controls'
import { namespaces } from '../../../data/demo'
import type { NamespaceId } from '../../../data/demo'
import { destinationById } from '../../../navigation/registry'
import { usePageInspector } from '../../../shell/PageInspector'
import { overviewFixture } from './fixtures'
import {
  overviewReducer,
  initialOverview,
  overviewSummary,
  queueCounts,
  workQueue,
  healthMatrix,
} from './model'
import type { OverviewScenario, QueueFilter, SortOrder } from './model'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { ResourceInspector } from './ResourceInspector'
import { StatusBadge } from './StatusBadge'
import { kindLabels, readinessLabels } from './presentation'
import { useGridMotion } from '../shared/motion/useGridMotion'
import { useResourceLighting } from '../shared/motion/useResourceLighting'
import styles from './overview.module.css'
import { HomePage, HomePageHeader, DemoNotice } from '../shared/HomePage'
import { ResourceCard, SummaryCard } from '../shared/HomeCards'
import homeStyles from '../shared/home.module.css'

const filters: readonly { id: QueueFilter; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'failed', label: readinessLabels.failed },
  { id: 'blocked', label: readinessLabels.blocked },
  { id: 'stale', label: readinessLabels.stale },
  { id: 'reconciling', label: readinessLabels.reconciling },
]
export function OverviewPage({
  namespace,
  scenario,
  lighting: lightingMode = 'auto',
}: {
  namespace: NamespaceId
  scenario?: OverviewScenario
  lighting?: 'auto' | 'css'
}) {
  const fixture = useMemo(
    () => overviewFixture(namespace, scenario),
    [namespace, scenario],
  )
  const [state, dispatch] = useReducer(
    overviewReducer,
    fixture,
    initialOverview,
  )
  const [filter, setFilter] = useState<QueueFilter>(
    scenario === 'filtered-empty' ? 'failed' : 'all',
  )
  const [sort, setSort] = useState<SortOrder>('severity')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [announcement, setAnnouncement] = useState('')
  const [recovered, setRecovered] = useState(false)
  const name = namespaces.find((item) => item.id === namespace)!.name
  const queue = workQueue(state.resources, filter, sort)
  const { stage: gridStageRef, capture: captureGrid } = useGridMotion()
  const lighting = useResourceLighting(lightingMode)
  const select = useCallback((id: string) => setSelectedId(id), [])
  const resolve = useCallback(
    (id: string) => {
      captureGrid()
      dispatch({ type: 'resolve', id })
      setAnnouncement(
        'Demo change applied. The item is ready; totals and activity have been updated. Your store has not been changed.',
      )
    },
    [captureGrid],
  )
  const resource = state.resources.find((item) => item.id === selectedId)
  const details = useMemo(
    () =>
      resource ? (
        <ResourceInspector
          key={resource.id}
          resource={resource}
          resources={state.resources}
          namespace={namespace}
          onSelect={select}
          onResolve={resolve}
        />
      ) : null,
    [resource, state.resources, namespace, select, resolve],
  )
  const inspector = usePageInspector(details)
  const inspect = (id: string, trigger: HTMLElement) => {
    setSelectedId(id)
    inspector.open(trigger)
  }
  const summary = overviewSummary(state.resources)
  const counts = queueCounts(state.resources)
  const matrix = healthMatrix(state.resources)
  const completed = fixture.setup.filter((step) => step.complete).length
  const presentation = recovered ? 'populated' : scenario
  function reset() {
    captureGrid()
    dispatch({ type: 'reset', fixture })
    setFilter('all')
    setSort('severity')
    setSelectedId(null)
    inspector.close()
    setAnnouncement('Demo reset. The original snapshot has been restored.')
  }
  const cards = [
    {
      label: 'Total items',
      value: summary.total,
      detail: 'Store content and configuration',
      Icon: Boxes,
      state: 'neutral',
    },
    {
      label: 'Needs attention',
      value: summary.attention,
      detail: 'Issues or outdated information',
      Icon: TriangleAlert,
      state: 'blocked',
    },
    {
      label: 'Processing',
      value: summary.reconciling,
      detail: 'Updates in progress in this demo',
      Icon: RefreshCw,
      state: 'reconciling',
    },
    {
      label: 'Ready',
      value: summary.ready,
      detail: 'Ready in this demo snapshot',
      Icon: CircleCheck,
      state: 'ready',
    },
  ]
  return (
    <HomePage ref={lighting}>
      <div
        className="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {announcement}
      </div>
      <HomePageHeader
        namespace={name}
        title="Overview"
        description="Your store at a glance."
        onReset={reset}
      />
      <DemoNotice page="Overview" />
      {presentation === 'loading' ? (
        <section className={styles.statePanel} aria-label="Loading overview">
          <h2>Loading your store overview.</h2>
          <div className={styles.skeleton} aria-hidden="true" />
          <p role="status">Loading demo overview</p>
        </section>
      ) : presentation === 'unavailable' ? (
        <section className={styles.statePanel} role="alert">
          <TriangleAlert size={30} aria-hidden="true" />
          <h2>Demo data unavailable</h2>
          <p>
            This example shows what happens when store information is
            unavailable.
          </p>
          <ActionButton onPress={() => setRecovered(true)}>
            Reload demo
          </ActionButton>
        </section>
      ) : presentation === 'restricted' ? (
        <section className={styles.statePanel}>
          <Layers size={30} aria-hidden="true" />
          <h2>No access in this preview</h2>
          <p>
            This example shows how Overview looks when access is restricted.
          </p>
          <Link
            className={styles.textLink}
            to="/w/$namespace/home/setup-guide"
            params={{ namespace }}
          >
            Open setup guide
            <ArrowUpRight size={14} aria-hidden="true" />
          </Link>
        </section>
      ) : (
        <>
          {presentation === 'stale' && (
            <div role="status" className={styles.staleNotice}>
              <TriangleAlert size={16} aria-hidden="true" />
              <span>
                <strong>Demo information is out of date.</strong> This example
                shows older store information. It is not a live update.
              </span>
            </div>
          )}
          <section aria-label="Store summary" className={homeStyles.metrics}>
            {cards.map(({ label, value, detail, Icon, state: readiness }) => (
              <SummaryCard
                key={label}
                label={label}
                value={value}
                detail={detail}
                Icon={Icon}
                state={readiness}
                ratio={summary.total ? value / summary.total : 0}
              />
            ))}
          </section>
          {state.resources.length === 0 ? (
            <section
              className={styles.firstUse}
              aria-labelledby="first-resource"
            >
              <div className={styles.firstUseArt} aria-hidden="true">
                <GitBranch size={70} strokeWidth={1} />
                <span>01 / BEGIN</span>
              </div>
              <div>
                <h2 id="first-resource">
                  Your catalog starts here
                  <span aria-hidden="true" className={homeStyles.titleDot}>
                    .
                  </span>
                </h2>
                <p>
                  {name} is a new demo workspace. Add your store details and
                  create your first product to get started.
                </p>
                <div className={styles.firstUseActions}>
                  <Link
                    className={styles.primaryLink}
                    to="/w/$namespace/home/setup-guide"
                    params={{ namespace }}
                  >
                    Open setup guide
                    <ArrowUpRight size={15} aria-hidden="true" />
                  </Link>
                  <Link
                    className={styles.textLink}
                    to="/w/$namespace/catalog/products"
                    params={{ namespace }}
                  >
                    Explore products
                    <ArrowUpRight size={14} aria-hidden="true" />
                  </Link>
                </div>
              </div>
            </section>
          ) : (
            <section
              className={styles.queueSection}
              aria-labelledby="work-queue-heading"
            >
              <div className={styles.sectionHeading}>
                <div>
                  <h2 id="work-queue-heading">
                    Updates and issues{' '}
                    <span className={styles.count}>{counts.all}</span>
                  </h2>
                </div>
                <ChoiceSelect
                  label="Sort by"
                  value={sort}
                  onChange={(value) => {
                    captureGrid()
                    setSort(value)
                  }}
                  options={[
                    { id: 'severity', label: 'Priority first' },
                    { id: 'name', label: 'Name A–Z' },
                  ]}
                />
              </div>
              <div className={styles.filtersRow}>
                <RadioGroup
                  aria-label="Filter updates and issues"
                  orientation="horizontal"
                  value={filter}
                  onChange={(value) => {
                    captureGrid()
                    setFilter(value as QueueFilter)
                  }}
                  className={styles.filters}
                >
                  {filters.map((item) => (
                    <Radio
                      key={item.id}
                      value={item.id}
                      className={styles.filter}
                    >
                      <span>{item.label}</span>
                      <span className={styles.filterCount}>
                        {counts[item.id]}
                      </span>
                    </Radio>
                  ))}
                </RadioGroup>
                {filter !== 'all' && (
                  <ActionButton
                    variant="ghost"
                    onPress={() => {
                      captureGrid()
                      setFilter('all')
                    }}
                  >
                    Clear filters
                  </ActionButton>
                )}
              </div>
              <div ref={gridStageRef} className={homeStyles.resourceStage}>
                {queue.length ? (
                  <div className={homeStyles.resourceGrid}>
                    {queue.map((item) => (
                      <ResourceCard
                        key={item.id}
                        id={item.id}
                        title={item.name}
                        kind={kindLabels[item.kind].singular}
                        badge={<StatusBadge state={item.readiness} />}
                        description={item.diagnostic}
                        state={item.readiness}
                        onInspect={(trigger) => inspect(item.id, trigger)}
                      />
                    ))}
                  </div>
                ) : (
                  <div className={styles.emptyQueue}>
                    <CircleCheck size={30} aria-hidden="true" />
                    <h3>
                      {counts.all === 0
                        ? 'Nothing needs attention.'
                        : 'No matching items.'}
                    </h3>
                    <p>
                      {counts.all === 0
                        ? 'All items in this demo are ready. There are no updates or issues to review.'
                        : 'Try another status or clear the filter to see all updates and issues.'}
                    </p>
                    {counts.all > 0 && (
                      <ActionButton
                        onPress={() => {
                          captureGrid()
                          setFilter('all')
                        }}
                      >
                        Clear filters
                      </ActionButton>
                    )}
                  </div>
                )}
              </div>
            </section>
          )}
          {state.resources.length > 0 && (
            <section
              className={styles.healthSection}
              aria-labelledby="resource-health-heading"
            >
              <div className={styles.sectionHeading}>
                <div>
                  <h2 id="resource-health-heading">Store status</h2>
                </div>
                <span className={styles.snapshotLabel}>
                  Demo snapshot · 2 Nov 2026
                </span>
              </div>
              <div
                className={styles.tableScroll}
                role="region"
                aria-label="Store status table"
                tabIndex={0}
              >
                <table className={styles.healthTable}>
                  <caption className="sr-only">
                    Item totals by type and status
                  </caption>
                  <thead>
                    <tr>
                      <th scope="col">Item type</th>
                      <th scope="col">Total</th>
                      <th scope="col">Ready</th>
                      <th scope="col">Processing</th>
                      <th scope="col">Needs attention</th>
                    </tr>
                  </thead>
                  <tbody>
                    {matrix.map((row) => (
                      <tr key={row.kind}>
                        <th scope="row">
                          <span className={styles.kindDot} />
                          {kindLabels[row.kind].plural}
                        </th>
                        <td>{row.total}</td>
                        <td className={styles.readyNumber}>{row.ready}</td>
                        <td>{row.reconciling || '—'}</td>
                        <td
                          className={
                            row.attention ? styles.attentionNumber : undefined
                          }
                        >
                          {row.attention || '—'}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          )}
          <div className={styles.bottomGrid}>
            <section
              className={styles.activitySection}
              aria-labelledby="recent-resource-activity"
            >
              <div className={styles.sectionHeading}>
                <div>
                  <h2 id="recent-resource-activity">Recent activity</h2>
                </div>
                <Link
                  className={styles.iconLink}
                  aria-label="Open recent activity"
                  to="/w/$namespace/home/recent-activity"
                  params={{ namespace }}
                >
                  <ArrowUpRight size={18} aria-hidden="true" />
                </Link>
              </div>
              {state.activity.length ? (
                <ol className={styles.activity}>
                  {state.activity.slice(0, 5).map((item) => (
                    <li key={item.id}>
                      <span className={styles.activityDot} />
                      <div>
                        <Button
                          className={styles.activityButton}
                          onPress={(event) =>
                            inspect(
                              item.resourceId,
                              event.target as HTMLElement,
                            )
                          }
                        >
                          {item.title}
                        </Button>
                        <small>
                          {item.local ? 'LOCAL DEMO CHANGE' : item.time}
                        </small>
                      </div>
                    </li>
                  ))}
                </ol>
              ) : (
                <div className={styles.emptyActivity}>
                  <ArrowDownWideNarrow size={22} aria-hidden="true" />
                  <p>No activity yet.</p>
                  <small>
                    Changes to your products, collections, and media will appear
                    here.
                  </small>
                </div>
              )}
            </section>
            <section
              className={styles.setupSection}
              aria-labelledby="setup-progress"
            >
              <div className={styles.sectionHeading}>
                <div>
                  <h2 id="setup-progress">Store setup</h2>
                </div>
                <Compass size={23} aria-hidden="true" />
              </div>
              <div className={styles.progressLabel}>
                <span>
                  {completed} of {fixture.setup.length} demo steps complete
                </span>
                <strong>
                  {Math.round((completed / fixture.setup.length) * 100)}%
                </strong>
              </div>
              <progress
                aria-label="Demo setup progress"
                value={completed}
                max={fixture.setup.length}
                className={styles.progress}
              />
              <ul className={styles.steps}>
                {fixture.setup.map((step) => {
                  const destination = destinationById(step.destinationId)
                  return (
                    <li key={step.destinationId}>
                      <span
                        className={
                          step.complete
                            ? styles.stepComplete
                            : styles.stepPending
                        }
                        aria-label={step.complete ? 'Complete' : 'Not complete'}
                        role="img"
                      >
                        {step.complete ? (
                          <CircleCheck size={15} aria-hidden="true" />
                        ) : (
                          <CircleDot size={15} aria-hidden="true" />
                        )}
                      </span>
                      <Link to={destination.to} params={{ namespace }}>
                        {step.label}
                        <ArrowUpRight size={13} aria-hidden="true" />
                      </Link>
                    </li>
                  )
                })}
              </ul>
              <p className={styles.disclaimer}>
                Example setup progress. These links open the existing
                destination placeholders.
              </p>
            </section>
          </div>
        </>
      )}
    </HomePage>
  )
}
