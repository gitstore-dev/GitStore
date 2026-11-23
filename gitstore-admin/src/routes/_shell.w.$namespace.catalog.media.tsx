import {
  createFileRoute,
  Outlet,
  stripSearchParams,
} from '@tanstack/react-router'
import type { SearchSchemaInput } from '@tanstack/react-router'
import { mediaSearch } from '../features/catalog/media/model'
import type { MediaSearch } from '../features/catalog/media/model'
import { readCatalogView } from '../state/catalog-view'
export const Route = createFileRoute('/_shell/w/$namespace/catalog/media')({
  validateSearch: (raw: Partial<MediaSearch> & SearchSchemaInput) =>
    mediaSearch({ ...raw, view: raw.view ?? readCatalogView('media') }),
  search: {
    middlewares: [
      stripSearchParams({
        q: '',
        status: 'all',
        usage: 'all',
        sort: 'updated',
      } as const),
    ],
  },
  component: Outlet,
})
