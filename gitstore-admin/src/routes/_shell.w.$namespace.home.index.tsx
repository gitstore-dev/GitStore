import { createFileRoute, redirect } from '@tanstack/react-router'
export const Route = createFileRoute('/_shell/w/$namespace/home/')({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: '/w/$namespace/home/overview',
      params: { namespace: params.namespace },
    })
  },
})
