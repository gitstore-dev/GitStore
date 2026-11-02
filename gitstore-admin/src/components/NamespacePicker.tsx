import {
  Button,
  Select,
  SelectValue,
  Popover,
  ListBox,
  ListBoxItem,
  ListBoxSection,
  Header,
  Label,
} from 'react-aria-components'
import { ChevronDown, Check, Layers, SlidersHorizontal } from 'lucide-react'
import { namespaces } from '../data/demo'
import type { NamespaceId } from '../data/demo'
import styles from '../shell/shell.module.css'
export function NamespacePicker({
  value,
  onChange,
  compact = false,
  platform = false,
  onPlatformChange,
}: {
  value: NamespaceId
  onChange: (id: NamespaceId) => void
  compact?: boolean
  platform?: boolean
  onPlatformChange?: () => void
}) {
  const selected =
    namespaces.find((choice) => choice.id === value) ?? namespaces[0]
  const selectedName = platform ? 'Control plane' : selected.name
  return (
    <Select
      className={styles.workspaceSelect}
      selectedKey={platform ? 'platform' : value}
      onSelectionChange={(key) =>
        key === 'platform' ? onPlatformChange?.() : onChange(key as NamespaceId)
      }
    >
      <Label className="sr-only">Workspace context</Label>
      <Button
        className={`${styles.workspaceButton} ${compact ? styles.workspaceCompact : ''}`}
        data-context={platform ? 'platform' : 'store'}
      >
        <span aria-hidden="true" className={styles.workspaceAvatar}>
          {platform ? (
            <SlidersHorizontal size={16} aria-hidden="true" />
          ) : (
            <Layers size={16} aria-hidden="true" />
          )}
        </span>{' '}
        {compact && (
          <SelectValue className="sr-only">{selectedName}</SelectValue>
        )}
        {!compact && (
          <>
            <span className={styles.workspaceName}>
              <SelectValue>{selectedName}</SelectValue>
              {platform && (
                <small className={styles.scopeLabel}>Platform</small>
              )}
            </span>
            <ChevronDown size={14} aria-hidden="true" />
          </>
        )}
      </Button>
      <Popover
        placement={compact ? 'right top' : 'bottom start'}
        offset={5}
        className={`${styles.popover} ${compact ? '' : styles.workspacePopover}`}
      >
        <ListBox className={styles.menu}>
          <ListBoxSection className={styles.contextSection}>
            <Header className={styles.scopeHeading}>Stores</Header>
            {namespaces.map((namespace) => (
              <ListBoxItem
                key={namespace.id}
                id={namespace.id}
                textValue={namespace.name}
                className={styles.menuItem}
              >
                {({ isSelected }) => (
                  <>
                    <Layers size={16} aria-hidden="true" />
                    <span>
                      <strong>{namespace.name}</strong>
                      <small>{namespace.description}</small>
                    </span>
                    {isSelected && (
                      <Check
                        size={15}
                        aria-hidden="true"
                        data-selection-indicator
                      />
                    )}
                  </>
                )}
              </ListBoxItem>
            ))}
          </ListBoxSection>
          {onPlatformChange && (
            <ListBoxSection
              className={`${styles.contextSection} ${styles.platformSection}`}
            >
              <Header className={styles.scopeHeading}>Platform</Header>
              <ListBoxItem
                id="platform"
                textValue="Control plane"
                className={styles.menuItem}
                data-context="platform"
              >
                {({ isSelected }) => (
                  <>
                    <SlidersHorizontal size={16} aria-hidden="true" />
                    <span>
                      <strong>Control plane</strong>
                      <small>Manage across all stores</small>
                    </span>
                    {isSelected && (
                      <Check
                        size={15}
                        aria-hidden="true"
                        data-selection-indicator
                      />
                    )}
                  </>
                )}
              </ListBoxItem>
            </ListBoxSection>
          )}
        </ListBox>
      </Popover>
    </Select>
  )
}
