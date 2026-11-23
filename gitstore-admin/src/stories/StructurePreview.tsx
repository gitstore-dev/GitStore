import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import { createAppRouter } from '../app/router'
import type {
  StructureDomain,
  StructureScenario,
} from '../features/catalog/structure/model'
import type { Theme, MotionPreference } from '../state/preferences'
export function StructurePreview({
  domain = 'attributes-options',
  namespace = 'atelier-kyoto',
  detail = '',
  query = '',
  scenario,
  theme = 'light',
  motion = 'system',
  inspector = false,
}: {
  domain?: StructureDomain
  namespace?: 'atelier-kyoto' | 'studio-berlin'
  detail?: string
  query?: string
  scenario?: StructureScenario
  theme?: Theme
  motion?: MotionPreference
  inspector?: boolean
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/catalog/${domain}${detail ? `/${detail}` : ''}${query ? `?${query}` : ''}`,
      context: {
        structureScenario: scenario,
        initialLayout: inspector ? 'inspector' : 'standard',
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} />
}
