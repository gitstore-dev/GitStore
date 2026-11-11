import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Heading } from 'react-aria-components'
import { ArrowUpRight, Check, Circle } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { destinationById } from '../../../navigation/registry'
import { ActionButton } from '../../../components/controls'
import { ConfirmationDialog } from '../../../components/ConfirmationDialog'
import type { SetupReviewId, setupProgress } from './model'
import styles from './setup-guide.module.css'
type Step = ReturnType<typeof setupProgress>['steps'][number]
export function SetupInspector({
  step,
  namespace,
  productCount,
  deletingProductCount,
  readOnly,
  onReview,
}: {
  step: Step
  namespace: NamespaceId
  productCount: number
  deletingProductCount: number
  readOnly: boolean
  onReview: (id: SetupReviewId, reviewed: boolean) => void
}) {
  const [confirming, setConfirming] = useState(false)
  const destination = destinationById(step.destination)
  const isProduct = step.id === 'first-product'
  return (
    <div className={styles.inspector}>
      <span className={styles.eyebrow}>DEMO SETUP GUIDANCE</span>
      <h2 id="setup-inspector-heading" tabIndex={-1}>
        {step.title}
      </h2>
      <p className={styles.status}>
        {step.complete ? (
          <Check size={14} aria-hidden="true" />
        ) : (
          <Circle size={14} aria-hidden="true" />
        )}
        {step.complete
          ? isProduct
            ? 'Product added in demo'
            : 'Reviewed in demo'
          : isProduct
            ? deletingProductCount > 0
              ? 'Product deleting'
              : 'No product added yet'
            : 'Not reviewed'}
      </p>
      <p>{step.description}</p>
      <ol className={styles.instructions}>
        {step.instructions.map((text) => (
          <li key={text}>{text}</li>
        ))}
      </ol>
      <div className={styles.explanation}>
        <strong>
          {isProduct ? 'What counts' : 'What this review records'}
        </strong>
        <p>
          {isProduct
            ? `${productCount} saved, non-deleting demo ${productCount === 1 ? 'product counts' : 'products count'} toward this step. Product readiness and publication are separate.`
            : 'This records that you reviewed the guidance in this demo. It does not configure settings, create categories, or resolve catalog issues.'}
        </p>
      </div>
      {isProduct && deletingProductCount > 0 && (
        <p className={styles.note}>
          Deleting products do not count toward this step. You can add another
          product while deletion completes.
        </p>
      )}
      <div className={styles.inspectorActions}>
        {isProduct ? (
          <>
            {!readOnly && (
              <Link
                className={styles.primaryLink}
                to="/w/$namespace/catalog/products/new"
                params={{ namespace }}
              >
                Add demo product <ArrowUpRight size={14} aria-hidden="true" />
              </Link>
            )}
          </>
        ) : (
          <ActionButton
            variant={step.complete ? 'secondary' : 'primary'}
            isDisabled={readOnly}
            onPress={() => {
              if (step.complete) onReview(step.id, false)
              else setConfirming(true)
            }}
          >
            {step.complete ? 'Mark not reviewed' : 'Mark reviewed in demo'}
          </ActionButton>
        )}
        <Link
          className={styles.textLink}
          to={destination.to}
          params={{ namespace }}
        >
          {step.linkLabel}
          <ArrowUpRight size={13} aria-hidden="true" />
        </Link>
        {step.planned && (
          <span className={styles.planned}>Page not implemented</span>
        )}
      </div>
      {readOnly && (
        <p className={styles.note}>
          Read-only preview. Review actions are unavailable in this specimen.
        </p>
      )}
      <ConfirmationDialog isOpen={confirming} onOpenChange={setConfirming}>
        <Heading slot="title">Mark this step reviewed in demo?</Heading>
        <p>
          <strong>{step.title}</strong>
        </p>
        <p>
          This only marks the guidance as reviewed for this store in this tab.
          No settings, categories, or resource conditions are changed. Nothing
          is published.
        </p>
        <div className={styles.dialogActions}>
          <ActionButton autoFocus onPress={() => setConfirming(false)}>
            Cancel
          </ActionButton>
          <ActionButton
            variant="primary"
            isDisabled={readOnly}
            onPress={() => {
              if (step.id !== 'first-product') onReview(step.id, true)
              setConfirming(false)
            }}
          >
            Confirm demo review
          </ActionButton>
        </div>
      </ConfirmationDialog>
    </div>
  )
}
