import type { ReactNode } from 'react'
import { SlidersHorizontal } from 'lucide-react'
import { ActionButton } from './controls'
import styles from './filter-bar.module.css'
/** One responsive filter arrangement for catalog and activity lists. */
export function FilterBar({
  search,
  children,
  tools,
  activeCount,
  onOpen,
}: {
  search: ReactNode
  children: ReactNode
  tools?: ReactNode
  activeCount: number
  onOpen: () => void
}) {
  return (
    <div className={styles.bar} data-filter-bar="" data-tools={!!tools}>
      <div className={styles.search}>{search}</div>
      <div className={styles.fields} data-desktop-filters="">
        {children}
      </div>
      <div className={styles.tools}>
        {tools}
        <ActionButton className={styles.mobileButton} onPress={onOpen}>
          <SlidersHorizontal size={16} aria-hidden="true" />
          Filters{activeCount ? ` (${activeCount})` : ''}
        </ActionButton>
      </div>
    </div>
  )
}
