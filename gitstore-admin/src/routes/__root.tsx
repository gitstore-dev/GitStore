import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
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
  const { preferences } = context
  return (
    <PreferencesProvider {...preferences}>
      <HomeDemoProvider seed={context}>
        <Outlet />
      </HomeDemoProvider>
    </PreferencesProvider>
  )
}
