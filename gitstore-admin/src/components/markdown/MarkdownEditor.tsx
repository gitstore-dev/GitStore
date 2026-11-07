import { lazy, Suspense, useState } from 'react'
import {
  RadioGroup,
  Radio,
  TextField,
  Label,
  TextArea,
} from 'react-aria-components'
import { MarkdownPreview } from './MarkdownPreview'
import { sourceOnlyReason } from './model'
import styles from './markdown.module.css'
const RichMarkdown = lazy(() => import('./RichMarkdown'))
export function MarkdownEditor({
  value,
  onChange,
}: {
  value: string
  onChange: (value: string) => void
}) {
  const [mode, setMode] = useState('write')
  const [unsupported, setUnsupported] = useState<{
    source: string
    reason: string
  } | null>(null)
  const reason =
    sourceOnlyReason(value) ??
    (unsupported?.source === value ? unsupported.reason : null)
  return (
    <section className={styles.editor} aria-label="Markdown description editor">
      <div className={styles.top}>
        <h2>Description</h2>
        <RadioGroup
          aria-label="Description mode"
          orientation="horizontal"
          value={reason && mode === 'write' ? 'markdown' : mode}
          onChange={setMode}
          className={styles.modes}
        >
          {['write', 'markdown', 'preview'].map((id) => (
            <Radio
              className={styles.mode}
              key={id}
              value={id}
              isDisabled={id === 'write' && !!reason}
            >
              {id === 'write'
                ? 'Write'
                : id === 'markdown'
                  ? 'Markdown'
                  : 'Preview'}
            </Radio>
          ))}
        </RadioGroup>
      </div>
      {reason && (
        <p role="status" className={styles.note}>
          {reason}
        </p>
      )}
      {mode === 'preview' ? (
        <MarkdownPreview source={value} />
      ) : mode === 'markdown' || reason ? (
        <TextField className={styles.source} value={value} onChange={onChange}>
          <Label>Markdown description</Label>
          <TextArea spellCheck={false} rows={12} />
        </TextField>
      ) : (
        <Suspense fallback={<p role="status">Opening editor…</p>}>
          <RichMarkdown
            source={value}
            onChange={onChange}
            onUnsupported={(reason) =>
              setUnsupported({ source: value, reason })
            }
          />
        </Suspense>
      )}
      <p className={styles.hint}>
        Markdown · Type / for blocks. Select text to format. Images in the
        description are separate from the product gallery.
      </p>
    </section>
  )
}
