import { createRouter, createMemoryHistory } from '@tanstack/react-router'
import { routeTree } from '../routeTree.gen'
import { ProductNavigation } from '../features/catalog/products/productNavigation'
import type { AppRouterContext } from './router-context'
export function createAppRouter(options?: {
  initialPath?: string
  context?: AppRouterContext
}) {
  const productNavigation = new ProductNavigation()
  return createRouter({
    routeTree,
    context: { ...options?.context, productNavigation },
    defaultViewTransition:
      typeof CSS !== 'undefined' &&
      CSS.supports('selector(:active-view-transition-type(a))')
        ? { types: productNavigation.types }
        : false,
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
