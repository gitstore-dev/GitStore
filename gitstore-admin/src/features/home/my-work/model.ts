export const workTabs = [
  'drafts',
  'submitted',
  'assigned',
  'completed',
] as const
export type WorkTab = (typeof workTabs)[number]
export const tabLabels: Record<WorkTab, string> = {
  drafts: 'Drafts',
  submitted: 'Submitted',
  assigned: 'Assigned to me',
  completed: 'Completed',
}
export type WorkSort = 'updated' | 'name'
export type WorkScenario =
  | 'populated'
  | 'empty'
  | 'filtered-empty'
  | 'loading'
  | 'unavailable'
  | 'read-only'
export interface ProductText {
  name: string
  description: string
}
export interface WorkItem {
  id: string
  status: WorkTab
  base: ProductText
  proposed: ProductText
  author: string
  reviewer: string
  path: string
  updatedAt: string
  outcome?: 'approved' | 'changes-requested'
  reviewNote?: string
}
export interface WorkState {
  items: readonly WorkItem[]
  revision: number
}
export type WorkAction =
  | { type: 'save'; id: string; text: ProductText }
  | { type: 'submit' | 'discard' | 'approve'; id: string }
  | { type: 'request-changes'; id: string; note: string }
  | { type: 'reset'; items: readonly WorkItem[] }
export function initialWork(items: readonly WorkItem[]): WorkState {
  return { items, revision: 0 }
}
export function validText(text: ProductText) {
  return text.name.trim().length > 0
}
export function sameText(a: ProductText, b: ProductText) {
  return a.name === b.name && a.description === b.description
}
export function changedFields(item: WorkItem) {
  return (['name', 'description'] as const).filter(
    (field) => item.base[field] !== item.proposed[field],
  )
}
export function workCounts(items: readonly WorkItem[]) {
  return Object.fromEntries(
    workTabs.map((tab) => [
      tab,
      items.filter((item) => item.status === tab).length,
    ]),
  ) as Record<WorkTab, number>
}
export function workList(
  items: readonly WorkItem[],
  tab: WorkTab,
  query: string,
  sort: WorkSort,
) {
  const search = query.trim().toLocaleLowerCase('en')
  return items
    .filter(
      (item) =>
        item.status === tab &&
        item.proposed.name.toLocaleLowerCase('en').includes(search),
    )
    .toSorted(
      (a, b) =>
        (sort === 'name'
          ? a.proposed.name.localeCompare(b.proposed.name, 'en')
          : b.updatedAt.localeCompare(a.updatedAt)) ||
        a.id.localeCompare(b.id, 'en'),
    )
}
export function workReducer(state: WorkState, action: WorkAction): WorkState {
  if (action.type === 'reset') return initialWork(action.items)
  const item = state.items.find((entry) => entry.id === action.id)
  if (!item) return state
  const draft = item.status === 'drafts'
  if (action.type === 'save' && (!draft || !validText(action.text)))
    return state
  if (action.type === 'submit' && (!draft || !changedFields(item).length))
    return state
  if (action.type === 'discard' && !draft) return state
  if (
    (action.type === 'approve' || action.type === 'request-changes') &&
    item.status !== 'assigned'
  )
    return state
  if (action.type === 'request-changes' && !action.note.trim()) return state
  const revision = state.revision + 1
  const updatedAt = new Date(
    Date.UTC(2026, 10, 2, 12, 0, revision),
  ).toISOString()
  const next: WorkItem = {
    ...item,
    updatedAt,
    ...(action.type === 'save'
      ? { proposed: { ...action.text, name: action.text.name.trim() } }
      : {}),
    ...(action.type === 'submit' ? { status: 'submitted' as const } : {}),
    ...(action.type === 'approve'
      ? { status: 'completed' as const, outcome: 'approved' as const }
      : {}),
    ...(action.type === 'request-changes'
      ? {
          status: 'completed' as const,
          outcome: 'changes-requested' as const,
          reviewNote: action.note.trim(),
        }
      : {}),
  }
  return {
    revision,
    items:
      action.type === 'discard'
        ? state.items.filter((entry) => entry.id !== item.id)
        : state.items.map((entry) => (entry.id === item.id ? next : entry)),
  }
}
export function workStatus(item: WorkItem) {
  return item.status === 'completed'
    ? item.outcome === 'approved'
      ? 'Approved'
      : 'Changes requested'
    : item.status === 'drafts'
      ? 'Draft'
      : item.status === 'assigned'
        ? 'Awaiting your review'
        : tabLabels[item.status]
}
export function workReadiness(item: WorkItem) {
  return item.status === 'drafts'
    ? 'stale'
    : item.status === 'completed'
      ? item.outcome === 'approved'
        ? 'ready'
        : 'blocked'
      : 'reconciling'
}
