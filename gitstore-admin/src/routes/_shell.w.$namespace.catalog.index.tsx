import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/catalog/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/catalog/products',
      params: { namespace: params.namespace },
    })
  },
})
