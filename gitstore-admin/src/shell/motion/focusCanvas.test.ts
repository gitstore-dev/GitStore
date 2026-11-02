import { afterEach, expect, it, vi } from 'vitest'
import { focusCanvasAPI } from './focusCanvas'

afterEach(() => {
  vi.unstubAllGlobals()
  Reflect.deleteProperty(navigator, 'gpu')
  Reflect.deleteProperty(HTMLCanvasElement.prototype, 'requestPaint')
  Reflect.deleteProperty(HTMLCanvasElement.prototype, 'layoutSubtree')
})

function enable(Queue: unknown) {
  vi.stubGlobal('GPUQueue', Queue)
  Object.defineProperty(navigator, 'gpu', { configurable: true, value: {} })
  Object.defineProperty(HTMLCanvasElement.prototype, 'requestPaint', {
    configurable: true,
    value() {},
  })
  Object.defineProperty(HTMLCanvasElement.prototype, 'layoutSubtree', {
    configurable: true,
    value: false,
  })
}

it('uses the current upload API without requiring an unused sizing helper', () => {
  const upload = vi.fn()
  class Queue {
    drawElementImageToTexture = upload
  }
  Object.defineProperty(Queue.prototype, 'drawElementImageToTexture', {
    value: upload,
  })
  enable(Queue)
  const api = focusCanvasAPI()!
  const canvas = document.createElement('canvas')
  const source = document.createElement('div')
  const texture = {} as GPUTexture
  api.configure(canvas)
  api.upload(new Queue() as unknown as GPUQueue, source, texture, 200, 100)
  expect(canvas).toHaveAttribute('content', 'drawable')
  expect(upload).toHaveBeenCalledWith(
    { source },
    { texture, size: { width: 200, height: 100 }, premultipliedAlpha: false },
  )
})

it('adapts the Chrome 150 dictionary signature and layoutsubtree opt-in', () => {
  const upload = vi.fn()
  class Queue {
    copyElementImageToTexture(source: unknown, destination: unknown) {
      upload(source, destination)
    }
  }
  enable(Queue)
  const api = focusCanvasAPI()!
  const canvas = document.createElement('canvas')
  const source = document.createElement('div')
  const texture = {} as GPUTexture
  api.configure(canvas)
  api.upload(new Queue() as unknown as GPUQueue, source, texture, 200, 100)
  expect(canvas).toHaveAttribute('layoutsubtree')
  expect(upload).toHaveBeenCalledWith(
    { source },
    {
      destination: { texture, premultipliedAlpha: false },
      width: 200,
      height: 100,
    },
  )
})

it('adapts the earlier four-argument signature', () => {
  const upload = vi.fn()
  class Queue {
    copyElementImageToTexture(
      source: unknown,
      width: number,
      height: number,
      destination: unknown,
    ) {
      upload(source, width, height, destination)
    }
  }
  enable(Queue)
  const source = document.createElement('div')
  const texture = {} as GPUTexture
  focusCanvasAPI()!.upload(
    new Queue() as unknown as GPUQueue,
    source,
    texture,
    200,
    100,
  )
  expect(upload).toHaveBeenCalledWith(source, 200, 100, {
    texture,
    premultipliedAlpha: false,
  })
})
