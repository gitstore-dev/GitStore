import { BreadcrumbLink, ShellBreadcrumbs } from './ShellBreadcrumbs'
import type { ShellCrumb } from './ShellBreadcrumbs'
import { productSearch } from '../features/catalog/products/model'
import { catalogSearch, catalogPaths } from '../features/catalog/browse/model'
import type {
  CatalogDomain,
  CatalogSearch,
} from '../features/catalog/browse/model'
import { useHomeDemo } from '../features/demo/HomeDemoProvider'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Link,
  Outlet,
  useLocation,
  useNavigate,
  useParams,
  useRouter,
  useSearch,
} from '@tanstack/react-router'
import {
  Button,
  DialogTrigger,
  ModalOverlay,
  Modal,
  Dialog,
  Heading,
} from 'react-aria-components'
import {
  Grid2X2,
  Search,
  X,
  Minimize2,
  ArrowUpRight,
  PanelRightClose,
} from 'lucide-react'
import { Sidebar } from './Sidebar'
import { Icon } from '../components/Icon'
import { NamespacePicker } from '../components/NamespacePicker'
import { WorkspaceLayout } from './WorkspaceLayout'
import { LayoutPicker } from './LayoutPicker'
import { PageInspectorContext } from './PageInspector'
import { useFocusMotion } from './motion/useFocusMotion'
import type { LayoutMode } from './WorkspaceLayout'
import { usePreferences, useMediaQuery } from '../state/preferences'
import { defaultNamespace, isDemoNamespace } from '../data/demo'
import type { NamespaceId } from '../data/demo'
import {
  destinationAtPath,
  destinations,
  destinationById,
} from '../navigation/registry'
import type { NavigationDestination } from '../navigation/registry'
import { ActionButton } from '../components/controls'
import { CommandPalette } from '../components/CommandPalette'
import styles from './shell.module.css'
export function AppShell({ children }: { children?: ReactNode }) {
  const preferences = usePreferences()
  const setRememberedNamespace = preferences.setNamespace
  const router = useRouter()
  const navigate = useNavigate()
  const pathname = useLocation({ select: (location) => location.pathname })
  const params = useParams({ strict: false })
  const routeNamespace = 'namespace' in params ? params.namespace : undefined
  const namespace: NamespaceId =
    routeNamespace && isDemoNamespace(routeNamespace)
      ? routeNamespace
      : preferences.namespace
  const active = destinationAtPath(pathname)
  const { session: productSession } = useHomeDemo(namespace)
  const productSegments = pathname.split('/').filter(Boolean)
  const nestedProduct =
    active?.id === 'catalog.products' && productSegments.length > 4
  const product = productSession.catalog[productSegments[4] ?? '']
  const productPageTitle =
    productSegments[4] === 'new'
      ? 'New product'
      : product?.kind === 'Product'
        ? productSegments[5] === 'edit'
          ? `Edit ${product.spec.title}`
          : product.spec.title
        : 'Product unavailable'

  const nestedCatalog =
    ['catalog.variants', 'catalog.categories', 'catalog.collections'].includes(
      active?.id ?? '',
    ) && productSegments.length > 4
  const catalogItemTitle =
    product?.kind !== 'File' && product && 'spec' in product
      ? product.spec.title
      : productSession.variants[productSegments[4] ?? '']?.spec.title
  const pageTitle =
    routeNamespace && !isDemoNamespace(routeNamespace)
      ? 'Unknown demo workspace'
      : nestedProduct
        ? productPageTitle
        : nestedCatalog
          ? (catalogItemTitle ?? 'Item unavailable')
          : (active?.label ?? 'Not found')
  const currentSearch = useSearch({ strict: false })
  const ancestors: ShellCrumb[] = []
  if (active) {
    const root = destinations.find((item) => item.groupId === active.groupId)!
    ancestors.push({
      label: active.groupLabel,
      link: (
        <BreadcrumbLink to={root.to} params={{ namespace }}>
          {active.groupLabel}
        </BreadcrumbLink>
      ),
      onSelect: () => {
        void navigate({ to: root.to, params: { namespace } })
      },
    })
    if (nestedProduct) {
      const search =
        router.options.context.productNavigation?.returnSearch(pathname) ??
        productSearch(currentSearch)
      ancestors.push({
        label: 'Products',
        link: (
          <BreadcrumbLink
            to="/w/$namespace/catalog/products"
            params={{ namespace }}
            search={search}
            resetScroll={false}
          >
            Products
          </BreadcrumbLink>
        ),
        onSelect: () => {
          void navigate({
            to: '/w/$namespace/catalog/products',
            params: { namespace },
            search,
            resetScroll: false,
          })
        },
      })
    } else if (nestedCatalog) {
      const domain = active.id.split('.')[1] as CatalogDomain
      const search =
        router.options.context.productNavigation?.returnSearch<CatalogSearch>(
          pathname,
        ) ?? catalogSearch(currentSearch, domain)
      ancestors.push({
        label: active.label,
        link: (
          <BreadcrumbLink
            to={catalogPaths[domain]}
            params={{ namespace }}
            search={search}
            resetScroll={false}
          >
            {active.label}
          </BreadcrumbLink>
        ),
        onSelect: () => {
          void navigate({
            to: catalogPaths[domain],
            params: { namespace },
            search,
            resetScroll: false,
          })
        },
      })
    }
  }
  useEffect(() => {
    document.title = `${pageTitle} · GitStore`
  }, [pageTitle])
  const tablet = useMediaQuery('(max-width: 1100px)')
  const [mode, setMode] = useState<LayoutMode>(
    () => router.options.context.initialLayout ?? 'standard',
  )
  const [mobileOpen, setMobileOpen] = useState(false)
  const [restoreLayoutFocus, setRestoreLayoutFocus] = useState(false)
  const [commandOpen, setCommandOpen] = useState(false)
  const [pageInspector, setPageInspector] = useState<{
    owner: string
    content: ReactNode
  } | null>(null)
  const inspectorTrigger = useRef<HTMLElement | null>(null)
  const closeGuard = useRef<{
    owner: string
    guard: (proceed: () => void) => void
  } | null>(null)
  const registerCloseGuard = useCallback(
    (owner: string, guard: ((proceed: () => void) => void) | null) => {
      if (guard) closeGuard.current = { owner, guard }
      else if (closeGuard.current?.owner === owner) closeGuard.current = null
    },
    [],
  )
  const registerInspector = useCallback(
    (owner: string, content: ReactNode | null) => {
      setPageInspector((current) =>
        content === null
          ? current?.owner === owner
            ? null
            : current
          : { owner, content },
      )
    },
    [],
  )
  const openInspector = useCallback((trigger?: HTMLElement) => {
    if (trigger) inspectorTrigger.current = trigger
    setMode('inspector')
  }, [])
  const closeInspector = useCallback(() => {
    const proceed = () => {
      setMode('standard')
      const trigger = inspectorTrigger.current
      // A page confirmation can still own modality in this event. Restore only
      // after React has removed it and made the page interactive again.
      requestAnimationFrame(() => {
        if (trigger?.isConnected && trigger.checkVisibility()) trigger.focus()
        else document.getElementById('main-content')?.focus()
      })
    }
    if (closeGuard.current?.owner === pathname)
      closeGuard.current.guard(proceed)
    else proceed()
  }, [pathname])
  const inspectorAPI = useMemo(
    () => ({
      owner: pathname,
      register: registerInspector,
      open: openInspector,
      close: closeInspector,
      registerCloseGuard,
    }),
    [
      pathname,
      registerInspector,
      openInspector,
      closeInspector,
      registerCloseGuard,
    ],
  )
  const customInspector =
    pageInspector?.owner === pathname ? pageInspector.content : null
  const {
    root: focusRoot,
    changeMode,
    prepareFocus,
  } = useFocusMotion(mode, setMode, pathname)
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setCommandOpen((open) => !open)
      }
    }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [])
  useEffect(() => {
    if (routeNamespace && isDemoNamespace(routeNamespace))
      setRememberedNamespace(routeNamespace)
  }, [routeNamespace, setRememberedNamespace])
  function go(destination: NavigationDestination) {
    void navigate({ to: destination.to, params: { namespace } })
    setMobileOpen(false)
  }
  function changeNamespace(id: NamespaceId) {
    preferences.setNamespace(id)
    if (active?.scope === 'namespace')
      void navigate({ to: active.to, params: { namespace: id } })
    else
      void navigate({
        to: '/w/$namespace/home/overview',
        params: { namespace: id },
      })
    setMobileOpen(false)
  }
  function openPlatform() {
    void navigate({ to: '/platform/namespaces' })
    setMobileOpen(false)
  }
  const mobileDestinations = (
    active?.scope === 'platform'
      ? [
          'control-plane.namespaces',
          'control-plane.resource-explorer',
          'control-plane.policies',
        ]
      : ['catalog.products', 'orders.all-orders', 'changes-releases.my-changes']
  ).map(destinationById)
  const inspector = (
    <div className={styles.inspectorContent}>
      <div className={styles.inspectorHeader}>
        <span className={styles.eyebrow}>CONTEXT</span>
        <ActionButton
          variant="ghost"
          aria-label="Close inspector"
          onPress={closeInspector}
        >
          <PanelRightClose size={17} aria-hidden="true" />
        </ActionButton>
      </div>
      {customInspector ?? (
        <>
          <h2>{active?.label ?? 'Workspace'}</h2>
          <p className={styles.muted}>A little room for the details.</p>
          <dl className={styles.contextList}>
            <div>
              <dt>Section</dt>
              <dd>{active?.groupLabel ?? '—'}</dd>
            </div>
            <div>
              <dt>Scope</dt>
              <dd>{active?.scope === 'platform' ? 'Platform' : namespace}</dd>
            </div>
            <div>
              <dt>Page status</dt>
              <dd>
                {active?.implementation === 'mock'
                  ? 'Mock data'
                  : 'Not implemented'}
              </dd>
            </div>
          </dl>
          <div className={styles.contextNote}>
            <ArrowUpRight size={18} aria-hidden="true" />
            <p>
              Future pages can use this rail for contextual information without
              leaving the workspace.
            </p>
          </div>
          <span className={styles.contextDemo}>LOCAL DEMO CONTEXT</span>
        </>
      )}
    </div>
  )
  return (
    <PageInspectorContext value={inspectorAPI}>
      <div
        ref={focusRoot}
        className={styles.frame}
        data-collapsed={preferences.collapsed || tablet || undefined}
        data-focus={mode === 'focus' || undefined}
      >
        <a href="#main-content" className={styles.skipLink}>
          Skip to content
        </a>
        <aside
          className={styles.desktopSidebar}
          aria-label="Workspace sidebar"
          data-focus-chrome="sidebar"
        >
          <Sidebar
            active={active}
            namespace={namespace}
            onNamespaceChange={changeNamespace}
            onPlatformChange={openPlatform}
            compact={preferences.collapsed || tablet}
            collapsed={tablet}
            onCollapse={() => preferences.setCollapsed(!preferences.collapsed)}
          />
        </aside>
        <header className={styles.header} data-focus-chrome="header">
          {mode === 'focus' ? (
            <>
              <span className={styles.focusBrand}>
                GitStore <span>/ FOCUS</span>
              </span>
              <ActionButton
                autoFocus
                onPress={() => {
                  setRestoreLayoutFocus(true)
                  changeMode('standard')
                }}
              >
                <Minimize2 size={16} aria-hidden="true" />
                Exit Focus Mode
              </ActionButton>
            </>
          ) : (
            <>
              <div className={styles.mobileContext}>
                <NamespacePicker
                  value={namespace}
                  onChange={changeNamespace}
                  platform={active?.scope === 'platform'}
                  onPlatformChange={openPlatform}
                />
              </div>
              <ShellBreadcrumbs ancestors={ancestors} current={pageTitle} />
              <div className={styles.headerActions}>
                <ActionButton
                  variant="ghost"
                  className={styles.searchTrigger}
                  onPress={() => setCommandOpen(true)}
                >
                  <Search size={16} aria-hidden="true" />
                  <span>Search navigation</span>{' '}
                  <kbd aria-hidden="true">⌘ K</kbd>
                </ActionButton>
                <span className={styles.headerDivider} />
                <LayoutPicker
                  value={mode}
                  onChange={(next) => {
                    if (mode === 'inspector' && next === 'standard')
                      closeInspector()
                    else changeMode(next)
                  }}
                  onPrepareFocus={prepareFocus}
                  autoFocus={restoreLayoutFocus}
                />
                <span className={styles.headerDemo}>DEMO</span>
              </div>
            </>
          )}
        </header>
        {mode !== 'focus' && (
          <nav
            aria-label="Quick navigation"
            className={styles.mobileBar}
            data-focus-chrome="mobile"
          >
            {mobileDestinations.map((destination, index) => (
              <Link
                key={destination.id}
                to={destination.to}
                params={{ namespace }}
                aria-current={
                  (
                    active?.scope === 'platform'
                      ? active.id === destination.id
                      : active?.groupId === destination.groupId
                  )
                    ? 'page'
                    : undefined
                }
                className={styles.mobileTab}
              >
                <Icon name={destination.icon} size={20} />
                <span>
                  {active?.scope === 'platform'
                    ? ['Namespaces', 'Resources', 'Policies'][index]
                    : ['Catalog', 'Orders', 'Changes'][index]}
                </span>
              </Link>
            ))}
            <DialogTrigger isOpen={mobileOpen} onOpenChange={setMobileOpen}>
              <Button
                className={styles.mobileTab}
                aria-label="Open navigation menu"
              >
                <Grid2X2 size={20} aria-hidden="true" />
                <span>Menu</span>
              </Button>
              <ModalOverlay
                isDismissable
                className={`${styles.drawerOverlay} ${styles.navigationOverlay}`}
              >
                <Modal className={styles.navDrawer}>
                  <Dialog
                    aria-label="Mobile navigation"
                    className={styles.drawerDialog}
                  >
                    <span className={styles.sheetHandle} aria-hidden="true" />
                    <Button
                      className={styles.drawerClose}
                      aria-label="Close navigation"
                      onPress={() => setMobileOpen(false)}
                    >
                      <X size={18} aria-hidden="true" />
                    </Button>
                    <Sidebar
                      active={active}
                      namespace={namespace}
                      onNamespaceChange={changeNamespace}
                      onPlatformChange={openPlatform}
                      compact={false}
                      collapsed={false}
                      onCollapse={() => {}}
                      onNavigate={() => setMobileOpen(false)}
                      mobile
                    />
                  </Dialog>
                </Modal>
              </ModalOverlay>
            </DialogTrigger>
          </nav>
        )}
        <div className={styles.workArea}>
          <WorkspaceLayout
            mode={tablet && mode === 'inspector' ? 'standard' : mode}
            inspector={tablet ? undefined : inspector}
          >
            {routeNamespace && !isDemoNamespace(routeNamespace) ? (
              <div className={styles.unknownNamespace}>
                <h1>Unknown demo workspace</h1>
                <p>Choose one of the local demo workspaces to continue.</p>
                <ActionButton onPress={() => changeNamespace(defaultNamespace)}>
                  Open Atelier Kyoto
                </ActionButton>
              </div>
            ) : (
              (children ?? <Outlet />)
            )}
          </WorkspaceLayout>
        </div>
        <ModalOverlay
          isOpen={tablet && mode === 'inspector'}
          onOpenChange={(open) => {
            if (!open) closeInspector()
          }}
          isDismissable
          className={styles.drawerOverlay}
        >
          <Modal className={styles.inspectorDrawer}>
            <Dialog aria-label="Page context" className={styles.drawerDialog}>
              <Heading slot="title" className={styles.srOnly}>
                Page context
              </Heading>
              {inspector}
            </Dialog>
          </Modal>
        </ModalOverlay>
        <CommandPalette
          open={commandOpen}
          onOpenChange={setCommandOpen}
          onNavigate={go}
        />
      </div>
    </PageInspectorContext>
  )
}
