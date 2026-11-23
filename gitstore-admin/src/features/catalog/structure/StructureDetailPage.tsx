import { useLayoutEffect, useMemo, useRef } from 'react'
import { Link, useLocation, useRouter, useSearch } from '@tanstack/react-router'
import { ArrowLeft } from 'lucide-react'
import type { NamespaceId } from '../../../data/demo'
import { namespaces } from '../../../data/demo'
import { useHomeDemo } from '../../demo/HomeDemoProvider'
import { HomePage, PageContext, DemoControls } from '../../home/shared/HomePage'
import { EmptyState } from '../../home/shared/EmptyState'
import { MarkdownPreview } from '../../../components/markdown/MarkdownPreview'
import { TechnicalDetails } from '../../../components/TechnicalDetails'
import { usePageInspector } from '../../../shell/PageInspector'
import {
  structureRoute,
  structureEntryForPath,
  structureReturnSearch,
  structurePaths,
  structureEntries,
  productTitle,
  optionSelections,
} from './model'
import type { StructureSearch } from './model'
import { StructureLink } from './StructureLink'
import { DesignNotice } from './DesignNotice'
import catalog from '../browse/catalog.module.css'
import styles from './structure.module.css'
export function StructureDetailPage({ namespace }: { namespace: NamespaceId }) {
  const { session, reset } = useHomeDemo(namespace),
    router = useRouter(),
    location = useLocation(),
    rawSearch = useSearch({ strict: false })
  const scenario = router.options.context.structureScenario,
    route = structureRoute(location.pathname),
    domain = route?.domain ?? 'attributes-options'
  const entry = useMemo(
    () => structureEntryForPath(session, location.pathname, scenario),
    [session, location.pathname, scenario],
  )
  const motion = router.options.context.productNavigation,
    page = useRef<HTMLDivElement>(null)
  const search =
    motion?.returnSearch<StructureSearch>(location.pathname) ??
    structureReturnSearch(rawSearch, domain, location.pathname)
  const rootLabel =
    domain === 'product-types' ? 'Product types' : 'Attributes & options'
  const definitionExamples = structureEntries(session, 'definitions', scenario)
  const types = structureEntries(session, 'types', scenario)
  const selections =
    entry?.source === 'option' ? optionSelections(session, entry) : []
  const content = useMemo(
    () =>
      entry ? (
        <div className={catalog.inspector}>
          <h2>{entry.title}</h2>
          <p>
            {entry.source === 'option'
              ? 'An option saved on its Product. Values and Variant selections below are from this demo catalog.'
              : 'A read-only design example. No Product assignments or rules are applied.'}
          </p>
          <div className={styles.values}>
            {entry.values.map((value, index) => (
              <span key={index}>{value}</span>
            ))}
          </div>
        </div>
      ) : null,
    [entry],
  )
  usePageInspector(content)
  useLayoutEffect(() => {
    motion?.bind(page.current)
    motion?.mounted(location.href, session.epoch)
  })
  const sourcePath =
    entry?.source === 'option'
      ? session.resources.find((r) => r.id === entry.productId)?.path
      : undefined
  return (
    <HomePage ref={page}>
      <div className={catalog.top}>
        <Link
          to={structurePaths[domain]}
          params={{ namespace }}
          search={search}
          resetScroll={false}
        >
          <ArrowLeft size={15} aria-hidden="true" />
          {rootLabel}
        </Link>
        <DemoControls
          onReset={reset}
          details="Product options are saved on Products. Other records on these pages are read-only design examples, not configured catalog resources."
        />
      </div>
      {!entry || scenario === 'unavailable' || scenario === 'loading' ? (
        <EmptyState
          title={
            scenario === 'loading'
              ? 'Loading catalog details'
              : domain === 'product-types'
                ? 'Product type example unavailable'
                : 'Option or definition unavailable'
          }
        >
          This entry is not available in the selected store. It may have been
          removed or renamed. Return to the list to continue browsing.
        </EmptyState>
      ) : (
        <article className={catalog.detail} data-product-surface={entry.id}>
          <div className={catalog.detailContent} data-product-content="detail">
            <PageContext
              namespace={namespaces.find((n) => n.id === namespace)!.name}
              section={
                entry.source === 'option'
                  ? 'Product option'
                  : entry.source === 'definition'
                    ? 'Reusable definition'
                    : 'Product type'
              }
            />
            <h1 tabIndex={-1} data-product-title="">
              {entry.title}
            </h1>
            {entry.source !== 'option' && (
              <DesignNotice type={entry.source === 'type'} />
            )}
            {entry.source === 'option' ? (
              <>
                <p className={catalog.note}>
                  Saved on{' '}
                  <Link
                    to="/w/$namespace/catalog/products/$productId"
                    params={{ namespace, productId: entry.productId }}
                  >
                    {productTitle(session, entry.productId)}
                  </Link>
                  .{' '}
                  {session.deleting.includes(entry.productId)
                    ? 'This Product is being deleted.'
                    : 'Edit this option from the Product editor.'}
                </p>
                <section>
                  <h2>Offered values</h2>
                  {entry.values.length ? (
                    <div className={styles.values}>
                      {entry.values.map((value, index) => (
                        <span key={index}>{value}</span>
                      ))}
                    </div>
                  ) : (
                    <p>No values offered.</p>
                  )}
                </section>
                <section>
                  <h2>Variant selections</h2>
                  {selections.length ? (
                    <ul className={styles.related}>
                      {selections.map((item) => (
                        <li key={item.id}>
                          <Link
                            to="/w/$namespace/catalog/variants/$itemId"
                            params={{ namespace, itemId: item.id }}
                          >
                            {item.title}
                          </Link>
                          <small>
                            {item.value ?? 'No value selected'}
                            {item.value !== undefined && !item.offered
                              ? ' — This value is not offered by the Product.'
                              : ''}
                          </small>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p className={catalog.note}>
                      No Variants for this Product in the demo.
                    </p>
                  )}
                </section>
                {sourcePath ? (
                  <TechnicalDetails
                    resourceType="Product"
                    sourcePath={sourcePath}
                  >
                    <dl>
                      <dt>Option key</dt>
                      <dd>{entry.optionName}</dd>
                      <dt>Product last updated</dt>
                      <dd>{entry.updatedAt}</dd>
                    </dl>
                  </TechnicalDetails>
                ) : (
                  <p className={catalog.note}>
                    Product source path unavailable.
                  </p>
                )}
              </>
            ) : (
              <>
                <MarkdownPreview source={entry.body} />
                {entry.source === 'definition' ? (
                  <>
                    <section>
                      <h2>Example choices</h2>
                      <div className={styles.values}>
                        {entry.values.map((value, index) => (
                          <span key={index}>{value}</span>
                        ))}
                      </div>
                    </section>
                    <section>
                      <h2>Used in type examples</h2>
                      <ul className={styles.related}>
                        {types
                          .filter(
                            (type) =>
                              type.source === 'type' &&
                              type.definitionIds.includes(entry.exampleId),
                          )
                          .map((type) => (
                            <li key={type.id}>
                              <StructureLink entry={type} namespace={namespace}>
                                {type.title}
                              </StructureLink>
                            </li>
                          ))}
                      </ul>
                      <p className={catalog.note}>
                        Existing Products do not consume this definition.
                      </p>
                    </section>
                  </>
                ) : (
                  <>
                    <section>
                      <h2>Definitions in this example</h2>
                      <ul className={styles.related}>
                        {entry.definitionIds.map((id) => {
                          const definition = definitionExamples.find(
                            (e) =>
                              e.source === 'definition' && e.exampleId === id,
                          )
                          return (
                            <li key={id}>
                              {definition ? (
                                <>
                                  <StructureLink
                                    entry={definition}
                                    namespace={namespace}
                                  >
                                    {definition.title}
                                  </StructureLink>
                                  <div className={styles.values}>
                                    {definition.values.map((value, index) => (
                                      <span key={index}>{value}</span>
                                    ))}
                                  </div>
                                </>
                              ) : (
                                <span>Definition example unavailable</span>
                              )}
                            </li>
                          )
                        })}
                      </ul>
                    </section>
                    <section>
                      <h2>Example Products</h2>
                      <p className={catalog.note}>
                        These links illustrate the idea. No type is assigned to
                        these Products.
                      </p>
                      <ul className={styles.related}>
                        {entry.productIds.map((id) => (
                          <li key={id}>
                            {productTitle(session, id) ? (
                              <Link
                                to="/w/$namespace/catalog/products/$productId"
                                params={{ namespace, productId: id }}
                              >
                                {productTitle(session, id)}
                              </Link>
                            ) : (
                              <span>Example Product unavailable</span>
                            )}
                          </li>
                        ))}
                      </ul>
                    </section>
                  </>
                )}
                <details className={styles.designDetails}>
                  <summary>Design details</summary>
                  <p>
                    Local design example. No manifest or backend resource exists
                    for this record.
                  </p>
                  <code>Example ID: {entry.exampleId}</code>
                </details>
              </>
            )}
          </div>
        </article>
      )}
    </HomePage>
  )
}
