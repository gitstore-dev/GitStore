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
    productSearch(raw),
  search: { middlewares: [stripSearchParams(productSearch({}))] },
  component: Outlet,
})
