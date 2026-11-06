import { useCallback, useEffect, useLayoutEffect, useRef } from 'react'
import { motionReduced } from '../../../../shell/motion/focusTransition'
import { GridMotion } from './gridMotion'

export function useGridMotion() {
  const stage = useRef<HTMLDivElement>(null)
  const paused = useRef(false)
  const controller = useRef<GridMotion | undefined>(undefined)
  useLayoutEffect(() => {
    if (stage.current && !controller.current)
      controller.current = new GridMotion(stage.current)
  })
  useEffect(() => {
    return () => {
      controller.current?.dispose()
      controller.current = undefined
    }
  }, [])
  useLayoutEffect(() => {
    controller.current?.play(motionReduced() || paused.current)
  })
  useEffect(() => {
    const stop = () => controller.current?.dispose()
    // Finish choreography before Focus snapshots, or when motion is reduced.
    const observer = new MutationObserver(() => {
      if (motionReduced()) stop()
    })
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-motion'],
    })
    const media = matchMedia('(prefers-reduced-motion: reduce)')
    media.addEventListener('change', stop)
    const focusMotion = (event: Event) => {
      paused.current = (event as CustomEvent<{ active: boolean }>).detail.active
      stop()
    }
    window.addEventListener('gitstore:focus-motion', focusMotion)
    return () => {
      observer.disconnect()
      media.removeEventListener('change', stop)
      window.removeEventListener('gitstore:focus-motion', focusMotion)
    }
  }, [])
  const capture = useCallback(() => {
    if (!paused.current && !motionReduced()) controller.current?.capture()
  }, [])
  return { stage, capture }
}
