import type { KeyboardEvent } from 'react'

/** Optional arrow shortcuts for the sidebar; links keep native Enter activation. */
export function moveNavigationFocus(event: KeyboardEvent<HTMLElement>) {
  if (
    event.defaultPrevented ||
    event.altKey ||
    event.ctrlKey ||
    event.metaKey ||
    event.shiftKey
  )
    return
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const current =
    event.target instanceof Element
      ? event.target.closest<HTMLElement>('a[href], button')
      : null
  const items = Array.from(
    event.currentTarget.querySelectorAll<HTMLElement>(
      'a[href], button:not(:disabled)',
    ),
  ).filter(
    (item) =>
      item.getClientRects().length > 0 &&
      item.getAttribute('aria-disabled') !== 'true',
  )
  const index = current ? items.indexOf(current) : -1
  if (index < 0) return
  event.preventDefault()
  const next =
    event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? items.length - 1
        : Math.max(
            0,
            Math.min(
              items.length - 1,
              index + (event.key === 'ArrowDown' ? 1 : -1),
            ),
          )
  items[next]?.focus()
}
