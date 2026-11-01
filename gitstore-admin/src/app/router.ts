import { createRouter, createMemoryHistory } from '@tanstack/react-router'
import { routeTree } from '../routeTree.gen'
import type { AppRouterContext } from './router-context'
export function createAppRouter(options?: {
  initialPath?: string
  context?: AppRouterContext
}) {
  return createRouter({
    routeTree,
    context: options?.context ?? {},
    scrollRestoration: true,
    defaultPreload: 'intent',
    ...(options?.initialPath
      ? {
          history: createMemoryHistory({
            initialEntries: [options.initialPath],
          }),
        }
      : {}),
  })
}
export const router = createAppRouter()
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
