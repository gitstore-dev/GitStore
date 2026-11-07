import {
  FieldError,
  Input,
  Label,
  TextArea,
  TextField,
} from 'react-aria-components'
import { ActionButton } from '../../../components/controls'
import { changedFields, sameText, validText, workTitle } from './model'
import type { ProductText, WorkItem } from './model'
import styles from './my-work.module.css'
import { TechnicalDetails } from '../../../components/TechnicalDetails'
import { ChangePreview } from '../demo/ChangePreview'
import { snapshotChanges } from '../demo/snapshots'
import type { ItemSnapshot } from '../demo/snapshots'
import { WorkBadge } from './WorkBadge'
export function WorkInspector({
  item,
  buffer,
  readOnly,
  onEdit,
  onText,
  onSave,
  onCancel,
  onAction,
  catalog,
  staleBase,
}: {
  item: WorkItem
  catalog: Readonly<Record<string, ItemSnapshot>>
  staleBase: boolean
  buffer: ProductText | null
  readOnly: boolean
  onEdit: () => void
  onText: (text: ProductText) => void
  onSave: () => void
  onCancel: () => void
  onAction: (
    action: 'submit' | 'discard' | 'approve' | 'request-changes',
  ) => void
}) {
  const fields = changedFields(item)
  const dirty =
    item.entryType === 'product-text' &&
    !!buffer &&
    !sameText(buffer, item.proposed)
  return (
    <div className={styles.details}>
      <span className={styles.eyebrow}>
        {item.entryType === 'recovery' ? 'DEMO RECOVERY' : 'PRODUCT CHANGE'}
      </span>
      <h2 id="work-inspector-heading" tabIndex={-1}>
        {workTitle(item)}
      </h2>
      <WorkBadge item={item} />
      <dl className={styles.metadata}>
        <dt>Author</dt>
        <dd>{item.author}</dd>
        <dt>Reviewer</dt>
        <dd>{item.reviewer}</dd>
        <dt>Updated</dt>
        <dd>
          <time dateTime={item.updatedAt}>
            {formatWorkTime(item.updatedAt)}
          </time>
        </dd>
      </dl>
      {item.reviewNote && (
        <section className={styles.note}>
          <h3>Review note</h3>
          <p>{item.reviewNote}</p>
        </section>
      )}
      {staleBase && (
        <p className={styles.note}>
          This draft was based on an earlier version of this product. Your draft
          text is preserved; review it against the current item before
          submitting.
        </p>
      )}
      {item.entryType === 'recovery' ? (
        <ChangePreview
          changes={snapshotChanges(
            item.recovery.before,
            item.recovery.after,
            catalog,
          )}
        />
      ) : (
        <section aria-label="Change preview" className={styles.preview}>
          <h3>
            {fields.length
              ? `${fields.length} ${fields.length === 1 ? 'field' : 'fields'} changed`
              : 'No changes to submit'}
          </h3>
          {fields.map((field) => (
            <section
              key={field}
              aria-label={
                field === 'title' ? 'Title change' : 'Description change'
              }
            >
              <h4>{field === 'title' ? 'Title' : 'Description'}</h4>
              <div className={styles.before}>
                <strong>Before</strong>
                <p>{item.base.spec[field] || 'Empty'}</p>
              </div>
              <div className={styles.after}>
                <strong>After</strong>
                <p>{item.proposed.spec[field] || 'Empty'}</p>
              </div>
            </section>
          ))}
        </section>
      )}
      {buffer && (
        <form
          aria-label="Edit demo draft"
          onSubmit={(event) => {
            event.preventDefault()
            if (validText(buffer)) onSave()
          }}
          className={styles.editor}
        >
          <h3>Edit product text</h3>
          <TextField
            value={buffer.spec.title}
            onChange={(title) => onText({ spec: { ...buffer.spec, title } })}
            isRequired
            isInvalid={!validText(buffer)}
            className={styles.field}
          >
            <Label>Title</Label>
            <Input autoFocus />
            <FieldError>Enter a title.</FieldError>
          </TextField>
          <TextField
            value={buffer.spec.description}
            onChange={(description) =>
              onText({ spec: { ...buffer.spec, description } })
            }
            className={styles.field}
          >
            <Label>Description</Label>
            <TextArea rows={6} />
          </TextField>
          <p role="status">{dirty ? 'Unsaved edits' : 'No unsaved edits'}</p>
          <ActionButton
            type="submit"
            variant="primary"
            isDisabled={!validText(buffer) || !dirty}
          >
            Save demo draft
          </ActionButton>
          <ActionButton onPress={onCancel}>Cancel editing</ActionButton>
        </form>
      )}
      {!readOnly && item.status === 'drafts' && (
        <div className={styles.actions}>
          {!buffer && (
            <ActionButton onPress={onEdit}>Edit demo draft</ActionButton>
          )}
          <ActionButton
            variant="primary"
            isDisabled={dirty || !fields.length}
            onPress={() => onAction('submit')}
          >
            Submit demo draft
          </ActionButton>
          <ActionButton onPress={() => onAction('discard')}>
            Discard demo draft
          </ActionButton>
        </div>
      )}
      {!readOnly && item.status === 'assigned' && (
        <div className={styles.actions}>
          <ActionButton variant="primary" onPress={() => onAction('approve')}>
            Approve demo changes
          </ActionButton>
          <ActionButton onPress={() => onAction('request-changes')}>
            Request demo changes
          </ActionButton>
        </div>
      )}
      {readOnly && (
        <p className={styles.note}>
          Read-only presentation example. Editing and review actions are
          unavailable.
        </p>
      )}
      {item.entryType === 'recovery' ? (
        <p className={styles.note}>
          This full authored snapshot was restored in the shared demo session.
          No review was performed, and nothing was published or marked ready.
        </p>
      ) : (
        item.status === 'completed' && (
          <p className={styles.note}>
            This review is complete. Its outcome does not publish the product or
            change its store status.
          </p>
        )
      )}
      <TechnicalDetails
        resourceType={item.resourceKind}
        sourcePath={item.path}
      />
      <p className={styles.muted}>
        Demo only. Your store and saved files are not changed.
      </p>
    </div>
  )
}
export function formatWorkTime(value: string) {
  return (
    new Intl.DateTimeFormat('en-GB', {
      dateStyle: 'medium',
      timeStyle: 'short',
      timeZone: 'UTC',
    }).format(new Date(value)) + ' UTC'
  )
}
