import { useEffect, useRef } from 'react'
import type { LayoutMode } from '../WorkspaceLayout'
import { motionReduced, transitionFocus } from './focusTransition'
import type { FocusGPU } from './focusGPU'
import './focusMotion.css'

/** GPU work is warmed on intent, bounded by an idle timeout, and never required. */
export function useFocusMotion(
  mode: LayoutMode,
  setMode: (mode: LayoutMode) => void,
  route: string,
) {
  const root = useRef<HTMLDivElement>(null)
  const active = useRef<AbortController | undefined>(undefined)
  const warming = useRef<AbortController | undefined>(undefined)
  const prepared = useRef<FocusGPU | undefined>(undefined)
  const idle = useRef<number | undefined>(undefined)
  const release = () => {
    clearTimeout(idle.current)
    warming.current?.abort()
    warming.current = undefined
    prepared.current?.dispose()
    prepared.current = undefined
  }
  useEffect(() => {
    return () => {
      active.current?.abort()
      clearTimeout(idle.current)
      warming.current?.abort()
      warming.current = undefined
      prepared.current?.dispose()
      prepared.current = undefined
    }
  }, [route])

  function prepareFocus() {
    if (motionReduced() || mode === 'focus' || warming.current) return
    const controller = (warming.current = new AbortController())
    idle.current = window.setTimeout(release, 8000)
    void import('./focusGPU')
      .then(({ prepareFocusGPU }) => prepareFocusGPU(controller.signal))
      .then((gpu) => {
        if (controller.signal.aborted) gpu?.dispose()
        else prepared.current = gpu
      })
      .catch(release)
  }

  function changeMode(next: LayoutMode) {
    active.current?.abort()
    if (!root.current || (mode !== 'focus' && next !== 'focus')) {
      release()
      setMode(next)
      return
    }
    const controller = (active.current = new AbortController())
    clearTimeout(idle.current)
    const gpu = prepared.current
    const warmController = warming.current
    warming.current = undefined
    prepared.current = undefined
    if (!gpu) warmController?.abort()
    void transitionFocus({
      root: root.current,
      direction: next === 'focus' ? 'enter' : 'leave',
      update: () => setMode(next),
      signal: controller.signal,
      gpu,
    }).finally(() => {
      if (active.current === controller) active.current = undefined
      gpu?.dispose()
      warmController?.abort()
    })
  }
  return { root, changeMode, prepareFocus }
}
