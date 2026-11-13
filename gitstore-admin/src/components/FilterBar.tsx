import { Children, Fragment, isValidElement } from 'react'
import type { CSSProperties, ReactNode } from 'react'
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
  function count(node: ReactNode): number {
    return Children.toArray(node).reduce<number>(
      (total, child) =>
        total +
        (typeof child === 'string' && !child.trim()
          ? 0
          : isValidElement<{ children?: ReactNode }>(child) &&
              child.type === Fragment
            ? count(child.props.children)
            : 1),
      0,
    )
  }
  const fields = count(children)
  return (
    <div
      className={styles.bar}
      data-filter-bar=""
      data-tools={!!tools}
      style={
        {
          '--filter-count': fields,
          '--filter-weight': `${fields}fr`,
        } as CSSProperties
      }
    >
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
