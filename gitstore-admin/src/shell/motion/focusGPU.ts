import { FOCUS_DURATION } from './focusTransition'
import { focusCanvasAPI } from './focusCanvas'
import type { DrawableCanvas } from './focusCanvas'

declare const GPUTextureUsage: {
  TEXTURE_BINDING: number
  COPY_DST: number
  RENDER_ATTACHMENT: number
}
declare const GPUBufferUsage: { UNIFORM: number; COPY_DST: number }

export interface FocusGPU {
  capture(
    regions: HTMLElement[],
    signal: AbortSignal,
  ): Promise<FocusStage | undefined>
  dispose(): void
}
interface FocusStage {
  play(signal: AbortSignal): Promise<void>
  dispose(): void
}

const shader = /* wgsl */ `
struct Vertex { @builtin(position) position: vec4f, @location(0) uv: vec2f }
@vertex fn vertex(@builtin(vertex_index) i: u32) -> Vertex {
  let p = array<vec2f, 3>(vec2f(-1, -1), vec2f(3, -1), vec2f(-1, 3))[i];
  var v: Vertex;
  v.position = vec4f(p, 0, 1);
  v.uv = vec2f((p.x + 1) * .5, (1 - p.y) * .5);
  return v;
}
@group(0) @binding(0) var image: texture_2d<f32>;
@group(0) @binding(1) var imageSampler: sampler;
@group(0) @binding(2) var<uniform> params: vec4f;
fn noise(p: vec2f) -> f32 { return fract(sin(dot(p, vec2f(127.1, 311.7))) * 43758.5453); }
@fragment fn fragment(v: Vertex) -> @location(0) vec4f {
  let uv = v.uv;
  let progress = params.x;
  var coordinate = select(uv.x, uv.y, params.y > .5);
  coordinate = select(coordinate, 1 - coordinate, params.z > .5);
  let grain = noise(floor(uv * vec2f(80, 180)));
  let edge = 1.08 - progress * 1.18 + (grain - .5) * .065;
  let mask = 1 - smoothstep(edge - .018, edge + .018, coordinate);
  let fringe = (1 - smoothstep(.0, .045, abs(coordinate - edge))) * progress;
  let drift = vec2f((grain - .5) * fringe * .012, 0);
  let pixel = textureSample(image, imageSampler, clamp(uv + drift, vec2f(0), vec2f(1)));
  let color = mix(pixel.rgb, vec3f(.78, .85, .62), fringe * .16);
  let alpha = pixel.a * mask;
  return vec4f(color * alpha, alpha);
}`

export function supportsFocusGPU() {
  return !!focusCanvasAPI()
}

/** Decorative copies only: no reparenting of live controls or working content. */
function snapshot(source: HTMLElement, width: number, height: number) {
  const clone = source.cloneNode(true) as HTMLElement
  const originals = [
    source,
    ...source.querySelectorAll<HTMLElement | SVGElement>('*'),
  ]
  const copies = [
    clone,
    ...clone.querySelectorAll<HTMLElement | SVGElement>('*'),
  ]
  copies.forEach((copy) => {
    copy.removeAttribute('id')
    copy.removeAttribute('autofocus')
    copy.style.viewTransitionName = 'none'
    copy.style.animation = 'none'
    copy.style.transition = 'none'
  })
  Object.assign(clone.style, {
    position: 'relative',
    inset: 'auto',
    margin: '0',
    width: `${width}px`,
    height: `${height}px`,
  })
  // Preserve the CSS ancestry instead of copying hundreds of computed properties
  // per node. The paint root remains a direct canvas child for the earlier API.
  let paintRoot = clone
  for (
    let ancestor = source.parentElement;
    ancestor && ancestor !== document.body;
    ancestor = ancestor.parentElement
  ) {
    const wrapper = ancestor.cloneNode(false) as HTMLElement
    wrapper.removeAttribute('id')
    Object.assign(wrapper.style, {
      display: 'block',
      position: 'relative',
      inset: 'auto',
      width: `${width}px`,
      height: `${height}px`,
      minHeight: '0',
      minWidth: '0',
      padding: '0',
      margin: '0',
      border: '0',
      background: 'transparent',
      transform: 'none',
      viewTransitionName: 'none',
    })
    wrapper.append(paintRoot)
    paintRoot = wrapper
  }
  paintRoot.setAttribute('drawable', '')
  paintRoot.inert = true
  return {
    paintRoot,
    restoreScroll() {
      originals.forEach((original, index) => {
        copies[index]!.scrollTop = original.scrollTop
        copies[index]!.scrollLeft = original.scrollLeft
      })
    },
  }
}

export async function prepareFocusGPU(
  signal: AbortSignal,
): Promise<FocusGPU | undefined> {
  const canvasAPI = focusCanvasAPI()
  if (!canvasAPI || signal.aborted) return
  let device: GPUDevice | undefined
  const stages = new Set<() => void>()
  let disposed = false
  const dispose = () => {
    if (disposed) return
    disposed = true
    stages.forEach((stop) => stop())
    device?.destroy()
    signal.removeEventListener('abort', dispose)
  }
  signal.addEventListener('abort', dispose, { once: true })
  try {
    const adapter = await navigator.gpu.requestAdapter({
      powerPreference: 'low-power',
    })
    if (!adapter || signal.aborted || disposed) {
      dispose()
      return
    }
    device = await adapter.requestDevice()
    if (signal.aborted || disposed) {
      device.destroy()
      return
    }
    const gpu = device
    const module = gpu.createShaderModule({ code: shader })
    const format = navigator.gpu.getPreferredCanvasFormat()
    const pipeline = await gpu.createRenderPipelineAsync({
      layout: 'auto',
      vertex: { module, entryPoint: 'vertex' },
      fragment: {
        module,
        entryPoint: 'fragment',
        targets: [
          {
            format,
            blend: {
              color: { srcFactor: 'one', dstFactor: 'one-minus-src-alpha' },
              alpha: { srcFactor: 'one', dstFactor: 'one-minus-src-alpha' },
            },
          },
        ],
      },
      primitive: { topology: 'triangle-list' },
    })
    if (signal.aborted || disposed) {
      dispose()
      return
    }
    void gpu.lost.then(dispose)
    const sampler = gpu.createSampler({
      magFilter: 'linear',
      minFilter: 'linear',
    })
    return {
      dispose,
      async capture(regions, captureSignal) {
        if (disposed || captureSignal.aborted) return
        const deadline = performance.now() + 60
        const resources: {
          canvas: DrawableCanvas
          context: GPUCanvasContext
          texture: GPUTexture
          uniform: GPUBuffer
          bind: GPUBindGroup
          axis: number
          reverse: number
        }[] = []
        let frame = 0
        let timer = 0
        let stopped = false
        let finish: (() => void) | undefined
        const disposers: (() => void)[] = []
        const cleanup = () => {
          if (stopped) return
          stopped = true
          cancelAnimationFrame(frame)
          clearTimeout(timer)
          disposers.reverse().forEach((disposeResource) => disposeResource())
          captureSignal.removeEventListener('abort', cleanup)
          stages.delete(cleanup)
          finish?.()
        }
        stages.add(cleanup)
        captureSignal.addEventListener('abort', cleanup, { once: true })
        try {
          const paints: Promise<void>[] = []
          for (const source of regions) {
            const rect = source.getBoundingClientRect()
            if (!rect.width || !rect.height) continue
            const canvas = document.createElement('canvas') as DrawableCanvas
            canvasAPI.configure(canvas)
            canvas.setAttribute('aria-hidden', 'true')
            canvas.dataset.focusGpu = ''
            canvas.inert = true
            Object.assign(canvas.style, {
              position: 'fixed',
              pointerEvents: 'none',
              zIndex: '1000',
              left: `${rect.left}px`,
              top: `${rect.top}px`,
              width: `${rect.width}px`,
              height: `${rect.height}px`,
            })
            const { paintRoot: clone, restoreScroll } = snapshot(
              source,
              rect.width,
              rect.height,
            )
            canvas.append(clone)
            document.body.append(canvas)
            restoreScroll()
            disposers.push(() => canvas.remove())
            const context = canvas.getContext(
              'webgpu',
            ) as GPUCanvasContext | null
            if (!context) {
              canvas.remove()
              throw new Error('WebGPU context unavailable')
            }
            const limit = Math.min(4096, gpu.limits.maxTextureDimension2D)
            const scale = Math.min(
              devicePixelRatio,
              2,
              limit / Math.max(rect.width, rect.height),
            )
            const width = (canvas.width = Math.max(
              1,
              Math.ceil(rect.width * scale),
            ))
            const height = (canvas.height = Math.max(
              1,
              Math.ceil(rect.height * scale),
            ))
            context.configure({
              device: gpu,
              format,
              alphaMode: 'premultiplied',
            })
            disposers.push(() => context.unconfigure())
            const texture = gpu.createTexture({
              size: [width, height],
              format: 'rgba8unorm',
              usage:
                GPUTextureUsage.TEXTURE_BINDING |
                GPUTextureUsage.COPY_DST |
                GPUTextureUsage.RENDER_ATTACHMENT,
            })
            disposers.push(() => texture.destroy())
            const uniform = gpu.createBuffer({
              size: 16,
              usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.COPY_DST,
            })
            disposers.push(() => uniform.destroy())
            const bind = gpu.createBindGroup({
              layout: pipeline.getBindGroupLayout(0),
              entries: [
                { binding: 0, resource: texture.createView() },
                { binding: 1, resource: sampler },
                { binding: 2, resource: { buffer: uniform } },
              ],
            })
            const region = source.dataset.focusChrome
            resources.push({
              canvas,
              context,
              texture,
              uniform,
              bind,
              axis: region === 'header' || region === 'mobile' ? 1 : 0,
              reverse: region === 'inspector' || region === 'mobile' ? 1 : 0,
            })
            paints.push(
              new Promise<void>((resolve, reject) => {
                canvas.addEventListener(
                  'paint',
                  () => {
                    if (stopped || disposed) return
                    try {
                      canvasAPI.upload(gpu.queue, clone, texture, width, height)
                      resolve()
                    } catch (error) {
                      reject(error)
                    }
                  },
                  { once: true },
                )
                canvas.requestPaint()
              }),
            )
          }
          if (!resources.length) {
            cleanup()
            return
          }
          const painted = await Promise.race([
            Promise.all(paints).then(() => true),
            new Promise<false>((resolve) => {
              timer = window.setTimeout(
                () => resolve(false),
                Math.max(0, deadline - performance.now()),
              )
            }),
          ])
          clearTimeout(timer)
          if (!painted || stopped || disposed) {
            cleanup()
            return
          }
          return {
            dispose: cleanup,
            play(playSignal) {
              return new Promise<void>((resolve) => {
                finish = resolve
                if (stopped || playSignal.aborted) {
                  cleanup()
                  resolve()
                  return
                }
                const start = performance.now()
                const render = (now: number) => {
                  if (stopped || disposed || playSignal.aborted) {
                    cleanup()
                    return
                  }
                  const progress = Math.min(1, (now - start) / FOCUS_DURATION)
                  try {
                    const encoder = gpu.createCommandEncoder()
                    for (const item of resources) {
                      gpu.queue.writeBuffer(
                        item.uniform,
                        0,
                        new Float32Array([
                          progress,
                          item.axis,
                          item.reverse,
                          0,
                        ]),
                      )
                      const pass = encoder.beginRenderPass({
                        colorAttachments: [
                          {
                            view: item.context.getCurrentTexture().createView(),
                            clearValue: [0, 0, 0, 0],
                            loadOp: 'clear',
                            storeOp: 'store',
                          },
                        ],
                      })
                      pass.setPipeline(pipeline)
                      pass.setBindGroup(0, item.bind)
                      pass.draw(3)
                      pass.end()
                    }
                    gpu.queue.submit([encoder.finish()])
                    if (progress < 1) frame = requestAnimationFrame(render)
                    else cleanup()
                  } catch {
                    cleanup()
                  }
                }
                render(start)
              })
            },
          }
        } catch {
          cleanup()
          return
        }
      },
    }
  } catch {
    dispose()
    return
  }
}
