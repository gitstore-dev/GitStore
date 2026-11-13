import { readCatalogView } from '../state/catalog-view'
import {
  createFileRoute,
  Outlet,
  stripSearchParams,
} from '@tanstack/react-router'
import type { SearchSchemaInput } from '@tanstack/react-router'
import type { CatalogSearch } from '../features/catalog/browse/model'
import { catalogSearch } from '../features/catalog/browse/model'
export const Route = createFileRoute('/_shell/w/$namespace/catalog/variants')({
  validateSearch: (raw: Partial<CatalogSearch> & SearchSchemaInput) =>
    catalogSearch(
      { ...raw, view: raw.view ?? readCatalogView('variants') },
      'variants',
    ),
  search: {
    middlewares: [
      stripSearchParams({
        q: '',
        status: 'all',
        related: 'all',
        sort: 'updated',
      } as const),
    ],
  },
  component: Outlet,
})
