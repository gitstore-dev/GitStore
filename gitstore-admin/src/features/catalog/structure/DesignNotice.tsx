import styles from './structure.module.css'
export function DesignNotice({ type = false }: { type?: boolean }) {
  return (
    <aside className={styles.notice} aria-label="Design preview">
      <strong>Design preview</strong>
      {type
        ? 'These examples explore possible product types. They are not assigned to your Products and do not enforce rules.'
        : 'These reusable definition examples are separate from saved Product options. Products do not use them yet.'}
    </aside>
  )
}
