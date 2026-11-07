import { resourceLabel } from './model'
import { useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Heading } from 'react-aria-components'
import { ArrowUpRight, Check } from 'lucide-react'
import { ActionButton } from '../../../components/controls'
import { destinationById } from '../../../navigation/registry'
import type { NamespaceId } from '../../../data/demo'
import type { DemoResource } from './model'
import { demoAction, resourceDestinations } from './model'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { StatusBadge } from './StatusBadge'
import { kindLabels } from './presentation'
import styles from './overview.module.css'
import homeStyles from '../shared/home.module.css'
import { TechnicalDetails } from '../../../components/TechnicalDetails'
import { ConfirmationDialog } from '../../../components/ConfirmationDialog'

export function ResourceInspector({
  resource,
  resources,
  namespace,
  onSelect,
  onResolve,
}: {
  resource: DemoResource
  resources: readonly DemoResource[]
  namespace: NamespaceId
  onSelect: (id: string) => void
  onResolve: (id: string) => void
}) {
  const [confirming, setConfirming] = useState(false)
  const heading = useRef<HTMLHeadingElement>(null)
  const destination = destinationById(resourceDestinations[resource.kind])
  const action = demoAction(resource)
  return (
    <div className={styles.resourceDetails}>
      <ChoiceSelect
        label="Choose another item"
        value={resource.id}
        onChange={onSelect}
        options={resources.map((item) => ({
          id: item.id,
          label: resourceLabel(item),
        }))}
      />
      <div className={styles.detailIdentity}>
        <span className={homeStyles.kicker}>
          {kindLabels[resource.kind].singular}
        </span>
        <h2 ref={heading} tabIndex={-1}>
          {resourceLabel(resource)}
        </h2>
        <StatusBadge state={resource.readiness} />
      </div>
      <section
        aria-label="Item status"
        className={styles.diagnostic}
        data-readiness={resource.readiness}
      >
        <h3>
          {resource.readiness === 'ready'
            ? 'Ready in this demo'
            : resource.readiness === 'reconciling'
              ? 'Update in progress'
              : resource.readiness === 'stale'
                ? 'Update needed'
                : 'What needs attention'}
        </h3>
        <p>{resource.diagnostic}</p>
      </section>
      <section className={styles.references}>
        <h3>
          Related items <span>{resource.references.length}</span>
        </h3>
        {resource.references.length ? (
          <ul>
            {resource.references.map((reference) => (
              <li key={reference.label}>
                {reference.resourceId &&
                resources.some((item) => item.id === reference.resourceId) ? (
                  <ActionButton
                    variant="ghost"
                    onPress={() => onSelect(reference.resourceId!)}
                  >
                    {reference.label}
                    <ArrowUpRight size={13} aria-hidden="true" />
                  </ActionButton>
                ) : (
                  <strong>{reference.label}</strong>
                )}
                <small>{reference.detail}</small>
              </li>
            ))}
          </ul>
        ) : (
          <p>No related items in this demo.</p>
        )}
      </section>
      {resource.readiness !== 'ready' && !resource.deleting ? (
        <ActionButton
          variant="primary"
          className={styles.resolveButton}
          onPress={() => setConfirming(true)}
        >
          {action}
          <ArrowUpRight size={14} aria-hidden="true" />
        </ActionButton>
      ) : (
        <p className={styles.resolved}>
          <Check size={14} aria-hidden="true" />
          {resource.deleting
            ? 'Complete deletion from the Product page'
            : 'No demo action needed'}
        </p>
      )}
      {resource.kind === 'Product' ? (
        <Link
          className={styles.textLink}
          to="/w/$namespace/catalog/products/$productId"
          params={{ namespace, productId: resource.id }}
        >
          Open product <ArrowUpRight size={13} aria-hidden="true" />
        </Link>
      ) : (
        <Link
          className={styles.textLink}
          to={destination.to}
          params={{ namespace }}
        >
          Open {destination.label.toLowerCase()}
          <ArrowUpRight size={13} aria-hidden="true" />
        </Link>
      )}
      <TechnicalDetails
        resourceType={resource.kind}
        sourcePath={resource.path}
      />
      <p className={styles.disclaimer}>
        Demo only. Your store and saved files are not changed.
      </p>
      <ConfirmationDialog isOpen={confirming} onOpenChange={setConfirming}>
        <span className={homeStyles.kicker}>LOCAL DEMO ACTION</span>
        <Heading slot="title">{action}?</Heading>
        <p>
          Mark <strong>{resourceLabel(resource)}</strong> as ready in this demo.
        </p>
        <div className={styles.confirmNote}>
          This removes the item from Updates and issues and updates the demo
          totals and activity. No changes are sent to GitStore.
        </div>
        <p className={styles.disclaimer}>
          Reloading or resetting the store demo restores the original example.
        </p>
        <div className={styles.confirmActions}>
          <ActionButton autoFocus onPress={() => setConfirming(false)}>
            Cancel
          </ActionButton>
          <ActionButton
            variant="primary"
            onPress={() => {
              setConfirming(false)
              onResolve(resource.id)
              requestAnimationFrame(() => heading.current?.focus())
            }}
          >
            Confirm demo change
          </ActionButton>
        </div>
      </ConfirmationDialog>
    </div>
  )
}
