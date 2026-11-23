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
  '/_shell/w/$namespace/catalog/product-types',
)({
  validateSearch: (raw: Partial<StructureSearch> & SearchSchemaInput) =>
    structureSearch(
      { ...raw, view: raw.view ?? readCatalogView('product-types') },
      'product-types',
    ),
  search: {
    middlewares: [
      stripSearchParams({ q: '', related: 'all', sort: 'updated' } as const),
    ],
  },
  component: Outlet,
})
