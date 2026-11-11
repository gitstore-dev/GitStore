import type { DemoSession } from '../../demo/model'
import type { NamespaceId } from '../../../data/demo'

export type SetupReviewId = 'store-details' | 'categories' | 'catalog-checks'
export type SetupStepId = SetupReviewId | 'first-product'
export type SetupReviews = Readonly<Record<SetupReviewId, boolean>>
export type SetupScenario =
  'partial' | 'complete' | 'reopened' | 'loading' | 'unavailable' | 'read-only'
export const setupDefinitions = [
  {
    id: 'store-details',
    title: 'Review store details',
    description:
      'Know which store you are working on and what details to prepare.',
    instructions: [
      'Check the selected store before making changes.',
      'Prepare your business contact details, currency, and location for the future Store details page.',
    ],
    destination: 'settings.store-details',
    linkLabel: 'Open store details',
    planned: true,
  },
  {
    id: 'categories',
    title: 'Review categories',
    description: 'Plan how shoppers will find their way through your catalog.',
    instructions: [
      'Consider the categories that will help shoppers browse your products.',
      'The Product editor can choose an existing demo category. Category editing is not implemented yet.',
    ],
    destination: 'catalog.categories',
    linkLabel: 'Open categories',
    planned: true,
  },
  {
    id: 'first-product',
    title: 'Add your first product',
    description:
      'Save a product with a Title and description in the demo catalog.',
    instructions: [
      'Use the existing Product editor to save your first demo product.',
      'A saved product counts while it is not deleting, even if it is retired or still processing. This does not mean it is ready or published.',
    ],
    destination: 'catalog.products',
    linkLabel: 'View products',
    planned: false,
  },
  {
    id: 'catalog-checks',
    title: 'Review catalog checks',
    description:
      'Learn where to review updates and issues before taking your next step.',
    instructions: [
      'Open Overview to see updates and issues for your store.',
      'Reviewing this guidance does not fix issues or change product readiness. You can return to the checks as your catalog grows.',
    ],
    destination: 'home.overview',
    linkLabel: 'Open Overview',
    planned: false,
  },
] as const
export function setupSearch(raw: Record<string, unknown>): {
  step?: SetupStepId
} {
  const definition = setupDefinitions.find((step) => step.id === raw.step)
  return definition ? { step: definition.id } : {}
}
export function initialSetupReviews(
  namespace: NamespaceId,
  scenario?: SetupScenario,
): SetupReviews {
  if (scenario === 'complete')
    return { 'store-details': true, categories: true, 'catalog-checks': true }
  if (scenario === 'partial')
    return { 'store-details': true, categories: false, 'catalog-checks': false }
  if (scenario === 'reopened')
    return { 'store-details': true, categories: false, 'catalog-checks': true }
  const reviewed = namespace === 'atelier-kyoto'
  return {
    'store-details': reviewed,
    categories: reviewed,
    'catalog-checks': false,
  }
}
export function setupProgress(
  session: Pick<DemoSession, 'catalog' | 'deleting' | 'setupReviews'>,
) {
  const products = Object.entries(session.catalog).filter(
    ([, item]) => item.kind === 'Product',
  )
  const productCount = products.filter(
    ([id]) => !session.deleting.includes(id),
  ).length
  const deletingProductCount = products.length - productCount
  const steps = setupDefinitions.map((step) => ({
    ...step,
    complete:
      step.id === 'first-product'
        ? productCount > 0
        : session.setupReviews[step.id],
  }))
  const completed = steps.filter((step) => step.complete).length
  return {
    steps,
    completed,
    total: steps.length,
    productCount,
    deletingProductCount,
    nextStep: steps.find((step) => !step.complete),
  }
}
