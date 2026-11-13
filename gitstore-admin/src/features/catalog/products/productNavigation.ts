import type { ParsedLocation } from '@tanstack/react-router'
import { motionReduced } from '../../../shell/motion/focusTransition'
import type { ProductSearch } from './model'
import './productMotion.css'

interface ProductRoute {
  namespace: string
  id?: string
  catalog?: string
}
export function productRoute(path: string): ProductRoute | undefined {
  const match =
    /^\/w\/([^/]+)\/catalog\/(products|variants|categories|collections)(?:\/([^/]+))?\/?$/.exec(
      path,
    )
  if (!match || match[3] === 'new') return
  return {
    namespace: match[1]!,
    id: match[3],
    ...(match[2] !== 'products' ? { catalog: match[2] } : {}),
  }
}
export function productJourney(from: string, to: string) {
  const a = productRoute(from),
    b = productRoute(to)
  if (
    !a ||
    !b ||
    a.namespace !== b.namespace ||
    a.catalog !== b.catalog ||
    !!a.id === !!b.id
  )
    return
  return {
    namespace: a.namespace,
    id: (a.id ?? b.id)!,
    direction: a.id ? ('return' as const) : ('open' as const),
  }
}
export interface ProductListPosition {
  epoch: number
  filterKey: string
  cursors: readonly string[]
  search: object
  scroll: number
  selected?: string
}
type Change = { fromLocation?: ParsedLocation; toLocation: ParsedLocation }

/** Per-router, bounded, memory-only state. No DOM clones or serialized demo data. */
export class ProductNavigation {
  private page?: HTMLElement
  private marked = new Set<HTMLElement>()
  bind(page: HTMLElement | null) {
    this.page = page ?? undefined
  }
  rendered(href: string) {
    if (this.page) this.mounted(href, Number(this.page.dataset.productEpoch))
  }
  private lists = new Map<string, ProductListPosition>()
  private origins = new Map<string, string>()
  private pending?: ReturnType<typeof productJourney>
  private destination = ''
  private origin = ''
  private owner = 0
  private cleanupTimer?: number
  private focused = false
  private positioned = false
  private focusActive = false
  private active?: ViewTransition

  remember(href: string, value: ProductListPosition) {
    const previous = this.lists.get(href)
    this.lists.delete(href)
    this.lists.set(href, {
      ...value,
      scroll: previous?.epoch === value.epoch ? previous.scroll : value.scroll,
      selected: previous?.epoch === value.epoch ? previous.selected : undefined,
    })
    if (this.lists.size > 32) this.lists.delete(this.lists.keys().next().value!)
  }
  position(href: string, epoch: number, filterKey?: string) {
    const value =
      this.lists.get(href) ??
      (filterKey
        ? [...this.lists.values()]
            .reverse()
            .find(
              (value) => value.filterKey === filterKey && value.epoch === epoch,
            )
        : undefined)
    return value?.epoch === epoch ? value : undefined
  }
  returnSearch<T = ProductSearch>(path: string) {
    const href = this.origins.get(path)
    return (href ? this.lists.get(href)?.search : undefined) as T | undefined
  }
  before({ fromLocation, toLocation }: Change) {
    this.finish()
    this.pending =
      fromLocation && productJourney(fromLocation.pathname, toLocation.pathname)
    this.destination = toLocation.href
    this.focused = false
    this.positioned = false
    if (!this.pending || !fromLocation) return
    if (this.pending.direction === 'open') {
      this.origin = fromLocation.href
      const list = this.lists.get(this.origin)
      if (list) {
        list.scroll = scrollY
        list.selected = this.pending.id
      }
      this.origins.set(toLocation.pathname, this.origin)
      if (this.origins.size > 32)
        this.origins.delete(this.origins.keys().next().value!)
    } else this.origin = toLocation.href
  }
  types = ({ fromLocation, toLocation }: Change): string[] | false => {
    const journey =
      fromLocation && productJourney(fromLocation.pathname, toLocation.pathname)
    if (
      !journey ||
      !this.pending ||
      motionReduced() ||
      this.focusActive ||
      document.hidden
    )
      return false
    const source = this.surface(journey.id)
    if (!source?.checkVisibility()) return false
    const sourceImage = source.querySelector<HTMLImageElement>(
      '[data-product-image] img',
    )
    if (sourceImage && (!sourceImage.complete || !sourceImage.naturalWidth))
      return false
    this.pending = journey
    window.dispatchEvent(
      new CustomEvent('gitstore:product-motion', { detail: { active: true } }),
    )
    document.documentElement.dataset.productTransition = journey.direction
    this.mark(journey.id)
    const owner = ++this.owner
    // Native completion owns cleanup; the timer bounds skipped/rejected captures
    // on engines without document.activeViewTransition.
    const previousTransition = (
      document as Document & { activeViewTransition?: ViewTransition }
    ).activeViewTransition
    queueMicrotask(() => {
      const active = (
        document as Document & { activeViewTransition?: ViewTransition }
      ).activeViewTransition
      if (
        active &&
        active !== previousTransition &&
        active.types.has(`product-${journey.direction}`)
      ) {
        this.active = active
        void active.ready.catch(() => undefined)
        void active.finished
          .catch(() => undefined)
          .then(() => {
            if (owner === this.owner) this.settle(owner)
          })
      }
    })
    clearTimeout(this.cleanupTimer)
    this.cleanupTimer = window.setTimeout(() => {
      if (owner === this.owner) this.clear()
    }, 1200)
    return [`product-${journey.direction}`]
  }
  mounted(href: string, epoch: number) {
    if (!this.pending || this.destination !== href) return
    const { id, direction } = this.pending
    const target = this.surface(id)
    if (!target) {
      this.finish()
      return
    }
    if (direction === 'return' && !this.positioned) {
      this.positioned = true
      const position = this.position(href, epoch)
      if (position)
        window.scrollTo({ top: position.scroll, behavior: 'instant' })
    }
    if (document.documentElement.dataset.productTransition) this.mark(id)
    if (!this.focused) {
      const owner = this.owner
      requestAnimationFrame(() => {
        if (owner !== this.owner || !target.isConnected) return
        const focus =
          direction === 'return'
            ? target
            : target.querySelector<HTMLElement>('h1')
        focus?.focus({ preventScroll: true })
        this.focused = document.activeElement === focus
      })
    }
    // Also use the actual pseudo-element animations to observe completion in
    // engines that support snapshots but not activeViewTransition.
    const owner = this.owner
    let attempts = 0
    const observeSnapshots = () => {
      if (owner !== this.owner) return
      const snapshots = document
        .getAnimations()
        .filter((animation) =>
          (
            animation.effect as KeyframeEffect | null
          )?.pseudoElement?.startsWith('::view-transition'),
        )
      if (snapshots.length)
        void Promise.allSettled(snapshots.map((a) => a.finished)).then(() => {
          if (owner === this.owner) this.settle(owner)
        })
      else if (++attempts < 8) requestAnimationFrame(observeSnapshots)
    }
    requestAnimationFrame(observeSnapshots)
  }
  private surface(id: string) {
    return Array.from(
      this.page?.querySelectorAll<HTMLElement>('[data-product-surface]') ?? [],
    ).find((node) => node.dataset.productSurface === id)
  }
  private mark(id: string) {
    const surface = this.surface(id)
    if (!surface) return
    this.marked.add(surface)
    surface.dataset.productMotion = 'surface'
    const content = surface.querySelector<HTMLElement>('[data-product-content]')
    if (content) {
      this.marked.add(content)
      content.dataset.productMotion = `${content.dataset.productContent}-content`
    }
    for (const role of ['title', 'image']) {
      const node = surface.querySelector<HTMLElement>(`[data-product-${role}]`)
      if (node) {
        this.marked.add(node)
        node.dataset.productMotion = role
      }
    }
  }
  private settle(owner: number) {
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        if (owner === this.owner) this.clear()
      }),
    )
  }
  private clear() {
    clearTimeout(this.cleanupTimer)
    this.marked.forEach((node) => node.removeAttribute('data-product-motion'))
    this.marked.clear()
    const wasActive = !!document.documentElement.dataset.productTransition
    delete document.documentElement.dataset.productTransition
    if (wasActive)
      window.dispatchEvent(
        new CustomEvent('gitstore:product-motion', {
          detail: { active: false },
        }),
      )
    this.active = undefined
  }
  finish = () => {
    ++this.owner
    this.active?.skipTransition()
    if (document.documentElement.dataset.productTransition) {
      document.getAnimations().forEach((animation) => {
        if (
          (
            animation.effect as KeyframeEffect | null
          )?.pseudoElement?.startsWith('::view-transition')
        )
          animation.cancel()
      })
    }
    this.clear()
  }
  focusMotion = (event: Event) => {
    this.focusActive = (event as CustomEvent<{ active: boolean }>).detail.active
    if (this.focusActive) this.finish()
  }
  reduced = () => {
    if (motionReduced()) this.finish()
  }
}
