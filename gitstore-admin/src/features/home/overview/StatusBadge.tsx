import { Check, TriangleAlert, CircleX, RefreshCw, Clock3 } from 'lucide-react'
import { readinessLabels } from './presentation'
import type { Readiness } from './model'
import { StatusPill } from '../shared/HomeCards'
const icons = {
  ready: Check,
  blocked: TriangleAlert,
  failed: CircleX,
  reconciling: RefreshCw,
  stale: Clock3,
}
export function StatusBadge({ state }: { state: Readiness }) {
  const Icon = icons[state]
  return <StatusPill label={readinessLabels[state]} state={state} Icon={Icon} />
}
