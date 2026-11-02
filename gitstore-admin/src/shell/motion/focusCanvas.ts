/** Adapts the evolving native HTML-in-Canvas API; no emulation or browser sniffing.
 * Chrome 150 uses layoutsubtree and copyElementImageToTexture; Chrome 155+
 * introduces content=drawable and drawElementImageToTexture.
 * https://developer.chrome.com/blog/html-in-canvas-ot-changes
 */
export interface DrawableCanvas extends HTMLCanvasElement {
  requestPaint(): void
}
type Destination = { texture: GPUTexture; premultipliedAlpha: boolean }
interface ElementQueue extends GPUQueue {
  drawElementImageToTexture?: (
    source: { source: Element },
    destination: Destination & { size: { width: number; height: number } },
  ) => void
  copyElementImageToTexture?: {
    (
      source: { source: Element },
      destination: { destination: Destination; width: number; height: number },
    ): void
    (
      source: Element,
      width: number,
      height: number,
      destination: Destination,
    ): void
  }
}
export interface FocusCanvasAPI {
  configure(canvas: HTMLCanvasElement): void
  upload(
    queue: GPUQueue,
    source: Element,
    texture: GPUTexture,
    width: number,
    height: number,
  ): void
}

export function focusCanvasAPI(): FocusCanvasAPI | undefined {
  if (
    !navigator.gpu ||
    typeof GPUQueue === 'undefined' ||
    !('requestPaint' in HTMLCanvasElement.prototype)
  )
    return
  const queue = GPUQueue.prototype as ElementQueue
  if (typeof queue.drawElementImageToTexture === 'function') {
    return {
      configure: (canvas) => canvas.setAttribute('content', 'drawable'),
      upload: (queue, source, texture, width, height) =>
        (queue as ElementQueue).drawElementImageToTexture!(
          { source },
          { texture, size: { width, height }, premultipliedAlpha: false },
        ),
    }
  }
  if (
    typeof queue.copyElementImageToTexture === 'function' &&
    'layoutSubtree' in HTMLCanvasElement.prototype
  ) {
    const positional = queue.copyElementImageToTexture.length >= 4
    return {
      configure: (canvas) => canvas.setAttribute('layoutsubtree', ''),
      upload(queue, source, texture, width, height) {
        const nativeQueue = queue as ElementQueue
        if (positional)
          nativeQueue.copyElementImageToTexture!(source, width, height, {
            texture,
            premultipliedAlpha: false,
          })
        else
          nativeQueue.copyElementImageToTexture!(
            { source },
            {
              destination: { texture, premultipliedAlpha: false },
              width,
              height,
            },
          )
      },
    }
  }
}
