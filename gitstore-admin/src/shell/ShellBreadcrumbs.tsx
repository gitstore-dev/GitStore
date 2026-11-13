import { useLayoutEffect, useRef, useState } from 'react'
import type { ComponentPropsWithRef, ReactNode } from 'react'
import { createLink } from '@tanstack/react-router'
import {
  Button,
  Menu,
  MenuItem,
  MenuTrigger,
  Popover,
} from 'react-aria-components'
import { ChevronRight } from 'lucide-react'
import styles from './shell-breadcrumbs.module.css'
// Ancestors are navigation links even when the router fuzzily matches their
// paths. Only the breadcrumb's final plain-text item describes the current page.
export const BreadcrumbLink = createLink(
  (props: ComponentPropsWithRef<'a'>) => {
    const anchorProps = { ...props }
    delete anchorProps['aria-current']
    return <a {...anchorProps} />
  },
)
export type ShellCrumb = {
  label: string
  link: ReactNode
  onSelect: () => void
}
/** Navigation location only. Resource ancestry belongs inside its detail card. */
export function ShellBreadcrumbs({
  ancestors,
  current,
}: {
  ancestors: readonly ShellCrumb[]
  current: string
}) {
  const frame = useRef<HTMLElement>(null)
  const measure = useRef<HTMLDivElement>(null)
  const [collapsed, setCollapsed] = useState(false)
  useLayoutEffect(() => {
    function fit() {
      if (!frame.current || !measure.current) return
      // Truncate a long current title before hiding an ancestor; short titles
      // should not force a menu when the complete trail already fits.
      const widths = Array.from(measure.current.children).map(
        (node) => node.getBoundingClientRect().width,
      )
      const currentWidth = widths.at(-1) ?? 0
      const ancestorWidth =
        widths.slice(0, -1).reduce((sum, width) => sum + width, 0) +
        Math.max(0, ancestors.length - 1) * 10
      setCollapsed(
        ancestors.length > 1 &&
          ancestorWidth + Math.min(currentWidth, 110) + 10 >
            frame.current.clientWidth,
      )
    }
    fit()
    const observer = new ResizeObserver(fit)
    if (frame.current) observer.observe(frame.current)
    if (measure.current) {
      observer.observe(measure.current)
      for (const child of measure.current.children) observer.observe(child)
    }
    return () => observer.disconnect()
  }, [ancestors, current])
  const separator = <ChevronRight size={13} aria-hidden="true" />
  return (
    <nav ref={frame} aria-label="Breadcrumb" className={styles.breadcrumbs}>
      <ol>
        {ancestors.map((item, index) =>
          index === 0 || !collapsed ? (
            <li className={styles.ancestor} key={`${index}-${item.label}`}>
              {item.link}
              {separator}
            </li>
          ) : index === 1 ? (
            <li className={styles.ancestor} key="more">
              <MenuTrigger>
                <Button
                  className={styles.more}
                  aria-label="Show navigation ancestors"
                >
                  …
                </Button>
                <Popover
                  className={styles.popover}
                  placement="bottom start"
                  offset={8}
                >
                  <Menu
                    aria-label="Navigation ancestors"
                    className={styles.menu}
                  >
                    {ancestors.slice(1).map((ancestor, position) => (
                      <MenuItem
                        key={position}
                        id={position}
                        textValue={ancestor.label}
                        className={styles.menuItem}
                        onAction={ancestor.onSelect}
                      >
                        {ancestor.label}
                      </MenuItem>
                    ))}
                  </Menu>
                </Popover>
              </MenuTrigger>
              {separator}
            </li>
          ) : null,
        )}
        <li className={styles.current} aria-current="page" title={current}>
          {current}
        </li>
      </ol>
      <div ref={measure} className={styles.measure} aria-hidden="true">
        {ancestors.map((item, index) => (
          <span key={index}>
            {item.label}
            {separator}
          </span>
        ))}
        <span data-current-measure="">{current}</span>
      </div>
    </nav>
  )
}
