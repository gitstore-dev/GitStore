# E2E Tests for GitStore Admin UI

End-to-end tests for the GitStore Admin UI using Playwright.

## Tests

### T082: Product CRUD Workflow (`product_crud.spec.ts`)
Tests the complete product management lifecycle:
- **Login**: Authenticate to admin interface
- **Create**: Add new products with full form validation
- **Read**: View products in list and search functionality
- **Update**: Edit product details with optimistic locking
- **Delete**: Remove products from catalog
- **Validation**: Test form validation errors
- **Concurrency**: Test optimistic locking on concurrent edits
- **Markdown**: Test markdown editor and preview
- **Auto-generation**: Test slug auto-generation from title

### T083: Category Drag-and-Drop Reordering (`category_reorder.spec.ts`)
Tests the drag-and-drop category reordering functionality:
- **Setup**: Create test categories
- **Drag and Drop**: Reorder categories via mouse interactions
- **Persistence**: Verify order persists after page reload
- **Visual Feedback**: Test dragging styles and indicators
- **Hierarchical**: Test parent/child category reordering
- **Keyboard**: Test escape key to cancel drag
- **Display**: Test category count and hierarchy display

## Prerequisites

1. **Services Running**: Ensure the following services are running:
   - API server (http://localhost:4000)
   - Admin UI dev server (http://localhost:4321)
   - Git server (if testing publish functionality)

2. **Test Data**: Tests will create and clean up their own test data

3. **Admin Credentials**: Tests use default credentials:
   - Username: `admin`
   - Password: `admin`

## Running Tests

### Install Dependencies
```bash
cd admin-ui
npm install
npx playwright install
```

### Run All E2E Tests
```bash
npm run test:e2e
```

### Run Specific Test
```bash
npx playwright test product_crud
npx playwright test category_reorder
```

### Run in Headed Mode (Watch)
```bash
npx playwright test --headed
```

### Run in UI Mode (Interactive)
```bash
npx playwright test --ui
```

### Run in Debug Mode
```bash
npx playwright test --debug
```

### Run Specific Browser
```bash
npx playwright test --project=chromium
npx playwright test --project=firefox
npx playwright test --project=webkit
```

## Test Reports

After running tests, view the HTML report:
```bash
npx playwright show-report
```

## CI/CD Integration

Tests are configured to run in CI with:
- **Retries**: 2 retries on failure (CI only)
- **Workers**: 1 worker in CI (sequential execution)
- **Screenshots**: Captured on failure
- **Traces**: Captured on first retry

## Configuration

Test configuration is in `playwright.config.ts`:
- **Base URL**: http://localhost:4321
- **Timeout**: 120 seconds for web server startup
- **Browsers**: Chromium, Firefox, WebKit
- **Test Directory**: `tests/e2e/`

## Writing New Tests

Follow these patterns when adding new E2E tests:

### Test Structure
```typescript
import { test, expect } from '@playwright/test';

test.describe('Feature Name', () => {
  test.beforeEach(async ({ page }) => {
    // Login and setup
  });

  test('should do something', async ({ page }) => {
    await test.step('Step 1', async () => {
      // Test logic
    });
  });
});
```

### Best Practices
1. **Use data attributes**: Prefer `data-testid` over CSS selectors
2. **Wait for network**: Use `waitForURL`, `waitForSelector` appropriately
3. **Clean up**: Each test should clean up its own data
4. **Independence**: Tests should not depend on each other
5. **Assertions**: Use meaningful assertions with clear error messages
6. **Timeouts**: Set reasonable timeouts for async operations

## Troubleshooting

### Web Server Not Starting
```bash
# Manually start the dev server
npm run dev

# Then run tests with existing server
npx playwright test
```

### Authentication Issues
Verify admin credentials are set correctly in environment or test configuration.

### Flaky Tests
- Increase timeouts for slow operations
- Add explicit waits for dynamic content
- Check for race conditions in async operations

### Debug Specific Test
```bash
npx playwright test category_reorder --debug --headed
```

## Screenshots and Videos

- **Screenshots**: Captured on failure in `test-results/`
- **Videos**: Can be enabled in `playwright.config.ts`
- **Traces**: View with `npx playwright show-trace trace.zip`

## Resources

- [Playwright Documentation](https://playwright.dev)
- [Best Practices](https://playwright.dev/docs/best-practices)
- [Debugging Guide](https://playwright.dev/docs/debug)
