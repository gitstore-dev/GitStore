import { useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Button,
  Menu,
  MenuItem,
  MenuTrigger,
  Popover,
} from 'react-aria-components'
import { PageContextTrail } from '../../home/shared/HomePage'
import styles from './category-breadcrumb.module.css'

export type CategoryCrumb = {
  id: string
  title: string
  link: ReactNode
  onSelect: () => void
}

/** Preserve the root and nearest context while fitting any depth into one line. */
export function CategoryBreadcrumb({
  items,
  incomplete = false,
}: {
  items: readonly CategoryCrumb[]
  incomplete?: boolean
}) {
  const container = useRef<HTMLDivElement>(null)
  const measure = useRef<HTMLDivElement>(null)
  const [tail, setTail] = useState(items.length - 1)
  useLayoutEffect(() => {
    function fit() {
      if (!container.current || !measure.current) return
      const widths = Array.from(measure.current.children).map(
        (node) => node.getBoundingClientRect().width,
      )
      const available = container.current.clientWidth - 24 // identity marker and its gap
      const full =
        widths.reduce((sum, width) => sum + width, 0) +
        Math.max(0, widths.length - 1) * 30
      if (full <= available || items.length <= 3) {
        setTail(items.length - 1)
        return
      }
      let count = 2 // immediate parent and current category always remain
      let used =
        widths[0]! +
        widths.slice(-2).reduce((sum, width) => sum + width, 0) +
        110
      while (
        count < items.length - 2 &&
        used + widths[items.length - count - 1]! + 30 <= available
      ) {
        used += widths[items.length - count - 1]! + 30
        count++
      }
      setTail(count)
    }
    fit()
    const observer = new ResizeObserver(fit)
    if (container.current) observer.observe(container.current)
    if (measure.current) observer.observe(measure.current)
    return () => observer.disconnect()
  }, [items])
  const collapsed = tail < items.length - 1
  const omitted = collapsed ? items.slice(1, -tail) : []
  const visible = collapsed ? [items[0]!, ...items.slice(-tail)] : items
  const crumb = (item: CategoryCrumb) =>
    item.id === items.at(-1)?.id ? (
      <span aria-current="page" title={item.title}>
        {item.title}
      </span>
    ) : (
      item.link
    )
  return (
    <>
      <nav className={styles.desktop} aria-label="Category path">
        <div
          ref={container}
          className={styles.container}
          data-collapsed={collapsed}
        >
          <PageContextTrail className={styles.trail}>
            {visible.map((item, index) => (
              <span className={styles.segment} key={item.id}>
                {index > 0 && (
                  <span className={styles.separator} aria-hidden="true">
                    /
                  </span>
                )}
                {index === 1 && collapsed && (
                  <>
                    <MenuTrigger>
                      <Button
                        className={styles.more}
                        aria-label="Show omitted category ancestors"
                      >
                        …
                      </Button>
                      <Popover
                        className={styles.popover}
                        placement="bottom start"
                        offset={8}
                      >
                        <Menu
                          aria-label="Omitted category ancestors"
                          className={styles.menu}
                        >
                          {omitted.map((ancestor) => (
                            <MenuItem
                              key={ancestor.id}
                              id={ancestor.id}
                              textValue={ancestor.title}
                              className={styles.menuItem}
                              onAction={ancestor.onSelect}
                            >
                              {ancestor.title}
                            </MenuItem>
                          ))}
                        </Menu>
                      </Popover>
                    </MenuTrigger>
                    <span className={styles.separator} aria-hidden="true">
                      /
                    </span>
                  </>
                )}
                {crumb(item)}
              </span>
            ))}
          </PageContextTrail>
          <div ref={measure} className={styles.measure} aria-hidden="true">
            {items.map((item) => (
              <span key={item.id}>{item.title}</span>
            ))}
          </div>
        </div>
      </nav>
      <details className={styles.mobile}>
        <summary>Category path</summary>
        <nav aria-label="Full category path">
          <ol>
            {items.map((item) => (
              <li key={item.id}>{crumb(item)}</li>
            ))}
          </ol>
        </nav>
      </details>
      {incomplete && <p className={styles.warning}>Incomplete hierarchy</p>}
    </>
  )
}
