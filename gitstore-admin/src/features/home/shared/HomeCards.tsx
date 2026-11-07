import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'
import { ArrowUpRight } from 'lucide-react'
import { Button } from 'react-aria-components'
import styles from './home.module.css'
export interface SummaryContentProps {
  label: string
  value: number
  detail: string
  Icon: LucideIcon
  ratio: number
}
export function SummaryCardContent({
  label,
  value,
  detail,
  Icon,
  ratio,
}: SummaryContentProps) {
  return (
    <>
      <div className={styles.metricLabel}>
        <span>{label}</span>
        <Icon size={17} aria-hidden="true" />
      </div>
      <strong className={styles.metricValue}>
        {value.toLocaleString('en')}
      </strong>
      <p className={styles.metricDetail}>{detail}</p>
      <div className={styles.metricFoot} aria-hidden="true">
        <span style={{ width: `${Math.max(0, Math.min(1, ratio)) * 100}%` }} />
      </div>
    </>
  )
}
export function SummaryCard({
  state,
  ...props
}: SummaryContentProps & { state?: string }) {
  return (
    <article
      className={styles.metric}
      data-halo-card={props.value ? '' : undefined}
      data-readiness={props.value ? state : undefined}
    >
      <SummaryCardContent {...props} />
    </article>
  )
}
/** Resource identity and footer geometry are shared across Home work queues. */
export function ResourceCard({
  id,
  title,
  kind,
  badge,
  description,
  state,
  meta,
  headingLevel = 3,
  onInspect,
}: {
  id: string
  title: string
  kind: string
  badge: ReactNode
  description: ReactNode
  state: string
  meta?: ReactNode
  headingLevel?: 2 | 3
  onInspect: (trigger: HTMLElement) => void
}) {
  const Heading = headingLevel === 2 ? 'h2' : 'h3'
  return (
    <article
      className={styles.resourceCard}
      data-resource-id={id}
      data-halo-card=""
      data-readiness={state}
    >
      <div className={styles.cardTop}>
        <span className={styles.kind}>{kind}</span>
        {badge}
      </div>
      <Heading>{title}</Heading>
      <div className={styles.cardDescription}>{description}</div>
      <div className={styles.cardBottom}>
        {meta && <div className={styles.cardMeta}>{meta}</div>}
        <Button
          className={styles.inspectButton}
          aria-label={`View details for ${title}`}
          onPress={(event) => onInspect(event.target as HTMLElement)}
        >
          View details
          <ArrowUpRight size={14} aria-hidden="true" />
        </Button>
      </div>
    </article>
  )
}

/** Labels remain domain-specific; the pill's visual treatment is shared. */
export function StatusPill({
  label,
  state,
  Icon,
}: {
  label: string
  state: string
  Icon: LucideIcon
}) {
  return (
    <span className={styles.badge} data-readiness={state}>
      <Icon size={12} aria-hidden="true" />
      {label}
    </span>
  )
}
