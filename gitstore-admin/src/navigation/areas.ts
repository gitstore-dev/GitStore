import { destinations } from './registry'
import type {
  GroupId,
  NavigationDestination,
  NavigationGroup,
} from './registry'

interface WorkspaceArea {
  id: string
  label: string
  icon: NavigationGroup['icon']
  groups: readonly GroupId[]
}

// Presentation contexts only: route identity and scope remain in the registry.
export const workspaceAreas = [
  { id: 'home', label: 'Home', icon: 'home', groups: ['home'] },
  {
    id: 'commerce',
    label: 'Commerce',
    icon: 'orders',
    groups: [
      'orders',
      'catalog',
      'inventory',
      'customers',
      'pricing-promotions',
    ],
  },
  { id: 'content', label: 'Content', icon: 'content', groups: ['content'] },
  {
    id: 'operations',
    label: 'Operations',
    icon: 'changes',
    groups: ['channels-markets', 'changes-releases', 'apps-automation'],
  },
  { id: 'insights', label: 'Insights', icon: 'insights', groups: ['insights'] },
] as const satisfies readonly WorkspaceArea[]

export function areaFor(
  destination: NavigationDestination | undefined,
): WorkspaceArea | undefined {
  return workspaceAreas.find((area) =>
    (area.groups as readonly GroupId[]).includes(
      destination?.groupId as GroupId,
    ),
  )
}

export function areaEntry(area: WorkspaceArea) {
  return destinations.find(
    (destination) => destination.groupId === area.groups[0],
  )!
}
