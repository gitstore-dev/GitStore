import { FileCode2 } from 'lucide-react'
import styles from './technical-details.module.css'

/** Canonical resource identity stays inside a shared native disclosure. */
export function TechnicalDetails({
  resourceType,
  sourcePath,
  defaultExpanded = false,
}: {
  resourceType: string
  sourcePath: string
  defaultExpanded?: boolean
}) {
  return (
    <details
      open={defaultExpanded || undefined}
      className={styles.details}
      data-technical-details=""
    >
      <summary>Technical details</summary>
      <dl>
        <dt>Resource type</dt>
        <dd>{resourceType}</dd>
      </dl>
      <div className={styles.source}>
        <FileCode2 size={15} aria-hidden="true" />
        <span>Source file</span>
        <code>{sourcePath}</code>
      </div>
    </details>
  )
}
