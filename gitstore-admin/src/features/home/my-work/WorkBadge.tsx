import {
  CircleCheck,
  ClipboardCheck,
  FilePenLine,
  MessageSquare,
  Send,
  RotateCcw,
} from 'lucide-react'
import { StatusPill } from '../shared/HomeCards'
import { workReadiness, workStatus } from './model'
import type { WorkItem } from './model'
export function WorkBadge({ item }: { item: WorkItem }) {
  const Icon =
    item.entryType === 'recovery'
      ? RotateCcw
      : item.status === 'completed'
        ? item.outcome === 'approved'
          ? CircleCheck
          : MessageSquare
        : { drafts: FilePenLine, submitted: Send, assigned: ClipboardCheck }[
            item.status
          ]
  return (
    <StatusPill
      label={workStatus(item)}
      state={workReadiness(item)}
      Icon={Icon}
    />
  )
}
