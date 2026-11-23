import {
  createFileRoute,
  Outlet,
  stripSearchParams,
} from '@tanstack/react-router'
import type { SearchSchemaInput } from '@tanstack/react-router'
import { structureSearch } from '../features/catalog/structure/model'
import type { StructureSearch } from '../features/catalog/structure/model'
import { readCatalogView } from '../state/catalog-view'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/attributes-options',
)({
  validateSearch: (raw: Partial<StructureSearch> & SearchSchemaInput) =>
    structureSearch(
      { ...raw, view: raw.view ?? readCatalogView('attributes-options') },
      'attributes-options',
    ),
  search: {
    middlewares: [
      stripSearchParams({ q: '', related: 'all', sort: 'updated' } as const),
    ],
  },
  component: Outlet,
})
