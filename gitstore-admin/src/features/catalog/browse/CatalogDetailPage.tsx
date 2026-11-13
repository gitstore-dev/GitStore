import { CategoryBreadcrumb } from './CategoryBreadcrumb'
import { useLayoutEffect, useMemo, useRef } from 'react'
import {
  Link,
  useLocation,
  useRouter,
  useSearch,
  useNavigate,
} from '@tanstack/react-router'
import { ArrowLeft, ArrowUpRight } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { snapshotTitle } from '../../demo/snapshots'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { usePageInspector } from '../../../shell/PageInspector'
import { HomePage, DemoControls, PageContext } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { TechnicalDetails } from '../../../components/TechnicalDetails'
import { MarkdownPreview } from '../../../components/markdown/MarkdownPreview'
import { StatusBadge } from '../../home/overview/StatusBadge'
import {
  catalogEntries,
  catalogLabels,
  matchingProducts,
  categoryHierarchy,
  categoryPath,
} from './model'
import type { CatalogDomain, CatalogSearch } from './model'
import { catalogPaths, catalogDetailPaths, catalogSearch } from './model'
import styles from './catalog.module.css'
export function CatalogDetailPage({
  namespace,
  domain,
  itemId,
}: {
  namespace: NamespaceId
  domain: CatalogDomain
  itemId: string
}) {
  const navigate = useNavigate()
  const { session, reset } = useHomeDemo(namespace)
  const router = useRouter(),
    location = useLocation()
  const search = catalogSearch(useSearch({ strict: false }), domain)
  const page = useRef<HTMLDivElement>(null)
  const motion = router.options.context.productNavigation
  const entry = useMemo(
    () => catalogEntries(session, domain).find((item) => item.id === itemId),
    [session, domain, itemId],
  )
  const snapshot = session.catalog[itemId]
  const variant = session.variants[itemId]
  const parent = variant ? session.catalog[variant.spec.productId] : undefined
  const parentProduct = parent?.kind === 'Product' ? parent : undefined
  const preview =
    snapshot?.kind === 'Collection'
      ? matchingProducts(session, snapshot.spec.selector)
      : []
  const observed = session.collectionMembers[itemId] ?? []
  const membershipChanged =
    JSON.stringify([...observed].sort()) !== JSON.stringify(preview)
  const hierarchy = categoryHierarchy(session)
  const problem = hierarchy.find((row) => row.item.id === itemId)?.problem
  const content = useMemo(
    () =>
      entry ? (
        <div className={styles.inspector}>
          <h2>{entry.title}</h2>
          <StatusBadge state={entry.status} />
          <p>{entry.resource.diagnostic}</p>
          <p>
            Read-only demo details. Catalog readiness and publication are
            separate.
          </p>
          <Link to="/w/$namespace/home/overview" params={{ namespace }}>
            Open Overview <ArrowUpRight size={13} />
          </Link>
        </div>
      ) : null,
    [entry, namespace],
  )
  usePageInspector(content)
  useLayoutEffect(() => {
    motion?.bind(page.current)
    motion?.mounted(location.href, session.epoch)
  })
  const unavailable =
    !entry || router.options.context.catalogScenario === 'unavailable'
  const products =
    domain === 'categories'
      ? Object.entries(session.catalog)
          .filter(
            ([id, item]) =>
              item.kind === 'Product' &&
              item.spec.categoryId === itemId &&
              !session.deleting.includes(id),
          )
          .map(([id]) => id)
      : preview
  function productLink(id: string) {
    const product = session.catalog[id]
    return product?.kind === 'Product' ? (
      <Link
        to="/w/$namespace/catalog/products/$productId"
        params={{ namespace, productId: id }}
      >
        {product.spec.title} <ArrowUpRight size={13} aria-hidden="true" />
      </Link>
    ) : (
      <span>
        Product unavailable <small>({id})</small>
      </span>
    )
  }
  return (
    <HomePage ref={page}>
      <div className={styles.top}>
        <Link
          to={catalogPaths[domain]}
          params={{ namespace }}
          search={
            motion?.returnSearch<CatalogSearch>(location.pathname) ?? search
          }
          resetScroll={false}
        >
          <ArrowLeft size={14} aria-hidden="true" /> {catalogLabels[domain]}
        </Link>
        <DemoControls onReset={reset} />
      </div>
      {unavailable ? (
        <EmptyState
          title={`${domain === 'variants' ? 'Variant' : domain === 'categories' ? 'Category' : 'Collection'} unavailable`}
        >
          This item is not available in the selected demo store. Follow the link
          above to browse its catalog.
        </EmptyState>
      ) : (
        <article
          className={styles.detail}
          data-product-surface={itemId}
          data-product-epoch={session.epoch}
        >
          <div className={styles.detailContent} data-product-content="detail">
            {domain === 'categories' ? (
              <CategoryBreadcrumb
                incomplete={!!problem}
                items={categoryPath(session, itemId).map((id) => ({
                  id,
                  title: snapshotTitle(session.catalog[id]!),
                  link: (
                    <Link
                      to={catalogDetailPaths.categories}
                      params={{ namespace, itemId: id }}
                      title={snapshotTitle(session.catalog[id]!)}
                    >
                      {snapshotTitle(session.catalog[id]!)}
                    </Link>
                  ),
                  onSelect: () => {
                    void navigate({
                      to: catalogDetailPaths.categories,
                      params: { namespace, itemId: id },
                    })
                  },
                }))}
              />
            ) : (
              <PageContext
                namespace={namespaces.find((n) => n.id === namespace)!.name}
                section={domain === 'variants' ? 'Variant' : 'Collection'}
              />
            )}
            <h1 tabIndex={-1} data-product-title="">
              {entry.title}
            </h1>
            <StatusBadge state={entry.status} />
            <p className={styles.note}>{entry.resource.diagnostic}</p>
            <MarkdownPreview source={entry.body} />
            <p className={styles.note}>
              Read-only demo. Changes can be explored through Products and Home;
              this page does not edit or publish catalog data.
            </p>
            {domain === 'variants' && variant && (
              <>
                <section>
                  <h2>Product and options</h2>
                  <dl>
                    <dt>Product</dt>
                    <dd>{productLink(variant.spec.productId)}</dd>
                    <dt>SKU</dt>
                    <dd>{variant.spec.sku}</dd>
                    {variant.spec.selectedOptions.map((option) => (
                      <div key={option.name} className={styles.definitionRow}>
                        <dt>
                          {parentProduct?.spec.options.find(
                            (item) => item.name === option.name,
                          )?.title ?? option.name}
                        </dt>
                        <dd>{option.value}</dd>
                      </div>
                    ))}
                  </dl>
                  {!variant.spec.selectedOptions.length && (
                    <p className={styles.note}>
                      No selected options in this demo.
                    </p>
                  )}
                </section>
                <section>
                  <h2>Pricing declaration</h2>
                  {variant.spec.pricing ? (
                    <dl>
                      <dt>Currency</dt>
                      <dd>{variant.spec.pricing.currencyCode}</dd>
                      <dt>Declared amount</dt>
                      <dd>{variant.spec.pricing.amount}</dd>
                    </dl>
                  ) : (
                    <p>No pricing declaration.</p>
                  )}
                  <p className={styles.note}>
                    Authored demo values. These are not evaluated checkout
                    prices.
                  </p>
                </section>
                <section>
                  <h2>Inventory declaration</h2>
                  <dl>
                    <dt>Track inventory</dt>
                    <dd>
                      {variant.spec.inventory.managed ? 'Managed' : 'Unmanaged'}
                    </dd>
                    <dt>Policy</dt>
                    <dd>
                      {variant.spec.inventory.policy === 'deny'
                        ? 'Deny when unavailable'
                        : 'Allow backorders'}
                    </dd>
                  </dl>
                  <p className={styles.note}>
                    No stock balances, availability checks, or fulfillment
                    actions are implemented.
                  </p>
                </section>
                <section>
                  <h2>Variant media</h2>
                  <p className={styles.note}>
                    No variant-specific media in this fixture. Product images
                    remain on the parent Product.
                  </p>
                </section>
              </>
            )}
            {snapshot?.kind === 'CategoryTaxonomy' && (
              <>
                <section>
                  <h2>Category relationships</h2>
                  {problem && (
                    <p className={styles.warning}>
                      {problem}. The category remains inspectable; no
                      relationship has been changed.
                    </p>
                  )}
                  <dl>
                    <dt>Parent category</dt>
                    <dd>
                      {snapshot.spec.parentId ? (
                        session.catalog[snapshot.spec.parentId]?.kind ===
                        'CategoryTaxonomy' ? (
                          <Link
                            to={catalogDetailPaths.categories}
                            params={{
                              namespace,
                              itemId: snapshot.spec.parentId,
                            }}
                          >
                            {snapshotTitle(
                              session.catalog[snapshot.spec.parentId]!,
                            )}
                          </Link>
                        ) : (
                          'Parent category unavailable'
                        )
                      ) : (
                        'Top-level category'
                      )}
                    </dd>
                  </dl>
                  <h3>Child categories</h3>
                  {catalogEntries(session, 'categories').some((item) => {
                    const v = session.catalog[item.id]
                    return (
                      v?.kind === 'CategoryTaxonomy' &&
                      v.spec.parentId === itemId
                    )
                  }) ? (
                    <ul>
                      {catalogEntries(session, 'categories')
                        .filter((item) => {
                          const v = session.catalog[item.id]
                          return (
                            v?.kind === 'CategoryTaxonomy' &&
                            v.spec.parentId === itemId
                          )
                        })
                        .map((item) => (
                          <li key={item.id}>
                            <Link
                              to={catalogDetailPaths.categories}
                              params={{ namespace, itemId: item.id }}
                            >
                              {item.title}
                            </Link>
                          </li>
                        ))}
                    </ul>
                  ) : (
                    <p className={styles.note}>No child categories.</p>
                  )}
                </section>
                <section>
                  <h2>Products in this category</h2>
                  <p className={styles.note}>
                    Direct assignments only. Descendant categories are listed
                    separately.
                  </p>
                  {products.length ? (
                    <ul className={styles.members}>
                      {products.map((id) => (
                        <li key={id}>{productLink(id)}</li>
                      ))}
                    </ul>
                  ) : (
                    <p>No directly assigned products.</p>
                  )}
                  <Link
                    to="/w/$namespace/catalog/products"
                    params={{ namespace }}
                    search={{ category: itemId }}
                  >
                    Browse products in this category
                  </Link>
                </section>
              </>
            )}
            {snapshot?.kind === 'Collection' && (
              <>
                <section>
                  <h2>Product rules</h2>
                  <p className={styles.note}>
                    All rules must match product labels. Tags are separate from
                    labels.
                  </p>
                  <ul className={styles.rules}>
                    {Object.entries(
                      snapshot.spec.selector?.matchLabels ?? {},
                    ).map(([key, value]) => (
                      <li key={key}>
                        <code>{key}</code> equals <code>{value}</code>
                      </li>
                    ))}
                    {snapshot.spec.selector?.matchExpressions?.map(
                      (rule, index) => (
                        <li key={index}>
                          <code>{rule.key}</code>{' '}
                          {
                            (
                              {
                                In: 'is one of',
                                NotIn: 'is not one of',
                                Exists: 'is present',
                                DoesNotExist: 'is absent',
                              } as const
                            )[rule.operator]
                          }{' '}
                          {rule.values?.join(', ')}
                        </li>
                      ),
                    )}
                  </ul>
                  {!Object.keys(snapshot.spec.selector?.matchLabels ?? {})
                    .length &&
                    !snapshot.spec.selector?.matchExpressions?.length && (
                      <p>
                        No product rules. An empty selector matches no products.
                      </p>
                    )}
                </section>
                <section>
                  <h2>Demo membership preview</h2>
                  <p className={styles.note}>
                    Computed from the current saved demo Products and labels,
                    excluding deleting Products. This preview is not observed
                    backend membership or publication.
                  </p>
                  {membershipChanged && (
                    <p className={styles.warning}>
                      The saved demo snapshot differs from this preview. Viewing
                      it does not update processing status.
                    </p>
                  )}
                  {preview.length ? (
                    <ul className={styles.members}>
                      {preview.map((id) => (
                        <li key={id}>{productLink(id)}</li>
                      ))}
                    </ul>
                  ) : (
                    <p>No products match these rules.</p>
                  )}
                </section>
                <section>
                  <h2>Recorded demo membership</h2>
                  <p className={styles.note}>
                    Fixed fixture membership, refreshed only by the existing
                    demo processing action in Overview.
                  </p>
                  {observed.length ? (
                    <ul>
                      {observed.map((id) => (
                        <li key={id}>{productLink(id)}</li>
                      ))}
                    </ul>
                  ) : (
                    <p>No products recorded.</p>
                  )}
                </section>
              </>
            )}
            <TechnicalDetails
              resourceType={entry.kind}
              sourcePath={entry.resource.path}
            >
              <dl>
                <dt>Identifier</dt>
                <dd>{itemId}</dd>
              </dl>
              {snapshot?.kind === 'Collection' && (
                <pre className={styles.code}>
                  {JSON.stringify(snapshot.spec.selector ?? {}, null, 2)}
                </pre>
              )}
            </TechnicalDetails>
          </div>
        </article>
      )}
    </HomePage>
  )
}
