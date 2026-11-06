import type { NamespaceId } from '../../../data/demo'
import { demoUser } from '../../../data/demo'
import type { WorkItem, WorkScenario, WorkTab } from './model'
const examples: readonly [string, string, WorkTab, string][] = [
  ['basin-draft', 'Kanso Stone Basin', 'drafts', 'kanso-stone-basin'],
  ['tunic-draft', 'Raw Silk Tunic', 'drafts', 'raw-silk-tunic'],
  ['cup-submitted', 'Ceramic Everyday Cup', 'submitted', 'ceramic-cup'],
  ['kit-submitted', 'Ceremonial Tea Ritual Kit', 'submitted', 'tea-ritual'],
  ['basin-review', 'Kanso Stone Basin', 'assigned', 'kanso-stone-basin'],
  ['cup-review', 'Ceramic Everyday Cup', 'assigned', 'ceramic-cup'],
  ['tunic-completed', 'Raw Silk Tunic', 'completed', 'raw-silk-tunic'],
]
/** Independent personal-work examples, not a shared copy of Overview state. */
export function workFixture(
  namespace: NamespaceId,
  scenario?: WorkScenario,
): readonly WorkItem[] {
  if (namespace === 'studio-berlin' || scenario === 'empty') return []
  return examples.map(([id, name, status, slug], index) => ({
    id,
    status,
    base: {
      name,
      description: `Discover ${name.toLowerCase()}, thoughtfully made for everyday use.`,
    },
    proposed: {
      name,
      description: `${name}, carefully selected for our collection. Find materials, care guidance, and thoughtful details in store.`,
    },
    author: status === 'assigned' ? 'Kenji Sato' : demoUser.name,
    reviewer: status === 'assigned' ? demoUser.name : 'Kenji Sato',
    path: `catalog/products/${slug}.md`,
    updatedAt: `2026-11-02T${String(11 - index).padStart(2, '0')}:00:00.000Z`,
    ...(status === 'completed' ? { outcome: 'approved' as const } : {}),
  }))
}
