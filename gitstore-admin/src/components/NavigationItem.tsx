import { Link } from '@tanstack/react-router'
import { Icon } from './Icon'
import { destinationHref } from '../navigation/registry'
import type { NavigationDestination } from '../navigation/registry'
import styles from '../shell/shell.module.css'
export function NavigationItem({
  destination,
  namespace,
  active,
  compact = false,
  label,
  onNavigate,
}: {
  destination: NavigationDestination
  namespace: string
  active: boolean
  compact?: boolean
  label?: string
  onNavigate?: () => void
}) {
  return (
    <Link
      to={destination.to}
      params={{ namespace }}
      onClick={onNavigate}
      aria-current={active ? 'page' : undefined}
      aria-label={compact ? (label ?? destination.label) : undefined}
      title={compact ? (label ?? destination.label) : undefined}
      className={`${styles.navItem} ${active ? styles.active : ''} ${compact ? styles.compactItem : ''}`}
      data-destination-id={destination.id}
      data-href={destinationHref(destination, namespace)}
    >
      {(compact || label) && <Icon name={destination.icon} />}
      {!compact && <span>{label ?? destination.label}</span>}
      {active && !compact && (
        <span className={styles.activeDot} aria-hidden="true" />
      )}
    </Link>
  )
}
