import { useEffect, useRef, useState } from 'react'
import { CrepeBuilder } from '@milkdown/crepe/builder'
import { blockEdit } from '@milkdown/crepe/feature/block-edit'
import { toolbar } from '@milkdown/crepe/feature/toolbar'
import { listItem } from '@milkdown/crepe/feature/list-item'
import { linkTooltip } from '@milkdown/crepe/feature/link-tooltip'
import { table } from '@milkdown/crepe/feature/table'
import { cursor } from '@milkdown/crepe/feature/cursor'
import { placeholder } from '@milkdown/crepe/feature/placeholder'
import { uploadConfig } from '@milkdown/kit/plugin/upload'
import {
  editorViewOptionsCtx,
  prosePluginsCtx,
  serializerCtx,
} from '@milkdown/kit/core'
import { Plugin } from '@milkdown/kit/prose/state'
import { remarkPreserveEmptyLinePlugin } from '@milkdown/kit/preset/commonmark'
import { equivalentMarkdown } from './model'
import '@milkdown/crepe/theme/common/style.css'
import styles from './markdown.module.css'
/** This instance owns only the derived view; callers retain original source bytes. */
export default function RichMarkdown({
  source,
  onChange,
  onUnsupported,
}: {
  source: string
  onChange: (source: string) => void
  onUnsupported: (reason: string) => void
}) {
  const root = useRef<HTMLDivElement>(null)
  const initial = useRef(source)
  const callbacks = useRef({ onChange, onUnsupported })
  const [ready, setReady] = useState(false)
  useEffect(() => {
    callbacks.current = { onChange, onUnsupported }
  }, [onChange, onUnsupported])
  useEffect(() => {
    const element = root.current!
    let disposed = false,
      accepting = false
    const editor = new CrepeBuilder({
      root: element,
      defaultValue: initial.current,
    })
      .addFeature(blockEdit, { advancedGroup: { image: null, math: null } })
      .addFeature(toolbar)
      .addFeature(listItem)
      .addFeature(linkTooltip)
      .addFeature(table)
      .addFeature(cursor)
      .addFeature(placeholder)
    editor.editor.config((ctx) => {
      ctx.update(editorViewOptionsCtx, (options) => ({
        ...options,
        editable: () => accepting,
        attributes: {
          role: 'textbox',
          'aria-label': 'Description',
          'aria-multiline': 'true',
        },
      }))
      ctx.update(uploadConfig.key, (previous) => ({
        ...previous,
        uploader: async () => [],
      }))
    })
    editor.editor.config((ctx) => {
      ctx.update(prosePluginsCtx, (plugins) => [
        ...plugins,
        new Plugin({
          view: () => ({
            update: (view, previous) => {
              if (!accepting || disposed || view.state.doc.eq(previous.doc))
                return
              callbacks.current.onChange(ctx.get(serializerCtx)(view.state.doc))
            },
          }),
        }),
      ])
    })
    // CommonMark blank lines stay blank lines; do not emit HTML <br /> placeholders.
    const created = editor.editor
      .remove(remarkPreserveEmptyLinePlugin)
      .then(() => editor.create())
      .then(() => {
        if (disposed) return
        if (!equivalentMarkdown(initial.current, editor.getMarkdown())) {
          callbacks.current.onUnsupported(
            'This Markdown cannot be represented faithfully in Write mode. Continue in Markdown mode; your original description is preserved.',
          )
          return
        }
        accepting = true
        editor.setReadonly(false)
        setReady(true)
      })
      .catch(() => {
        if (!disposed)
          callbacks.current.onUnsupported(
            'The rich editor could not open. You can continue editing the original Markdown.',
          )
      })
    return () => {
      disposed = true
      accepting = false
      void created.then(() => editor.destroy())
    }
  }, [])
  return (
    <div className={styles.rich} data-markdown-ready={ready || undefined}>
      {!ready && <p role="status">Opening editor…</p>}
      <div ref={root} />
    </div>
  )
}
