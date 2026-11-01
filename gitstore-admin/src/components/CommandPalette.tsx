import { useRef, useState } from 'react'
import {
  ModalOverlay,
  Modal,
  Dialog,
  Heading,
  SearchField,
  Input,
  ListBox,
  ListBoxItem,
} from 'react-aria-components'
import { Search, ArrowUpRight, X } from 'lucide-react'
import { searchDestinations } from '../navigation/registry'
import type { NavigationDestination } from '../navigation/registry'
import { Icon } from './Icon'
import { ActionButton } from './controls'
import styles from '../shell/shell.module.css'
export function CommandPalette({
  open,
  onOpenChange,
  onNavigate,
}: {
  open: boolean
  onOpenChange: (value: boolean) => void
  onNavigate: (destination: NavigationDestination) => void
}) {
  const [query, setQuery] = useState('')
  const list = useRef<HTMLDivElement>(null)
  const results = searchDestinations(query)
  return (
    <ModalOverlay
      isOpen={open}
      onOpenChange={(value) => {
        onOpenChange(value)
        if (!value) setQuery('')
      }}
      isDismissable
      className={`${styles.overlay} ${styles.commandOverlay}`}
    >
      <Modal className={styles.commandModal}>
        <Dialog className={styles.dialog}>
          <Heading slot="title" className={styles.srOnly}>
            Search navigation
          </Heading>
          <div className={styles.commandInput}>
            <Search size={20} aria-hidden="true" />
            <SearchField
              aria-label="Search pages"
              value={query}
              onChange={setQuery}
              className={styles.searchField}
            >
              <Input
                autoFocus
                placeholder="Where would you like to go?"
                onKeyDown={(event) => {
                  if (event.key === 'ArrowDown') {
                    event.preventDefault()
                    list.current?.focus()
                  }
                }}
              />
            </SearchField>
            <ActionButton
              aria-label="Close search"
              variant="ghost"
              onPress={() => {
                onOpenChange(false)
                setQuery('')
              }}
            >
              <X size={16} />
            </ActionButton>
          </div>
          {results.length ? (
            <ListBox
              ref={list}
              aria-label="Navigation results"
              items={results}
              className={styles.commandResults}
              onAction={(id) => {
                const destination = results.find((result) => result.id === id)
                if (destination) {
                  onNavigate(destination)
                  onOpenChange(false)
                  setQuery('')
                }
              }}
            >
              {(destination) => (
                <ListBoxItem
                  id={destination.id}
                  textValue={`${destination.groupLabel} ${destination.label}`}
                  className={styles.commandOption}
                >
                  <span className={styles.commandIcon}>
                    <Icon name={destination.icon} />
                  </span>
                  <span>
                    <strong>{destination.label}</strong>
                    <small>
                      {destination.groupLabel}
                      {destination.scope === 'platform' ? ' · Platform' : ''}
                    </small>
                  </span>
                  <ArrowUpRight size={16} aria-hidden="true" />
                </ListBoxItem>
              )}
            </ListBox>
          ) : (
            <div className={styles.noResults}>
              <Search size={24} aria-hidden="true" />
              <h3>No destinations found</h3>
              <p>Try a page name or section, such as “Catalog”.</p>
            </div>
          )}
          <div className={styles.commandFooter}>
            <span>Navigation only</span>
            <span>
              {results.length} destinations <kbd>↑</kbd> <kbd>↓</kbd> to move{' '}
              <kbd>↵</kbd> to open
            </span>
          </div>
        </Dialog>
      </Modal>
    </ModalOverlay>
  )
}
