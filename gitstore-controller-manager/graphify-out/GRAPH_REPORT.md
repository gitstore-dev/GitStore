# Graph Report - gitstore-controller-manager  (2026-09-17)

## Corpus Check
- 104 files · ~60,628 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 1139 nodes · 2786 edges · 70 communities (65 shown, 5 thin omitted)
- Extraction: 82% EXTRACTED · 18% INFERRED · 0% AMBIGUOUS · INFERRED: 510 edges (avg confidence: 0.8)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `cd46afd9`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- NewMemoryStore
- seedCache
- Condition
- resolver.go
- Cache
- NewServiceAccountSource
- Subscription
- newRunner
- ReconcileResult
- Runner[T]
- Record
- health_test.go
- Queue
- NewReconciler
- Repository
- WorkItemKey
- newSigner
- AsReadOnly
- newSyncedCache
- HandlerFunc
- Manager
- NewGraphQLStorageClient
- graphql_listwatcher.go
- QuarantineStore
- NewStaticToken
- .List
- ResultTerminal
- WatchEvent
- Pool
- RunWithRetry
- manager_dispatch_test.go
- status_patch_test.go
- reconciler_contract_test.go
- Client
- NewCategoryTaxonomyListWatcher
- ReconcilerRegistration
- CategoryTaxonomy
- reconcilerFor
- .Apply
- cache_test.go
- .List
- Controller Manager
- TestProductList_EnumeratesNamespacesThenPaginatesProducts
- repository_listwatcher.go
- NewGraphQLResourceStatusClient
- NewGraphQLStatusClient
- scriptedPages
- computeFileRefCondition
- cache_contract_test.go
- graphqlDeletionClient
- main_test.go
- .Reconcile
- TestIntegration_Reconcile_TransientFailureThenSucceeds
- NewRepositoryListWatcher
- T
- funcReconciler
- NewNamespaceListWatcher
- safeReconcile
- TransientFailure
- scriptedReconciler
- runner_update_test.go
- statusConflictReconciler
- .Reconcile
- InitLogger
- TerminalFailure
- github.com/gitstore-dev/gitstore/controller-manager

## God Nodes (most connected - your core abstractions)
1. `WorkItemKey` - 102 edges
2. `NewStaticToken()` - 49 edges
3. `Client` - 40 edges
4. `NewMemoryStore()` - 35 edges
5. `newRunner()` - 30 edges
6. `ReconcileResult` - 29 edges
7. `ResultOK()` - 28 edges
8. `seedCache()` - 27 edges
9. `Repository` - 27 edges
10. `Runner[T]` - 25 edges

## Surprising Connections (you probably didn't know these)
- `main()` --calls--> `NewFilesystemStore()`  [INFERRED]
  cmd/controller/main.go → internal/checkpoint/filesystem.go
- `main()` --calls--> `LoadFrom()`  [INFERRED]
  cmd/controller/main.go → internal/config/config.go
- `main()` --calls--> `NewRepositoryListWatcher()`  [INFERRED]
  cmd/controller/main.go → internal/listwatch/repository_listwatcher.go
- `main()` --calls--> `InitLogger()`  [INFERRED]
  cmd/controller/main.go → internal/manager/logger.go
- `main()` --calls--> `NewGraphQLStorageClient()`  [INFERRED]
  cmd/controller/main.go → internal/repository/graphql_client.go

## Import Cycles
- None detected.

## Communities (70 total, 5 thin omitted)

### Community 0 - "NewMemoryStore"
Cohesion: 0.08
Nodes (59): Store, enqueueRecorder, failingStore, stubListWatcher, stubListWatcher[T], stubWatcher, stubWatcher[T], widget (+51 more)

### Community 1 - "seedCache"
Cohesion: 0.08
Nodes (55): EnqueueFunc, fakeDeletionClient, fakeStatusClient, ProductCounter, Reconciler, ResolvedCategoryTaxonomy, DeletionClient, computeAcyclic() (+47 more)

### Community 2 - "Condition"
Cohesion: 0.10
Nodes (44): CacheAccessor, preserveLastTransitionTimes(), conditionTrue(), mergeControllerConditions(), preserveTransitionTime(), hasFinalizer(), NewReconciler(), repositoryFixture() (+36 more)

### Community 3 - "resolver.go"
Cohesion: 0.09
Nodes (43): Config, ControllerConfig, LogConfig, bindServiceAccountEnvironment(), Duration, hasServiceAccountKeyRef(), Load(), LoadFrom() (+35 more)

### Community 4 - "Cache"
Cohesion: 0.08
Nodes (33): Cache, Cache[T], EventHandler, enqueueCall, Product, productsListResponse, stateReadingReconciler, activeProductCache (+25 more)

### Community 5 - "NewServiceAccountSource"
Cohesion: 0.11
Nodes (28): mockTokenSigner, ServiceAccountSource, StaticToken, TokenSigner, Context, Duration, Mutex, Time (+20 more)

### Community 6 - "Subscription"
Cohesion: 0.09
Nodes (20): Conn, Error, gqlRequest, gqlResponse, subscribePayload, Subscription, wsMessage, Once (+12 more)

### Community 7 - "newRunner"
Cohesion: 0.09
Nodes (23): enqueueRecorder, stubListWatcher, stubListWatcher[T], stubWatcher, stubWatcher[T], widget, T, TestIntegration_Disconnect_ReconnectsWithBackoff() (+15 more)

### Community 8 - "ReconcileResult"
Cohesion: 0.15
Nodes (15): Context, Context, Context, Context, ResultAfter(), ResultOK(), ResultTransient(), Context (+7 more)

### Community 9 - "Runner[T]"
Cohesion: 0.18
Nodes (5): ExponentialBackOff, Context, Duration, T, Runner[T]

### Community 10 - "Record"
Cohesion: 0.15
Nodes (19): FilesystemStore, MemoryStore, Record, RawMessage, Time, Context, NewFilesystemStore(), validateRecord() (+11 more)

### Community 11 - "health_test.go"
Cohesion: 0.12
Nodes (20): blockingReconciler, CredentialReadiness, healthResponse, KindStat, ManagerStats, testCredentialReadiness, testManagerStats, Handler (+12 more)

### Community 12 - "Queue"
Cohesion: 0.13
Nodes (16): CancelFunc, Cond, Context, Duration, Mutex, New(), RateLimitedEnqueue(), T (+8 more)

### Community 13 - "NewReconciler"
Cohesion: 0.29
Nodes (20): NewReconciler(), admittedCondition(), conditionByType(), Context, T, namespaceKey(), seedNamespaceCache(), TestReconcileAdmittedNamespaceProvisionsSystemRepositoryAndMarksReady() (+12 more)

### Community 14 - "Repository"
Cohesion: 0.18
Nodes (15): Context, repositoryRegistrationListWatcher, Context, Mutex, T, repo(), repositoryRunner(), TestRepositoryRunnerBootstrapDrainDeduplicatesListedWatchState() (+7 more)

### Community 15 - "WorkItemKey"
Cohesion: 0.17
Nodes (12): fakeStatusClient, Context, Context, toConditionInputs(), toUpdateCategoryStatusInput(), fakeStatusClient, fakeStatusClient, graphqlStatusClient (+4 more)

### Community 16 - "newSigner"
Cohesion: 0.20
Nodes (16): decodedAssertion, PrivateKeyTokenSigner, Int, encodeECDSASignature(), Context, Duration, NewPrivateKeyTokenSigner(), randomJWTID() (+8 more)

### Community 17 - "AsReadOnly"
Cohesion: 0.26
Nodes (17): buildCredentialSource(), buildMux(), Config, Context, Handler, Logger, main(), parseConfigFile() (+9 more)

### Community 18 - "newSyncedCache"
Cohesion: 0.22
Nodes (16): errorBody, Requeuer, alwaysFailReconciler, ResponseWriter, ListPoisonHandler(), RequeuePoisonHandler(), writeJSON(), T (+8 more)

### Community 19 - "HandlerFunc"
Cohesion: 0.24
Nodes (13): HandlerFunc, Context, NewGraphQLDeletionClient(), NewGraphQLRepositoryClient(), T, TestGraphQLDeletionClientCompletesDeletion(), TestGraphQLDeletionClientReturnsConflict(), TestGraphQLRepositoryClientAcceptsIdempotentProvisioning() (+5 more)

### Community 20 - "Manager"
Cohesion: 0.22
Nodes (8): Config, Context, Logger, Mutex, RWMutex, Time, kindState, Manager

### Community 21 - "NewGraphQLStorageClient"
Cohesion: 0.21
Nodes (13): TestRegisterRepositoryAcrossTwoControllerManagers(), Context, NewGraphQLCompletionClient(), NewGraphQLStorageClient(), T, TestGraphQLCompletionClientCompletesRepositoryDeletion(), TestGraphQLCompletionClientReturnsConflictForRetry(), TestGraphQLCompletionClientTreatsNotFoundAsIdempotentSuccess() (+5 more)

### Community 22 - "graphql_listwatcher.go"
Cohesion: 0.21
Nodes (15): Time, categoriesListResponse, categoryMetadataJSON, categoryNodeJSON, categorySpecJSON, categoryStatusJSON, mediaJSON, namespacesListResponse (+7 more)

### Community 23 - "QuarantineStore"
Cohesion: 0.18
Nodes (9): RWMutex, Time, NewQuarantineStore(), PoisonItem, QuarantineStore, T, TestQuarantineStore_Len(), TestQuarantineStore_List() (+1 more)

### Community 24 - "NewStaticToken"
Cohesion: 0.39
Nodes (15): New(), Server, T, stubGraphQLWSServer(), TestMutate_DecodesData(), TestQuery_GraphQLErrorSurfacesExtensionsCode(), TestQuery_HTTPErrorStatusReturnsError(), TestQuery_RateLimitStatusReturnsRetryableSentinel() (+7 more)

### Community 25 - ".List"
Cohesion: 0.23
Nodes (7): derefOr(), Context, isWatchExpiredErr(), listNamespaceIdentifiers(), CategoryTaxonomyListWatcher, ProductListWatcher, Watcher

### Community 26 - "ResultTerminal"
Cohesion: 0.26
Nodes (14): ResultTerminal(), newScriptedReconciler(), T, TestObservability_ActiveWorkers_ReflectsRunningReconciles(), TestObservability_PoisonItemsTotal_IncrementsOnQuarantine(), TestObservability_QuarantineLog_IncludesLastError(), TestObservability_QueueDepth_ReflectsPendingItems(), TestObservability_ReconcileTotal_LabeledByOutcome() (+6 more)

### Community 27 - "WatchEvent"
Cohesion: 0.18
Nodes (6): Once, repositoryRegistrationWatch, categoryWatcher, EventType, productWatcher, WatchEvent

### Community 28 - "Pool"
Cohesion: 0.18
Nodes (8): Context, New(), T, TestPool_ExecutesTasks(), TestPool_Resize(), TestPool_RunningWorkers(), Task, Pool

### Community 29 - "RunWithRetry"
Cohesion: 0.24
Nodes (12): Context, Duration, Logger, RunWithRetry(), T, TestRunWithRetry_ContextCancellation(), TestRunWithRetry_QuarantinesAfterMaxAttempts(), TestRunWithRetry_RetriesAndSucceeds() (+4 more)

### Community 30 - "manager_dispatch_test.go"
Cohesion: 0.26
Nodes (13): T, TestManager_CRDKind_DispatchedOnSamePathAsCoreKind(), TestManager_DispatchHeldUntilCacheSynced(), TestManager_DuplicateRegistration_ReturnsError(), TestManager_NilCache_ReturnsError(), TestManager_NilReconciler_ReturnsError(), TestManager_ReconcilerDispatchedOnce(), TestManager_ReconcilerPanic_LogsStackTrace() (+5 more)

### Community 31 - "status_patch_test.go"
Cohesion: 0.26
Nodes (11): mockStatusClient, Context, T, TestStatusClient_Conflict_ReturnsErrConflict(), TestStatusClient_NoOpPatch_SkipsApply(), TestStatusPatch_IsNoOp_AllFieldsMatch(), TestStatusPatch_IsNoOp_NilResolvedIsUnchanged(), TestStatusPatch_IsNoOp_OneFieldDiffers() (+3 more)

### Community 32 - "reconciler_contract_test.go"
Cohesion: 0.26
Nodes (10): stubReconciler, Context, Int64, T, TestQueue_DeduplicatesEnqueue(), TestQueue_DirtyReenqueuesAfterDone(), TestQueue_EnqueueAfterShutDown_ReturnsError(), TestQueue_ShutDown_UnblocksDequeue() (+2 more)

### Community 33 - "Client"
Cohesion: 0.26
Nodes (7): Dialer, Client, Context, Context, NewGraphQLCompletionClient(), GraphQLCompletionClient, Uint64

### Community 34 - "NewCategoryTaxonomyListWatcher"
Cohesion: 0.41
Nodes (11): NewCategoryTaxonomyListWatcher(), categoryNodeJSON(), Server, T, stubWatchCategoriesServer(), TestList_DecodesCategoryLifecycleMetadata(), TestList_EmptyDatasetReturnsNonEmptySentinelResourceVersion(), TestList_HTTPErrorReturnsError() (+3 more)

### Community 35 - "ReconcilerRegistration"
Cohesion: 0.21
Nodes (7): applyDefaults(), New(), Duration, Reconciler, ReconcilerRegistration, syncChecker, terminalDuringRetryError

### Community 36 - "CategoryTaxonomy"
Cohesion: 0.24
Nodes (9): CategoryTaxonomy, MediaRef, OwnerReference, Time, AcceptWatchUpdate(), ShouldEnqueueWatchUpdate(), T, TestAcceptWatchUpdate() (+1 more)

### Community 37 - "reconcilerFor"
Cohesion: 0.38
Nodes (9): Context, Reconciler, T, productKey(), reconcilerFor(), TestReconcileActiveProductDoesNotComplete(), TestReconcileCompletesTerminatingProduct(), TestReconcileConflictRequeues() (+1 more)

### Community 38 - ".Apply"
Cohesion: 0.24
Nodes (6): deletionStatusClient, restartDeletionClient, Context, Mutex, T, TestIntegration_CategoryDeletionResumesAfterControllerRestart()

### Community 39 - "cache_test.go"
Cohesion: 0.36
Nodes (9): T, TestCache_ConcurrentAccess(), TestCache_Delete(), TestCache_HasSynced(), TestCache_List(), TestCache_OnAdd_Called(), TestCache_OnDelete_Called(), TestCache_OnUpdate_Called() (+1 more)

### Community 40 - ".List"
Cohesion: 0.36
Nodes (3): Context, RepositoryListWatcher, repositoryWatcher

### Community 41 - "Controller Manager"
Cohesion: 0.20
Nodes (9): Boundaries, Commands, Configuration Highlights, Controller Manager, Deeper Docs, HTTP Surface, License, Project Structure (+1 more)

### Community 42 - "TestProductList_EnumeratesNamespacesThenPaginatesProducts"
Cohesion: 0.39
Nodes (8): NewProductListWatcher(), Request, ResponseWriter, T, productNodeJSON(), serveProductBootstrapBookmark(), TestProductList_EmptyDatasetReturnsNonEmptySentinelResourceVersion(), TestProductList_EnumeratesNamespacesThenPaginatesProducts()

### Community 43 - "repository_listwatcher.go"
Cohesion: 0.39
Nodes (8): conditionJSON, repositoriesControllerListResponse, repositoryMetadataJSON, repositoryNodeJSON, repositoryResolvedJSON, repositorySpecJSON, repositoryStatusJSON, repositoryWatchEventJSON

### Community 44 - "NewGraphQLResourceStatusClient"
Cohesion: 0.33
Nodes (7): NewGraphQLResourceStatusClient(), T, TestResourceStatusApplyConflictMapsToErrConflict(), TestResourceStatusApplyNotFoundMapsToErrNotFound(), TestResourceStatusApplySendsKindAwareMutation(), graphqlResourceStatusClient, updateResourceStatusResponse

### Community 45 - "NewGraphQLStatusClient"
Cohesion: 0.61
Nodes (8): NewGraphQLStatusClient(), T, TestApply_ConflictExtensionMapsToErrConflict(), TestApply_ForbiddenExtensionReturnsPlainWrappedError(), TestApply_NotFoundExtensionMapsToErrNotFound(), TestApply_SendsUpdateCategoryStatusMutation(), testKey(), testPatch()

### Community 46 - "scriptedPages"
Cohesion: 0.29
Nodes (5): scriptedPages, Context, Mutex, T, TestIntegration_CategoryDeletionHighCardinalityContinuation()

### Community 47 - "computeFileRefCondition"
Cohesion: 0.46
Nodes (6): computeFileRefCondition(), T, TestComputeFileRefCondition_MixedMediaOnlyFlagsRequired(), TestComputeFileRefCondition_NoMediaProducesNoCondition(), TestComputeFileRefCondition_OptionalMediaProducesNoCondition(), TestComputeFileRefCondition_RequiredMediaProducesUnknownCondition()

### Community 48 - "cache_contract_test.go"
Cohesion: 0.43
Nodes (7): T, TestCacheAccessor_ReadOnly(), TestInformerCache_EventHandlerFiredOnDelete(), TestInformerCache_EventHandlerFiredOnSet(), TestInformerCache_HasSynced(), TestInformerCache_List(), TestInformerCache_SetGetDelete()

### Community 49 - "graphqlDeletionClient"
Cohesion: 0.38
Nodes (4): DeletionClient, graphqlDeletionClient, Context, NewGraphQLDeletionClient()

### Community 50 - "main_test.go"
Cohesion: 0.38
Nodes (6): credentialReadiness(), T, TestBuildCredentialSourceRejectsMissingCredentialConfiguration(), TestBuildCredentialSourceUsesResolvedServiceAccountKey(), TestParseConfigFile(), CredentialSource

### Community 51 - ".Reconcile"
Cohesion: 0.29
Nodes (5): blockingOnceReconciler, Context, Int64, T, TestManager_BurstOfSameKeyEnqueues_CollapsesToOnePendingItem()

### Community 52 - "TestIntegration_Reconcile_TransientFailureThenSucceeds"
Cohesion: 0.38
Nodes (6): blockingSelectReconciler, Mutex, T, TestIntegration_Reconcile_SucceedsOnFirstAttempt(), TestIntegration_Reconcile_TransientFailureThenSucceeds(), TestIntegration_Restart_ResumesFromCheckpoint_NoLostOrDuplicateWork()

### Community 53 - "NewRepositoryListWatcher"
Cohesion: 0.57
Nodes (6): NewRepositoryListWatcher(), T, repositoryNodeJSON(), TestRepositoryListWatcherListsRepositoriesAcrossNamespaces(), TestRepositoryListWatcherMapsExpiredCursor(), TestRepositoryListWatcherMapsTypedWatchEvents()

### Community 54 - "T"
Cohesion: 0.40
Nodes (3): readOnlyCache, readOnlyCache[T], T

### Community 55 - "funcReconciler"
Cohesion: 0.47
Nodes (4): countingReconciler, funcReconciler, Context, Int64

### Community 56 - "NewNamespaceListWatcher"
Cohesion: 0.60
Nodes (5): NewNamespaceListWatcher(), T, namespaceNodeJSON(), TestNamespaceListWatcherListsNamespaces(), TestNamespaceListWatcherMapsGenericWatchEvents()

### Community 57 - "safeReconcile"
Cohesion: 0.33
Nodes (4): Context, Reconciler, safeReconcile(), PanicError

### Community 58 - "TransientFailure"
Cohesion: 0.40
Nodes (3): Duration, RequeueAfter, TransientFailure

### Community 60 - "runner_update_test.go"
Cohesion: 0.50
Nodes (3): T, TestRunnerModifiedEventUpdatePolicies(), updatePolicyItem

### Community 61 - "statusConflictReconciler"
Cohesion: 0.67
Nodes (3): fakeStatusClient, statusConflictReconciler, Mutex

## Knowledge Gaps
- **19 isolated node(s):** `github.com/gitstore-dev/gitstore/controller-manager`, `errorBody`, `productsListResponse`, `enqueueCall`, `gqlRequest` (+14 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **5 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `WorkItemKey` connect `WorkItemKey` to `NewMemoryStore`, `seedCache`, `Condition`, `Cache`, `newRunner`, `ReconcileResult`, `Runner[T]`, `Record`, `Queue`, `NewReconciler`, `Repository`, `AsReadOnly`, `Manager`, `QuarantineStore`, `RunWithRetry`, `status_patch_test.go`, `reconciler_contract_test.go`, `ReconcilerRegistration`, `reconcilerFor`, `.Apply`, `NewGraphQLStatusClient`, `.Reconcile`, `TestIntegration_Reconcile_TransientFailureThenSucceeds`, `T`, `funcReconciler`, `safeReconcile`, `scriptedReconciler`, `statusConflictReconciler`, `.Reconcile`?**
  _High betweenness centrality (0.393) - this node is a cross-community bridge._
- **Why does `registerCategoryTaxonomy()` connect `AsReadOnly` to `Client`, `NewCategoryTaxonomyListWatcher`, `CategoryTaxonomy`, `Cache`, `Record`, `NewGraphQLStatusClient`, `WorkItemKey`, `Manager`?**
  _High betweenness centrality (0.158) - this node is a cross-community bridge._
- **Why does `buildCredentialSource()` connect `AsReadOnly` to `newSigner`, `main_test.go`, `resolver.go`, `NewServiceAccountSource`?**
  _High betweenness centrality (0.126) - this node is a cross-community bridge._
- **Are the 48 inferred relationships involving `HandlerFunc` (e.g. with `TestRegisterRepositoryAcrossTwoControllerManagers()` and `stubGraphQLWSServer()`) actually correct?**
  _`HandlerFunc` has 48 INFERRED edges - model-reasoned connections that need verification._
- **Are the 47 inferred relationships involving `NewStaticToken()` (e.g. with `TestRegisterRepositoryAcrossTwoControllerManagers()` and `TestMutate_DecodesData()`) actually correct?**
  _`NewStaticToken()` has 47 INFERRED edges - model-reasoned connections that need verification._
- **Are the 33 inferred relationships involving `NewMemoryStore()` (e.g. with `TestRepositoryRunnerBootstrapDrainDeduplicatesListedWatchState()` and `TestRepositoryRunnerExpiryRelistsAndEnqueuesChangedRevision()`) actually correct?**
  _`NewMemoryStore()` has 33 INFERRED edges - model-reasoned connections that need verification._
- **What connects `github.com/gitstore-dev/gitstore/controller-manager`, `errorBody`, `productsListResponse` to the rest of the system?**
  _19 weakly-connected nodes found - possible documentation gaps or missing edges._