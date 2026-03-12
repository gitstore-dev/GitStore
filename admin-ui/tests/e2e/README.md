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

### Option 1: Auto-start Dev Server (Recommended)
Playwright will automatically start the dev server when you run tests. Just ensure:
- API server is running on port 4000
- Port 4321 is available for the dev server
- Dependencies are installed (`npm install`)

### Option 2: Manual Server Start
If you prefer to start servers manually:
1. **API Server**: Start on http://localhost:4000
   ```bash
   cd api && go run cmd/server/main.go
   ```

2. **Admin UI Dev Server**: Start on http://localhost:4321
   ```bash
   cd admin-ui && npm run dev
   ```

3. **Run tests with existing server**:
   ```bash
   # Add this to playwright.config.ts webServer section:
   # reuseExistingServer: true
   npm run test:e2e
   ```

### Test Data
Tests will create and clean up their own test data automatically.

### Admin Credentials
Tests use default credentials:
- Username: `admin`
- Password: `admin`

⚠️ **Important**: The API server must be running before starting tests!

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

### "Timed out waiting from config.webServer"
This means Playwright couldn't start the dev server. Common causes:

1. **API server not running**: Start the API server first
   ```bash
   cd api && go run cmd/server/main.go
   ```

2. **Port 4321 already in use**: Kill the process or change the port
   ```bash
   lsof -ti:4321 | xargs kill -9
   ```

3. **Dependencies not installed**: Install npm packages
   ```bash
   npm install
   ```

### Manual Server Approach
If auto-start isn't working, start servers manually:

```bash
# Terminal 1: Start API
cd api && go run cmd/server/main.go

# Terminal 2: Start Admin UI
cd admin-ui && npm run dev

# Terminal 3: Run tests (server will be reused)
cd admin-ui && npx playwright test
```

### Authentication Issues
Verify admin credentials are set correctly in environment or test configuration.

### API Connection Errors
Ensure the API server is running and accessible at http://localhost:4000/graphql

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
