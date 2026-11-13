import { useRef, useState } from 'react'
import { ActionButton } from '../../../components/controls'
import type { DemoSession } from '../../demo/model'
import type { CatalogEntry } from './model'
import { categoryHierarchy } from './model'
import styles from './catalog.module.css'
export function CategorySpatial({
  session,
  entries,
  selected,
  onSelect,
}: {
  session: DemoSession
  entries: CatalogEntry[]
  selected?: string
  onSelect: (id: string, trigger: HTMLElement) => void
}) {
  const viewport = useRef<HTMLDivElement>(null)
  const drag = useRef<{
    x: number
    y: number
    panX: number
    panY: number
  } | null>(null)
  const [zoom, setZoom] = useState(1)
  const [pan, setPan] = useState({ x: 24, y: 24 })
  const rows = categoryHierarchy(session, entries)
  const positions = new Map(
    rows.map((row, index) => [
      row.item.id,
      { x: row.depth * 285, y: index * 125 },
    ]),
  )
  const width = Math.max(300, ...rows.map((r) => r.depth * 285 + 255))
  const height = Math.max(180, rows.length * 125)
  function scale(value: number) {
    setZoom(Math.min(1.8, Math.max(0.35, value)))
  }
  function fit() {
    const rect = viewport.current?.getBoundingClientRect()
    if (!rect) return
    setZoom(Math.min(1, (rect.width - 48) / width, (rect.height - 48) / height))
    setPan({ x: 24, y: 24 })
  }
  return (
    <section aria-label="Category map" className={styles.map}>
      <div className={styles.mapToolbar}>
        <span>
          Category map <small>{Math.round(zoom * 100)}%</small>
        </span>
        <div>
          <ActionButton
            aria-label="Zoom out"
            onPress={() => scale(zoom - 0.15)}
          >
            −
          </ActionButton>
          <ActionButton aria-label="Zoom in" onPress={() => scale(zoom + 0.15)}>
            +
          </ActionButton>
          <ActionButton onPress={fit}>Fit map</ActionButton>
        </div>
      </div>
      <p id="map-help" className={styles.note}>
        Drag the background to pan. Focus the map and use arrow keys to pan, +
        or − to zoom, and 0 to fit. Tab to a category to bring it into view.
      </p>
      <div
        ref={viewport}
        className={styles.mapViewport}
        tabIndex={0}
        role="region"
        aria-label="Pan and zoom categories"
        aria-describedby="map-help"
        onKeyDown={(event) => {
          if (event.target !== event.currentTarget) return
          const delta: Record<string, [number, number]> = {
            ArrowLeft: [40, 0],
            ArrowRight: [-40, 0],
            ArrowUp: [0, 40],
            ArrowDown: [0, -40],
          }
          if (delta[event.key]) {
            event.preventDefault()
            const [x, y] = delta[event.key]!
            setPan((p) => ({ x: p.x + x, y: p.y + y }))
          } else if (event.key === '+' || event.key === '=') {
            event.preventDefault()
            scale(zoom + 0.15)
          } else if (event.key === '-') {
            event.preventDefault()
            scale(zoom - 0.15)
          } else if (event.key === '0') {
            event.preventDefault()
            fit()
          }
        }}
        onPointerDown={(event) => {
          if ((event.target as HTMLElement).closest('button')) return
          drag.current = {
            x: event.clientX,
            y: event.clientY,
            panX: pan.x,
            panY: pan.y,
          }
          event.currentTarget.setPointerCapture(event.pointerId)
        }}
        onPointerMove={(event) => {
          if (drag.current)
            setPan({
              x: drag.current.panX + event.clientX - drag.current.x,
              y: drag.current.panY + event.clientY - drag.current.y,
            })
        }}
        onPointerUp={() => {
          drag.current = null
        }}
        onPointerCancel={() => {
          drag.current = null
        }}
      >
        <div
          className={styles.mapScene}
          style={{
            width,
            height,
            transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`,
          }}
        >
          <svg
            width={width}
            height={height}
            aria-hidden="true"
            className={styles.edges}
          >
            {rows.map(({ item }) => {
              const value = session.catalog[item.id]
              const parent =
                value?.kind === 'CategoryTaxonomy' && value.spec.parentId
                  ? positions.get(value.spec.parentId)
                  : undefined
              const point = positions.get(item.id)!
              return parent ? (
                <path
                  key={item.id}
                  d={`M${parent.x + 245},${parent.y + 40} H${point.x - 20} V${point.y + 40} H${point.x}`}
                />
              ) : null
            })}
          </svg>
          {rows.map(({ item, problem }) => {
            const point = positions.get(item.id)!
            return (
              <ActionButton
                key={item.id}
                className={styles.mapNode}
                style={{ left: point.x, top: point.y }}
                aria-pressed={selected === item.id}
                onFocus={(event) => {
                  if (!event.currentTarget.matches(':focus-visible')) return
                  const rect = viewport.current?.getBoundingClientRect()
                  if (rect)
                    setPan({
                      x: rect.width / 2 - (point.x + 122) * zoom,
                      y: rect.height / 2 - (point.y + 40) * zoom,
                    })
                }}
                onPress={(event) =>
                  onSelect(item.id, event.target as HTMLElement)
                }
              >
                <strong>{item.title}</strong>
                <small>{problem ?? 'View category guidance'}</small>
              </ActionButton>
            )
          })}
        </div>
      </div>
    </section>
  )
}
