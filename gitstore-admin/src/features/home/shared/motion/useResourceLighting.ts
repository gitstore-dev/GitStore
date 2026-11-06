import { useEffect, useRef } from 'react'
import { motionReduced } from '../../../../shell/motion/focusTransition'
import type { HaloCard, ResourceLighting } from './resourceLighting'
import { haloColor } from './haloColor'
import { usePreferences } from '../../../../state/preferences'

/** Demand-driven rendering: static at rest, bounded sweeps on explicit input. */
export function useResourceLighting(mode: 'auto' | 'css' = 'auto') {
  const { motion } = usePreferences()
  const root = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const page = root.current
    if (!page) return
    let renderer: ResourceLighting | undefined
    let controller: AbortController | undefined
    let canvas: HTMLCanvasElement | undefined
    let frame = 0
    let until = 0
    let started = 0
    let paused = false
    let visible = true
    let failed = false
    let disposed = false
    const cards = () =>
      Array.from(page.querySelectorAll<HTMLElement>('[data-halo-card]'))
    const release = () => {
      cancelAnimationFrame(frame)
      frame = 0
      controller?.abort()
      controller = undefined
      renderer?.dispose()
      renderer = undefined
      canvas?.remove()
      canvas = undefined
      page.dataset.lighting = 'css'
    }
    const permitted = () =>
      !disposed &&
      !failed &&
      mode === 'auto' &&
      motion !== 'reduced' &&
      !motionReduced()
    const render = (now: number) => {
      frame = 0
      if (!renderer || !permitted()) return
      if (paused || !visible || document.hidden) {
        renderer.clear()
        return
      }
      const bounds = page.getBoundingClientRect()
      if (!bounds.width || !bounds.height) return
      const scaleX = bounds.width / page.offsetWidth
      const scaleY = bounds.height / page.offsetHeight
      const pulse = Math.max(0, (until - now) / 650)
      const items: HaloCard[] = cards().flatMap((node) => {
        const rect = node.getBoundingClientRect()
        if (
          rect.bottom < 0 ||
          rect.top > innerHeight ||
          rect.right < 0 ||
          rect.left > innerWidth
        )
          return []
        const active = node.matches(':hover, :focus-within')
        return [
          {
            bounds: [
              (rect.left - bounds.left) / scaleX,
              (rect.top - bounds.top) / scaleY,
              rect.width / scaleX,
              rect.height / scaleY,
            ],
            color: haloColor(
              getComputedStyle(node).getPropertyValue('--state-color'),
            ),
            emphasis: active ? 0.35 + 0.65 * pulse : 0.3 * pulse,
          } as HaloCard,
        ]
      })
      renderer.draw(
        page.offsetWidth,
        page.offsetHeight,
        items,
        (now - started) / 1000,
      )
      if (now < until && items.length) frame = requestAnimationFrame(render)
    }
    const invalidate = (duration = 0) => {
      if (disposed) return
      until = Math.max(until, performance.now() + duration)
      if (!frame && renderer) frame = requestAnimationFrame(render)
    }
    const sync = () => {
      if (!permitted()) {
        release()
        return
      }
      if (controller) {
        invalidate()
        return
      }
      if (document.hidden || !visible || !navigator.gpu) {
        page.dataset.lighting = 'css'
        return
      }
      const attempt = new AbortController()
      controller = attempt
      const surface = document.createElement('canvas')
      surface.setAttribute('aria-hidden', 'true')
      surface.inert = true
      surface.dataset.resourceLighting = ''
      void import('./resourceLighting')
        .then(({ prepareResourceLighting }) =>
          prepareResourceLighting(surface, attempt.signal, () => {
            failed = true
            release()
          }),
        )
        .then((result) => {
          if (attempt.signal.aborted || disposed) {
            result?.dispose()
            return
          }
          if (!result) {
            failed = true
            release()
            return
          }
          renderer = result
          canvas = surface
          started = performance.now()
          page.prepend(surface)
          page.dataset.lighting = 'gpu'
          invalidate()
        })
        .catch(() => {
          if (!attempt.signal.aborted) {
            failed = true
            release()
          }
        })
    }
    const interaction = () => invalidate(650)
    const geometry = () => invalidate(340)
    const preferences = new MutationObserver(sync)
    preferences.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-motion', 'data-theme'],
    })
    const content = new MutationObserver((records) => {
      if (
        records.some(
          (record) =>
            record.type === 'childList' ||
            record.attributeName === 'data-grid-moving' ||
            record.attributeName === 'data-readiness',
        )
      )
        geometry()
    })
    content.observe(page, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['data-readiness', 'data-grid-moving'],
    })
    const resize = new ResizeObserver(geometry)
    resize.observe(page)
    const visibility = new IntersectionObserver(([entry]) => {
      visible = !!entry?.isIntersecting
      sync()
      invalidate()
    })
    visibility.observe(page)
    const focusMotion = (event: Event) => {
      paused = (event as CustomEvent<{ active: boolean }>).detail.active
      if (paused) {
        cancelAnimationFrame(frame)
        frame = 0
        renderer?.clear()
      } else geometry()
    }
    const media = matchMedia('(prefers-reduced-motion: reduce)')
    const tabVisibility = () => {
      sync()
      invalidate()
    }
    media.addEventListener('change', sync)
    document.addEventListener('visibilitychange', tabVisibility)
    window.addEventListener('gitstore:focus-motion', focusMotion)
    window.addEventListener('scroll', geometry, {
      passive: true,
      capture: true,
    })
    window.addEventListener('resize', geometry)
    page.addEventListener('pointerover', interaction)
    page.addEventListener('pointerout', interaction)
    page.addEventListener('focusin', interaction)
    page.addEventListener('focusout', interaction)
    sync()
    return () => {
      disposed = true
      release()
      preferences.disconnect()
      content.disconnect()
      resize.disconnect()
      visibility.disconnect()
      media.removeEventListener('change', sync)
      document.removeEventListener('visibilitychange', tabVisibility)
      window.removeEventListener('gitstore:focus-motion', focusMotion)
      window.removeEventListener('scroll', geometry, true)
      window.removeEventListener('resize', geometry)
      page.removeEventListener('pointerover', interaction)
      page.removeEventListener('pointerout', interaction)
      page.removeEventListener('focusin', interaction)
      page.removeEventListener('focusout', interaction)
    }
  }, [mode, motion])
  return root
}
