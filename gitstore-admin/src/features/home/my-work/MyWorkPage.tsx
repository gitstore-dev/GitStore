import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from 'react'
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
  Check,
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
import { workFixture } from './fixtures'
import {
  changedFields,
  initialWork,
  sameText,
  tabLabels,
  validText,
  workCounts,
  workList,
  workReadiness,
  workReducer,
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
import { WorkBadge } from './WorkBadge'
import { HomePage, HomePageHeader, DemoNotice } from '../shared/HomePage'
import { ResourceCard, SummaryCardContent } from '../shared/HomeCards'
import homeStyles from '../shared/home.module.css'
import { ConfirmationDialog } from '../../../components/ConfirmationDialog'
type Confirmation =
  'submit' | 'discard' | 'approve' | 'request-changes' | 'reset' | 'unsaved'
const titles: Record<Confirmation, string> = {
  submit: 'Submit demo draft?',
  discard: 'Discard demo draft?',
  approve: 'Approve demo changes?',
  'request-changes': 'Request demo changes?',
  reset: 'Reset My work demo?',
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
  const fixture = useMemo(
    () => workFixture(namespace, scenario),
    [namespace, scenario],
  )
  const [state, dispatch] = useReducer(workReducer, fixture, initialWork)
  const [tab, setTab] = useState<WorkTab>(initialTab)
  const [query, setQuery] = useState(
    scenario === 'filtered-empty' ? 'No matching product' : '',
  )
  const [sort, setSort] = useState<WorkSort>('updated')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [buffer, setBuffer] = useState<ProductText | null>(null)
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const [note, setNote] = useState('')
  const [announcement, setAnnouncement] = useState('')
  const [recovered, setRecovered] = useState(false)
  const pending = useRef<(() => void) | null>(null)
  const selected = state.items.find((item) => item.id === selectedId)
  const dirty = !!selected && !!buffer && !sameText(selected.proposed, buffer)
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
    (action: WorkAction, message: string) => {
      capture()
      dispatch(action)
      setBuffer(null)
      setAnnouncement(message)
      focusHeading()
    },
    [capture, focusHeading],
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
    [request, focusHeading],
  )
  const edit = useCallback(() => {
    if (selected) setBuffer({ ...selected.proposed })
  }, [selected])
  const content = useMemo(
    () =>
      selected ? (
        <WorkInspector
          item={selected}
          buffer={buffer}
          readOnly={readOnly}
          onEdit={edit}
          onText={setBuffer}
          onSave={save}
          onCancel={cancelEdit}
          onAction={ask}
        />
      ) : null,
    [selected, buffer, readOnly, edit, save, cancelEdit, ask],
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
  }, [owner, registerCloseGuard, request])
  function select(id: string, trigger: HTMLElement) {
    request(() => {
      setBuffer(null)
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
    if (action === 'reset') {
      capture()
      dispatch({ type: 'reset', items: fixture })
      setBuffer(null)
      setSelectedId(null)
      setTab('drafts')
      setQuery('')
      setSort('updated')
      setAnnouncement('My work demo reset. Original examples restored.')
      focusHeading()
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
        description={`Drafts and reviews for ${demoUser.name}.`}
        onReset={() => request(() => ask('reset'))}
      />
      <DemoNotice page="My work" />
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
            <TabList aria-label="My work views" className={homeStyles.metrics}>
              {workTabs.map((key) => (
                <Tab
                  key={key}
                  id={key}
                  className={homeStyles.metric}
                  data-halo-card=""
                  data-readiness={
                    {
                      drafts: 'stale',
                      submitted: 'reconciling',
                      assigned: 'blocked',
                      completed: 'ready',
                    }[key]
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
                <Input placeholder="Search by product name" />
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
                  { id: 'name', label: 'Product name' },
                ]}
              />
            </div>
            <TabPanel id={tab} className={styles.panel}>
              <div ref={stage} className={homeStyles.resourceStage}>
                {items.length ? (
                  <div className={homeStyles.resourceGrid}>
                    {items.map((item) => (
                      <ResourceCard
                        key={item.id}
                        id={item.id}
                        title={item.proposed.name}
                        kind="Product"
                        headingLevel={2}
                        state={workReadiness(item)}
                        badge={<WorkBadge item={item} />}
                        description={
                          <>
                            {changedFields(item).length}{' '}
                            {changedFields(item).length === 1
                              ? 'field'
                              : 'fields'}{' '}
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
                  <section className={styles.empty}>
                    {query.trim() ? (
                      <>
                        <h2>No matching results</h2>
                        <p>
                          No products in {tabLabels[tab].toLowerCase()} match “
                          {query}”.
                        </p>
                        <ActionButton
                          onPress={() => {
                            capture()
                            setQuery('')
                          }}
                        >
                          Clear search
                        </ActionButton>
                      </>
                    ) : (
                      <>
                        <Check size={28} aria-hidden="true" />
                        <h2>
                          {namespace === 'studio-berlin'
                            ? 'Your work starts here'
                            : {
                                drafts: 'No drafts',
                                submitted: 'No submitted changes',
                                assigned: 'No reviews assigned to you',
                                completed: 'No completed reviews',
                              }[tab]}
                        </h2>
                        <p>
                          {namespace === 'studio-berlin'
                            ? 'Your drafts and assigned reviews will appear here as you build your store.'
                            : 'There is nothing in this view. Try another tab or reset the demo.'}
                        </p>
                        <Link
                          to="/w/$namespace/home/overview"
                          params={{ namespace }}
                        >
                          Open Overview{' '}
                          <ArrowUpRight size={13} aria-hidden="true" />
                        </Link>
                      </>
                    )}
                  </section>
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
            : confirmation === 'reset'
              ? 'Restore the original work examples, counts, and draft text for this store.'
              : confirmation === 'discard'
                ? `Remove the saved demo draft for ${selected?.proposed.name}.`
                : confirmation === 'submit'
                  ? `Move the saved draft for ${selected?.proposed.name} to Submitted.`
                  : `Complete your demo review for ${selected?.proposed.name}. This does not publish the product.`}
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
          This changes only this page’s demo. Your store and saved files are not
          changed.
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
