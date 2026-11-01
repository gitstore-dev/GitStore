import { createFileRoute, redirect } from '@tanstack/react-router'
import { defaultNamespace } from '../data/demo'
export const Route = createFileRoute('/')({
  beforeLoad: () => {
    throw redirect({
      to: '/w/$namespace/home/overview',
      params: { namespace: defaultNamespace },
    })
  },
})
