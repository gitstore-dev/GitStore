import { createContext, useContext, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { defaultNamespace } from '../data/demo'
import type { NamespaceId } from '../data/demo'
export type Theme = 'light' | 'dark' | 'system'
export type MotionPreference = 'system' | 'reduced'
function stored(key: string) {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}
function persist(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* Private browsing still supports the shell. */
  }
}
export function readTheme(): Theme {
  const value = stored('gitstore.theme')
  return value === 'light' || value === 'dark' ? value : 'system'
}
export function effectiveTheme(theme: Theme, systemDark: boolean) {
  return theme === 'system' ? (systemDark ? 'dark' : 'light') : theme
}
interface Preferences {
  theme: Theme
  setTheme: (value: Theme) => void
  motion: MotionPreference
  setMotion: (value: MotionPreference) => void
  collapsed: boolean
  setCollapsed: (value: boolean) => void
  namespace: NamespaceId
  setNamespace: (value: NamespaceId) => void
}
const Context = createContext<Preferences | null>(null)
export function PreferencesProvider({
  children,
  initialTheme,
  initialMotion,
  initialCollapsed,
}: {
  children: ReactNode
  initialTheme?: Theme
  initialMotion?: MotionPreference
  initialCollapsed?: boolean
}) {
  const [theme, setTheme] = useState<Theme>(() => initialTheme ?? readTheme())
  const [motion, setMotion] = useState<MotionPreference>(
    () =>
      initialMotion ??
      (stored('gitstore.motion') === 'reduced' ? 'reduced' : 'system'),
  )
  const [collapsed, setCollapsed] = useState(
    () => initialCollapsed ?? stored('gitstore.sidebar') === 'collapsed',
  )
  const [namespace, setNamespace] = useState<NamespaceId>(defaultNamespace)
  useEffect(() => {
    const colors = matchMedia('(prefers-color-scheme: dark)')
    const movement = matchMedia('(prefers-reduced-motion: reduce)')
    const apply = () => {
      document.documentElement.dataset.theme = effectiveTheme(
        theme,
        colors.matches,
      )
      document.documentElement.dataset.motion =
        motion === 'reduced' || movement.matches ? 'reduced' : 'full'
    }
    apply()
    colors.addEventListener('change', apply)
    movement.addEventListener('change', apply)
    persist('gitstore.theme', theme)
    persist('gitstore.motion', motion)
    return () => {
      colors.removeEventListener('change', apply)
      movement.removeEventListener('change', apply)
    }
  }, [theme, motion])
  useEffect(() => {
    persist('gitstore.sidebar', collapsed ? 'collapsed' : 'expanded')
  }, [collapsed])
  return (
    <Context
      value={{
        theme,
        setTheme,
        motion,
        setMotion,
        collapsed,
        setCollapsed,
        namespace,
        setNamespace,
      }}
    >
      {children}
    </Context>
  )
}
export function usePreferences() {
  const context = useContext(Context)
  if (!context) throw new Error('PreferencesProvider is required')
  return context
}
export function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(() => matchMedia(query).matches)
  useEffect(() => {
    const media = matchMedia(query)
    const update = () => setMatches(media.matches)
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [query])
  return matches
}
