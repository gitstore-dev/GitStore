import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
import { PreferencesProvider } from '../state/preferences'
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
  const { preferences } = Route.useRouteContext()
  return (
    <PreferencesProvider {...preferences}>
      <Outlet />
    </PreferencesProvider>
  )
}
