# Changelog

## [0.1.0-alpha.11](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.10...v0.1.0-alpha.11) (2026-11-14)


### Features

* **authz:** add opt-in Git-backed RBAC and embedded OPA ([#455](https://github.com/gitstore-dev/GitStore/issues/455)) ([3da5101](https://github.com/gitstore-dev/GitStore/commit/3da5101acc13937d47df2f9e6b9bf6fa376ae40c))


### Bug Fixes

* **api:** normalize frontmatter enum boundaries ([#549](https://github.com/gitstore-dev/GitStore/issues/549)) ([260e73e](https://github.com/gitstore-dev/GitStore/commit/260e73e896af421dbb44d45e9d89e45e2acf2982))
* **auth:** match USER namespace policy to authenticated subject ([#553](https://github.com/gitstore-dev/GitStore/issues/553)) ([1d9eb26](https://github.com/gitstore-dev/GitStore/commit/1d9eb26326be661e2b1ecbce3249bd2c59463053))
* **cdc:** advance quiet streams after completed query windows ([#555](https://github.com/gitstore-dev/GitStore/issues/555)) ([777813b](https://github.com/gitstore-dev/GitStore/commit/777813bdbb2773e4788c14b690249ec30ba0bde8))
* **config:** remove alpha legacy configuration mappings ([#552](https://github.com/gitstore-dev/GitStore/issues/552)) ([dca7cb2](https://github.com/gitstore-dev/GitStore/commit/dca7cb2b0658df942d13c7b94cb4bc45c8a48f0c))
* **controller:** defer API outages without poisoning pending work ([#551](https://github.com/gitstore-dev/GitStore/issues/551)) ([9aeea3a](https://github.com/gitstore-dev/GitStore/commit/9aeea3a97c8ad3fbbd33f2eda6414fa020760a6d))
* **controller:** treat empty checkpoint fanout as idle ([#554](https://github.com/gitstore-dev/GitStore/issues/554)) ([5d71e9b](https://github.com/gitstore-dev/GitStore/commit/5d71e9b05267f8371e8370f5a4639bd175bef9e2))


### Documentation

* align maintained guides with current runtime ([#556](https://github.com/gitstore-dev/GitStore/issues/556)) ([3d1e011](https://github.com/gitstore-dev/GitStore/commit/3d1e011a10f11d90f79be3502c8a27be6dcf5de0))

## [0.1.0-alpha.10](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.9...v0.1.0-alpha.10) (2026-11-09)


### ⚠ BREAKING CHANGES

* **observability:** record metrics through OpenTelemetry SDKs ([#543](https://github.com/gitstore-dev/GitStore/issues/543))

### Features

* **auth:** implement Kratos user directory and actor administration ([#454](https://github.com/gitstore-dev/GitStore/issues/454)) ([010a4bc](https://github.com/gitstore-dev/GitStore/commit/010a4bcd5c120f8886515cbc8a98fa54166e1a8c))
* **ownership:** complete resource owner-reference lifecycle ([#379](https://github.com/gitstore-dev/GitStore/issues/379)) ([845c8eb](https://github.com/gitstore-dev/GitStore/commit/845c8ebb93bd8c6e5b0d260ce27a9f1e977df256))


### Bug Fixes

* **admission:** finish endpoint-split push convergence acceptance ([#535](https://github.com/gitstore-dev/GitStore/issues/535)) ([fad8bbb](https://github.com/gitstore-dev/GitStore/commit/fad8bbba7ca219217d504dd807a2786d5a49156a))
* **capacity:** require controller readiness in managed runs ([#541](https://github.com/gitstore-dev/GitStore/issues/541)) ([52791c9](https://github.com/gitstore-dev/GitStore/commit/52791c9444162c073ea67b92759dc757806939a3))
* salvage Git negotiation and capacity metric correctness ([#544](https://github.com/gitstore-dev/GitStore/issues/544)) ([b5c3f97](https://github.com/gitstore-dev/GitStore/commit/b5c3f97471961c1e9c066f883d18eedea197113b))
* **scylla:** paginate owner dependents by kind ([#546](https://github.com/gitstore-dev/GitStore/issues/546)) ([e9cbe2d](https://github.com/gitstore-dev/GitStore/commit/e9cbe2d7b22d9ca62be52bde64f20fa891a625a3))


### Documentation

* **spec:** correct Status for specs 050 and 070 ([#539](https://github.com/gitstore-dev/GitStore/issues/539)) ([4551c9d](https://github.com/gitstore-dev/GitStore/commit/4551c9d4d2f1a2aee71f2f7449f260e0b4c22723))


### Code Refactoring

* **observability:** record metrics through OpenTelemetry SDKs ([#543](https://github.com/gitstore-dev/GitStore/issues/543)) ([1022add](https://github.com/gitstore-dev/GitStore/commit/1022add004df8586ee63c6cef5480d4ad3d11417))

## [0.1.0-alpha.9](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.8...v0.1.0-alpha.9) (2026-11-05)


### ⚠ BREAKING CHANGES

* **api:** replace /graphql and api.port with /admin/graphql on api.admin_port and /storefront/graphql on api.storefront_port; upgrade API clients and routing together.
* **scylla:** partition Products and isolate namespace catalog fences ([#527](https://github.com/gitstore-dev/GitStore/issues/527))
* **api:** File reads now use file(by: FileBy!); the flat namespace/name query arguments are removed.

### Features

* **api:** split Admin and Storefront GraphQL endpoints ([#531](https://github.com/gitstore-dev/GitStore/issues/531)) ([be97e94](https://github.com/gitstore-dev/GitStore/commit/be97e94223b8151b158c043260ced4fca75eea4f))
* **api:** unify resource metadata with scope-aware namespace validation ([#521](https://github.com/gitstore-dev/GitStore/issues/521)) ([4751d4e](https://github.com/gitstore-dev/GitStore/commit/4751d4e842851aac0b05aa6fdb68b594207dcae3))
* **auth:** bound authorization decision diagnostics ([#536](https://github.com/gitstore-dev/GitStore/issues/536)) ([21984ed](https://github.com/gitstore-dev/GitStore/commit/21984ed9d011a4839c1aaa8fc5e9ce6c65bf0e63))
* **auth:** expose bounded registry reload diagnostics ([#537](https://github.com/gitstore-dev/GitStore/issues/537)) ([824bb96](https://github.com/gitstore-dev/GitStore/commit/824bb96b9321ae04caba464863f7fe1b5d27ecec))
* **controller:** record consumed resource snapshots ([#533](https://github.com/gitstore-dev/GitStore/issues/533)) ([52498f3](https://github.com/gitstore-dev/GitStore/commit/52498f33cbd2687f6b8226bc5139c1152fa0b19e))


### Bug Fixes

* **api:** align File lookups with resource selectors ([#523](https://github.com/gitstore-dev/GitStore/issues/523)) ([50f2da6](https://github.com/gitstore-dev/GitStore/commit/50f2da6c3f1f1702d9baa543be7c8d6c91b0d1ca))
* **controller:** bind repository storage verification to UID ([#530](https://github.com/gitstore-dev/GitStore/issues/530)) ([3f08b65](https://github.com/gitstore-dev/GitStore/commit/3f08b656cf414d592a1be29e47cc829b286b3655))
* **controller:** preserve authored catalog revisions ([#534](https://github.com/gitstore-dev/GitStore/issues/534)) ([7e934fd](https://github.com/gitstore-dev/GitStore/commit/7e934fd882d8b420692eb79664f650d0e6de907d))
* **controller:** preserve category work across status conflicts ([#529](https://github.com/gitstore-dev/GitStore/issues/529)) ([4648798](https://github.com/gitstore-dev/GitStore/commit/4648798eea4a4cfca21e8db7fbc9a903deef627a))
* **scylla:** partition Products and isolate namespace catalog fences ([#527](https://github.com/gitstore-dev/GitStore/issues/527)) ([e84c66c](https://github.com/gitstore-dev/GitStore/commit/e84c66cb697cd63c554f5750d54ce0befa13205f))


### Documentation

* clarify resource storage groups and operational guidance ([#528](https://github.com/gitstore-dev/GitStore/issues/528)) ([b7645d9](https://github.com/gitstore-dev/GitStore/commit/b7645d99d1c31c7821b36f5bcd0689ad17a8a374))

## [0.1.0-alpha.8](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.7...v0.1.0-alpha.8) (2026-11-03)


### ⚠ BREAKING CHANGES

* **file:** select storage with spec.provider ([#515](https://github.com/gitstore-dev/GitStore/issues/515))
* **api:** Missing typed and Node resources return null with NOT_FOUND diagnostics instead of silent absence. Invalid batch IDs return indexed BAD_USER_INPUT errors while retaining other authorized results.

### Features

* **infra:** add DigitalOcean infrastructure provisioning ([#514](https://github.com/gitstore-dev/GitStore/issues/514)) ([5be0f8b](https://github.com/gitstore-dev/GitStore/commit/5be0f8bb4e930ac07ef0b4d40b44edb638bef1d2))
* **observability:** extract private bounded Go profiling ([#513](https://github.com/gitstore-dev/GitStore/issues/513)) ([d5da686](https://github.com/gitstore-dev/GitStore/commit/d5da6860a4141d94598b6f98db58438e212e429a))
* **observability:** route core signals through the collector ([#511](https://github.com/gitstore-dev/GitStore/issues/511)) ([5369cca](https://github.com/gitstore-dev/GitStore/commit/5369ccaab8fd0c126392fb730e5bad02614d26db))


### Bug Fixes

* **api:** unify GraphQL resource lookup error envelopes ([#510](https://github.com/gitstore-dev/GitStore/issues/510)) ([85f5994](https://github.com/gitstore-dev/GitStore/commit/85f599405aa1a5bae736716d132fc6ca1a788d49))
* **makefile:** separate controller signing generation and enrollment ([#517](https://github.com/gitstore-dev/GitStore/issues/517)) ([07ec053](https://github.com/gitstore-dev/GitStore/commit/07ec0530c9d260694c222506bf3be26c5ef45bc7))


### Documentation

* **architecture:** define app capabilities and installation contracts ([#507](https://github.com/gitstore-dev/GitStore/issues/507)) ([8ee50fd](https://github.com/gitstore-dev/GitStore/commit/8ee50fdf7b2efbe6001862fee1c2bafd44d0e5f9))
* **auth:** align workload assertions with reference OIDC and UserDir ([#508](https://github.com/gitstore-dev/GitStore/issues/508)) ([99a9d3a](https://github.com/gitstore-dev/GitStore/commit/99a9d3a32eb1f590e1d24ac68a3d3e59df93b6b5))
* **authorization:** explain local RBAC policies and examples ([#518](https://github.com/gitstore-dev/GitStore/issues/518)) ([dafde0a](https://github.com/gitstore-dev/GitStore/commit/dafde0a16182aa94671b7a3237973f5206410c16))
* restore GitStore branding assets and README banner ([#520](https://github.com/gitstore-dev/GitStore/issues/520)) ([242a769](https://github.com/gitstore-dev/GitStore/commit/242a76955799b97ecbb096b86f114deb7e91ece5))
* scope contributor instructions and general guardrails ([#519](https://github.com/gitstore-dev/GitStore/issues/519)) ([5ee783d](https://github.com/gitstore-dev/GitStore/commit/5ee783d539b82065286cbc448b6ededb9fc3e595))


### Code Refactoring

* **file:** select storage with spec.provider ([#515](https://github.com/gitstore-dev/GitStore/issues/515)) ([0065b76](https://github.com/gitstore-dev/GitStore/commit/0065b76c9d49fcfb0f66ac90b022558cf1164496))

## [0.1.0-alpha.7](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.6...v0.1.0-alpha.7) (2026-10-11)


### ⚠ BREAKING CHANGES

* **auth:** adopt canonical authorization and cluster identities ([#453](https://github.com/gitstore-dev/GitStore/issues/453))

### Features

* **auth:** adopt canonical authorization and cluster identities ([#453](https://github.com/gitstore-dev/GitStore/issues/453)) ([4c16dbc](https://github.com/gitstore-dev/GitStore/commit/4c16dbc53feff278605ca3f3eadf4578a966bd46))
* **auth:** authorize every complete*Deletion mutation as &lt;kind&gt;.purge ([#478](https://github.com/gitstore-dev/GitStore/issues/478)) ([aa839a6](https://github.com/gitstore-dev/GitStore/commit/aa839a6463297b1c33531aef47bfca81861679cf))
* **category:** scope products to resolved subtree ([#480](https://github.com/gitstore-dev/GitStore/issues/480)) ([41e68ff](https://github.com/gitstore-dev/GitStore/commit/41e68ff39c4791f6750d358531e4e57ea45144b7))


### Bug Fixes

* **api:** align namespace and repository generation semantics ([#483](https://github.com/gitstore-dev/GitStore/issues/483)) ([9eb0f6b](https://github.com/gitstore-dev/GitStore/commit/9eb0f6b4aa21fb55e397ada65f461ea88d45b2e7))
* **lifecycle:** align namespace and repository Git-backed deletion ([#485](https://github.com/gitstore-dev/GitStore/issues/485)) ([91ffa11](https://github.com/gitstore-dev/GitStore/commit/91ffa11eadf35d0448af286f4f5034aede820ef2))
* preserve CDC ordering and concurrent Product updates ([#486](https://github.com/gitstore-dev/GitStore/issues/486)) ([b11da97](https://github.com/gitstore-dev/GitStore/commit/b11da979e1556ef066ae683992675eaa03aaf16d))

## [0.1.0-alpha.6](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.5...v0.1.0-alpha.6) (2026-10-09)


### ⚠ BREAKING CHANGES

* **api:** Category.path/depth and DeleteCategoryPayload.deletedCategoryId/ orphanedProductIds are removed; Namespace mutation errors use the shared envelope codes; RESOURCE_VERSION_CONFLICT is folded into CONFLICT; category reads and mutations require the categoryTaxonomy.* actions. Roll out the API before the controllers.
* **config:** every renamed/removed config key and environment variable listed in docs/configuration.md's "Upgrading configuration" section now fails startup instead of silently falling back to a default. Operators must update config.toml and GITSTORE_* environment variables (including .env files, which this change does not touch) before upgrading. Highlights: auth.* -> api.auth.* (gitstore-api); auth.jwt.duration -> api.auth.jwt.ttl; auth.grpc.hmac_secret / GITSTORE_AUTH__GRPC__HMAC_SECRET -> grpc_auth.hmac_secret / GITSTORE_GRPC_AUTH__HMAC_SECRET (shared); datastore.* -> api.datastore.*; watch.namespace.* -> api.watch.journal.* (readers_enabled/materializer_enabled/cdc_retention_seconds/bucket_size removed); git.grpc.uri -> api.git_service.uri; grpc.port -> git_service. grpc_port; git.data_dir -> git_service.data_dir; git.repo.max_file_size / max_pack_size_bytes removed in favor of shared push_limits.max_file_size / max_pack_size (IEC size strings); hooks.git_receive_pack.* removed (always on); schema_validation.* / admission_control.* folded into git_service.validation.timeout / git_service.admission.branch_pattern; catalog_service.uri -> git_service.catalog.uri (now dns:///host:port); api.rate_limit_per_second/burst -> api.rate_limit.per_second/burst; controller.api_client.requests_per_second/burst -> controller.api_client.rate_limit.per_second/burst.
* **api:** the config.go `features.namespace_repository_fence` key and the GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE environment variable are no longer recognised; config.Load() now fails startup with a clear error if either is present, instead of silently ignoring them. The NAMESPACE_REPOSITORY_FENCE_DISABLED GraphQL error code is removed, along with NamespaceRepositoryFenceMode/NamespaceRepositoryFenceEnabled() and the empty Features/FeatureConfig struct.

### Features

* **api:** Git-backed category mutations, subtree filter and shared mutation error envelope ([#383](https://github.com/gitstore-dev/GitStore/issues/383)) ([d5f4d2a](https://github.com/gitstore-dev/GitStore/commit/d5f4d2aa4dccb42f4c9a4ffc4fd07cf024c033bd))


### Bug Fixes

* **api:** align namespace completion and delete input shapes with other kinds ([#461](https://github.com/gitstore-dev/GitStore/issues/461)) ([df6d841](https://github.com/gitstore-dev/GitStore/commit/df6d8416136b3e891fef94461decba0a860257f6))
* **api:** align Product Git mutations with admission ([#475](https://github.com/gitstore-dev/GitStore/issues/475)) ([b004f34](https://github.com/gitstore-dev/GitStore/commit/b004f34625178c2e450b53a86f086eecea43fa32))
* **api:** make namespace repository fence always on and remove TransferRepository ([#457](https://github.com/gitstore-dev/GitStore/issues/457)) ([b01979a](https://github.com/gitstore-dev/GitStore/commit/b01979ac605ccbd51288b1e89012094c4a852bcf))
* **api:** repair admission after API commits ([#473](https://github.com/gitstore-dev/GitStore/issues/473)) ([7cbb06a](https://github.com/gitstore-dev/GitStore/commit/7cbb06a1503c0584e2d7d224d62434301345a4ec))
* **controller:** complete namespace deletion with the current payload shape ([#456](https://github.com/gitstore-dev/GitStore/issues/456)) ([9bd7b87](https://github.com/gitstore-dev/GitStore/commit/9bd7b877a11d7a79377760534be4858d37837809))
* **scylla:** correct projection kind attribution ([#474](https://github.com/gitstore-dev/GitStore/issues/474)) ([04b523d](https://github.com/gitstore-dev/GitStore/commit/04b523deb3048959232fe4fc8b4655425e1ea82b))


### Documentation

* **adr:** define ADR status lifecycle and mark implemented decisions accepted ([#460](https://github.com/gitstore-dev/GitStore/issues/460)) ([4012a14](https://github.com/gitstore-dev/GitStore/commit/4012a14889edd5b1ad09338577197e81077b51fb))


### Code Refactoring

* **config:** group configuration by service and normalize durations, sizes and names ([#458](https://github.com/gitstore-dev/GitStore/issues/458)) ([ee19e7d](https://github.com/gitstore-dev/GitStore/commit/ee19e7d111b2b6dbde7c8e8ee8fd32b35603c6fc))

## [0.1.0-alpha.5](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.4...v0.1.0-alpha.5) (2026-10-05)


### ⚠ BREAKING CHANGES

* **controller:** bound recovery and harden capacity execution ([#452](https://github.com/gitstore-dev/GitStore/issues/452))

### Bug Fixes

* **controller:** bound recovery and harden capacity execution ([#452](https://github.com/gitstore-dev/GitStore/issues/452)) ([a97363a](https://github.com/gitstore-dev/GitStore/commit/a97363a503db8652cb718c8e3ce14c72ec8e9484))
* **gitctl:** surface the real cause of enroll-serviceaccount bootstrap login failures ([#448](https://github.com/gitstore-dev/GitStore/issues/448)) ([995f44a](https://github.com/gitstore-dev/GitStore/commit/995f44a7db21118aee7f5a171896031af9509b45))


### Documentation

* **spec:** roll up Status flips for 055, 058, 060-063 ([#450](https://github.com/gitstore-dev/GitStore/issues/450)) ([63aea4d](https://github.com/gitstore-dev/GitStore/commit/63aea4d6090c111026fdeeccdcaa5269a6e8a76b))

## [0.1.0-alpha.4](https://github.com/gitstore-dev/GitStore/compare/v1.0.0-alpha.3...v0.1.0-alpha.4) (2026-10-04)


### ⚠ BREAKING CHANGES

* **secrets:** File credentialsRef requires a typed wrapper and controller configuration uses canonical nested keys without legacy aliases.
* **catalog:** require Product spec.title, drop totalCount from connections ([#437](https://github.com/gitstore-dev/GitStore/issues/437))
* **api:** MetadataInput is renamed to ObjectMetaInput; ServiceAccount mutation payloads are reshaped to object-typed (serviceAccount/tokenRequest); TokenRequestSpec input becomes TokenRequestSpecInput with audiences/ expirationSeconds; TokenRequestStatus.expiresAt becomes expirationTimestamp.
* **api:** clients must migrate to metadata.name, metadata.namespace, spec.defaultBranch, status.resolved.{storagePath,storageClass}, and NamespaceBy.name; createdBy/updatedBy audit fields have no replacement and are no longer exposed over GraphQL.

### Features

* **api:** ADR-0010 §14 ownership assignment and transfer foundation ([#433](https://github.com/gitstore-dev/GitStore/issues/433)) ([2f50d2e](https://github.com/gitstore-dev/GitStore/commit/2f50d2e716a0614f7f232e3feb38939c14e0ba07))
* **api:** controller-manager service-account authentication (spec 061) ([#409](https://github.com/gitstore-dev/GitStore/issues/409)) ([874f27d](https://github.com/gitstore-dev/GitStore/commit/874f27ddb9eceb5aec20b28f45da14242fadb54b))
* **api:** object-typed ServiceAccount surface and unified ObjectMetaInput ([#429](https://github.com/gitstore-dev/GitStore/issues/429)) ([9a13196](https://github.com/gitstore-dev/GitStore/commit/9a13196ad23c70c3af687e2d9538baee7b706c51))
* **auth:** add oidc-jwt provider and optional Hydra/Kratos reference OIDC stack (spec 059) ([#401](https://github.com/gitstore-dev/GitStore/issues/401)) ([0d6041f](https://github.com/gitstore-dev/GitStore/commit/0d6041fa3d75d03e336b37f07800c1b413e09e71))
* **auth:** complete local multi-user static-users migration ([#405](https://github.com/gitstore-dev/GitStore/issues/405)) ([5325b97](https://github.com/gitstore-dev/GitStore/commit/5325b97cbecee8395a22a1f72a4d01c6cdd45da5))
* **capacity:** harden namespace watch production gate ([#416](https://github.com/gitstore-dev/GitStore/issues/416)) ([c9b2424](https://github.com/gitstore-dev/GitStore/commit/c9b24248912c53fd42f368256435f09f6162d81b))
* centralize git authorization context ([#414](https://github.com/gitstore-dev/GitStore/issues/414)) ([1e49049](https://github.com/gitstore-dev/GitStore/commit/1e4904992bb6e7c5c096f770d02ba2f731ecee17))
* **ci:** add Release Please for automated semver releases ([#384](https://github.com/gitstore-dev/GitStore/issues/384)) ([4abcb47](https://github.com/gitstore-dev/GitStore/commit/4abcb4761d48a1b1ce1b8733ff3b02053b82a189))
* **config:** add shared local compose profile ([#410](https://github.com/gitstore-dev/GitStore/issues/410)) ([a1ba400](https://github.com/gitstore-dev/GitStore/commit/a1ba400a2dcfa39cdfd9b0202e38eb601f58ac9c))
* **config:** support additive config-file overlays in api and controller-manager ([#442](https://github.com/gitstore-dev/GitStore/issues/442)) ([598a635](https://github.com/gitstore-dev/GitStore/commit/598a635071b108dfed918e10275075ab734005b3))
* **namespace:** add durable namespace watch contract ([#371](https://github.com/gitstore-dev/GitStore/issues/371)) ([b065762](https://github.com/gitstore-dev/GitStore/commit/b0657625c99a5835e8ac92bb8dad09893f29e0f8))
* **namespace:** enforce admission matrix ([#370](https://github.com/gitstore-dev/GitStore/issues/370)) ([b6d7c48](https://github.com/gitstore-dev/GitStore/commit/b6d7c48a44159b1a1af331e4aa01cae830027332))
* product category and readiness reconciliation ([#426](https://github.com/gitstore-dev/GitStore/issues/426)) ([0a38bd9](https://github.com/gitstore-dev/GitStore/commit/0a38bd9c91ae6254b88605103c8ecfead78045c0))
* **product:** add git-backed lifecycle foundations ([#380](https://github.com/gitstore-dev/GitStore/issues/380)) ([7fd2a80](https://github.com/gitstore-dev/GitStore/commit/7fd2a80d5e084d6452b9e11d90887d60255bf079))
* **repository:** add Git-backed lifecycle and durable watches ([#394](https://github.com/gitstore-dev/GitStore/issues/394)) ([84b7bb4](https://github.com/gitstore-dev/GitStore/commit/84b7bb4d5b86dc64dce0a8729e10e5e44f00ae0a))
* **secrets:** implement typed references and renewable controller credentials ([#439](https://github.com/gitstore-dev/GitStore/issues/439)) ([edb2d5e](https://github.com/gitstore-dev/GitStore/commit/edb2d5e62979f5e4f233f61b4addc116c46927c5))


### Bug Fixes

* **api:** resolve Product watch bootstrap bug, namespace quarantine, and add resource JSON schemas ([#415](https://github.com/gitstore-dev/GitStore/issues/415)) ([533a298](https://github.com/gitstore-dev/GitStore/commit/533a298434a4f5b84511eaaac0e0c612456a36ce))
* **api:** resource-specific status mutations, dead-code removal, and uid/id consistency ([#425](https://github.com/gitstore-dev/GitStore/issues/425)) ([42f7e95](https://github.com/gitstore-dev/GitStore/commit/42f7e95bfba16080352d899b56463f5e6663f094))
* **catalog:** require Product spec.title, drop totalCount from connections ([#437](https://github.com/gitstore-dev/GitStore/issues/437)) ([0717142](https://github.com/gitstore-dev/GitStore/commit/07171428837a0d289663c2d0175fe1883b69fff4))
* **ci:** activate prerelease versioning and bound initial release history ([#386](https://github.com/gitstore-dev/GitStore/issues/386)) ([09dde6d](https://github.com/gitstore-dev/GitStore/commit/09dde6db7acb74b4dda2bddf64d6c0f43da485ee))
* **ci:** don't emit an invalid sha tag on tag-triggered builds ([#389](https://github.com/gitstore-dev/GitStore/issues/389)) ([0238038](https://github.com/gitstore-dev/GitStore/commit/023803879aa38dfe03656acb9ef52d6970fc5807))
* **ci:** reseed manifest to 0.1.0-alpha.0 (minor-anchored, patch=0) ([#387](https://github.com/gitstore-dev/GitStore/issues/387)) ([eeb16fa](https://github.com/gitstore-dev/GitStore/commit/eeb16fa8fe3e4b2e468d2167325a29f5267a2033))
* **config:** remove dead cache.ttl key and wire auth.userdir.provider ([#411](https://github.com/gitstore-dev/GitStore/issues/411)) ([d4ba8dc](https://github.com/gitstore-dev/GitStore/commit/d4ba8dc9b406e205345bb4a3b26013d40eb50bbd))
* **controller-manager:** stop Repository/CategoryTaxonomy/Namespace reconcile self-trigger loops ([#428](https://github.com/gitstore-dev/GitStore/issues/428)) ([8fef66d](https://github.com/gitstore-dev/GitStore/commit/8fef66ddc96bcaabb07a767b33724d4db8e60a48))
* converge concurrent File admissions ([#402](https://github.com/gitstore-dev/GitStore/issues/402)) ([169ae20](https://github.com/gitstore-dev/GitStore/commit/169ae209bf25b65b5bc90474ba3121506c0e3216))
* de-flake CategoryTaxonomy cycle detection and Collection push tests ([#391](https://github.com/gitstore-dev/GitStore/issues/391)) ([c16f0f0](https://github.com/gitstore-dev/GitStore/commit/c16f0f046e569db2c604f1c8eb922930242ebff6))
* **deps:** patch open Dependabot/code-scanning security alerts ([#396](https://github.com/gitstore-dev/GitStore/issues/396)) ([b4af524](https://github.com/gitstore-dev/GitStore/commit/b4af5243e61243a3995e3bf30b8dfcbf848b1804))
* enforce File production-readiness boundaries ([#400](https://github.com/gitstore-dev/GitStore/issues/400)) ([c0fc464](https://github.com/gitstore-dev/GitStore/commit/c0fc464e107493753e9ad079b25e973b9806c1d0))
* EnsureSystemRepository now provisions gitstore-system on a fresh namespace ([#408](https://github.com/gitstore-dev/GitStore/issues/408)) ([f52a700](https://github.com/gitstore-dev/GitStore/commit/f52a700ce5e0e0a06d360169bfa5e76129448249))
* forward OwnerReferenceStore/CategoryTaxonomyDeletionStore through InstrumentedDatastore ([#406](https://github.com/gitstore-dev/GitStore/issues/406)) ([482b650](https://github.com/gitstore-dev/GitStore/commit/482b65081eca69150eeaa669dd9e0ff36875b23f))
* guard catalog deletes by resource version ([#399](https://github.com/gitstore-dev/GitStore/issues/399)) ([5ae2040](https://github.com/gitstore-dev/GitStore/commit/5ae204019a8d82ac64c9806ed438f12bb0e731d5))
* match condition-status casing to the GraphQL wire enum in CategoryTaxonomy reconciler ([#397](https://github.com/gitstore-dev/GitStore/issues/397)) ([64f507b](https://github.com/gitstore-dev/GitStore/commit/64f507b40a959dc737b76708c36e1ee8d8d203e9))
* match Namespace condition-status casing to the GraphQL wire enum ([#404](https://github.com/gitstore-dev/GitStore/issues/404)) ([9a254d1](https://github.com/gitstore-dev/GitStore/commit/9a254d1b638f302b61c1b9a4fb04c838312bc449))
* **release:** stop breaking changes from bumping major pre-1.0 ([#443](https://github.com/gitstore-dev/GitStore/issues/443)) ([569bb62](https://github.com/gitstore-dev/GitStore/commit/569bb62905d6233ba55afd59a2d9f21c0343cea6))
* resolve controller-manager and API shutdown test flakes ([#435](https://github.com/gitstore-dev/GitStore/issues/435)) ([0f87057](https://github.com/gitstore-dev/GitStore/commit/0f87057d87be3f5ab00e7c7b3a58cab36699f2eb))
* resolve Qodana findings and remove two dead proto fields ([#432](https://github.com/gitstore-dev/GitStore/issues/432)) ([5f5ee2a](https://github.com/gitstore-dev/GitStore/commit/5f5ee2a9c6552745bf1fe3eb2e8ab3fd22fe2cae))
* scope resource deletion to owning ref, de-flake DoesNotExist selector test ([#393](https://github.com/gitstore-dev/GitStore/issues/393)) ([d7af026](https://github.com/gitstore-dev/GitStore/commit/d7af026912f9a753c1acdf42adc03d4324c380eb))
* sort memdb label-selector matches and de-flake TestCollection_SelectorNotIn ([#407](https://github.com/gitstore-dev/GitStore/issues/407)) ([755a84c](https://github.com/gitstore-dev/GitStore/commit/755a84cc4878ccc12e529732713b9a3ea75e3c0d))


### Performance Improvements

* **namespace:** share watch tailer and add capacity harness ([#412](https://github.com/gitstore-dev/GitStore/issues/412)) ([ff28ca7](https://github.com/gitstore-dev/GitStore/commit/ff28ca7749f21d2175d9d9b14a14c0716b73a825))


### Documentation

* add ADR 0012 (admin/storefront endpoints) and ADR 0013 (Markdown IR) ([#434](https://github.com/gitstore-dev/GitStore/issues/434)) ([a7871e3](https://github.com/gitstore-dev/GitStore/commit/a7871e3484eabae6501d8ff555aa09f3c17a86d0))
* add ADRs for catalog release, lifecycle hooks, CRDs and controller fencing ([#438](https://github.com/gitstore-dev/GitStore/issues/438)) ([d065d08](https://github.com/gitstore-dev/GitStore/commit/d065d0808800fb4ce253aa29f92ef0d867c81d53))
* add production-readiness testing runbook ([#398](https://github.com/gitstore-dev/GitStore/issues/398)) ([66ea844](https://github.com/gitstore-dev/GitStore/commit/66ea844ef4c9dc21576500cd5b29d47e01ca1303))
* add secret material ADRs and authorization docs for spec 063 ([#431](https://github.com/gitstore-dev/GitStore/issues/431)) ([f384044](https://github.com/gitstore-dev/GitStore/commit/f3840447aaea1bc2d1ee140ccf87fcf937c95cca))
* flag gitstore-dev/quickstart drift risk in AGENTS.md ([#445](https://github.com/gitstore-dev/GitStore/issues/445)) ([f563e4c](https://github.com/gitstore-dev/GitStore/commit/f563e4cc8464e4fe16f1a30766c80f949bb7474d))
* introduce custom seller and buyer workflows with lifecycle management ([#422](https://github.com/gitstore-dev/GitStore/issues/422)) ([ab18f75](https://github.com/gitstore-dev/GitStore/commit/ab18f752b662582a73b6f65a9b377d576baf990f))
* **release:** fix inaccurate claim about local git rebase dropping empty commits ([a200143](https://github.com/gitstore-dev/GitStore/commit/a200143fe6e11d5de94f7dc9b9a61b39dfaaa947))
* **release:** warn that --allow-empty Release-As commits get dropped by rebase-merge ([9a72efe](https://github.com/gitstore-dev/GitStore/commit/9a72efe78df37d7ba46ff9379be5c89590b29e77))


### Miscellaneous

* release main ([ff3c7c0](https://github.com/gitstore-dev/GitStore/commit/ff3c7c09e872641163443394ddd8d1c963e10896))

## [1.0.0-alpha.3](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.3...v1.0.0-alpha.3) (2026-09-18)


### ⚠ BREAKING CHANGES

* **api:** clients must migrate to metadata.name, metadata.namespace, spec.defaultBranch, status.resolved.{storagePath,storageClass}, and NamespaceBy.name; createdBy/updatedBy audit fields have no replacement and are no longer exposed over GraphQL.

### Features

* **auth:** add oidc-jwt provider and optional Hydra/Kratos reference OIDC stack (spec 059) ([#401](https://github.com/gitstore-dev/GitStore/issues/401)) ([0d6041f](https://github.com/gitstore-dev/GitStore/commit/0d6041fa3d75d03e336b37f07800c1b413e09e71))
* **product:** add git-backed lifecycle foundations ([#380](https://github.com/gitstore-dev/GitStore/issues/380)) ([7fd2a80](https://github.com/gitstore-dev/GitStore/commit/7fd2a80d5e084d6452b9e11d90887d60255bf079))
* **repository:** add Git-backed lifecycle and durable watches ([#394](https://github.com/gitstore-dev/GitStore/issues/394)) ([84b7bb4](https://github.com/gitstore-dev/GitStore/commit/84b7bb4d5b86dc64dce0a8729e10e5e44f00ae0a))


### Bug Fixes

* **api:** resource-specific status mutations, dead-code removal, and uid/id consistency ([#425](https://github.com/gitstore-dev/GitStore/issues/425)) ([42f7e95](https://github.com/gitstore-dev/GitStore/commit/42f7e95bfba16080352d899b56463f5e6663f094))


### Documentation

* introduce custom seller and buyer workflows with lifecycle management ([#422](https://github.com/gitstore-dev/GitStore/issues/422)) ([ab18f75](https://github.com/gitstore-dev/GitStore/commit/ab18f752b662582a73b6f65a9b377d576baf990f))

## [0.1.0-alpha.3](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.2...v0.1.0-alpha.3) (2026-09-04)


### Features

* **api:** controller-manager service-account authentication (spec 061) ([#409](https://github.com/gitstore-dev/GitStore/issues/409)) ([874f27d](https://github.com/gitstore-dev/GitStore/commit/874f27ddb9eceb5aec20b28f45da14242fadb54b))
* **auth:** complete local multi-user static-users migration ([#405](https://github.com/gitstore-dev/GitStore/issues/405)) ([5325b97](https://github.com/gitstore-dev/GitStore/commit/5325b97cbecee8395a22a1f72a4d01c6cdd45da5))
* **capacity:** harden namespace watch production gate ([#416](https://github.com/gitstore-dev/GitStore/issues/416)) ([c9b2424](https://github.com/gitstore-dev/GitStore/commit/c9b24248912c53fd42f368256435f09f6162d81b))
* centralize git authorization context ([#414](https://github.com/gitstore-dev/GitStore/issues/414)) ([1e49049](https://github.com/gitstore-dev/GitStore/commit/1e4904992bb6e7c5c096f770d02ba2f731ecee17))
* **config:** add shared local compose profile ([#410](https://github.com/gitstore-dev/GitStore/issues/410)) ([a1ba400](https://github.com/gitstore-dev/GitStore/commit/a1ba400a2dcfa39cdfd9b0202e38eb601f58ac9c))
* **namespace:** add durable namespace watch contract ([#371](https://github.com/gitstore-dev/GitStore/issues/371)) ([b065762](https://github.com/gitstore-dev/GitStore/commit/b0657625c99a5835e8ac92bb8dad09893f29e0f8))
* **namespace:** enforce admission matrix ([#370](https://github.com/gitstore-dev/GitStore/issues/370)) ([b6d7c48](https://github.com/gitstore-dev/GitStore/commit/b6d7c48a44159b1a1af331e4aa01cae830027332))


### Bug Fixes

* **api:** resolve Product watch bootstrap bug, namespace quarantine, and add resource JSON schemas ([#415](https://github.com/gitstore-dev/GitStore/issues/415)) ([533a298](https://github.com/gitstore-dev/GitStore/commit/533a298434a4f5b84511eaaac0e0c612456a36ce))
* **config:** remove dead cache.ttl key and wire auth.userdir.provider ([#411](https://github.com/gitstore-dev/GitStore/issues/411)) ([d4ba8dc](https://github.com/gitstore-dev/GitStore/commit/d4ba8dc9b406e205345bb4a3b26013d40eb50bbd))
* converge concurrent File admissions ([#402](https://github.com/gitstore-dev/GitStore/issues/402)) ([169ae20](https://github.com/gitstore-dev/GitStore/commit/169ae209bf25b65b5bc90474ba3121506c0e3216))
* de-flake CategoryTaxonomy cycle detection and Collection push tests ([#391](https://github.com/gitstore-dev/GitStore/issues/391)) ([c16f0f0](https://github.com/gitstore-dev/GitStore/commit/c16f0f046e569db2c604f1c8eb922930242ebff6))
* **deps:** patch open Dependabot/code-scanning security alerts ([#396](https://github.com/gitstore-dev/GitStore/issues/396)) ([b4af524](https://github.com/gitstore-dev/GitStore/commit/b4af5243e61243a3995e3bf30b8dfcbf848b1804))
* enforce File production-readiness boundaries ([#400](https://github.com/gitstore-dev/GitStore/issues/400)) ([c0fc464](https://github.com/gitstore-dev/GitStore/commit/c0fc464e107493753e9ad079b25e973b9806c1d0))
* EnsureSystemRepository now provisions gitstore-system on a fresh namespace ([#408](https://github.com/gitstore-dev/GitStore/issues/408)) ([f52a700](https://github.com/gitstore-dev/GitStore/commit/f52a700ce5e0e0a06d360169bfa5e76129448249))
* forward OwnerReferenceStore/CategoryTaxonomyDeletionStore through InstrumentedDatastore ([#406](https://github.com/gitstore-dev/GitStore/issues/406)) ([482b650](https://github.com/gitstore-dev/GitStore/commit/482b65081eca69150eeaa669dd9e0ff36875b23f))
* guard catalog deletes by resource version ([#399](https://github.com/gitstore-dev/GitStore/issues/399)) ([5ae2040](https://github.com/gitstore-dev/GitStore/commit/5ae204019a8d82ac64c9806ed438f12bb0e731d5))
* match condition-status casing to the GraphQL wire enum in CategoryTaxonomy reconciler ([#397](https://github.com/gitstore-dev/GitStore/issues/397)) ([64f507b](https://github.com/gitstore-dev/GitStore/commit/64f507b40a959dc737b76708c36e1ee8d8d203e9))
* match Namespace condition-status casing to the GraphQL wire enum ([#404](https://github.com/gitstore-dev/GitStore/issues/404)) ([9a254d1](https://github.com/gitstore-dev/GitStore/commit/9a254d1b638f302b61c1b9a4fb04c838312bc449))
* scope resource deletion to owning ref, de-flake DoesNotExist selector test ([#393](https://github.com/gitstore-dev/GitStore/issues/393)) ([d7af026](https://github.com/gitstore-dev/GitStore/commit/d7af026912f9a753c1acdf42adc03d4324c380eb))
* sort memdb label-selector matches and de-flake TestCollection_SelectorNotIn ([#407](https://github.com/gitstore-dev/GitStore/issues/407)) ([755a84c](https://github.com/gitstore-dev/GitStore/commit/755a84cc4878ccc12e529732713b9a3ea75e3c0d))


### Performance Improvements

* **namespace:** share watch tailer and add capacity harness ([#412](https://github.com/gitstore-dev/GitStore/issues/412)) ([ff28ca7](https://github.com/gitstore-dev/GitStore/commit/ff28ca7749f21d2175d9d9b14a14c0716b73a825))


### Documentation

* add production-readiness testing runbook ([#398](https://github.com/gitstore-dev/GitStore/issues/398)) ([66ea844](https://github.com/gitstore-dev/GitStore/commit/66ea844ef4c9dc21576500cd5b29d47e01ca1303))

## [0.1.0-alpha.2](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.1...v0.1.0-alpha.2) (2026-08-29)


### Bug Fixes

* **ci:** don't emit an invalid sha tag on tag-triggered builds ([#389](https://github.com/gitstore-dev/GitStore/issues/389)) ([0238038](https://github.com/gitstore-dev/GitStore/commit/023803879aa38dfe03656acb9ef52d6970fc5807))

## [0.1.0-alpha.1](https://github.com/gitstore-dev/GitStore/compare/v0.1.0-alpha.0...v0.1.0-alpha.1) (2026-08-29)


### Features

* **ci:** add Release Please for automated semver releases ([#384](https://github.com/gitstore-dev/GitStore/issues/384)) ([4abcb47](https://github.com/gitstore-dev/GitStore/commit/4abcb4761d48a1b1ce1b8733ff3b02053b82a189))


### Bug Fixes

* **ci:** activate prerelease versioning and bound initial release history ([#386](https://github.com/gitstore-dev/GitStore/issues/386)) ([09dde6d](https://github.com/gitstore-dev/GitStore/commit/09dde6db7acb74b4dda2bddf64d6c0f43da485ee))
* **ci:** reseed manifest to 0.1.0-alpha.0 (minor-anchored, patch=0) ([#387](https://github.com/gitstore-dev/GitStore/issues/387)) ([eeb16fa](https://github.com/gitstore-dev/GitStore/commit/eeb16fa8fe3e4b2e468d2167325a29f5267a2033))
