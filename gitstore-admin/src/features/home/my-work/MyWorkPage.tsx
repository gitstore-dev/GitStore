import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import {
  Button,
  Heading,
  Input,
  Label,
  SearchField,
  Tab,
  TabList,
  TabPanel,
  Tabs,
  TextArea,
  TextField,
} from 'react-aria-components'
import {
  ArrowUpRight,
  Clock,
  FilePenLine,
  Send,
  ClipboardCheck,
  CircleCheck,
  Search,
} from 'lucide-react'
import { ActionButton } from '../../../components/controls'
import { demoUser, namespaces } from '../../../data/demo'
import type { NamespaceId } from '../../../data/demo'
import { usePageInspector } from '../../../shell/PageInspector'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { useGridMotion } from '../shared/motion/useGridMotion'
import { useResourceLighting } from '../shared/motion/useResourceLighting'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import {
  workChangeCount,
  workTitle,
  sameText,
  tabLabels,
  validText,
  workCounts,
  workList,
  workReadiness,
  workTabs,
} from './model'
import type {
  ProductText,
  WorkAction,
  WorkScenario,
  WorkSort,
  WorkTab,
} from './model'
import { formatWorkTime, WorkInspector } from './WorkInspector'
import styles from './my-work.module.css'
import { kindLabels } from '../overview/presentation'
import { WorkBadge } from './WorkBadge'
import { EmptyState } from '../shared/EmptyState'
import { HomePage, HomePageHeader } from '../shared/HomePage'
import { ResourceCard, SummaryCardContent } from '../shared/HomeCards'
import homeStyles from '../shared/home.module.css'
import { ConfirmationDialog } from '../../../components/ConfirmationDialog'
type Confirmation =
  'submit' | 'discard' | 'approve' | 'request-changes' | 'unsaved'
const titles: Record<Confirmation, string> = {
  submit: 'Submit demo draft?',
  discard: 'Discard demo draft?',
  approve: 'Approve demo changes?',
  'request-changes': 'Request demo changes?',
  unsaved: 'Discard unsaved edits?',
}
export function MyWorkPage({
  namespace,
  scenario,
  initialTab = 'drafts',
}: {
  namespace: NamespaceId
  scenario?: WorkScenario
  initialTab?: WorkTab
}) {
  const {
    session,
    dispatch: homeDispatch,
    reset: requestReset,
  } = useHomeDemo(namespace)
  const state = session.work
  const dispatch = useCallback(
    (action: Exclude<WorkAction, { type: 'reset' }>) =>
      homeDispatch({ type: 'work', action }),
    [homeDispatch],
  )
  const [tab, setTab] = useState<WorkTab>(initialTab)
  const [query, setQuery] = useState(
    scenario === 'filtered-empty' ? 'No matching product' : '',
  )
  const [sort, setSort] = useState<WorkSort>('updated')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const [note, setNote] = useState('')
  const [announcement, setAnnouncement] = useState('')
  const [recovered, setRecovered] = useState(false)
  const pending = useRef<(() => void) | null>(null)
  const selected = state.items.find((item) => item.id === selectedId)
  const buffer = selectedId ? (session.buffers[selectedId] ?? null) : null
  const setBuffer = useCallback(
    (text: ProductText | null) => {
      if (selectedId) homeDispatch({ type: 'buffer', id: selectedId, text })
    },
    [homeDispatch, selectedId],
  )
  const current = selected ? session.catalog[selected.resourceId] : undefined
  const staleBase =
    !!selected &&
    selected.entryType === 'product-text' &&
    current?.kind === 'Product' &&
    !sameText(selected.base, {
      body: current.body,
      spec: {
        title: current.spec.title,
      },
    })
  const dirty =
    !!selected &&
    selected.entryType === 'product-text' &&
    !!buffer &&
    !sameText(selected.proposed, buffer)
  const readOnly = scenario === 'read-only'
  const counts = workCounts(state.items)
  const items = workList(state.items, tab, query, sort)
  const { stage, capture } = useGridMotion()
  const lighting = useResourceLighting()
  const request = useCallback(
    (action: () => void) => {
      if (dirty) {
        pending.current = action
        setConfirmation('unsaved')
      } else action()
    },
    [dirty],
  )
  const focusHeading = useCallback(() => {
    requestAnimationFrame(() =>
      (
        document.getElementById('work-inspector-heading') ??
        document.getElementById('my-work-heading')
      )?.focus(),
    )
  }, [])
  const ask = useCallback((action: Confirmation) => {
    setNote('')
    setConfirmation(action)
  }, [])
  const act = useCallback(
    (action: Exclude<WorkAction, { type: 'reset' }>, message: string) => {
      capture()
      dispatch(action)
      setBuffer(null)
      setAnnouncement(message)
      focusHeading()
    },
    [capture, focusHeading, dispatch, setBuffer],
  )
  const save = useCallback(() => {
    if (selected && buffer && validText(buffer) && !readOnly)
      act(
        { type: 'save', id: selected.id, text: buffer },
        'Demo draft saved. Your store has not been changed.',
      )
  }, [selected, buffer, readOnly, act])
  const cancelEdit = useCallback(
    () =>
      request(() => {
        setBuffer(null)
        focusHeading()
      }),
    [request, focusHeading, setBuffer],
  )
  const edit = useCallback(() => {
    if (selected?.entryType === 'product-text')
      setBuffer(structuredClone(selected.proposed))
  }, [selected, setBuffer])
  const content = useMemo(
    () =>
      selected ? (
        <WorkInspector
          namespace={namespace}
          item={selected}
          buffer={buffer}
          readOnly={readOnly}
          catalog={session.catalog}
          staleBase={staleBase}
          onEdit={edit}
          onText={setBuffer}
          onSave={save}
          onCancel={cancelEdit}
          onAction={ask}
        />
      ) : null,
    [
      namespace,
      selected,
      buffer,
      readOnly,
      edit,
      save,
      cancelEdit,
      ask,
      session.catalog,
      staleBase,
      setBuffer,
    ],
  )
  const inspector = usePageInspector(content)
  const { owner, registerCloseGuard } = inspector
  useEffect(() => {
    registerCloseGuard(owner, (proceed) =>
      request(() => {
        setBuffer(null)
        proceed()
      }),
    )
    return () => registerCloseGuard(owner, null)
  }, [owner, registerCloseGuard, request, setBuffer])
  function select(id: string, trigger: HTMLElement) {
    if (id === selectedId) {
      inspector.open(trigger)
      focusHeading()
      return
    }
    request(() => {
      if (selectedId !== id) setBuffer(null)
      setSelectedId(id)
      inspector.open(trigger)
      focusHeading()
    })
  }
  function changeTab(next: WorkTab) {
    request(() => {
      capture()
      setBuffer(null)
      setTab(next)
    })
  }
  function confirm() {
    const action = confirmation
    setConfirmation(null)
    if (action === 'unsaved') {
      setBuffer(null)
      const proceed = pending.current
      pending.current = null
      proceed?.()
      return
    }
    if (!selected || !action || readOnly) return
    if (action === 'request-changes')
      act(
        { type: action, id: selected.id, note },
        'Demo review completed: changes requested. No changes were published.',
      )
    else
      act(
        { type: action, id: selected.id },
        action === 'submit'
          ? 'Demo draft submitted for review. Your store has not been changed.'
          : action === 'discard'
            ? 'Demo draft discarded. Your store has not been changed.'
            : 'Demo review completed: approved. No changes were published.',
      )
    if (action === 'discard') setSelectedId(null)
  }
  const namespaceName = namespaces.find((item) => item.id === namespace)!.name
  return (
    <HomePage ref={lighting}>
      <HomePageHeader
        namespace={namespaceName}
        title="My work"
        headingId="my-work-heading"
        description={`Drafts, reviews, and recovery for ${demoUser.name}.`}
        onReset={requestReset}
      />
      <p role="status" aria-live="polite" className={styles.srOnly}>
        {announcement}
      </p>
      {scenario === 'loading' ? (
        <section
          aria-busy="true"
          aria-label="Loading My work"
          className={styles.empty}
        >
          <h2>Loading your work</h2>
          <p>Presentation example; no network request is running.</p>
        </section>
      ) : scenario === 'unavailable' && !recovered ? (
        <section className={styles.empty}>
          <h2>My work is unavailable</h2>
          <p>Presentation example. Your store has not been contacted.</p>
          <ActionButton
            onPress={() => {
              setRecovered(true)
              setAnnouncement('Demo work examples restored.')
            }}
          >
            Retry demo
          </ActionButton>
        </section>
      ) : (
        <>
          {readOnly && (
            <p className={styles.notice}>
              Read-only presentation example. No permissions or authentication
              are enforced.
            </p>
          )}
          <Tabs
            selectedKey={tab}
            onSelectionChange={(key) => changeTab(key as WorkTab)}
            keyboardActivation="manual"
            className={styles.tabs}
          >
            <TabList
              aria-label="My work views"
              className={homeStyles.metrics}
              data-empty={state.items.length === 0 || undefined}
            >
              {workTabs.map((key) => (
                <Tab
                  key={key}
                  id={key}
                  className={homeStyles.metric}
                  data-halo-card={counts[key] ? '' : undefined}
                  data-readiness={
                    counts[key]
                      ? {
                          drafts: 'stale',
                          submitted: 'reconciling',
                          assigned: 'blocked',
                          completed: 'ready',
                        }[key]
                      : undefined
                  }
                >
                  <SummaryCardContent
                    label={tabLabels[key]}
                    value={counts[key]}
                    detail={
                      {
                        drafts: 'Continue editing your product text',
                        submitted: 'Changes you have sent for review',
                        assigned: 'Reviews waiting for your response',
                        completed: 'Your completed work and reviews',
                      }[key]
                    }
                    Icon={
                      {
                        drafts: FilePenLine,
                        submitted: Send,
                        assigned: ClipboardCheck,
                        completed: CircleCheck,
                      }[key]
                    }
                    ratio={
                      state.items.length ? counts[key] / state.items.length : 0
                    }
                  />
                </Tab>
              ))}
            </TabList>
            {state.items.length > 0 && (
              <div className={styles.toolbar}>
                <SearchField
                  aria-label="Search my work"
                  value={query}
                  onChange={(value) => {
                    capture()
                    setQuery(value)
                  }}
                  className={styles.search}
                >
                  <Search size={16} aria-hidden="true" />
                  <Input placeholder="Search by item title" />
                  <Button aria-label="Clear search">×</Button>
                </SearchField>
                <ChoiceSelect
                  label="Sort work"
                  value={sort}
                  onChange={(value) => {
                    capture()
                    setSort(value as WorkSort)
                  }}
                  options={[
                    { id: 'updated', label: 'Last updated' },
                    { id: 'title', label: 'Title' },
                  ]}
                />
              </div>
            )}
            <TabPanel id={tab} className={styles.panel}>
              <div ref={stage} className={homeStyles.resourceStage}>
                {items.length ? (
                  <div className={homeStyles.resourceGrid}>
                    {items.map((item) => (
                      <ResourceCard
                        key={item.id}
                        id={item.id}
                        title={workTitle(item)}
                        kind={kindLabels[item.resourceKind].singular}
                        headingLevel={2}
                        state={workReadiness(item)}
                        badge={<WorkBadge item={item} />}
                        description={
                          <>
                            {session.buffers[item.id] && (
                              <span>Unsaved edits · </span>
                            )}
                            {workChangeCount(item)}{' '}
                            {workChangeCount(item) === 1 ? 'field' : 'fields'}{' '}
                            changed ·{' '}
                            {item.status === 'assigned'
                              ? `From ${item.author}`
                              : `Reviewer: ${item.reviewer}`}
                            {item.reviewNote && (
                              <p className={styles.reviewNote}>
                                {item.reviewNote}
                              </p>
                            )}
                          </>
                        }
                        meta={
                          <time
                            dateTime={item.updatedAt}
                            className={styles.cardTime}
                          >
                            <Clock size={12} aria-hidden="true" />
                            {formatWorkTime(item.updatedAt)}
                          </time>
                        }
                        onInspect={(trigger) => select(item.id, trigger)}
                      />
                    ))}
                  </div>
                ) : (
                  <EmptyState
                    title={
                      query.trim()
                        ? 'No matching results'
                        : !state.items.length
                          ? 'No work yet'
                          : {
                              drafts: 'No drafts',
                              submitted: 'No submitted changes',
                              assigned: 'No reviews assigned to you',
                              completed: 'No completed work',
                            }[tab]
                    }
                    actions={
                      query.trim() ? (
                        <ActionButton
                          onPress={() => {
                            capture()
                            setQuery('')
                          }}
                        >
                          Clear search
                        </ActionButton>
                      ) : !state.items.length ? (
                        <Link
                          to="/w/$namespace/home/overview"
                          params={{ namespace }}
                        >
                          Open Overview{' '}
                          <ArrowUpRight size={13} aria-hidden="true" />
                        </Link>
                      ) : undefined
                    }
                  >
                    {query.trim()
                      ? `No items in ${tabLabels[tab].toLowerCase()} match “${query}”.`
                      : !state.items.length
                        ? 'Your drafts and assigned reviews will appear here.'
                        : 'There is nothing in this view. Choose another tab to see your other work.'}
                  </EmptyState>
                )}
              </div>
            </TabPanel>
          </Tabs>
        </>
      )}
      <ConfirmationDialog
        isOpen={confirmation !== null}
        onOpenChange={(open) => {
          if (!open) {
            setConfirmation(null)
            pending.current = null
          }
        }}
      >
        <span className={styles.eyebrow}>LOCAL DEMO ACTION</span>
        <Heading slot="title">
          {confirmation ? titles[confirmation] : ''}
        </Heading>
        <p>
          {confirmation === 'unsaved'
            ? 'Your unsaved product text will be discarded. The last saved demo draft will remain.'
            : confirmation === 'discard'
              ? `Remove the saved demo draft for ${selected ? workTitle(selected) : ''}.`
              : confirmation === 'submit'
                ? `Move the saved draft for ${selected ? workTitle(selected) : ''} to Submitted.`
                : `Complete your demo review for ${selected ? workTitle(selected) : ''}. This does not publish the product.`}
        </p>
        {confirmation === 'request-changes' && (
          <TextField
            isRequired
            value={note}
            onChange={setNote}
            className={styles.field}
          >
            <Label>Review note</Label>
            <TextArea rows={4} />
          </TextField>
        )}
        <p className={styles.muted}>
          This changes the shared demo session in this tab. Your store and saved
          files are not changed.
        </p>
        <div className={styles.dialogActions}>
          <ActionButton
            autoFocus
            onPress={() => {
              setConfirmation(null)
              pending.current = null
            }}
          >
            Cancel
          </ActionButton>
          <ActionButton
            variant="primary"
            isDisabled={confirmation === 'request-changes' && !note.trim()}
            onPress={confirm}
          >
            {confirmation === 'unsaved'
              ? 'Discard unsaved edits'
              : 'Confirm demo action'}
          </ActionButton>
        </div>
      </ConfirmationDialog>
    </HomePage>
  )
}
