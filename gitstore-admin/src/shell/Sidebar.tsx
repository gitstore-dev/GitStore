import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import {
  Button,
  MenuTrigger,
  Menu,
  MenuItem,
  Popover,
  DialogTrigger,
  Dialog,
  TooltipTrigger,
  Tooltip,
} from 'react-aria-components'
import {
  ChevronDown,
  PanelLeftClose,
  PanelLeftOpen,
  Monitor,
  ArrowUpRight,
  ArrowLeft,
} from 'lucide-react'
import { groups, destinations, destinationById } from '../navigation/registry'
import type {
  NavigationGroup,
  NavigationDestination,
} from '../navigation/registry'
import type { NamespaceId } from '../data/demo'
import { workspaceAreas, areaFor, areaEntry } from '../navigation/areas'
import { moveNavigationFocus } from '../navigation/keyboard'
import { Icon } from '../components/Icon'
import { NavigationItem } from '../components/NavigationItem'
import { NamespacePicker } from '../components/NamespacePicker'
import { ThemePicker } from '../components/controls'
import { ProfileMenu } from '../components/ProfileMenu'
import styles from './shell.module.css'
function Group({
  group,
  active,
  namespace,
  compact,
  onNavigate,
}: {
  group: NavigationGroup
  active: NavigationDestination | undefined
  namespace: NamespaceId
  compact: boolean
  onNavigate?: () => void
}) {
  const items = destinations.filter(
    (destination) => destination.groupId === group.id,
  )
  const [open, setOpen] = useState(active?.groupId === group.id)
  const navigate = useNavigate()
  if (compact)
    return (
      <MenuTrigger>
        <TooltipTrigger delay={300}>
          <Button
            aria-label={`${group.label} pages`}
            className={`${styles.groupButton} ${styles.compactItem} ${active?.groupId === group.id ? styles.activeGroup : ''}`}
          >
            <Icon name={group.icon} />
          </Button>
          <Tooltip placement="right" className={styles.tooltip}>
            {group.label}
          </Tooltip>
        </TooltipTrigger>
        <Popover placement="right top" offset={12} className={styles.popover}>
          <p className={styles.menuHeading}>{group.label}</p>
          <Menu
            aria-label={`${group.label} pages`}
            items={items}
            className={styles.menu}
            onAction={(id) => {
              const destination = destinationById(String(id))
              void navigate({ to: destination.to, params: { namespace } })
              onNavigate?.()
            }}
          >
            {(destination) => (
              <MenuItem id={destination.id} className={styles.menuItem}>
                {destination.label}
                {active?.id === destination.id && (
                  <ArrowUpRight size={14} aria-hidden="true" />
                )}
              </MenuItem>
            )}
          </Menu>
        </Popover>
      </MenuTrigger>
    )
  return (
    <div className={styles.group}>
      <Button
        onPress={() => setOpen((value) => !value)}
        aria-expanded={open}
        aria-controls={`nav-${group.id}`}
        className={`${styles.groupButton} ${active?.groupId === group.id ? styles.activeGroup : ''}`}
      >
        <Icon name={group.icon} />
        <span>{group.label}</span>
        <ChevronDown
          size={13}
          aria-hidden="true"
          className={open ? styles.chevronOpen : styles.chevron}
        />
      </Button>
      {open && (
        <div id={`nav-${group.id}`} className={styles.groupChildren}>
          {items.map((destination) => (
            <NavigationItem
              key={destination.id}
              destination={destination}
              namespace={namespace}
              active={active?.id === destination.id}
              onNavigate={onNavigate}
            />
          ))}
        </div>
      )}
    </div>
  )
}
export function Sidebar({
  active,
  namespace,
  onNamespaceChange,
  onPlatformChange,
  compact,
  collapsed,
  onCollapse,
  onNavigate,
  mobile = false,
}: {
  active: NavigationDestination | undefined
  namespace: NamespaceId
  onNamespaceChange: (id: NamespaceId) => void
  onPlatformChange: () => void
  compact: boolean
  collapsed: boolean
  onCollapse: () => void
  onNavigate?: () => void
  mobile?: boolean
}) {
  const navigate = useNavigate()
  const platform = active?.scope === 'platform'
  const settings = active?.groupId === 'settings'
  const area = areaFor(active)
  const home = !platform && !settings && (!area || area.id === 'home')
  const contextGroups = groups.filter((group) =>
    platform
      ? group.id === 'control-plane'
      : settings
        ? group.id === 'settings'
        : area?.groups.includes(group.id),
  )
  const contextLabel = platform
    ? 'Control plane'
    : settings
      ? 'Settings'
      : (area?.label ?? 'Home')
  const contextIcon = platform
    ? 'platform'
    : settings
      ? 'settings'
      : (area?.icon ?? 'home')
  return (
    <div className={styles.sidebarContents} data-compact={compact || undefined}>
      <div className={styles.brand}>
        <img
          src="/branding/logo-light.svg"
          alt=""
          className={styles.lightLogo}
          width="34"
          height="34"
        />
        <img
          src="/branding/logo-dark.svg"
          alt=""
          className={styles.darkLogo}
          width="34"
          height="34"
        />
        {!compact && (
          <span className={styles.wordmark}>
            GitStore<span>ADMIN</span>
          </span>
        )}
        {!compact && !mobile && (
          <Button
            onPress={onCollapse}
            aria-label="Collapse sidebar"
            className={styles.collapseButton}
          >
            <PanelLeftClose size={17} aria-hidden="true" />
          </Button>
        )}
      </div>
      <div className={styles.workspacePicker}>
        <NamespacePicker
          value={namespace}
          onChange={onNamespaceChange}
          platform={platform}
          onPlatformChange={onPlatformChange}
          compact={compact}
        />
      </div>
      <nav
        onKeyDown={moveNavigationFocus}
        aria-label={mobile ? 'Mobile navigation' : 'Main navigation'}
        className={styles.navigation}
      >
        {!home && (
          <Link
            to="/w/$namespace/home/overview"
            params={{ namespace }}
            onClick={onNavigate}
            className={`${styles.navItem} ${styles.backLink} ${compact ? styles.compactItem : ''}`}
            aria-label={compact ? 'Back to workspace' : undefined}
            title={compact ? 'Back to workspace' : undefined}
          >
            <ArrowLeft size={16} aria-hidden="true" />
            {!compact && <span>Back to workspace</span>}
          </Link>
        )}
        {home ? (
          <>
            {workspaceAreas.map((item) => (
              <NavigationItem
                key={item.id}
                destination={areaEntry(item)}
                namespace={namespace}
                active={false}
                compact={compact}
                label={item.label}
                onNavigate={onNavigate}
              />
            ))}
            {!compact && <p className={styles.contextCaption}>HOME</p>}
          </>
        ) : (
          <MenuTrigger>
            <Button
              aria-label={
                compact ? `Workspace areas: ${contextLabel}` : undefined
              }
              className={`${styles.groupButton} ${styles.contextPicker} ${compact ? styles.compactItem : ''}`}
            >
              <Icon name={contextIcon} />
              {!compact && (
                <>
                  <span>{contextLabel}</span>
                  <ChevronDown size={13} aria-hidden="true" />
                </>
              )}
            </Button>
            <Popover
              placement={compact ? 'right top' : 'bottom start'}
              className={styles.popover}
            >
              <p className={styles.menuHeading}>Workspace areas</p>
              <Menu
                aria-label="Workspace areas"
                className={styles.menu}
                onAction={(id) => {
                  const target = workspaceAreas.find((item) => item.id === id)!
                  void navigate({
                    to: areaEntry(target).to,
                    params: { namespace },
                  })
                  onNavigate?.()
                }}
              >
                {workspaceAreas.map((item) => (
                  <MenuItem
                    key={item.id}
                    id={item.id}
                    className={styles.menuItem}
                  >
                    <Icon name={item.icon} />
                    {item.label}
                  </MenuItem>
                ))}
              </Menu>
            </Popover>
          </MenuTrigger>
        )}
        {contextGroups.map((group) =>
          compact || contextGroups.length > 1 ? (
            <Group
              key={`${group.id}-${active?.groupId === group.id}`}
              group={group}
              active={active}
              namespace={namespace}
              compact={compact}
              onNavigate={onNavigate}
            />
          ) : (
            <div key={group.id} className={styles.contextPages}>
              {destinations
                .filter((destination) => destination.groupId === group.id)
                .map((destination) => (
                  <NavigationItem
                    key={destination.id}
                    destination={destination}
                    namespace={namespace}
                    active={active?.id === destination.id}
                    onNavigate={onNavigate}
                  />
                ))}
            </div>
          ),
        )}
      </nav>
      <div className={styles.sidebarFooter}>
        {!settings && !platform && (
          <NavigationItem
            destination={destinationById('settings.store-details')}
            namespace={namespace}
            active={active?.groupId === 'settings'}
            compact={compact}
            label="Settings"
            onNavigate={onNavigate}
          />
        )}
        {compact ? (
          <DialogTrigger>
            <Button
              aria-label="Appearance"
              className={`${styles.groupButton} ${styles.compactItem}`}
            >
              <Monitor size={18} aria-hidden="true" />
            </Button>
            <Popover
              placement="right bottom"
              className={styles.popover}
              offset={12}
            >
              <Dialog aria-label="Appearance" className={styles.themeDialog}>
                <h3>Appearance</h3>
                <ThemePicker />
              </Dialog>
            </Popover>
          </DialogTrigger>
        ) : (
          <ThemePicker />
        )}
        <ProfileMenu compact={compact} mobile={mobile} />
        {compact && !mobile && !collapsed && (
          <Button
            onPress={onCollapse}
            aria-label="Expand sidebar"
            className={styles.railExpand}
          >
            <PanelLeftOpen size={16} aria-hidden="true" />
          </Button>
        )}
        {!compact && (
          <div className={styles.footerNote}>
            <span>PREPARE / REVIEW / RECOVER</span>
            <span>0.1</span>
          </div>
        )}
      </div>
    </div>
  )
}
