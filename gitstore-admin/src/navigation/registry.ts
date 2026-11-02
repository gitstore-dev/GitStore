import type { FileRoutesByTo } from '../routeTree.gen'
export const groups = [
  {
    id: 'home',
    label: 'Home',
    icon: 'home',
    scope: 'namespace',
    pages: ['Overview', 'My work', 'Recent activity', 'Setup guide'],
  },
  {
    id: 'orders',
    label: 'Orders',
    icon: 'orders',
    scope: 'namespace',
    pages: [
      'All orders',
      'Draft orders',
      'Abandoned checkouts',
      'Fulfillments',
      'Returns & exchanges',
      'Refunds',
    ],
  },
  {
    id: 'catalog',
    label: 'Catalog',
    icon: 'catalog',
    scope: 'namespace',
    pages: [
      'Products',
      'Variants',
      'Categories',
      'Collections',
      'Attributes & options',
      'Product types',
      'Media',
    ],
  },
  {
    id: 'inventory',
    label: 'Inventory',
    icon: 'inventory',
    scope: 'namespace',
    pages: [
      'Stock overview',
      'Locations',
      'Reservations',
      'Transfers',
      'Adjustments',
      'Incoming stock',
    ],
  },
  {
    id: 'customers',
    label: 'Customers',
    icon: 'customers',
    scope: 'namespace',
    pages: ['Customers', 'Groups & segments', 'Companies / B2B'],
  },
  {
    id: 'pricing-promotions',
    label: 'Pricing & promotions',
    icon: 'pricing',
    scope: 'namespace',
    pages: [
      'Price lists',
      'Promotions',
      'Discount codes',
      'Campaigns',
      'Gift cards',
      'Store credit',
    ],
  },
  {
    id: 'content',
    label: 'Content',
    icon: 'content',
    scope: 'namespace',
    pages: ['Pages', 'Navigation menus', 'Structured content', 'Translations'],
  },
  {
    id: 'channels-markets',
    label: 'Channels & markets',
    icon: 'channels',
    scope: 'namespace',
    pages: [
      'Sales channels',
      'Markets & regions',
      'Currencies',
      'Catalog assignments',
      'Publication targets',
    ],
  },
  {
    id: 'changes-releases',
    label: 'Changes & releases',
    icon: 'changes',
    scope: 'namespace',
    pages: [
      'My changes',
      'Review queue',
      'Admission reports',
      'Releases',
      'Publications',
      'Compare & recover',
      'Activity',
    ],
  },
  {
    id: 'insights',
    label: 'Insights',
    icon: 'insights',
    scope: 'namespace',
    pages: [
      'Overview',
      'Sales',
      'Products',
      'Customers',
      'Inventory',
      'Saved reports',
      'Exports',
    ],
  },
  {
    id: 'apps-automation',
    label: 'Apps & automation',
    icon: 'apps',
    scope: 'namespace',
    pages: [
      'Installed apps',
      'Connections',
      'Workflows',
      'Runs',
      'Webhooks & deliveries',
      'Jobs',
      'Developer tools',
    ],
  },
  {
    id: 'control-plane',
    label: 'Control plane',
    icon: 'platform',
    scope: 'platform',
    pages: [
      'Namespaces',
      'Repositories',
      'Resource explorer',
      'Reconciliation health',
      'Service accounts',
      'Policies',
      'System health',
    ],
  },
  {
    id: 'settings',
    label: 'Settings',
    icon: 'settings',
    scope: 'namespace',
    pages: [
      'Store details',
      'Team & access',
      'Shipping & delivery',
      'Payments',
      'Taxes & duties',
      'Checkout',
      'Notifications',
      'Domains',
      'Languages',
      'Data & privacy',
    ],
  },
] as const
export type NavigationGroup = (typeof groups)[number]
export type GroupId = NavigationGroup['id']
export type DestinationId = `${GroupId}.${string}`
export interface NavigationDestination {
  id: DestinationId
  label: string
  groupId: GroupId
  groupLabel: string
  icon: NavigationGroup['icon']
  scope: 'namespace' | 'platform'
  to: keyof FileRoutesByTo
  ordinal: number
  description: string
  implementation: 'mock' | 'placeholder'
}
export function slug(value: string) {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '')
}
export const destinations: readonly NavigationDestination[] = groups
  .flatMap((group) =>
    group.pages.map((label) => ({
      id: `${group.id}.${slug(label)}` as DestinationId,
      label,
      groupId: group.id,
      groupLabel: group.label,
      icon: group.icon,
      scope: group.scope,
      to: (group.scope === 'platform'
        ? `/platform/${slug(label)}`
        : `/w/$namespace/${group.id}/${slug(label)}`) as keyof FileRoutesByTo,
      ordinal: 0,
      description: `The future home for ${label.toLowerCase()} in ${group.label.toLowerCase()}.`,
    })),
  )
  .map((destination, index) => ({
    ...destination,
    ordinal: index + 1,
    implementation: destination.id === 'home.overview' ? 'mock' : 'placeholder',
    description:
      destination.id === 'home.overview'
        ? 'Resource work, readiness, and activity in your demo workspace.'
        : destination.description,
  }))
export function destinationById(id: string) {
  const result = destinations.find((destination) => destination.id === id)
  if (!result) throw new Error(`Unknown navigation destination: ${id}`)
  return result
}
export function destinationHref(
  destination: NavigationDestination,
  namespace: string,
) {
  return destination.to.replace('$namespace', encodeURIComponent(namespace))
}
export function destinationAtPath(pathname: string) {
  const normalized = pathname
    .replace(/^\/w\/[^/]+\//, '/w/$namespace/')
    .replace(/\/$/, '')
  return destinations.find((destination) => destination.to === normalized)
}
export function searchDestinations(query: string) {
  const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean)
  return destinations.filter((destination) =>
    words.every((word) =>
      `${destination.groupLabel} ${destination.label}`
        .toLowerCase()
        .includes(word),
    ),
  )
}
