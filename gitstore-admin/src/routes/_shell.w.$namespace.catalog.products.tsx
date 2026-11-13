import { readCatalogView } from '../state/catalog-view'
import {
  createFileRoute,
  Outlet,
  stripSearchParams,
} from '@tanstack/react-router'
import type { SearchSchemaInput } from '@tanstack/react-router'
import type { ProductSearch } from '../features/catalog/products/model'
import { productSearch } from '../features/catalog/products/model'
export const Route = createFileRoute('/_shell/w/$namespace/catalog/products')({
  validateSearch: (raw: Partial<ProductSearch> & SearchSchemaInput) =>
    productSearch({ ...raw, view: raw.view ?? readCatalogView('products') }),
  search: {
    middlewares: [
      stripSearchParams({
        q: '',
        status: 'all',
        lifecycle: 'all',
        category: 'all',
        sort: 'updated',
      } as const),
    ],
  },
  component: Outlet,
})
