import { useEffect } from 'react'
import {
  createRootRouteWithContext,
  Outlet,
  useRouter,
} from '@tanstack/react-router'
import { PreferencesProvider } from '../state/preferences'
import { HomeDemoProvider } from '../features/demo/HomeDemoProvider'
import { AppShell } from '../shell/AppShell'
import { NotFoundPage } from '../pages/PlaceholderPage'
import type { AppRouterContext } from '../app/router-context'
export const Route = createRootRouteWithContext<AppRouterContext>()({
  component: Root,
  notFoundComponent: () => (
    <AppShell>
      <NotFoundPage />
    </AppShell>
  ),
})
function Root() {
  const context = Route.useRouteContext()
  const { preferences, productNavigation } = context
  const router = useRouter()
  useEffect(() => {
    if (!productNavigation) return
    const before = router.subscribe('onBeforeLoad', (event) =>
      productNavigation.before(event),
    )
    const rendered = router.subscribe('onRendered', (event) =>
      productNavigation.rendered(event.toLocation.href),
    )
    window.addEventListener(
      'gitstore:focus-motion',
      productNavigation.focusMotion,
    )
    const media = matchMedia('(prefers-reduced-motion: reduce)')
    media.addEventListener('change', productNavigation.reduced)
    const observer = new MutationObserver(productNavigation.reduced)
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-motion'],
    })
    return () => {
      before()
      rendered()
      observer.disconnect()
      media.removeEventListener('change', productNavigation.reduced)
      window.removeEventListener(
        'gitstore:focus-motion',
        productNavigation.focusMotion,
      )
      productNavigation.finish()
    }
  }, [router, productNavigation])
  return (
    <PreferencesProvider {...preferences}>
      <HomeDemoProvider seed={context}>
        <Outlet />
      </HomeDemoProvider>
    </PreferencesProvider>
  )
}
