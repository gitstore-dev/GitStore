import { test, expect } from '@playwright/test';

/**
 * E2E Test: Product CRUD Workflow (T082)
 *
 * Tests the complete product lifecycle:
 * 1. Login to admin interface
 * 2. Create a new product
 * 3. View product in list
 * 4. Edit product details
 * 5. Delete product
 *
 * This test validates the full product management workflow from the
 * Admin UI perspective, ensuring all CRUD operations work end-to-end.
 */

// ProductList and ProductForm still use mock data — live GraphQL mutations not yet wired.
// See tests/e2e/STATUS.md. Re-enable the suite once the UI is wired to real backend calls.
test.describe.skip('Product CRUD Workflow', () => {
  // Setup: Login before each test
  test.beforeEach(async ({ page }) => {
    // Navigate to login page
    await page.goto('/login');

    // Fill in login credentials
    await page.fill('input[name="username"]', process.env.E2E_ADMIN_USERNAME ?? 'admin');
    await page.fill('input[name="password"]', process.env.E2E_ADMIN_PASSWORD ?? 'admin123');

    // Submit login form
    await page.click('button[type="submit"]');

    // Wait for redirect to products page
    await page.waitForURL('**/products/**');
    await expect(page).toHaveURL(/\/products/);
  });

  test('should complete full product CRUD lifecycle', async ({ page }) => {
    // Step 1: Create a new product
    await test.step('Create Product', async () => {
      // Navigate to new product page
      await page.click('a[href="/products/new"]');
      await expect(page).toHaveURL('/products/new');

      // Fill in product form
      await page.fill('input[name="title"]', 'Test Product E2E');
      await page.fill('input[name="slug"]', 'test-product-e2e');
      await page.fill('input[name="sku"]', 'TEST-E2E-001');
      await page.fill('input[name="price"]', '49.99');
      await page.fill('input[name="inventory"]', '100');

      // Select status
      await page.selectOption('select[name="status"]', 'published');

      // Fill in description (markdown editor)
      await page.fill('textarea[name="description"]', '## Test Product\n\nThis is a test product for E2E testing.');

      // Submit form
      await page.click('button[type="submit"]');

      // Wait for success and redirect to products list
      await page.waitForURL('**/products/**');

      // Verify product appears in list
      await expect(page.locator('text=Test Product E2E')).toBeVisible();
    });

    // Step 2: Search for the product
    await test.step('Search for Product', async () => {
      // Use search functionality
      await page.fill('input[placeholder*="Search"]', 'Test Product E2E');

      // Verify filtered results
      await expect(page.locator('text=Test Product E2E')).toBeVisible();

      // Clear search
      await page.fill('input[placeholder*="Search"]', '');
    });

    // Step 3: View product details
    await test.step('View Product Details', async () => {
      // Find the product row and click edit
      const productRow = page.locator('tr:has-text("Test Product E2E")');
      await productRow.locator('a:has-text("Edit")').click();

      // Verify we're on the edit page
      await expect(page).toHaveURL(/\/products\/prod_/);

      // Verify form fields are populated
      await expect(page.locator('input[name="title"]')).toHaveValue('Test Product E2E');
      await expect(page.locator('input[name="sku"]')).toHaveValue('TEST-E2E-001');
      await expect(page.locator('input[name="price"]')).toHaveValue('49.99');
    });

    // Step 4: Edit product
    await test.step('Edit Product', async () => {
      // Update product details
      await page.fill('input[name="title"]', 'Test Product E2E Updated');
      await page.fill('input[name="price"]', '59.99');
      await page.fill('input[name="inventory"]', '150');

      // Update description
      await page.fill('textarea[name="description"]', '## Updated Test Product\n\nThis product has been updated.');

      // Submit update
      await page.click('button[type="submit"]:has-text("Save")');

      // Wait for success
      await page.waitForURL('**/products/**');

      // Verify updated product appears in list
      await expect(page.locator('text=Test Product E2E Updated')).toBeVisible();

      // Verify price is updated in the list
      const productRow = page.locator('tr:has-text("Test Product E2E Updated")');
      await expect(productRow).toContainText('$59.99');
    });

    // Step 5: Delete product
    await test.step('Delete Product', async () => {
      // Find the product row
      const productRow = page.locator('tr:has-text("Test Product E2E Updated")');

      // Click delete button
      await productRow.locator('button:has-text("Delete")').click();

      // Confirm deletion in dialog
      page.on('dialog', dialog => dialog.accept());

      // Wait for deletion to complete
      await page.waitForTimeout(1000);

      // Verify product no longer appears in list
      await expect(page.locator('text=Test Product E2E Updated')).not.toBeVisible();
    });
  });

  test('should handle validation errors on create', async ({ page }) => {
    // Navigate to new product page
    await page.click('a[href="/products/new"]');

    // Try to submit without required fields
    await page.click('button[type="submit"]');

    // Verify validation errors are shown
    await expect(page.locator('text=/Title is required|required/i')).toBeVisible();
  });

  test('should handle optimistic locking on concurrent edits', async ({ page, context }) => {
    // Create a product first
    await page.click('a[href="/products/new"]');
    await page.fill('input[name="title"]', 'Concurrency Test Product');
    await page.fill('input[name="slug"]', 'concurrency-test');
    await page.fill('input[name="sku"]', 'CONC-001');
    await page.fill('input[name="price"]', '29.99');
    await page.click('button[type="submit"]');
    await page.waitForURL('**/products/**');

    // Open product in first tab
    const productRow = page.locator('tr:has-text("Concurrency Test Product")');
    await productRow.locator('a:has-text("Edit")').click();
    await expect(page).toHaveURL(/\/products\/prod_/);

    // Open same product in second tab
    const page2 = await context.newPage();
    await page2.goto(page.url());

    // Edit in first tab
    await page.fill('input[name="title"]', 'Concurrency Test - Tab 1');
    await page.click('button[type="submit"]:has-text("Save")');
    await page.waitForURL('**/products/**');

    // Try to edit in second tab (should trigger conflict)
    await page2.fill('input[name="title"]', 'Concurrency Test - Tab 2');
    await page2.click('button[type="submit"]:has-text("Save")');

    // Should show conflict modal or error
    // Note: Exact behavior depends on implementation
    await expect(
      page2.locator('text=/conflict|version mismatch|concurrent/i')
    ).toBeVisible({ timeout: 5000 });

    // Cleanup
    await page2.close();
  });

  test('should preview markdown description', async ({ page }) => {
    await page.click('a[href="/products/new"]');

    // Fill in markdown description
    await page.fill('textarea[name="description"]', '# Heading\n\n**Bold** and *italic*');

    // Toggle preview
    const previewButton = page.locator('button:has-text("Preview")');
    if (await previewButton.isVisible()) {
      await previewButton.click();

      // Verify markdown is rendered
      await expect(page.locator('h1:has-text("Heading")')).toBeVisible();
      await expect(page.locator('strong:has-text("Bold")')).toBeVisible();
      await expect(page.locator('em:has-text("italic")')).toBeVisible();
    }
  });

  test('should auto-generate slug from title', async ({ page }) => {
    await page.click('a[href="/products/new"]');

    // Type title
    await page.fill('input[name="title"]', 'My Awesome Product');

    // Blur to trigger slug generation
    await page.locator('input[name="title"]').blur();

    // Verify slug is auto-generated
    await expect(page.locator('input[name="slug"]')).toHaveValue('my-awesome-product');
  });
});
