import { useMediaQuery } from '../../../state/preferences'
import { useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { ArrowUpRight, ChevronDown, ChevronRight } from 'lucide-react'
import { ActionButton } from '../../../components/controls'
import type { DemoSession } from '../../demo/model'
import type { CatalogEntry } from './model'
import { categoryHierarchy, categoryPath } from './model'
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
  const mobile = useMediaQuery('(max-width: 600px)')
  const viewport = useRef<HTMLDivElement>(null)
  const drag = useRef<{
    x: number
    y: number
    panX: number
    panY: number
  } | null>(null)
  const [zoom, setZoom] = useState(1),
    [pan, setPan] = useState({ x: 24, y: 24 }),
    [panning, setPanning] = useState(false)
  const rows = categoryHierarchy(session, entries)
  const parent = (id: string) => {
    const item = session.catalog[id]
    return item?.kind === 'CategoryTaxonomy' ? item.spec.parentId : null
  }
  const roots = rows.filter(
    (row) =>
      !rows.some((other) => other.item.id === parent(row.item.id)) ||
      (row.problem?.includes('cycle') && row.depth === 0),
  )
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(
    () =>
      new Set([
        ...roots.map((row) => row.item.id),
        ...(selected ? categoryPath(session, selected) : []),
      ]),
  )
  const [productLimit, setProductLimit] = useState(6)
  const path = new Set(selected ? categoryPath(session, selected) : [])
  const compact = zoom < 0.45
  const visible = rows.filter((row) =>
    compact
      ? roots.some((root) => root.item.id === row.item.id)
      : categoryPath(session, row.item.id)
          .slice(0, -1)
          .every((id) => expanded.has(id)),
  )
  const positions = new Map<string, { x: number; y: number }>()
  let leaf = 0
  const place = (id: string, depth: number, seen: Set<string>): number => {
    if (seen.has(id)) return leaf++ * 155
    const next = new Set(seen).add(id)
    const children = visible.filter(
      (row) => parent(row.item.id) === id && !next.has(row.item.id),
    )
    const ys = children.map((row) => place(row.item.id, depth + 1, next))
    const y = ys.length
      ? ys.reduce((a, b) => a + b, 0) / ys.length
      : leaf++ * 155
    positions.set(id, { x: compact ? 0 : depth * 300, y })
    return y
  }
  visible
    .filter(
      (row) =>
        !visible.some((other) => other.item.id === parent(row.item.id)) ||
        (row.problem?.includes('cycle') && row.depth === 0),
    )
    .forEach((row) => place(row.item.id, 0, new Set()))
  visible
    .filter((row) => !positions.has(row.item.id))
    .forEach((row) => place(row.item.id, 0, new Set()))
  const products = selected
    ? Object.entries(session.catalog).filter(
        ([id, item]) =>
          item.kind === 'Product' &&
          !session.deleting.includes(id) &&
          item.spec.categoryId === selected,
      )
    : []
  const productX = Math.max(0, ...[...positions.values()].map((p) => p.x)) + 320
  const productPositions = new Map(
    !compact && !mobile
      ? products
          .slice(0, productLimit)
          .map(([id], index) => [id, { x: productX, y: index * 125 }] as const)
      : [],
  )
  const width = Math.max(
    300,
    ...[...positions.values(), ...productPositions.values()].map(
      (p) => p.x + 255,
    ),
  )
  const height = Math.max(
    180,
    ...[...positions.values(), ...productPositions.values()].map(
      (p) => p.y + 110,
    ),
  )
  const scale = (value: number) => setZoom(Math.min(1.8, Math.max(0.25, value)))
  function fit() {
    const rect = viewport.current?.getBoundingClientRect()
    if (!rect) return
    setZoom(
      Math.max(
        0.25,
        Math.min(1, (rect.width - 48) / width, (rect.height - 48) / height),
      ),
    )
    setPan({ x: 24, y: 24 })
  }
  function center(point: { x: number; y: number }, scaleValue = zoom) {
    const rect = viewport.current?.getBoundingClientRect()
    if (rect)
      setPan({
        x: rect.width / 2 - (point.x + 122) * scaleValue,
        y: rect.height / 2 - (point.y + 40) * scaleValue,
      })
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
        Explore a category to unfold its branches and highlight its path.
        Connected products appear alongside it. Drag to pan; use arrows, +/−, or
        0 when the map is focused.
      </p>
      <div
        ref={viewport}
        className={styles.mapViewport}
        data-panning={panning || undefined}
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
          } else if (['+', '=', '-', '0'].includes(event.key)) {
            event.preventDefault()
            if (event.key === '0') fit()
            else scale(zoom + (event.key === '-' ? -0.15 : 0.15))
          }
        }}
        onPointerDown={(event) => {
          if ((event.target as Element).closest('button,a')) return
          drag.current = {
            x: event.clientX,
            y: event.clientY,
            panX: pan.x,
            panY: pan.y,
          }
          setPanning(true)
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
          setPanning(false)
        }}
        onPointerCancel={() => {
          drag.current = null
          setPanning(false)
        }}
      >
        <div
          className={styles.mapScene}
          data-detail={compact ? 'compact' : 'full'}
          style={{
            width,
            height,
            transform: `translate(${pan.x}px,${pan.y}px) scale(${zoom})`,
          }}
        >
          <svg
            width={width}
            height={height}
            aria-hidden="true"
            className={styles.edges}
          >
            {visible.map(({ item }) => {
              const source = positions.get(parent(item.id) ?? '')
              const target = positions.get(item.id)!
              return source ? (
                <path
                  key={item.id}
                  data-active={path.has(item.id) || undefined}
                  pathLength={1}
                  d={`M${source.x + 245},${source.y + 40} C${source.x + 280},${source.y + 40} ${target.x - 40},${target.y + 40} ${target.x},${target.y + 40}`}
                />
              ) : null
            })}
            {selected &&
              positions.has(selected) &&
              [...productPositions].map(([id, p]) => {
                const source = positions.get(selected)!
                return (
                  <path
                    key={`product-${id}`}
                    data-active="true"
                    pathLength={1}
                    d={`M${source.x + 245},${source.y + 40} C${source.x + 280},${source.y + 40} ${p.x - 40},${p.y + 40} ${p.x},${p.y + 40}`}
                  />
                )
              })}
          </svg>
          {visible.map(({ item, problem }) => {
            const point = positions.get(item.id)!
            const childCount = rows.filter(
              (row) => parent(row.item.id) === item.id,
            ).length
            return (
              <div
                key={item.id}
                className={styles.mapNode}
                data-path={path.has(item.id) || undefined}
                data-selected={selected === item.id || undefined}
                style={{ left: point.x, top: point.y }}
              >
                <ActionButton
                  className={styles.mapSelect}
                  aria-pressed={selected === item.id}
                  onFocus={(event) => {
                    if (event.currentTarget.matches(':focus-visible'))
                      center(point)
                  }}
                  onPress={(event) => {
                    if (compact) scale(1)
                    const connected = Object.entries(session.catalog).filter(
                      ([id, value]) =>
                        value.kind === 'Product' &&
                        !session.deleting.includes(id) &&
                        value.spec.categoryId === item.id,
                    ).length
                    const bounds = viewport.current?.getBoundingClientRect()
                    if (connected && !mobile && bounds) {
                      setZoom(
                        Math.max(
                          0.5,
                          Math.min(
                            1,
                            (bounds.width -
                              (window.innerWidth > 1100 ? 300 : 0) -
                              48) /
                              (productX + 255),
                            (bounds.height - 48) /
                              Math.max(
                                height,
                                Math.min(connected, productLimit) * 125,
                              ),
                          ),
                        ),
                      )
                      setPan({ x: 24, y: 24 })
                    }
                    setExpanded((current) => new Set(current).add(item.id))
                    onSelect(item.id, event.target as HTMLElement)
                  }}
                >
                  <strong
                    style={{ fontSize: compact ? Math.min(44, 16 / zoom) : 22 }}
                  >
                    {item.title}
                  </strong>
                  <small>
                    {problem ??
                      (compact
                        ? `${childCount} subcategories`
                        : 'View category guidance')}
                  </small>
                </ActionButton>
                {!!childCount && !problem?.includes('cycle') && (
                  <ActionButton
                    className={styles.branchToggle}
                    aria-label={`${expanded.has(item.id) ? 'Fold' : 'Unfold'} ${item.title} branch`}
                    aria-expanded={expanded.has(item.id)}
                    onPress={() =>
                      setExpanded((current) => {
                        const next = new Set(current)
                        if (next.has(item.id)) next.delete(item.id)
                        else next.add(item.id)
                        return next
                      })
                    }
                  >
                    {expanded.has(item.id) ? (
                      <ChevronDown size={14} />
                    ) : (
                      <ChevronRight size={14} />
                    )}
                  </ActionButton>
                )}
              </div>
            )
          })}
          {[...productPositions].map(([id, point]) => {
            const item = session.catalog[id]!
            return (
              <Link
                key={id}
                className={styles.mapProduct}
                style={{ left: point.x, top: point.y }}
                to="/w/$namespace/catalog/products/$productId"
                params={{ namespace: session.namespace, productId: id }}
              >
                <small>Product</small>
                <strong>{'spec' in item ? item.spec.title : id}</strong>
                <ArrowUpRight size={13} aria-hidden="true" />
              </Link>
            )
          })}
        </div>
      </div>
      {selected && mobile && (
        <section
          className={styles.mobileProducts}
          aria-label="Connected products"
        >
          <h3>Connected products</h3>
          {products.length ? (
            <ul>
              {products.slice(0, productLimit).map(([id, item]) => (
                <li key={id}>
                  <Link
                    to="/w/$namespace/catalog/products/$productId"
                    params={{ namespace: session.namespace, productId: id }}
                  >
                    {'spec' in item ? item.spec.title : id}
                    <ArrowUpRight size={12} aria-hidden="true" />
                  </Link>
                </li>
              ))}
            </ul>
          ) : (
            <p>No directly assigned products.</p>
          )}
        </section>
      )}
      {selected && (
        <div className={styles.mapSelection}>
          <span>
            {path.size} categories on the selected path · {products.length}{' '}
            connected products
          </span>
          {products.length > productLimit && (
            <ActionButton onPress={() => setProductLimit((value) => value + 6)}>
              Show more connected products
            </ActionButton>
          )}
        </div>
      )}
    </section>
  )
}
