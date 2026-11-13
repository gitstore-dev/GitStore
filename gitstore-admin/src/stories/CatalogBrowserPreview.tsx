import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import { createAppRouter } from '../app/router'
import type {
  CatalogDomain,
  CatalogScenario,
} from '../features/catalog/browse/model'
import type { Theme, MotionPreference } from '../state/preferences'
export function CatalogBrowserPreview({
  domain = 'variants',
  namespace = 'atelier-kyoto',
  itemId,
  query = '',
  scenario,
  theme = 'light',
  motion = 'system',
}: {
  domain?: CatalogDomain
  namespace?: 'atelier-kyoto' | 'studio-berlin'
  itemId?: string
  query?: string
  scenario?: CatalogScenario
  theme?: Theme
  motion?: MotionPreference
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/catalog/${domain}${itemId ? `/${itemId}` : ''}${query ? `?${query}` : ''}`,
      context: {
        catalogScenario: scenario,
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} />
}
