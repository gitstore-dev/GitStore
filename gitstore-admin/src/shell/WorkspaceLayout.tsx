import type { ReactNode } from 'react'
import styles from './shell.module.css'
export type LayoutMode = 'standard' | 'inspector' | 'focus'
export function WorkspaceLayout({
  children,
  inspector,
  mode = 'standard',
}: {
  children: ReactNode
  inspector?: ReactNode
  mode?: LayoutMode
}) {
  return (
    <div className={styles.workspaceLayout} data-layout={mode}>
      <main id="main-content" className={styles.main} tabIndex={-1}>
        {children}
      </main>
      {mode === 'inspector' && inspector && (
        <aside aria-label="Page context" className={styles.inspector}>
          {inspector}
        </aside>
      )}
    </div>
  )
}
