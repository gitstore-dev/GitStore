import { createFileRoute, Outlet } from '@tanstack/react-router'
export const Route = createFileRoute(
  '/_shell/w/$namespace/catalog/products/$productId',
)({ component: Outlet })
