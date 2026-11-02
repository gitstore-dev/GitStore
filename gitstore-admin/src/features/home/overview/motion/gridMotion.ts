export const GRID_DURATION = 300
const easing = 'cubic-bezier(.2,.75,.2,1)'
interface Snapshot {
  id: string
  rect: DOMRect
  copy: HTMLElement
}

/** Measured DOM choreography; outgoing copies never remain interactive. */
export class GridMotion {
  private before: Snapshot[] = []
  private height = 0
  private pending = false
  private animations = new Set<Animation>()
  private copies = new Set<HTMLElement>()

  constructor(private readonly stage: HTMLElement) {}

  capture() {
    // Read current visual bounds before cancelling an interrupted movement.
    const bounds = this.stage.getBoundingClientRect()
    this.before = Array.from(
      this.stage.querySelectorAll<HTMLElement>('[data-resource-id]'),
    ).map((node) => {
      const rect = node.getBoundingClientRect()
      const copy = node.cloneNode(true) as HTMLElement
      copy.inert = true
      copy.setAttribute('aria-hidden', 'true')
      copy.dataset.gridExit = ''
      for (const child of [copy, ...copy.querySelectorAll<HTMLElement>('*')]) {
        child.removeAttribute('id')
        child.removeAttribute('data-resource-id')
        child.removeAttribute('data-halo-card')
        child.removeAttribute('autofocus')
      }
      Object.assign(copy.style, {
        position: 'absolute',
        margin: '0',
        left: `${rect.left - bounds.left}px`,
        top: `${rect.top - bounds.top}px`,
        width: `${rect.width}px`,
        height: `${rect.height}px`,
        pointerEvents: 'none',
        transform: 'none',
        zIndex: '2',
      })
      return { id: node.dataset.resourceId!, rect, copy }
    })
    this.height = bounds.height
    this.pending = true
    this.stop()
  }

  private animate(
    node: Element,
    frames: Keyframe[],
    duration: number,
    delay = 0,
  ) {
    const animation = node.animate(frames, {
      duration,
      delay,
      easing,
      fill: 'both',
    })
    this.animations.add(animation)
    void animation.finished
      .catch(() => undefined)
      .then(() => {
        animation.cancel()
        this.animations.delete(animation)
        if (node instanceof HTMLElement && this.copies.delete(node))
          node.remove()
        if (!this.animations.size) delete this.stage.dataset.gridMoving
      })
  }

  play(reduced: boolean) {
    if (!this.pending) return
    this.pending = false
    const previous = new Map(this.before.map((item) => [item.id, item]))
    this.before = []
    if (reduced || typeof this.stage.animate !== 'function') return
    const nodes = Array.from(
      this.stage.querySelectorAll<HTMLElement>('[data-resource-id]'),
    )
    const height = this.stage.getBoundingClientRect().height
    this.stage.dataset.gridMoving = ''
    let arriving = 0
    for (const node of nodes) {
      const rect = node.getBoundingClientRect()
      const old = previous.get(node.dataset.resourceId!)
      previous.delete(node.dataset.resourceId!)
      if (old) {
        const x = old.rect.left - rect.left
        const y = old.rect.top - rect.top
        if (
          Math.abs(x) + Math.abs(y) > 0.5 ||
          Math.abs(old.rect.width - rect.width) > 0.5
        ) {
          this.animate(
            node,
            [
              {
                transform: `translate(${x}px, ${y}px) scale(${old.rect.width / rect.width}, ${old.rect.height / rect.height})`,
                transformOrigin: 'top left',
              },
              { transform: 'none', transformOrigin: 'top left' },
            ],
            GRID_DURATION,
          )
        }
      } else {
        this.animate(
          node,
          [
            { opacity: 0, transform: 'translateY(10px) scale(.985)' },
            { opacity: 1, transform: 'none' },
          ],
          220,
          Math.min(arriving++ * 24, 96),
        )
      }
    }
    for (const { copy } of previous.values()) {
      this.stage.append(copy)
      this.copies.add(copy)
      this.animate(
        copy,
        [
          { opacity: 1, transform: 'scale(1)' },
          { opacity: 0, transform: 'translateY(-6px) scale(.975)' },
        ],
        140,
      )
    }
    if (Math.abs(this.height - height) > 0.5) {
      this.animate(
        this.stage,
        [{ height: `${this.height}px` }, { height: `${height}px` }],
        GRID_DURATION,
      )
    }
    if (!this.animations.size) delete this.stage.dataset.gridMoving
  }

  stop() {
    this.animations.forEach((animation) => animation.cancel())
    this.animations.clear()
    this.copies.forEach((node) => node.remove())
    this.copies.clear()
    delete this.stage.dataset.gridMoving
  }

  dispose() {
    this.stop()
    this.before = []
    this.pending = false
  }
}
