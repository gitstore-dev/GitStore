import { useLayoutEffect, useRef, useState } from 'react'
import {
  Link,
  useNavigate,
  useRouter,
  useLocation,
} from '@tanstack/react-router'
import {
  TextField,
  Label,
  Input,
  FieldError,
  Heading,
} from 'react-aria-components'
import { ArrowLeft, ArrowUpRight, Pencil, Save } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { sameSnapshot, snapshotTitle } from '../../demo/snapshots'
import { ActionButton } from '../../../components/controls'
import { TechnicalDetails } from '../../../components/TechnicalDetails'
import { ConfirmationDialog } from '../../../components/ConfirmationDialog'
import { MarkdownEditor } from '../../../components/markdown/MarkdownEditor'
import { MarkdownPreview } from '../../../components/markdown/MarkdownPreview'
import { PageContext, DemoControls } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { StatusBadge } from '../../home/overview/StatusBadge'
import {
  productAt,
  emptyProduct,
  productProblem,
  nextProductId,
  staleBuffer,
  deletionBlockers,
} from './model'
import type { ProductBuffer } from './model'
import { ProductFields } from './ProductFields'
import { ProductMedia } from './ProductMedia'
import styles from './products.module.css'
type Confirmation = 'cancel' | 'reload' | 'delete' | 'complete-delete'
export function ProductPage({
  namespace,
  productId,
  editing = false,
}: {
  namespace: NamespaceId
  productId?: string
  editing?: boolean
}) {
  const { session, dispatch, reset, announce } = useHomeDemo(namespace)
  const navigate = useNavigate()
  const motion = useRouter().options.context?.productNavigation
  const location = useLocation()
  const canvas = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    motion?.bind(canvas.current)
    motion?.mounted(location.href, session.epoch)
  })
  const key = productId ?? 'new'
  const current = productId ? productAt(session, productId) : undefined
  const [fallback, setFallback] = useState<ProductBuffer>({
    value: current ?? emptyProduct(),
    base: current ?? null,
  })
  const buffer = session.productBuffers[key] ?? fallback
  const dirty = buffer.base
    ? !sameSnapshot(buffer.value, buffer.base)
    : !sameSnapshot(buffer.value, emptyProduct())
  const stale = !!productId && staleBuffer(session, productId, buffer)
  const deleting = !!productId && session.deleting.includes(productId)
  const resource = session.resources.find((r) => r.id === productId)
  const blockers = productId ? deletionBlockers(session, productId) : []
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null)
  const [error, setError] = useState('')
  const heading = useRef<HTMLHeadingElement>(null)
  const editTitle = productId ? 'Edit product' : 'New product'
  function leave() {
    if (productId)
      void navigate({
        to: '/w/$namespace/catalog/products/$productId',
        params: { namespace, productId },
      })
    else
      void navigate({
        to: '/w/$namespace/catalog/products',
        params: { namespace },
      })
  }
  function save() {
    const problem = productProblem(buffer.value, session)
    if (problem || stale || deleting) {
      setError(
        problem ??
          'This product changed or is being deleted. Reload its saved version before editing.',
      )
      return
    }
    const id = productId ?? nextProductId(session, buffer.value.spec.title)
    dispatch({
      type: 'product-save',
      id,
      value: buffer.value,
      base: buffer.base,
    })
    announce(
      `${productId ? 'Product changes saved' : 'Product created'} in demo. Processing; nothing was published.`,
    )
    void navigate({
      to: '/w/$namespace/catalog/products/$productId',
      params: { namespace, productId: id },
    })
  }
  function confirm() {
    if (confirmation === 'cancel') {
      dispatch({ type: 'product-buffer', id: key, buffer: null })
      announce('Unsaved Product edits discarded in this demo.')
      leave()
    }
    if (confirmation === 'reload' && current) {
      setFallback({ base: current, value: current })
      dispatch({
        type: 'product-buffer',
        id: key,
        buffer: { base: current, value: current },
      })
      setError('')
      announce('Saved product reloaded. Unsaved Product edits discarded.')
    }
    if (confirmation === 'delete' && productId) {
      dispatch({ type: 'product-delete', id: productId })
      announce(
        'Product deletion started in demo. Open the product to complete deletion.',
      )
    }
    if (confirmation === 'complete-delete' && productId) {
      dispatch({ type: 'product-complete-delete', id: productId })
      announce('Demo product deleted. History and drafts preserved.')
      leave()
    }
    setConfirmation(null)
    requestAnimationFrame(() => heading.current?.focus())
  }
  const missing = !!productId && !current
  return (
    <div
      className={styles.canvasPage}
      ref={canvas}
      data-product-epoch={session.epoch}
    >
      <div className={styles.canvasTop}>
        <Link
          className={styles.back}
          search={motion?.returnSearch(location.pathname)}
          resetScroll={false}
          to="/w/$namespace/catalog/products"
          params={{ namespace }}
        >
          <ArrowLeft size={15} />
          Products
        </Link>
        <DemoControls onReset={reset} />
      </div>
      {editing && (
        <div className={styles.pageActions}>
          <div className={styles.actions}>
            {editing && !missing && !deleting ? (
              <>
                <ActionButton
                  onPress={() => (dirty ? setConfirmation('cancel') : leave())}
                >
                  Cancel
                </ActionButton>
                <ActionButton
                  variant="primary"
                  onPress={save}
                  isDisabled={stale || (productId !== undefined && !dirty)}
                >
                  <Save size={14} />
                  {productId ? 'Save demo changes' : 'Create demo product'}
                </ActionButton>
              </>
            ) : null}
          </div>
        </div>
      )}
      {missing ? (
        <EmptyState
          title="Product unavailable"
          actions={
            <Link to="/w/$namespace/catalog/products" params={{ namespace }}>
              Back to Products
            </Link>
          }
        >
          This product is no longer in the demo catalog. Its history and
          personal drafts remain available.
        </EmptyState>
      ) : (
        <article
          className={styles.document}
          data-product-surface={!editing ? productId : undefined}
          data-product-epoch={session.epoch}
        >
          <div data-product-content="detail">
            {!editing && current && !deleting && (
              <Link
                className={styles.editIcon}
                aria-label="Edit product"
                title="Edit product"
                to="/w/$namespace/catalog/products/$productId/edit"
                params={{ namespace, productId: productId! }}
              >
                <Pencil size={17} aria-hidden="true" />
              </Link>
            )}
            <PageContext
              namespace={namespaces.find((n) => n.id === namespace)!.name}
              section={editing ? editTitle : 'Product'}
            />
            {editing && !deleting ? (
              <>
                <h1 className="sr-only" ref={heading} tabIndex={-1}>
                  {editTitle}
                </h1>
                <TextField
                  value={buffer.value.spec.title}
                  onChange={(title) => {
                    setError('')
                    dispatch({
                      type: 'product-buffer',
                      id: key,
                      buffer: {
                        ...buffer,
                        value: {
                          ...buffer.value,
                          spec: { ...buffer.value.spec, title },
                        },
                      },
                    })
                  }}
                  isRequired
                  isInvalid={!!error && !buffer.value.spec.title.trim()}
                  className={styles.titleField}
                >
                  <Label>Title</Label>
                  <Input placeholder="Untitled product" maxLength={200} />
                  <FieldError>Enter a Title.</FieldError>
                </TextField>
                <p className={styles.muted}>
                  {dirty ? 'Unsaved demo edits' : 'Saved demo version'} · Kept
                  in this tab until reload or reset.
                </p>
                {stale && (
                  <div role="alert" className={styles.notice}>
                    <strong>The saved Product has changed.</strong>
                    <p>
                      Your edits are preserved. Reload the current saved version
                      before saving again.
                    </p>
                    <ActionButton onPress={() => setConfirmation('reload')}>
                      Reload saved product
                    </ActionButton>
                  </div>
                )}
                {error && (
                  <p role="alert" className={styles.notice}>
                    {error}
                  </p>
                )}
                <MarkdownEditor
                  key={`${key}:${stale ? 'stale' : 'current'}`}
                  value={buffer.value.body}
                  onChange={(body) =>
                    dispatch({
                      type: 'product-buffer',
                      id: key,
                      buffer: { ...buffer, value: { ...buffer.value, body } },
                    })
                  }
                />
                <ProductFields
                  key={`${key}:${JSON.stringify(buffer.base)}`}
                  value={buffer.value}
                  session={session}
                  onChange={(value) =>
                    dispatch({
                      type: 'product-buffer',
                      id: key,
                      buffer: { ...buffer, value },
                    })
                  }
                />
              </>
            ) : (
              current && (
                <>
                  <div className={styles.titleLine}>
                    <h1 ref={heading} tabIndex={-1} data-product-title="">
                      {current.spec.title}
                    </h1>
                    <span className={styles.badge}>
                      {deleting
                        ? 'Deleting'
                        : current.spec.lifecycle.state === 'ACTIVE'
                          ? 'Lifecycle · Active'
                          : 'Lifecycle · Retired'}
                    </span>
                    {!deleting && (
                      <span>
                        Checks ·{' '}
                        <StatusBadge
                          state={resource?.readiness ?? 'reconciling'}
                        />
                      </span>
                    )}
                  </div>
                  <p className={styles.muted}>{resource?.diagnostic}</p>
                  {deleting && (
                    <section className={styles.notice}>
                      <h2>Deletion in progress</h2>
                      <p>
                        Editing is unavailable. Completing this demo action
                        checks dependent variants again before removing this
                        product.
                      </p>
                      {blockers.length > 0 && (
                        <p>
                          Deletion is blocked by {blockers.length} dependent
                          variants.
                        </p>
                      )}
                      <ActionButton
                        variant="primary"
                        isDisabled={blockers.length > 0}
                        onPress={() => setConfirmation('complete-delete')}
                      >
                        Complete demo deletion
                      </ActionButton>
                    </section>
                  )}
                  <MarkdownPreview source={current.body} />
                  <section className={styles.section}>
                    <div className={styles.sectionHeader}>
                      <h2>Product images</h2>
                      <span>{current.spec.mediaIds.length} images</span>
                    </div>
                    <div className={styles.gallery}>
                      {current.spec.mediaIds.length ? (
                        current.spec.mediaIds.map((id, index) => (
                          <div
                            key={id}
                            data-product-image={index === 0 ? '' : undefined}
                          >
                            <ProductMedia id={id} priority={index === 0} />
                          </div>
                        ))
                      ) : (
                        <div data-product-image="">
                          <ProductMedia />
                        </div>
                      )}
                    </div>
                  </section>
                  <section className={styles.section}>
                    <div className={styles.sectionHeader}>
                      <h2>Organization</h2>
                    </div>
                    <dl className={styles.metadata}>
                      <dt>Category</dt>
                      <dd>
                        {current.spec.categoryId &&
                        session.catalog[current.spec.categoryId] ? (
                          <Link
                            to="/w/$namespace/catalog/categories/$itemId"
                            params={{
                              namespace,
                              itemId: current.spec.categoryId,
                            }}
                          >
                            {snapshotTitle(
                              session.catalog[current.spec.categoryId]!,
                            )}
                          </Link>
                        ) : (
                          'No category'
                        )}
                      </dd>
                      <dt>Tags</dt>
                      <dd>{current.spec.tags.join(', ') || 'No tags'}</dd>
                      {current.spec.options.map((option) => (
                        <div key={option.name}>
                          <dt>{option.title || option.name}</dt>
                          <dd>{option.values.join(' · ')}</dd>
                        </div>
                      ))}
                    </dl>
                  </section>
                </>
              )
            )}
            {current && (
              <>
                <section className={styles.section}>
                  <div className={styles.sectionHeader}>
                    <h2>Variants</h2>
                    <span>{blockers.length} linked · Read only</span>
                  </div>
                  {blockers.length ? (
                    <ul className={styles.variants}>
                      {blockers.map((variant) => (
                        <li key={variant.id}>
                          <strong>
                            <Link
                              to="/w/$namespace/catalog/variants/$itemId"
                              params={{ namespace, itemId: variant.id }}
                            >
                              {'spec' in variant
                                ? variant.spec.title
                                : variant.name}
                            </Link>
                          </strong>
                          <StatusBadge state={variant.readiness} />
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p className={styles.muted}>No variants in this demo.</p>
                  )}
                </section>
                <TechnicalDetails
                  resourceType="Product"
                  sourcePath={
                    resource?.path ?? `catalog/products/${productId}.md`
                  }
                >
                  <dl className={styles.metadata}>
                    <dt>Identity</dt>
                    <dd>
                      <code>{productId}</code>
                    </dd>
                    <dt>Namespace</dt>
                    <dd>{namespace}</dd>
                    <dt>API version</dt>
                    <dd>catalog.gitstore.dev/v1beta1</dd>
                    <dt>Labels / annotations</dt>
                    <dd>None in this demo</dd>
                  </dl>
                </TechnicalDetails>
              </>
            )}
            {!editing && current && !deleting && (
              <section className={styles.dangerZone}>
                <h2>Delete product</h2>
                {blockers.length ? (
                  <>
                    <p>
                      This product has {blockers.length} dependent variants and
                      cannot be deleted. Review the variants above, or retire
                      the Product to keep it in your catalog.
                    </p>
                    <Link
                      to="/w/$namespace/catalog/products/$productId/edit"
                      params={{ namespace, productId: productId! }}
                    >
                      Edit lifecycle <ArrowUpRight size={13} />
                    </Link>
                  </>
                ) : (
                  <p>
                    Start deletion in this demo. You will complete it as a
                    separate step.
                  </p>
                )}
                <ActionButton
                  isDisabled={blockers.length > 0}
                  onPress={() => setConfirmation('delete')}
                >
                  Delete demo product
                </ActionButton>
              </section>
            )}
            <p className={styles.footnote}>
              Demo data · Your store and saved files are unchanged. Saving,
              retiring, and deleting do not publish a release.
            </p>
          </div>
        </article>
      )}
      <ConfirmationDialog
        isOpen={confirmation !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmation(null)
        }}
      >
        <Heading slot="title">
          {confirmation === 'cancel'
            ? 'Discard unsaved Product edits?'
            : confirmation === 'reload'
              ? 'Reload saved Product?'
              : confirmation === 'delete'
                ? 'Start demo deletion?'
                : 'Complete demo deletion?'}
        </Heading>
        <p>
          {confirmation === 'cancel' || confirmation === 'reload'
            ? 'Your unsaved Product edits will be discarded. Personal drafts in My work are preserved.'
            : confirmation === 'delete'
              ? 'The Product will enter Deleting. It remains visible until you explicitly complete the demo deletion. No variants are removed.'
              : 'Remove the Product from the demo catalog after a fresh dependency check. Activity and personal drafts remain available.'}
        </p>
        <div className={styles.actions}>
          <ActionButton autoFocus onPress={() => setConfirmation(null)}>
            Cancel
          </ActionButton>
          <ActionButton variant="primary" onPress={confirm}>
            {confirmation === 'cancel'
              ? 'Discard edits'
              : confirmation === 'reload'
                ? 'Confirm reload'
                : confirmation === 'delete'
                  ? 'Confirm demo deletion'
                  : 'Confirm completion'}
          </ActionButton>
        </div>
      </ConfirmationDialog>
    </div>
  )
}
