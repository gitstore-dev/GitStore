import { FilterSheet } from '../../../components/FilterSheet'
import type { ComponentProps } from 'react'
export function ActivityFiltersSheet(
  props: Omit<
    ComponentProps<typeof FilterSheet>,
    'title' | 'itemLabel' | 'count' | 'applyLabel'
  >,
) {
  return (
    <FilterSheet
      {...props}
      title="Filter activity"
      applyLabel="Apply filters"
    />
  )
}
