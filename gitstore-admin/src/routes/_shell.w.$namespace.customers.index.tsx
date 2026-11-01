import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/customers/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/customers/customers',
      params: { namespace: params.namespace },
    })
  },
})
