# Changelog

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
