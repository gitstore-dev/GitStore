import { useCallback, useEffect, useMemo, useRef } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Button } from 'react-aria-components'
import { ArrowRight, ArrowUpRight, Check, Circle, Compass } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { ActionButton } from '../../../components/controls'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { usePageInspector } from '../../../shell/PageInspector'
import { HomePage, HomePageHeader } from '../shared/HomePage'
import { EmptyState } from '../shared/EmptyState'
import { setupDefinitions, setupProgress } from './model'
import type { SetupReviewId, SetupScenario, SetupStepId } from './model'
import { SetupInspector } from './SetupInspector'
import styles from './setup-guide.module.css'

export function SetupGuidePage({
  namespace,
  step,
  scenario,
}: {
  namespace: NamespaceId
  step?: SetupStepId
  scenario?: SetupScenario
}) {
  const { session, dispatch, reset, announce } = useHomeDemo(namespace)
  const navigate = useNavigate()
  const progress = useMemo(() => setupProgress(session), [session])
  const readOnly = scenario === 'read-only'
  const available = scenario !== 'loading' && scenario !== 'unavailable'
  const selected = available
    ? progress.steps.find((item) => item.id === step)
    : undefined
  const previous = useRef<SetupStepId | undefined>(undefined)
  const trigger = useRef<HTMLElement | undefined>(undefined)
  const onReview = useCallback(
    (id: SetupReviewId, reviewed: boolean) => {
      if (readOnly || !available) return
      dispatch({ type: 'setup-review', step: id, reviewed })
      const title = setupDefinitions.find((item) => item.id === id)!.title
      announce(
        `${title}: ${reviewed ? 'reviewed in demo' : 'marked not reviewed'}. Only your demo checklist changed.`,
      )
      requestAnimationFrame(() =>
        document
          .getElementById('setup-inspector-heading')
          ?.focus({ preventScroll: true }),
      )
    },
    [dispatch, announce, readOnly, available],
  )
  const content = useMemo(
    () =>
      selected ? (
        <SetupInspector
          key={selected.id}
          step={selected}
          namespace={namespace}
          productCount={progress.productCount}
          deletingProductCount={progress.deletingProductCount}
          readOnly={readOnly}
          onReview={onReview}
        />
      ) : null,
    [
      selected,
      namespace,
      progress.productCount,
      progress.deletingProductCount,
      readOnly,
      onReview,
    ],
  )
  const { owner, open, close, registerCloseGuard } = usePageInspector(content)
  useEffect(() => {
    registerCloseGuard(owner, (proceed) => {
      proceed()
      void navigate({
        to: '/w/$namespace/home/setup-guide',
        params: { namespace },
        search: {},
        replace: true,
        resetScroll: false,
      })
    })
    return () => registerCloseGuard(owner, null)
  }, [owner, registerCloseGuard, navigate, namespace])
  useEffect(() => {
    if (selected) {
      open(
        trigger.current ??
          document.getElementById(`setup-task-${selected.id}`) ??
          undefined,
      )
      requestAnimationFrame(() =>
        document
          .getElementById('setup-inspector-heading')
          ?.focus({ preventScroll: true }),
      )
    } else if (previous.current) close()
    previous.current = selected?.id
  }, [selected, open, close])
  function select(id: SetupStepId, element?: HTMLElement) {
    trigger.current = element
    if (step === id) {
      open(element)
      requestAnimationFrame(() =>
        document
          .getElementById('setup-inspector-heading')
          ?.focus({ preventScroll: true }),
      )
    } else
      void navigate({
        to: '/w/$namespace/home/setup-guide',
        params: { namespace },
        search: { step: id },
        resetScroll: false,
      })
  }
  const complete = progress.completed === progress.total
  return (
    <HomePage>
      <HomePageHeader
        namespace={namespaces.find((item) => item.id === namespace)!.name}
        title="Setup guide"
        description="A few clear steps to get familiar with your store."
        onReset={reset}
        demoDetails="Review confirmations only update this checklist. Product completion follows the shared demo catalog; it does not establish readiness or publication."
      />
      {!available ? (
        <EmptyState
          title={
            scenario === 'loading'
              ? 'Loading setup guide'
              : 'Setup guide unavailable'
          }
        >
          {scenario === 'loading'
            ? 'Loading presentation specimen. No network request is running.'
            : 'Unavailable presentation specimen. Your demo catalog and drafts are unchanged.'}
        </EmptyState>
      ) : (
        <>
          {readOnly && (
            <p className={styles.readOnly} role="note">
              Read-only preview. You can inspect guidance, but review actions
              are unavailable.
            </p>
          )}
          <section
            className={styles.progressPanel}
            aria-labelledby="setup-progress-heading"
          >
            <div className={styles.progressTop}>
              <div>
                <span className={styles.eyebrow}>YOUR STORE, STEP BY STEP</span>
                <h2 id="setup-progress-heading">
                  {complete ? 'Checklist complete' : 'Your next steps'}
                </h2>
              </div>
              <Compass size={25} aria-hidden="true" />
            </div>
            <div className={styles.progressLabel}>
              <span>
                {progress.completed} of {progress.total} steps complete
              </span>
              <strong>
                {Math.round((progress.completed / progress.total) * 100)}%
              </strong>
            </div>
            <progress
              aria-label="Demo setup progress"
              max={progress.total}
              value={progress.completed}
            />
            {complete ? (
              <div className={styles.completion}>
                <p>
                  You’ve reviewed the demo setup checklist. This does not verify
                  store configuration, payments, or publication.
                </p>
                <div className={styles.inlineLinks}>
                  <Link
                    to="/w/$namespace/catalog/products"
                    params={{ namespace }}
                  >
                    View products <ArrowUpRight size={13} aria-hidden="true" />
                  </Link>
                  <Link to="/w/$namespace/home/overview" params={{ namespace }}>
                    Open Overview <ArrowUpRight size={13} aria-hidden="true" />
                  </Link>
                </div>
              </div>
            ) : (
              <ActionButton
                variant="primary"
                onPress={(event) =>
                  select(progress.nextStep!.id, event.target as HTMLElement)
                }
              >
                Continue setup <ArrowRight size={14} aria-hidden="true" />
              </ActionButton>
            )}
            {!complete && (
              <p className={styles.progressNote}>
                Reviews record guidance read in this demo. Products count when
                saved and not deleting.
              </p>
            )}
          </section>
          <section
            aria-labelledby="setup-checklist-heading"
            className={styles.checklist}
          >
            <h2 id="setup-checklist-heading">Your setup checklist</h2>
            <ol className={styles.steps}>
              {progress.steps.map((item, index) => (
                <li key={item.id} data-complete={item.complete || undefined}>
                  <Button
                    id={`setup-task-${item.id}`}
                    className={styles.stepButton}
                    data-selected={step === item.id || undefined}
                    aria-current={step === item.id ? 'step' : undefined}
                    onPress={(event) =>
                      select(item.id, event.target as HTMLElement)
                    }
                  >
                    <span className="sr-only">Review guidance: </span>
                    <span className={styles.stepIndex} aria-hidden="true">
                      {item.complete ? (
                        <Check size={19} aria-hidden="true" />
                      ) : (
                        String(index + 1).padStart(2, '0')
                      )}
                    </span>
                    <span className={styles.stepBody}>
                      <span className={styles.stepTitle}>{item.title}</span>
                      <span className={styles.stepDescription}>
                        {item.description}
                      </span>
                      <span className={styles.stepStatus}>
                        {item.complete ? (
                          <Check size={12} aria-hidden="true" />
                        ) : (
                          <Circle size={12} aria-hidden="true" />
                        )}
                        {item.complete
                          ? item.id === 'first-product'
                            ? 'Added in demo'
                            : 'Reviewed in demo'
                          : item.id === 'first-product'
                            ? 'Not added'
                            : 'Not reviewed'}
                      </span>
                    </span>
                    <ArrowUpRight size={16} aria-hidden="true" />
                  </Button>
                </li>
              ))}
            </ol>
          </section>
          <section className={styles.later} aria-label="Beyond this checklist">
            <details>
              <summary>Where your catalog comes from</summary>
              <p>
                GitStore keeps authored catalog content in versioned Markdown
                files. Repository connections and credentials are not configured
                or verified by this demo guide.
              </p>
            </details>
            <details>
              <summary>When you’re ready to sell</summary>
              <p>
                Payments, shipping, checkout, and storefront connections are
                later setup work. Completing this demo checklist does not
                configure them.
              </p>
            </details>
          </section>
        </>
      )}
    </HomePage>
  )
}
