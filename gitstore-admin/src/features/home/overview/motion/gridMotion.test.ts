import { afterEach, expect, it, vi } from 'vitest'
import { GridMotion } from './gridMotion'

function rect(x = 0, y = 0, width = 400, height = 150) {
  return {
    x,
    y,
    left: x,
    top: y,
    width,
    height,
    right: x + width,
    bottom: y + height,
    toJSON() {},
  }
}
function fixture() {
  const stage = document.createElement('div')
  document.body.append(stage)
  let height = 300
  vi.spyOn(stage, 'getBoundingClientRect').mockImplementation(() =>
    rect(0, 0, 400, height),
  )
  const add = (id: string, y: number) => {
    const card = document.createElement('article')
    card.dataset.resourceId = id
    card.dataset.haloCard = ''
    card.innerHTML = '<button id="inspect">Inspect resource</button>'
    let top = y
    vi.spyOn(card, 'getBoundingClientRect').mockImplementation(() =>
      rect(0, top),
    )
    stage.append(card)
    return {
      node: card,
      move: (value: number) => {
        top = value
      },
    }
  }
  const animations: {
    cancel: ReturnType<typeof vi.fn>
    finish: () => void
    frames: Keyframe[]
    node: Element
  }[] = []
  Object.defineProperty(Element.prototype, 'animate', {
    configurable: true,
    value: function (this: Element, frames: Keyframe[]) {
      let finish!: () => void
      const finished = new Promise<void>((resolve) => {
        finish = resolve
      })
      const cancel = vi.fn(finish)
      animations.push({ cancel, finish, frames, node: this })
      return { finished, cancel }
    },
  })
  return {
    stage,
    add,
    animations,
    motion: new GridMotion(stage),
    height: (value: number) => {
      height = value
    },
  }
}
afterEach(() => {
  document.body.replaceChildren()
  Reflect.deleteProperty(Element.prototype, 'animate')
  vi.restoreAllMocks()
})

it('moves surviving cards and makes exits inert, unnamed decorative copies', async () => {
  const { motion, stage, add, animations, height } = fixture()
  const first = add('failed', 0)
  const second = add('blocked', 150)
  motion.capture()
  first.node.remove()
  second.move(0)
  height(150)
  motion.play(false)
  const exit = stage.querySelector<HTMLElement>('[data-grid-exit]')!
  expect(exit.inert).toBe(true)
  expect(exit).toHaveAttribute('aria-hidden', 'true')
  expect(exit.querySelector('[id]')).toBeNull()
  expect(exit).not.toHaveAttribute('data-resource-id')
  expect(exit).not.toHaveAttribute('data-halo-card')
  expect(
    animations.find((animation) => animation.node === second.node)?.frames[0]
      ?.transform,
  ).toContain('150px')
  expect(animations.some((animation) => animation.node === stage)).toBe(true)
  animations.forEach((animation) => animation.finish())
  await vi.waitFor(() =>
    expect(stage.querySelector('[data-grid-exit]')).toBeNull(),
  )
  expect(stage).not.toHaveAttribute('data-grid-moving')
})

it('reduced motion updates immediately and creates no ghost cards', () => {
  const { motion, stage, add, animations } = fixture()
  const first = add('a', 0)
  motion.capture()
  first.node.remove()
  motion.play(true)
  expect(animations).toHaveLength(0)
  expect(stage.querySelector('[data-grid-exit]')).toBeNull()
})

it('interrupts from current visual bounds, cancels old work, and cleans up on disposal', () => {
  const { motion, stage, add, animations } = fixture()
  const a = add('a', 0)
  const b = add('b', 150)
  motion.capture()
  a.move(150)
  b.move(0)
  motion.play(false)
  a.move(70) // Current interpolated visual position in a rapid second change.
  motion.capture()
  expect(
    animations.every((animation) => animation.cancel.mock.calls.length > 0),
  ).toBe(true)
  a.move(0)
  b.node.remove()
  motion.play(false)
  const latest = animations
    .filter((animation) => animation.node === a.node)
    .at(-1)!
  expect(latest.frames[0]?.transform).toContain('70px')
  motion.dispose()
  expect(stage.querySelector('[data-grid-exit]')).toBeNull()
  expect(stage).not.toHaveAttribute('data-grid-moving')
})

it('animates arrivals when a filter changes from empty to populated', () => {
  const { motion, add, animations } = fixture()
  motion.capture()
  add('new', 0)
  motion.play(false)
  expect(animations[0]?.frames[0]?.opacity).toBe(0)
  motion.dispose()
})
