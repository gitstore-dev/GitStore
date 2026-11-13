import { Button } from 'react-aria-components'
import { X } from 'lucide-react'
import styles from './active-filters.module.css'
export function ActiveFilters({
  label,
  filters,
}: {
  label: string
  filters: readonly { id: string; label: string; onRemove: () => void }[]
}) {
  if (!filters.length) return null
  return (
    <div className={styles.chips} aria-label={label}>
      {filters.map((filter) => (
        <Button
          key={filter.id}
          onPress={filter.onRemove}
          aria-label={`Remove ${filter.id} filter: ${filter.label}`}
        >
          {filter.label}
          <X size={12} aria-hidden="true" />
        </Button>
      ))}
    </div>
  )
}
