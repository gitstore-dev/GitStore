import { TagInput } from '../../../components/TagInput'
import { TextField, Label, Input } from 'react-aria-components'
import { ArrowUp, ArrowDown, Plus, X } from 'lucide-react'
import type { DemoSession } from '../../demo/model'
import { snapshotTitle } from '../../demo/snapshots'
import type { Product } from './model'
import { reorderMedia } from './model'
import { ActionButton } from '../../../components/controls'
import { ChoiceSelect } from '../../../components/ChoiceSelect'
import { ProductMedia } from './ProductMedia'
import styles from './products.module.css'
export function ProductFields({
  value,
  session,
  onChange,
}: {
  value: Product
  session: DemoSession
  onChange: (value: Product) => void
}) {
  const set = (spec: Partial<Product['spec']>) =>
    onChange({ ...value, spec: { ...value.spec, ...spec } })
  return (
    <>
      <section className={styles.section}>
        <div className={styles.sectionHeader}>
          <h2>Organization</h2>
          <span>Make it easy to find</span>
        </div>
        <div className={styles.fieldGrid}>
          <ChoiceSelect
            label="Category"
            value={value.spec.categoryId ?? 'none'}
            onChange={(categoryId) =>
              set({ categoryId: categoryId === 'none' ? null : categoryId })
            }
            options={[
              { id: 'none', label: 'No category' },
              ...Object.entries(session.catalog)
                .filter(([, p]) => p.kind === 'CategoryTaxonomy')
                .map(([id, p]) => ({ id, label: snapshotTitle(p) })),
            ]}
          />
          <ChoiceSelect
            label="Lifecycle"
            value={value.spec.lifecycle.state}
            onChange={(state) => set({ lifecycle: { state } })}
            options={[
              { id: 'ACTIVE', label: 'Active' },
              { id: 'RETIRED', label: 'Retired' },
            ]}
          />
        </div>
        <TagInput value={value.spec.tags} onChange={(tags) => set({ tags })} />
        <p className={styles.muted}>
          Retirement keeps this product in the catalog. It does not change any
          published release.
        </p>
      </section>
      <section className={styles.section}>
        <div className={styles.sectionHeader}>
          <h2>Product images</h2>
          <span>{value.spec.mediaIds.length} selected</span>
        </div>
        <p className={styles.muted}>
          Choose existing demo media. Use the arrows to change gallery order.
        </p>
        <ol className={styles.mediaList}>
          {value.spec.mediaIds.map((id, index) => (
            <li key={id}>
              <ProductMedia id={id} />
              <div>
                <strong>
                  {session.catalog[id]
                    ? snapshotTitle(session.catalog[id]!)
                    : 'Unavailable media'}
                </strong>
                <span>
                  {index === 0 ? 'Cover image' : `Image ${index + 1}`}
                </span>
              </div>
              <div className={styles.actions}>
                <ActionButton
                  aria-label={`Move image ${index + 1} earlier`}
                  isDisabled={index === 0}
                  onPress={() =>
                    set({
                      mediaIds: reorderMedia(value.spec.mediaIds, index, -1),
                    })
                  }
                >
                  <ArrowUp size={14} />
                </ActionButton>
                <ActionButton
                  aria-label={`Move image ${index + 1} later`}
                  isDisabled={index === value.spec.mediaIds.length - 1}
                  onPress={() =>
                    set({
                      mediaIds: reorderMedia(value.spec.mediaIds, index, 1),
                    })
                  }
                >
                  <ArrowDown size={14} />
                </ActionButton>
                <ActionButton
                  aria-label={`Remove image ${index + 1}`}
                  onPress={() =>
                    set({
                      mediaIds: value.spec.mediaIds.filter((v) => v !== id),
                    })
                  }
                >
                  <X size={14} />
                </ActionButton>
              </div>
            </li>
          ))}
        </ol>
        <div className={styles.mediaPicker}>
          {Object.entries(session.catalog)
            .filter(
              ([id, item]) =>
                item.kind === 'File' && !value.spec.mediaIds.includes(id),
            )
            .map(([id, item]) => (
              <ActionButton
                key={id}
                onPress={() => set({ mediaIds: [...value.spec.mediaIds, id] })}
              >
                <Plus size={14} />
                Add {snapshotTitle(item)}
              </ActionButton>
            ))}
        </div>
        {!Object.values(session.catalog).some(
          (item) => item.kind === 'File',
        ) && <p className={styles.muted}>No demo media in this store.</p>}
      </section>
      <section className={styles.section}>
        <div className={styles.sectionHeader}>
          <h2>Options</h2>
          <ActionButton
            variant="ghost"
            onPress={() =>
              set({
                options: [
                  ...value.spec.options,
                  { name: '', title: '', values: [] },
                ],
              })
            }
          >
            <Plus size={14} />
            Add option
          </ActionButton>
        </div>
        <p className={styles.muted}>
          Define choices such as size or color. This does not create or edit
          variants.
        </p>
        {value.spec.options.map((option, index) => {
          const change = (patch: Partial<typeof option>) =>
            set({
              options: value.spec.options.map((o, i) =>
                i === index ? { ...o, ...patch } : o,
              ),
            })
          return (
            <fieldset key={index} className={styles.option}>
              <legend>Option {index + 1}</legend>
              <TextField
                className={styles.field}
                value={option.name}
                onChange={(name) => change({ name })}
              >
                <Label>Option name</Label>
                <Input placeholder="size" />
              </TextField>
              <TextField
                className={styles.field}
                value={option.title}
                onChange={(title) => change({ title })}
              >
                <Label>Option Title</Label>
                <Input placeholder="Size" />
              </TextField>
              <TextField
                className={styles.field}
                value={option.values.join(', ')}
                onChange={(values) =>
                  change({
                    values: values.split(',').map((v) => v.trim()),
                  })
                }
              >
                <Label>Values, separated by commas</Label>
                <Input placeholder="S, M, L" />
              </TextField>
              <ActionButton
                onPress={() =>
                  set({
                    options: value.spec.options.filter((_, i) => i !== index),
                  })
                }
              >
                Remove option {index + 1}
              </ActionButton>
            </fieldset>
          )
        })}
      </section>
    </>
  )
}
