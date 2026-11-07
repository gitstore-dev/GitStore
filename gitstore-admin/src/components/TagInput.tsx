import { useId, useRef, useState } from 'react'
import {
  Button,
  Input,
  Tag,
  TagGroup,
  TagList,
  TextField,
} from 'react-aria-components'
import { X } from 'lucide-react'
import styles from './tag-input.module.css'
export function addTags(current: readonly string[], input: string): string[] {
  return [
    ...new Set([
      ...current,
      ...input
        .split(/[,\n]/)
        .map((value) => value.trim())
        .filter(Boolean),
    ]),
  ]
}
export function TagInput({
  value,
  onChange,
}: {
  value: readonly string[]
  onChange: (tags: readonly string[]) => void
}) {
  const [draft, setDraft] = useState('')
  const label = useId(),
    hint = useId()
  const root = useRef<HTMLDivElement>(null)
  function commit(input = draft) {
    if (input.trim()) onChange(addTags(value, input))
    setDraft('')
  }
  return (
    <div className={styles.field}>
      <span id={label}>Tags</span>
      <div ref={root} className={styles.inputArea}>
        <TagGroup
          aria-labelledby={label}
          onRemove={(keys) => onChange(value.filter((tag) => !keys.has(tag)))}
          className={styles.group}
        >
          <TagList
            items={value.map((tag) => ({ id: tag }))}
            className={styles.list}
          >
            {(tag) => (
              <Tag
                id={tag.id}
                textValue={tag.id}
                className={styles.tag}
                data-tag=""
              >
                <span>{tag.id}</span>
                <Button slot="remove" aria-label="Remove tag">
                  <X size={12} aria-hidden="true" />
                </Button>
              </Tag>
            )}
          </TagList>
        </TagGroup>
        <TextField
          value={draft}
          onChange={setDraft}
          aria-label="Add tag"
          className={styles.entry}
        >
          <Input
            placeholder="Add a tag"
            aria-describedby={hint}
            onBlur={() => commit()}
            onKeyDown={(event) => {
              if (event.nativeEvent.isComposing) return
              if (event.key === 'Enter' || event.key === ',') {
                event.preventDefault()
                commit()
              } else if (event.key === 'Backspace' && !draft && value.length) {
                event.preventDefault()
                root.current
                  ?.querySelectorAll<HTMLElement>('[data-tag]')
                  .item(value.length - 1)
                  ?.focus()
              }
            }}
            onPaste={(event) => {
              const text = event.clipboardData.getData('text')
              if (/[,\n]/.test(text)) {
                event.preventDefault()
                commit(draft + text)
              }
            }}
          />
        </TextField>
      </div>
      <small id={hint}>Press Enter or comma to add a tag.</small>
    </div>
  )
}
