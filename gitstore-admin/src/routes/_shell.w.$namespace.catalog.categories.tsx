import {
  createFileRoute,
  Outlet,
  stripSearchParams,
} from '@tanstack/react-router'
import type { SearchSchemaInput } from '@tanstack/react-router'
import type { CatalogSearch } from '../features/catalog/browse/model'
import { catalogSearch } from '../features/catalog/browse/model'
export const Route = createFileRoute('/_shell/w/$namespace/catalog/categories')(
  {
    validateSearch: (raw: Partial<CatalogSearch> & SearchSchemaInput) =>
      catalogSearch(raw, 'categories'),
    search: {
      middlewares: [stripSearchParams(catalogSearch({}, 'categories'))],
    },
    component: Outlet,
  },
)
