import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/orders/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/orders/all-orders',
      params: { namespace: params.namespace },
    })
  },
})
