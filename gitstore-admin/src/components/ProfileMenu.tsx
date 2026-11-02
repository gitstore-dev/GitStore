import { useState } from 'react'
import {
  MenuTrigger,
  Menu,
  MenuItem,
  Popover,
  ModalOverlay,
  Modal,
  Dialog,
  Heading,
  RadioGroup,
  Radio,
} from 'react-aria-components'
import {
  MoreHorizontal,
  UserRound,
  Keyboard,
  Accessibility,
  X,
  Check,
  LogOut,
} from 'lucide-react'
import { ActionButton, ThemePicker } from './controls'
import { demoUser } from '../data/demo'
import { usePreferences } from '../state/preferences'
import type { MotionPreference } from '../state/preferences'
import styles from '../shell/shell.module.css'
export function ProfileMenu({
  compact = false,
  mobile = false,
}: {
  compact?: boolean
  mobile?: boolean
}) {
  const [panel, setPanel] = useState<'profile' | 'shortcuts' | null>(null)
  const { motion, setMotion } = usePreferences()
  return (
    <>
      <MenuTrigger>
        <ActionButton
          className={styles.profileButton}
          aria-label={
            compact
              ? `${demoUser.initials} — Profile menu for ${demoUser.name}`
              : undefined
          }
          variant="ghost"
        >
          <span aria-hidden="true" className={styles.avatar}>
            {demoUser.initials}
          </span>
          {!compact && (
            <>
              <span className={styles.profileName}>
                <strong>{demoUser.name}</strong> <small>{demoUser.role}</small>
              </span>
              <MoreHorizontal size={17} aria-hidden="true" />
            </>
          )}
        </ActionButton>
        {/* The mobile drawer already owns modality and focus containment. */}
        <Popover
          isNonModal={mobile}
          placement={mobile ? 'top start' : 'right bottom'}
          offset={8}
          containerPadding={12}
          className={`${styles.popover} ${mobile ? styles.mobileProfilePopover : ''}`}
        >
          <Menu aria-label="Profile actions" className={styles.menu}>
            <MenuItem
              id="profile"
              onAction={() => setPanel('profile')}
              className={styles.menuItem}
            >
              <UserRound size={16} aria-hidden="true" />
              Profile & preferences
            </MenuItem>
            <MenuItem
              id="shortcuts"
              onAction={() => setPanel('shortcuts')}
              className={styles.menuItem}
            >
              <Keyboard size={16} aria-hidden="true" />
              Keyboard shortcuts
            </MenuItem>
            <MenuItem
              id="motion"
              onAction={() =>
                setMotion(motion === 'reduced' ? 'system' : 'reduced')
              }
              className={styles.menuItem}
            >
              <Accessibility size={16} aria-hidden="true" />
              Reduce motion
              {motion === 'reduced' && <Check size={15} aria-hidden="true" />}
            </MenuItem>
            <MenuItem
              id="logout"
              isDisabled
              textValue="Log out"
              className={styles.menuItem}
            >
              <LogOut size={16} aria-hidden="true" />
              <span>
                Log out<small>Authentication not connected</small>
              </span>
            </MenuItem>
          </Menu>
        </Popover>
      </MenuTrigger>
      <ModalOverlay
        isOpen={panel !== null}
        onOpenChange={(open) => {
          if (!open) setPanel(null)
        }}
        isDismissable
        className={styles.overlay}
      >
        <Modal className={styles.modal}>
          <Dialog className={styles.dialog}>
            <div className={styles.dialogHeader}>
              <Heading slot="title">
                {panel === 'shortcuts'
                  ? 'Keyboard shortcuts'
                  : 'Your workspace preferences'}
              </Heading>
              <ActionButton
                variant="ghost"
                aria-label="Close preferences"
                onPress={() => setPanel(null)}
              >
                <X size={18} />
              </ActionButton>
            </div>
            {panel === 'shortcuts' ? (
              <dl className={styles.shortcutList}>
                <div>
                  <dt>Search navigation</dt>
                  <dd>
                    <kbd>⌘ / Ctrl</kbd> <kbd>K</kbd>
                  </dd>
                </div>
                <div>
                  <dt>Dismiss menus and dialogs</dt>
                  <dd>
                    <kbd>Esc</kbd>
                  </dd>
                </div>
                <div>
                  <dt>Move through navigation and menus</dt>
                  <dd>
                    <kbd>↑</kbd> <kbd>↓</kbd>
                  </dd>
                </div>
                <div>
                  <dt>Open the focused page</dt>
                  <dd>
                    <kbd>Enter</kbd>
                  </dd>
                </div>
              </dl>
            ) : (
              <>
                <p className={styles.muted}>
                  These preferences stay in this browser. This is a local demo
                  identity.
                </p>
                <div className={styles.identityCard}>
                  <span className={styles.avatar}>{demoUser.initials}</span>
                  <span>
                    <strong>{demoUser.name}</strong>
                    <small>{demoUser.role}</small>
                  </span>
                </div>
                <h3>Appearance</h3>
                <ThemePicker />
                <h3>Motion</h3>
                <RadioGroup
                  aria-label="Motion"
                  value={motion}
                  onChange={(v) => setMotion(v as MotionPreference)}
                  className={styles.motionChoices}
                >
                  <Radio value="system">Follow system</Radio>
                  <Radio value="reduced">Reduce motion</Radio>
                </RadioGroup>
                <p className={styles.muted}>
                  Your system’s reduced-motion setting is always respected.
                </p>
              </>
            )}
          </Dialog>
        </Modal>
      </ModalOverlay>
    </>
  )
}
