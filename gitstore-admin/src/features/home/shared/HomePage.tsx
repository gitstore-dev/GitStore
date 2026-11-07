import { useState } from 'react'
import type { ReactNode, Ref } from 'react'
import { CircleDot, RotateCcw } from 'lucide-react'
import { DialogTrigger, Popover, Dialog, Heading } from 'react-aria-components'
import { ActionButton } from '../../../components/controls'
import styles from './home.module.css'

/** Shared Home composition: spacing, context trail, heading and demo controls. */
export function HomePage({
  children,
  className = '',
  ref,
}: {
  children: ReactNode
  className?: string
  ref?: Ref<HTMLDivElement>
}) {
  return (
    <div ref={ref} className={`${styles.page} ${className}`}>
      {children}
    </div>
  )
}
export function PageContext({
  namespace,
  section = 'Home',
}: {
  namespace: string
  section?: string
}) {
  return (
    <div className={styles.kicker} data-page-context="">
      <span className={styles.headingMark} aria-hidden="true" />
      <span>{namespace}</span>
      <span className={styles.headingSlash} aria-hidden="true">
        /
      </span>
      <span>{section}</span>
    </div>
  )
}
export function HomePageHeader({
  namespace,
  title,
  description,
  headingId,
  onReset,
  demoDetails,
}: {
  namespace: string
  title: string
  description: string
  headingId?: string
  onReset: () => void
  demoDetails?: ReactNode
}) {
  return (
    <header className={styles.pageHeading}>
      <div>
        <PageContext namespace={namespace} />
        <h1 id={headingId} tabIndex={-1}>
          {title}
          <span aria-hidden="true" className={styles.titleDot}>
            .
          </span>
        </h1>
        <p>{description}</p>
      </div>
      <DemoControls onReset={onReset} details={demoDetails} />
    </header>
  )
}
export function DemoControls({
  onReset,
  details,
}: {
  onReset: () => void
  details?: ReactNode
}) {
  const [open, setOpen] = useState(false)
  return (
    <DialogTrigger isOpen={open} onOpenChange={setOpen}>
      <ActionButton variant="ghost" className={styles.demoLabel}>
        <CircleDot size={12} aria-hidden="true" />
        Demo data
      </ActionButton>
      <Popover
        placement="bottom end"
        containerPadding={12}
        className={styles.demoPopover}
      >
        <Dialog className={styles.demoDialog}>
          <Heading slot="title">About this demo</Heading>
          <p>
            Demo changes and unsaved drafts stay in this tab across pages and
            stores. Reloading resets both stores; Reset demo restores the
            selected store’s original Home examples.
          </p>
          {details && <p>{details}</p>}
          <ActionButton
            onPress={() => {
              setOpen(false)
              onReset()
            }}
          >
            <RotateCcw size={14} aria-hidden="true" />
            Reset demo
          </ActionButton>
        </Dialog>
      </Popover>
    </DialogTrigger>
  )
}
