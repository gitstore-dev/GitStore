import type { NamespaceId } from '../../../data/demo'
import type { DemoResource, OverviewFixture, OverviewScenario } from './model'

const resources: readonly DemoResource[] = [
  {
    id: 'stone-basin',
    kind: 'Product',
    spec: { title: 'Kanso Stone Basin' },
    readiness: 'blocked',
    diagnostic:
      'Add the missing shipping weight so this product can pass its checks.',
    path: 'catalog/products/kanso-stone-basin.md',
    references: [
      {
        label: 'Homeware',
        detail: 'Assigned category',
        resourceId: 'homeware',
      },
    ],
  },
  {
    id: 'silk-tunic',
    kind: 'Product',
    spec: { title: 'Raw Silk Tunic' },
    readiness: 'ready',
    diagnostic: 'No issues in this demo snapshot.',
    path: 'catalog/products/raw-silk-tunic.md',
    references: [],
  },
  {
    id: 'ceramic-cup',
    kind: 'Product',
    spec: { title: 'Ceramic Everyday Cup' },
    readiness: 'ready',
    diagnostic: 'No issues in this demo snapshot.',
    path: 'catalog/products/ceramic-cup.md',
    references: [
      {
        label: 'Homeware',
        detail: 'Assigned category',
        resourceId: 'homeware',
      },
    ],
  },
  {
    id: 'tea-ritual',
    kind: 'Product',
    spec: { title: 'Ceremonial Tea Ritual Kit' },
    readiness: 'stale',
    diagnostic:
      'The latest changes to this product are not reflected in this demo yet.',
    path: 'catalog/products/tea-ritual.md',
    references: [],
  },
  {
    id: 'tunic-natural-m',
    kind: 'ProductVariant',
    spec: { title: 'Raw Silk Tunic — Natural / M' },
    readiness: 'failed',
    diagnostic:
      'This barcode is also used by the Black / M variant. Give each variant a unique barcode.',
    path: 'catalog/variants/tunic-natural-m.md',
    references: [
      {
        label: 'Raw Silk Tunic — Black / M',
        detail: 'Uses the same barcode in this demo',
        resourceId: 'tunic-black-m',
      },
      {
        label: 'Raw Silk Tunic',
        detail: 'Product',
        resourceId: 'silk-tunic',
      },
    ],
  },
  {
    id: 'tunic-black-m',
    kind: 'ProductVariant',
    spec: { title: 'Raw Silk Tunic — Black / M' },
    readiness: 'ready',
    diagnostic: 'No issues in this demo snapshot.',
    path: 'catalog/variants/tunic-black-m.md',
    references: [
      {
        label: 'Raw Silk Tunic',
        detail: 'Product',
        resourceId: 'silk-tunic',
      },
    ],
  },
  {
    id: 'tea-kit-natural',
    kind: 'ProductVariant',
    spec: { title: 'Tea Ritual Kit — Natural' },
    readiness: 'ready',
    diagnostic: 'No issues in this demo snapshot.',
    path: 'catalog/variants/tea-kit-natural.md',
    references: [
      {
        label: 'Ceremonial Tea Ritual Kit',
        detail: 'Product',
        resourceId: 'tea-ritual',
      },
    ],
  },
  {
    id: 'homeware',
    kind: 'CategoryTaxonomy',
    spec: { title: 'Homeware' },
    readiness: 'ready',
    diagnostic: 'No issues in this demo snapshot.',
    path: 'catalog/taxonomies/homeware.md',
    references: [],
  },
  {
    id: 'autumn-objects',
    kind: 'Collection',
    spec: { title: 'Autumn Objects' },
    readiness: 'reconciling',
    diagnostic:
      'Products matching this collection are being checked in this demo.',
    path: 'catalog/collections/autumn-objects.md',
    references: [
      {
        label: 'Homeware',
        detail: 'Category used to select products',
        resourceId: 'homeware',
      },
    ],
  },
  {
    id: 'everyday',
    kind: 'Collection',
    spec: { title: 'Everyday Essentials' },
    readiness: 'ready',
    diagnostic: 'No issues in this demo snapshot.',
    path: 'catalog/collections/everyday.md',
    references: [],
  },
  {
    id: 'lookbook',
    kind: 'File',
    name: 'lookbook-hero.webp',
    readiness: 'reconciling',
    diagnostic: 'Image sizes are being prepared in this demo.',
    path: 'media/lookbook-hero.md',
    references: [],
  },
  {
    id: 'basin-image',
    kind: 'File',
    name: 'basin-detail.webp',
    readiness: 'blocked',
    diagnostic:
      'This image does not match the expected file. Review it and upload it again if needed.',
    path: 'media/basin-detail.md',
    references: [
      {
        label: 'Kanso Stone Basin',
        detail: 'Used by this product',
        resourceId: 'stone-basin',
      },
    ],
  },
  {
    id: 'namespace',
    kind: 'Namespace',
    name: 'Atelier Kyoto',
    readiness: 'ready',
    diagnostic: 'Example workspace settings.',
    path: 'namespaces/atelier-kyoto.md',
    references: [],
  },
  {
    id: 'repository',
    kind: 'Repository',
    name: 'Atelier catalog',
    readiness: 'ready',
    diagnostic: 'Example source for store files. No connection is active.',
    path: 'repositories/atelier-catalog.md',
    references: [],
  },
  ...[
    ['ceramics', 'Ceramics', 'homeware'],
    ['tableware', 'Tableware', 'ceramics'],
    ['lifestyle', 'Lifestyle', ''],
  ].map(([id, title, parent]): DemoResource => ({
    id: id!,
    kind: 'CategoryTaxonomy',
    spec: { title: title! },
    readiness: 'ready',
    diagnostic: 'No issues in this demo snapshot.',
    path: `catalog/taxonomies/${id}.md`,
    references: parent
      ? [
          {
            resourceId: parent,
            label: parent === 'homeware' ? 'Homeware' : 'Ceramics',
            detail: 'Parent category',
          },
        ]
      : [],
  })),
  {
    id: 'seasonal-selection',
    kind: 'Collection',
    spec: { title: 'Seasonal Selection' },
    readiness: 'ready',
    diagnostic: 'No product rules have been set in this demo.',
    path: 'catalog/collections/seasonal-selection.md',
    references: [],
  },
]
const atelier: OverviewFixture = {
  namespace: 'atelier-kyoto',
  resources,
  activity: [
    {
      id: 'activity-1',
      resourceId: 'tunic-natural-m',
      title: 'Duplicate barcode found for Raw Silk Tunic',
      time: '2 Nov 2026 · 09:30 UTC',
    },
    {
      id: 'activity-2',
      resourceId: 'autumn-objects',
      title: 'Checking products for Autumn Objects',
      time: '2 Nov 2026 · 09:15 UTC',
    },
    {
      id: 'activity-3',
      resourceId: 'ceramic-cup',
      title: 'Ceramic Everyday Cup marked ready',
      time: '2 Nov 2026 · 09:00 UTC',
    },
  ],
}
const berlin: OverviewFixture = {
  namespace: 'studio-berlin',
  resources: [],
  activity: [],
}
export function overviewFixture(
  namespace: NamespaceId,
  scenario?: OverviewScenario,
): OverviewFixture {
  const fixture =
    scenario === 'first-use'
      ? berlin
      : scenario
        ? atelier
        : namespace === 'studio-berlin'
          ? berlin
          : atelier
  return {
    ...fixture,
    namespace,
    ...(scenario === 'all-clear'
      ? {
          resources: fixture.resources.map((resource) => ({
            ...resource,
            readiness: 'ready' as const,
            diagnostic: 'No issues in this demo snapshot.',
          })),
        }
      : {}),
    ...(scenario === 'filtered-empty'
      ? {
          resources: fixture.resources.filter(
            (resource) => resource.readiness !== 'failed',
          ),
        }
      : {}),
  }
}
