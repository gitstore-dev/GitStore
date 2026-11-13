import type { ReactNode } from 'react'
import { ActionButton } from './controls'
import styles from './filter-bar.module.css'

/** Keep the filter-to-results rhythm consistent, including when no filters apply. */
export function FilterResults({
  children,
  filtered,
  onClear,
}: {
  children: ReactNode
  filtered: boolean
  onClear: () => void
}) {
  return (
    <div className={styles.results} data-filter-results="">
      <div className={styles.resultContent}>{children}</div>
      {filtered && <ClearFiltersButton onClear={onClear} />}
    </div>
  )
}

export function ClearFiltersButton({ onClear }: { onClear: () => void }) {
  return (
    <ActionButton variant="ghost" className={styles.clear} onPress={onClear}>
      Clear filters
    </ActionButton>
  )
}
