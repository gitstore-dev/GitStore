import { afterEach, expect, it, vi } from 'vitest'
import { prepareResourceLighting } from './resourceLighting'
import { haloColor } from './haloColor'

function fixture() {
  const buffer = () => ({ destroy: vi.fn() })
  const buffers: ReturnType<typeof buffer>[] = []
  let lose!: () => void
  const pass = {
    setPipeline: vi.fn(),
    setBindGroup: vi.fn(),
    draw: vi.fn(),
    end: vi.fn(),
  }
  const device = {
    destroy: vi.fn(),
    lost: new Promise<void>((resolve) => {
      lose = resolve
    }),
    addEventListener: vi.fn(),
    limits: { maxTextureDimension2D: 8192 },
    createShaderModule: vi.fn(),
    createRenderPipelineAsync: vi.fn(async () => ({
      getBindGroupLayout: vi.fn(),
    })),
    createBuffer: vi.fn(() => {
      const item = buffer()
      buffers.push(item)
      return item
    }),
    createBindGroup: vi.fn(),
    createCommandEncoder: () => ({
      beginRenderPass: () => pass,
      finish: vi.fn(),
    }),
    queue: { writeBuffer: vi.fn(), submit: vi.fn() },
  }
  const requestDevice = vi.fn(async () => device)
  Object.defineProperty(navigator, 'gpu', {
    configurable: true,
    value: {
      requestAdapter: async () => ({ requestDevice }),
      getPreferredCanvasFormat: () => 'bgra8unorm',
    },
  })
  vi.stubGlobal('GPUBufferUsage', { UNIFORM: 64, STORAGE: 128, COPY_DST: 8 })
  const context = {
    configure: vi.fn(),
    unconfigure: vi.fn(),
    getCurrentTexture: () => ({ createView: vi.fn() }),
  }
  const canvas = document.createElement('canvas')
  vi.spyOn(canvas, 'getContext').mockReturnValue(
    context as unknown as GPUCanvasContext,
  )
  const controller = new AbortController()
  const failure = vi.fn()
  return {
    canvas,
    controller,
    device,
    buffers,
    context,
    failure,
    lose,
    requestDevice,
    pass,
  }
}
afterEach(() => {
  Reflect.deleteProperty(navigator, 'gpu')
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

it('uses WebGPU without any HTML-in-Canvas APIs and bounds its drawing surface', async () => {
  const f = fixture()
  const renderer = await prepareResourceLighting(
    f.canvas,
    f.controller.signal,
    f.failure,
  )
  expect(renderer).toBeDefined()
  renderer!.draw(
    1800,
    3600,
    [{ bounds: [20, 30, 200, 150], color: [1, 0, 0], emphasis: 1 }],
    0.3,
  )
  expect(f.canvas.width).toBeLessThanOrEqual(2048)
  expect(f.canvas.height).toBeLessThanOrEqual(2048)
  expect(f.pass.draw).toHaveBeenCalledWith(6, 1)
  renderer!.clear()
  renderer!.dispose()
  renderer!.dispose()
  expect(f.device.destroy).toHaveBeenCalledTimes(1)
  expect(f.context.unconfigure).toHaveBeenCalledTimes(1)
  expect(
    f.buffers.every((buffer) => buffer.destroy.mock.calls.length === 1),
  ).toBe(true)
})

it('falls back after device loss without leaking GPU resources', async () => {
  const f = fixture()
  await prepareResourceLighting(f.canvas, f.controller.signal, f.failure)
  f.lose()
  await vi.waitFor(() => expect(f.failure).toHaveBeenCalledTimes(1))
  expect(f.device.destroy).toHaveBeenCalledTimes(1)
  expect(
    f.buffers.every((buffer) => buffer.destroy.mock.calls.length === 1),
  ).toBe(true)
})

it('destroys a device delivered after navigation cancels initialization', async () => {
  const f = fixture()
  let deliver!: (value: typeof f.device) => void
  f.requestDevice.mockReturnValueOnce(
    new Promise((resolve) => {
      deliver = resolve
    }),
  )
  const pending = prepareResourceLighting(
    f.canvas,
    f.controller.signal,
    f.failure,
  )
  await vi.waitFor(() => expect(f.requestDevice).toHaveBeenCalled())
  f.controller.abort()
  deliver(f.device)
  await expect(pending).resolves.toBeUndefined()
  expect(f.device.destroy).toHaveBeenCalledTimes(1)
  expect(f.buffers).toHaveLength(0)
})

it('returns to CSS when pipeline initialization fails or WebGPU is missing', async () => {
  const f = fixture()
  f.device.createRenderPipelineAsync.mockRejectedValueOnce(
    new Error('Unavailable'),
  )
  await expect(
    prepareResourceLighting(f.canvas, f.controller.signal, f.failure),
  ).resolves.toBeUndefined()
  expect(f.device.destroy).toHaveBeenCalledTimes(1)
  Reflect.deleteProperty(navigator, 'gpu')
  await expect(
    prepareResourceLighting(f.canvas, new AbortController().signal, f.failure),
  ).resolves.toBeUndefined()
})

it('uses the page semantic colors and tolerates missing neutral colors', () => {
  expect(haloColor('#ff8000')).toEqual([1, 128 / 255, 0])
  expect(haloColor('rgb(20, 40, 60)')).toEqual([20 / 255, 40 / 255, 60 / 255])
  expect(haloColor('')).toEqual([0.36, 0.46, 0.4])
})
