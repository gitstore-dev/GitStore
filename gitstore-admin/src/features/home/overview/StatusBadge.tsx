import { Check, TriangleAlert, CircleX, RefreshCw, Clock3 } from 'lucide-react'
import { readinessLabels } from './presentation'
import type { Readiness } from './model'
import styles from './overview.module.css'
const icons = {
  ready: Check,
  blocked: TriangleAlert,
  failed: CircleX,
  reconciling: RefreshCw,
  stale: Clock3,
}
export function StatusBadge({ state }: { state: Readiness }) {
  const Icon = icons[state]
  return (
    <span className={styles.badge} data-readiness={state}>
      <Icon size={12} aria-hidden="true" />
      {readinessLabels[state]}
    </span>
  )
}
