import { afterEach, describe, expect, it, vi } from 'vitest'
import { transitionFocus } from './focusTransition'
import { prepareFocusGPU } from './focusGPU'

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  Reflect.deleteProperty(HTMLCanvasElement.prototype, 'requestPaint')
  Reflect.deleteProperty(
    HTMLCanvasElement.prototype,
    'getElementImageDefaultSize',
  )
  vi.unstubAllGlobals()
  Reflect.deleteProperty(document, 'startViewTransition')
  Reflect.deleteProperty(navigator, 'gpu')
  delete document.documentElement.dataset.motion
  delete document.documentElement.dataset.focusTransition
  delete document.documentElement.dataset.focusTransitionOwner
})

function fixture() {
  const root = document.createElement('div')
  const controller = new AbortController()
  const update = vi.fn()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  return {
    root,
    signal: controller.signal,
    update,
    direction: 'enter' as const,
    controller,
  }
}

describe('focus transition recovery', () => {
  it('commits immediately with reduced motion and releases prepared GPU resources', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const start = vi.fn()
    vi.stubGlobal(
      'document',
      Object.assign(document, { startViewTransition: start }),
    )
    const gpu = { dispose: vi.fn(), capture: vi.fn() }
    const input = fixture()
    await transitionFocus({ ...input, gpu })
    expect(input.update).toHaveBeenCalledTimes(1)
    expect(start).not.toHaveBeenCalled()
    expect(gpu.capture).not.toHaveBeenCalled()
    expect(gpu.dispose).toHaveBeenCalledTimes(1)
  })

  it('still commits exactly once when the browser refuses snapshots', async () => {
    Object.defineProperty(document, 'startViewTransition', {
      configurable: true,
      value: () => {
        throw new Error('Snapshots unavailable')
      },
    })
    const input = fixture()
    await transitionFocus(input)
    expect(input.update).toHaveBeenCalledTimes(1)
    expect(document.documentElement).not.toHaveAttribute(
      'data-focus-transition',
    )
  })

  it('does not apply an abandoned transition callback after a newer toggle', async () => {
    let updateOld: (() => unknown) | undefined
    const skip = vi.fn()
    let finish!: () => void
    Object.defineProperty(document, 'startViewTransition', {
      configurable: true,
      value: (update: () => unknown) => {
        updateOld = update
        return {
          ready: Promise.resolve(),
          updateCallbackDone: Promise.resolve(),
          finished: new Promise<void>((resolve) => {
            finish = resolve
          }),
          skipTransition: skip,
        }
      },
    })
    const input = fixture()
    const running = transitionFocus(input)
    input.controller.abort()
    updateOld!()
    finish()
    await running
    expect(skip).toHaveBeenCalledTimes(1)
    expect(input.update).not.toHaveBeenCalled()
    expect(document.documentElement).not.toHaveAttribute(
      'data-focus-transition',
    )
  })

  it('falls through when GPU snapshot capture throws, and destroys the device', async () => {
    Object.defineProperty(document, 'startViewTransition', {
      configurable: true,
      value: () => {
        throw new Error('Unavailable')
      },
    })
    const input = fixture()
    const gpu = {
      dispose: vi.fn(),
      capture: vi.fn().mockRejectedValue(new Error('Device lost')),
    }
    await transitionFocus({ ...input, gpu })
    expect(input.update).toHaveBeenCalledTimes(1)
    expect(gpu.dispose).toHaveBeenCalledTimes(1)
  })

  it('does not request GPU hardware without native HTML-in-Canvas support', async () => {
    const requestAdapter = vi.fn()
    Object.defineProperty(navigator, 'gpu', {
      configurable: true,
      value: { requestAdapter },
    })
    await expect(
      prepareFocusGPU(new AbortController().signal),
    ).resolves.toBeUndefined()
    expect(requestAdapter).not.toHaveBeenCalled()
  })
})

it('destroys a device that arrives after GPU preparation was cancelled', async () => {
  class Queue {
    drawElementImageToTexture() {}
  }
  vi.stubGlobal('GPUQueue', Queue)
  Object.defineProperty(HTMLCanvasElement.prototype, 'requestPaint', {
    configurable: true,
    value() {},
  })
  Object.defineProperty(
    HTMLCanvasElement.prototype,
    'getElementImageDefaultSize',
    {
      configurable: true,
      value() {
        return { width: 100, height: 100 }
      },
    },
  )
  let resolveDevice!: (value: { destroy: () => void }) => void
  const requestDevice = vi.fn(
    () =>
      new Promise<{ destroy: () => void }>((resolve) => {
        resolveDevice = resolve
      }),
  )
  Object.defineProperty(navigator, 'gpu', {
    configurable: true,
    value: { requestAdapter: async () => ({ requestDevice }) },
  })
  const controller = new AbortController()
  const pending = prepareFocusGPU(controller.signal)
  await vi.waitFor(() => expect(requestDevice).toHaveBeenCalledTimes(1))
  controller.abort()
  const destroy = vi.fn()
  resolveDevice({ destroy })
  await expect(pending).resolves.toBeUndefined()
  expect(destroy).toHaveBeenCalledTimes(1)
})

it('bounds a missing canvas paint event and removes every allocated snapshot resource', async () => {
  vi.useFakeTimers()
  class Queue {
    drawElementImageToTexture() {}
  }
  vi.stubGlobal('GPUQueue', Queue)
  vi.stubGlobal('GPUTextureUsage', {
    TEXTURE_BINDING: 4,
    COPY_DST: 2,
    RENDER_ATTACHMENT: 16,
  })
  vi.stubGlobal('GPUBufferUsage', { UNIFORM: 64, COPY_DST: 8 })
  Object.defineProperty(HTMLCanvasElement.prototype, 'requestPaint', {
    configurable: true,
    value() {},
  })
  Object.defineProperty(
    HTMLCanvasElement.prototype,
    'getElementImageDefaultSize',
    {
      configurable: true,
      value() {
        return { width: 100, height: 100 }
      },
    },
  )
  const context = { configure: vi.fn(), unconfigure: vi.fn() }
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(
    context as unknown as GPUCanvasContext,
  )
  const texture = { createView: vi.fn(), destroy: vi.fn() }
  const buffer = { destroy: vi.fn() }
  const device = {
    destroy: vi.fn(),
    lost: new Promise(() => undefined),
    limits: { maxTextureDimension2D: 4096 },
    createShaderModule: vi.fn(),
    createRenderPipelineAsync: async () => ({ getBindGroupLayout: vi.fn() }),
    createSampler: vi.fn(),
    createTexture: () => texture,
    createBuffer: () => buffer,
    createBindGroup: vi.fn(),
  }
  Object.defineProperty(navigator, 'gpu', {
    configurable: true,
    value: {
      requestAdapter: async () => ({ requestDevice: async () => device }),
      getPreferredCanvasFormat: () => 'bgra8unorm',
    },
  })
  const controller = new AbortController()
  const gpu = await prepareFocusGPU(controller.signal)
  const source = document.createElement('div')
  vi.spyOn(source, 'getBoundingClientRect').mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    width: 100,
    height: 100,
    right: 100,
    bottom: 100,
    toJSON() {},
  })
  const pending = gpu!.capture([source], controller.signal)
  await vi.advanceTimersByTimeAsync(61)
  await expect(pending).resolves.toBeUndefined()
  expect(document.querySelectorAll('[data-focus-gpu]')).toHaveLength(0)
  expect(texture.destroy).toHaveBeenCalledTimes(1)
  expect(buffer.destroy).toHaveBeenCalledTimes(1)
  expect(context.unconfigure).toHaveBeenCalledTimes(1)
  gpu!.dispose()
  expect(device.destroy).toHaveBeenCalledTimes(1)
})
