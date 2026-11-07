import { useId } from 'react'
import type { ReactNode } from 'react'
import styles from './home.module.css'

/** One empty-state anatomy, with copy and actions appropriate to each view. */
export function EmptyState({
  title,
  children,
  actions,
}: {
  title: string
  children: ReactNode
  actions?: ReactNode
}) {
  const heading = useId()
  return (
    <section
      className={styles.emptyState}
      aria-labelledby={heading}
      data-home-empty-state=""
    >
      <svg
        className={styles.emptyArt}
        width="72"
        height="72"
        viewBox="0 0 72 72"
        fill="none"
        aria-hidden="true"
      >
        <path
          d="m12 29 24-12 24 12-24 12-24-12Z"
          fill="var(--hover)"
          stroke="var(--muted)"
        />
        <path d="m12 39 24 12 24-12M12 49l24 12 24-12" stroke="var(--muted)" />
        <path
          d="M36 17v12l-9 5 9 5v12"
          stroke="var(--accent)"
          strokeWidth="3"
        />
      </svg>
      <div className={styles.emptyContent}>
        <h2 id={heading}>{title}</h2>
        <p>{children}</p>
        {actions && <div className={styles.emptyActions}>{actions}</div>}
      </div>
    </section>
  )
}
