import type { FieldChange } from './snapshots'
import styles from './demo.module.css'
export function ChangePreview({
  changes,
  label = 'Change preview',
}: {
  changes: readonly FieldChange[]
  label?: string
}) {
  return (
    <section aria-label={label} className={styles.changes}>
      <h3>
        {changes.length} {changes.length === 1 ? 'field' : 'fields'} changed
      </h3>
      {changes.length ? (
        changes.map((change) => (
          <section key={change.label} aria-label={`${change.label} change`}>
            <h4>{change.label}</h4>
            <div className={styles.before}>
              <strong>Before</strong>
              <p>{change.before}</p>
            </div>
            <div className={styles.after}>
              <strong>After</strong>
              <p>{change.after}</p>
            </div>
          </section>
        ))
      ) : (
        <p>No authored fields changed.</p>
      )}
    </section>
  )
}
