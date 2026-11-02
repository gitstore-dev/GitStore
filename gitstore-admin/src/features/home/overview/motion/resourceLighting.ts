/** A single, decorative light field. No element capture or duplicate controls. */
export interface HaloCard {
  bounds: [number, number, number, number]
  color: [number, number, number]
  emphasis: number
}
export interface ResourceLighting {
  draw(width: number, height: number, cards: HaloCard[], progress: number): void
  clear(): void
  dispose(): void
}
declare const GPUBufferUsage: {
  UNIFORM: number
  STORAGE: number
  COPY_DST: number
}
const MAX_CARDS = 32
const shader = /* wgsl */ `
struct Card { bounds: vec4f, color: vec4f, emphasis: vec4f }
struct Vertex {
  @builtin(position) position: vec4f,
  @location(0) local: vec2f,
  @location(1) @interpolate(flat) index: u32,
}
@group(0) @binding(0) var<uniform> scene: vec4f;
@group(0) @binding(1) var<storage, read> cards: array<Card>;
@vertex fn vertex(@builtin(vertex_index) i: u32, @builtin(instance_index) index: u32) -> Vertex {
  let corners = array<vec2f, 6>(vec2f(0,0), vec2f(1,0), vec2f(0,1), vec2f(0,1), vec2f(1,0), vec2f(1,1));
  let card = cards[index];
  let local = (corners[i] - .5) * (card.bounds.zw + 52);
  let pixel = card.bounds.xy + card.bounds.zw * .5 + local;
  var out: Vertex;
  out.position = vec4f(pixel.x / scene.x * 2 - 1, 1 - pixel.y / scene.y * 2, 0, 1);
  out.local = local;
  out.index = index;
  return out;
}
@fragment fn fragment(v: Vertex) -> @location(0) vec4f {
  let card = cards[v.index];
  let q = abs(v.local) - (card.bounds.zw * .5 - 4);
  let distance = length(max(q, vec2f(0))) + min(max(q.x, q.y), 0) - 4;
  if (distance < 0) { discard; }
  let angle = atan2(v.local.y, v.local.x);
  let sweep = pow(.5 + .5 * cos(angle - scene.z * 5), 4);
  let strength = .12 + card.emphasis.x * .18;
  let bloom = exp(-distance * distance / 110) * strength;
  let edge = exp(-pow(distance - 1, 2) / 3) * (.08 + sweep * card.emphasis.x * .2);
  let alpha = min(.48, bloom + edge);
  return vec4f(card.color.rgb * alpha, alpha);
}`

export async function prepareResourceLighting(
  canvas: HTMLCanvasElement,
  signal: AbortSignal,
  onFailure: () => void,
): Promise<ResourceLighting | undefined> {
  if (!navigator.gpu || signal.aborted) return
  let device: GPUDevice | undefined
  let context: GPUCanvasContext | null = null
  let scene: GPUBuffer | undefined
  let cards: GPUBuffer | undefined
  let disposed = false
  const dispose = () => {
    if (disposed) return
    disposed = true
    signal.removeEventListener('abort', dispose)
    scene?.destroy()
    cards?.destroy()
    context?.unconfigure()
    device?.destroy()
  }
  signal.addEventListener('abort', dispose, { once: true })
  try {
    const adapter = await navigator.gpu.requestAdapter({
      powerPreference: 'low-power',
    })
    if (!adapter || signal.aborted) {
      dispose()
      return
    }
    device = await adapter.requestDevice()
    if (signal.aborted || disposed) {
      device.destroy()
      return
    }
    const gpu = device
    context = canvas.getContext('webgpu') as GPUCanvasContext | null
    if (!context) {
      dispose()
      return
    }
    const format = navigator.gpu.getPreferredCanvasFormat()
    const module = gpu.createShaderModule({ code: shader })
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
    scene = gpu.createBuffer({
      size: 16,
      usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.COPY_DST,
    })
    cards = gpu.createBuffer({
      size: MAX_CARDS * 48,
      usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_DST,
    })
    const bind = gpu.createBindGroup({
      layout: pipeline.getBindGroupLayout(0),
      entries: [
        { binding: 0, resource: { buffer: scene } },
        { binding: 1, resource: { buffer: cards } },
      ],
    })
    context.configure({ device: gpu, format, alphaMode: 'premultiplied' })
    const ctx = context
    const sceneBuffer = scene
    const cardBuffer = cards
    const fail = () => {
      if (!disposed) {
        dispose()
        onFailure()
      }
    }
    void gpu.lost.then(fail)
    gpu.addEventListener('uncapturederror', fail)
    const render = (
      width: number,
      height: number,
      items: HaloCard[],
      progress: number,
    ) => {
      if (disposed) return
      try {
        const limit = Math.min(2048, gpu.limits.maxTextureDimension2D)
        const scale = Math.min(
          devicePixelRatio,
          1.5,
          limit / Math.max(width, height),
        )
        const w = Math.max(1, Math.round(width * scale))
        const h = Math.max(1, Math.round(height * scale))
        if (canvas.width !== w) canvas.width = w
        if (canvas.height !== h) canvas.height = h
        gpu.queue.writeBuffer(
          sceneBuffer,
          0,
          new Float32Array([width, height, progress, 0]),
        )
        const count = Math.min(items.length, MAX_CARDS)
        if (count) {
          const data = new Float32Array(count * 12)
          items
            .slice(0, count)
            .forEach((card, index) =>
              data.set(
                [...card.bounds, ...card.color, 1, card.emphasis, 0, 0, 0],
                index * 12,
              ),
            )
          gpu.queue.writeBuffer(cardBuffer, 0, data)
        }
        const encoder = gpu.createCommandEncoder()
        const pass = encoder.beginRenderPass({
          colorAttachments: [
            {
              view: ctx.getCurrentTexture().createView(),
              clearValue: [0, 0, 0, 0],
              loadOp: 'clear',
              storeOp: 'store',
            },
          ],
        })
        if (count) {
          pass.setPipeline(pipeline)
          pass.setBindGroup(0, bind)
          pass.draw(6, count)
        }
        pass.end()
        gpu.queue.submit([encoder.finish()])
      } catch {
        fail()
      }
    }
    return {
      draw: render,
      clear: () =>
        render(canvas.clientWidth || 1, canvas.clientHeight || 1, [], 0),
      dispose,
    }
  } catch {
    dispose()
    return
  }
}
