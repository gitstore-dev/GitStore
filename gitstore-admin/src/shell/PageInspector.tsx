import { createContext, useContext, useEffect } from 'react'
import type { ReactNode } from 'react'
export interface PageInspectorAPI {
  owner: string
  register: (owner: string, content: ReactNode | null) => void
  open: (trigger?: HTMLElement) => void
  close: () => void
  registerCloseGuard: (
    owner: string,
    guard: ((proceed: () => void) => void) | null,
  ) => void
}
export const PageInspectorContext = createContext<PageInspectorAPI | null>(null)
export function usePageInspector(content: ReactNode | null) {
  const api = useContext(PageInspectorContext)
  if (!api) throw new Error('Page inspector requires the Admin shell')
  const { owner, register } = api
  useEffect(() => {
    register(owner, content)
    return () => register(owner, null)
  }, [owner, register, content])
  return api
}
