import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useReducer,
  useState,
} from 'react'
import type { ReactNode } from 'react'
import { Heading } from 'react-aria-components'
import { namespaces } from '../../data/demo'
import type { NamespaceId } from '../../data/demo'
import { ActionButton } from '../../components/controls'
import { ConfirmationDialog } from '../../components/ConfirmationDialog'
import {
  createSession,
  projectedResources,
  sessionReducer,
  resetSession,
} from './model'
import type { DemoSeed, DemoSession, HomeAction } from './model'
import styles from './demo.module.css'
interface StoreAction {
  namespace: NamespaceId
  action: HomeAction | { type: 'reset' }
}
interface StoreAPI {
  sessions: Record<NamespaceId, DemoSession>
  dispatch: (action: StoreAction) => void
  requestReset: (namespace: NamespaceId) => void
  announce: (message: string) => void
}
const Context = createContext<StoreAPI | null>(null)
export function HomeDemoProvider({
  children,
  seed = {},
}: {
  children: ReactNode
  seed?: DemoSeed
}) {
  const [sessions, storeDispatch] = useReducer(
    (
      state: Record<NamespaceId, DemoSession>,
      { namespace, action }: StoreAction,
    ) => ({
      ...state,
      [namespace]:
        action.type === 'reset'
          ? resetSession(state[namespace], seed)
          : sessionReducer(state[namespace], action),
    }),
    seed,
    (initial) => ({
      'atelier-kyoto': createSession('atelier-kyoto', initial),
      'studio-berlin': createSession('studio-berlin', initial),
    }),
  )
  const [resetting, setResetting] = useState<NamespaceId | null>(null)
  const [message, setMessage] = useState('')
  const [announcementId, setAnnouncementId] = useState(0)
  const announce = useCallback((text: string) => {
    setMessage(text)
    setAnnouncementId((id) => id + 1)
  }, [])
  const dispatch = useCallback((action: StoreAction) => {
    setMessage('')
    storeDispatch(action)
  }, [])
  const requestReset = useCallback(
    (namespace: NamespaceId) => setResetting(namespace),
    [],
  )
  const api = useMemo(
    () => ({ sessions, dispatch, requestReset, announce }),
    [sessions, dispatch, requestReset, announce],
  )
  const storeName = namespaces.find((entry) => entry.id === resetting)?.name
  return (
    <Context value={api}>
      {children}
      {message && (
        <p role="status" aria-live="polite" className={styles.srOnly}>
          <span key={announcementId}>{message}</span>
        </p>
      )}
      <ConfirmationDialog
        isOpen={resetting !== null}
        onOpenChange={(open) => {
          if (!open) setResetting(null)
        }}
      >
        <Heading slot="title">
          Reset catalog and Home demo for {storeName}?
        </Heading>
        <p>
          Restore the original catalog (Products, Variants, Categories, and
          Collections), Overview, My work, Recent activity, and Setup guide
          examples for {storeName}. This discards all saved demo changes,
          recoveries, setup reviews, and unsaved draft text for this store.
          Other stores are unchanged.
        </p>
        <div className={styles.actions}>
          <ActionButton autoFocus onPress={() => setResetting(null)}>
            Cancel
          </ActionButton>
          <ActionButton
            variant="primary"
            onPress={() => {
              if (resetting)
                dispatch({ namespace: resetting, action: { type: 'reset' } })
              announce(
                `Catalog and Home demo reset for ${storeName}. Original examples restored.`,
              )
              setResetting(null)
              requestAnimationFrame(() =>
                document
                  .querySelector<HTMLElement>('#main-content h1')
                  ?.focus(),
              )
            }}
          >
            Confirm demo reset
          </ActionButton>
        </div>
      </ConfirmationDialog>
    </Context>
  )
}
export function useHomeDemo(namespace: NamespaceId) {
  const context = useContext(Context)
  if (!context) throw new Error('Home pages require HomeDemoProvider')
  const { sessions, dispatch: storeDispatch, requestReset } = context
  const session = sessions[namespace]
  const dispatch = useCallback(
    (action: HomeAction) => storeDispatch({ namespace, action }),
    [storeDispatch, namespace],
  )
  const reset = useCallback(
    () => requestReset(namespace),
    [requestReset, namespace],
  )
  const resources = useMemo(() => projectedResources(session), [session])
  return { session, resources, dispatch, reset, announce: context.announce }
}
