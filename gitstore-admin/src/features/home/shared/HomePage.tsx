import type { ReactNode, Ref } from 'react'
import { CircleDot, RotateCcw } from 'lucide-react'
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
}: {
  namespace: string
  title: string
  description: string
  headingId?: string
  onReset: () => void
}) {
  return (
    <header className={styles.pageHeading}>
      <div>
        <PageContext namespace={namespace} />
        <h1 id={headingId} tabIndex={headingId ? -1 : undefined}>
          {title}
          <span aria-hidden="true" className={styles.titleDot}>
            .
          </span>
        </h1>
        <p>{description}</p>
      </div>
      <DemoControls onReset={onReset} />
    </header>
  )
}
export function DemoControls({ onReset }: { onReset: () => void }) {
  return (
    <div className={styles.headingActions}>
      <span className={styles.demoLabel}>
        <CircleDot size={12} aria-hidden="true" />
        Demo data
      </span>
      <ActionButton variant="ghost" onPress={onReset}>
        <RotateCcw size={14} aria-hidden="true" />
        Reset demo
      </ActionButton>
    </div>
  )
}
export function DemoNotice({ page }: { page: string }) {
  return (
    <p className={styles.demoNotice}>
      Changes stay in this demo and reset when you leave {page}, switch stores,
      or reload.
    </p>
  )
}
