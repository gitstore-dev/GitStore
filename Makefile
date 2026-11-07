TARGET ?= all
ADMIN := gitstore-admin
.PHONY: help install dev build lint test check pr-ready
help:
	@printf 'GitStore Admin\n\nmake install\nmake dev TARGET=admin|storybook\nmake build TARGET=admin|storybook|all\nmake lint\nmake test TARGET=unit|e2e|all\nmake check TARGET=all\nmake pr-ready\n'
install:
	pnpm --dir $(ADMIN) install --frozen-lockfile
dev:
ifeq ($(TARGET),storybook)
	pnpm --dir $(ADMIN) storybook
else
	pnpm --dir $(ADMIN) dev
endif
build:
ifeq ($(TARGET),storybook)
	pnpm --dir $(ADMIN) storybook:build
else ifeq ($(TARGET),admin)
	pnpm --dir $(ADMIN) build
else
	pnpm --dir $(ADMIN) build
	pnpm --dir $(ADMIN) storybook:build
endif
lint:
	pnpm --dir $(ADMIN) lint
test:
ifeq ($(TARGET),e2e)
	pnpm --dir $(ADMIN) test:e2e $(E2E_ARGS)
else ifeq ($(TARGET),unit)
	pnpm --dir $(ADMIN) test
else
	pnpm --dir $(ADMIN) test
	pnpm --dir $(ADMIN) test:e2e $(E2E_ARGS)
endif
check:
	pnpm --dir $(ADMIN) typecheck
	pnpm --dir $(ADMIN) lint
	pnpm --dir $(ADMIN) test
	$(MAKE) build TARGET=all
pr-ready: check
	$(MAKE) test TARGET=e2e
