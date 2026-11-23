import { useState } from 'react'
import { RouterProvider } from '@tanstack/react-router'
import { createAppRouter } from '../app/router'
import type { MediaScenario } from '../features/catalog/media/model'
import type { Theme, MotionPreference } from '../state/preferences'
export function MediaPreview({
  namespace = 'atelier-kyoto',
  itemId,
  query = '',
  scenario,
  theme = 'light',
  motion = 'system',
}: {
  namespace?: 'atelier-kyoto' | 'studio-berlin'
  itemId?: string
  query?: string
  scenario?: MediaScenario
  theme?: Theme
  motion?: MotionPreference
}) {
  const [router] = useState(() =>
    createAppRouter({
      initialPath: `/w/${namespace}/catalog/media${itemId ? `/${itemId}` : ''}${query ? `?${query}` : ''}`,
      context: {
        mediaScenario: scenario,
        preferences: { initialTheme: theme, initialMotion: motion },
      },
    }),
  )
  return <RouterProvider router={router} />
}
