import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute(
  '/_shell/w/$namespace/pricing-promotions/',
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/pricing-promotions/price-lists',
      params: { namespace: params.namespace },
    })
  },
})
