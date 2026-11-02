import { flushSync } from 'react-dom'

export const FOCUS_DURATION = 420
export type FocusDirection = 'enter' | 'leave'
export type FocusRegion = 'sidebar' | 'header' | 'inspector' | 'mobile'
let transitionSerial = 0
let activeTransitions = 0

export function motionReduced() {
  return (
    document.documentElement.dataset.motion === 'reduced' ||
    matchMedia('(prefers-reduced-motion: reduce)').matches
  )
}

function chrome(root: HTMLElement) {
  return Array.from(
    root.querySelectorAll<HTMLElement>('[data-focus-chrome]'),
  ).filter((node) => node.checkVisibility())
}

/** Keeps the working DOM mounted; only presentation changes across the boundary. */
export async function transitionFocus({
  root,
  direction,
  update,
  signal,
  gpu,
}: {
  root: HTMLElement
  direction: FocusDirection
  update: () => void
  signal: AbortSignal
  gpu?: import('./focusGPU').FocusGPU
}) {
  let committed = false
  const commit = () => {
    if (committed || signal.aborted) return
    committed = true
    const scroll = { x: scrollX, y: scrollY }
    flushSync(update)
    window.scrollTo(scroll.x, scroll.y)
  }
  if (motionReduced() || signal.aborted) {
    commit()
    gpu?.dispose()
    return
  }
  activeTransitions++
  window.dispatchEvent(
    new CustomEvent('gitstore:focus-motion', { detail: { active: true } }),
  )
  const animations: Animation[] = []
  const stop = () => animations.forEach((animation) => animation.cancel())
  signal.addEventListener('abort', stop, { once: true })
  const animate = (
    node: Element | null,
    frames: Keyframe[],
    duration: number,
  ) => {
    if (!node || signal.aborted) return
    const animation = node.animate(frames, {
      duration,
      easing: 'cubic-bezier(.2,.7,.2,1)',
      fill: 'both',
    })
    animations.push(animation)
    return animation.finished.catch(() => undefined)
  }
  const settle = (duration: number) =>
    animate(
      root.querySelector('main'),
      [
        { transform: 'scale(.985)', opacity: 0.92 },
        { transform: 'scale(1)', opacity: 1 },
      ],
      duration,
    )
  try {
    // A prepared adapter never holds up the normal transition. Capture has a
    // bounded paint deadline; unsupported/late/lost devices use the DOM path.
    if (gpu && direction === 'enter') {
      const before = root.querySelector('main')?.getBoundingClientRect()
      const stage = await gpu.capture(chrome(root), signal)
      if (stage && !signal.aborted && !motionReduced()) {
        try {
          commit()
          const main = root.querySelector('main')
          const after = main?.getBoundingClientRect()
          if (before && after) {
            void animate(
              main,
              [
                {
                  transform: `translate(${before.x - after.x}px, ${before.y - after.y}px) scale(${before.width / after.width}, 1)`,
                  transformOrigin: 'top left',
                },
                {
                  transform: 'translate(0, 0) scale(.985)',
                  transformOrigin: 'top left',
                  offset: 0.55,
                },
                { transform: 'none', transformOrigin: 'top left' },
              ],
              FOCUS_DURATION,
            )
          }
          void animate(
            root.querySelector('[data-focus-chrome="header"]'),
            [
              { opacity: 0, offset: 0 },
              { opacity: 0, offset: 0.45 },
              { opacity: 1 },
            ],
            FOCUS_DURATION,
          )
          await stage.play(signal)
          return
        } finally {
          stage.dispose()
        }
      }
    }
    if (signal.aborted) return
    if (motionReduced()) {
      commit()
      return
    }
    if (typeof document.startViewTransition === 'function') {
      const html = document.documentElement
      const owner = String(++transitionSerial)
      html.dataset.focusTransitionOwner = owner
      html.dataset.focusTransition = direction
      let transition: ViewTransition | undefined
      const skip = () => transition?.skipTransition()
      try {
        transition = document.startViewTransition(commit)
        void transition.updateCallbackDone.catch(() => undefined)
        signal.addEventListener('abort', skip, { once: true })
        // Rejection means the browser skipped the visual effect. The state
        // update is still required and finished/updateCallbackDone are handled.
        await transition.ready.catch(() => undefined)
        await transition.finished.catch(() => undefined)
        commit()
      } catch {
        commit()
      } finally {
        signal.removeEventListener('abort', skip)
        if (html.dataset.focusTransitionOwner === owner) {
          delete html.dataset.focusTransition
          delete html.dataset.focusTransitionOwner
        }
      }
      return
    }
    // Older engines: actual DOM wipes, followed by the same working-page settle.
    if (direction === 'enter') {
      await Promise.all(
        chrome(root).map((node) => {
          const region = node.dataset.focusChrome
          const end =
            region === 'sidebar'
              ? 'inset(0 100% 0 0)'
              : region === 'inspector'
                ? 'inset(0 0 0 100%)'
                : 'inset(0 0 100% 0)'
          return animate(
            node,
            [
              { clipPath: 'inset(0)', opacity: 1 },
              { clipPath: end, opacity: 0 },
            ],
            220,
          )
        }),
      )
      commit()
      await settle(200)
    } else {
      commit()
      await Promise.all([
        settle(FOCUS_DURATION),
        ...chrome(root).map((node) =>
          animate(
            node,
            [
              { opacity: 0, transform: 'translateY(-10px)' },
              { opacity: 1, transform: 'none' },
            ],
            300,
          ),
        ),
      ])
    }
  } catch {
    commit()
  } finally {
    signal.removeEventListener('abort', stop)
    activeTransitions--
    if (!activeTransitions)
      window.dispatchEvent(
        new CustomEvent('gitstore:focus-motion', { detail: { active: false } }),
      )
    stop()
    gpu?.dispose()
  }
}
