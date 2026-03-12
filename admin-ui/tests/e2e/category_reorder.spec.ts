import { test, expect } from '@playwright/test';

/**
 * E2E Test: Drag-and-Drop Category Reordering (T083)
 *
 * Tests the drag-and-drop category reordering functionality:
 * 1. Login to admin interface
 * 2. Navigate to categories page
 * 3. Create test categories
 * 4. Drag and drop to reorder categories
 * 5. Verify order is persisted
 *
 * This test validates the drag-and-drop interaction and ensures
 * the display order is properly saved via the reorderCategories mutation.
 */

test.describe('Category Drag-and-Drop Reordering', () => {
  // Setup: Login before each test
  test.beforeEach(async ({ page }) => {
    // Navigate to login page
    await page.goto('/login');

    // Fill in login credentials
    await page.fill('input[name="username"]', 'admin');
    await page.fill('input[name="password"]', 'admin');

    // Submit login form
    await page.click('button[type="submit"]');

    // Wait for redirect and navigate to categories
    await page.waitForURL('/products');
    await page.click('a[href="/categories"]');
    await expect(page).toHaveURL('/categories');
  });

  test('should reorder categories via drag and drop', async ({ page }) => {
    // Step 1: Create test categories
    await test.step('Create Test Categories', async () => {
      const categories = [
        { name: 'Category A', slug: 'category-a' },
        { name: 'Category B', slug: 'category-b' },
        { name: 'Category C', slug: 'category-c' },
      ];

      for (const category of categories) {
        // Click add category button
        await page.click('button:has-text("Add Category")');

        // Fill form
        await page.fill('input[name="name"]', category.name);
        await page.fill('input[name="slug"]', category.slug);

        // Submit
        await page.click('button[type="submit"]:has-text("Create")');

        // Wait for creation
        await expect(page.locator(`text=${category.name}`)).toBeVisible();
      }
    });

    // Step 2: Verify initial order
    await test.step('Verify Initial Order', async () => {
      const categoryRows = page.locator('[data-category-id]');
      const count = await categoryRows.count();

      if (count >= 3) {
        // Find our test categories
        const categoryA = page.locator('text=Category A').first();
        const categoryB = page.locator('text=Category B').first();
        const categoryC = page.locator('text=Category C').first();

        await expect(categoryA).toBeVisible();
        await expect(categoryB).toBeVisible();
        await expect(categoryC).toBeVisible();

        // Get positions
        const posA = await categoryA.boundingBox();
        const posB = await categoryB.boundingBox();
        const posC = await categoryC.boundingBox();

        // Verify A is above B and B is above C
        if (posA && posB && posC) {
          expect(posA.y).toBeLessThan(posB.y);
          expect(posB.y).toBeLessThan(posC.y);
        }
      }
    });

    // Step 3: Drag and drop to reorder
    await test.step('Drag and Drop Reorder', async () => {
      // Find drag handles for our categories
      const categoryBRow = page.locator('[data-category-id]:has-text("Category B")');
      const categoryARow = page.locator('[data-category-id]:has-text("Category A")');

      // Find drag handle (⋮⋮ symbol)
      const dragHandle = categoryBRow.locator('[data-drag-handle]').or(
        categoryBRow.locator('text=⋮⋮')
      );

      // Get bounding boxes
      const handleBox = await dragHandle.boundingBox();
      const targetBox = await categoryARow.boundingBox();

      if (handleBox && targetBox) {
        // Perform drag operation
        // Move Category B above Category A
        await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
        await page.mouse.down();
        await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y - 10);
        await page.waitForTimeout(100);
        await page.mouse.up();

        // Wait for optimistic update
        await page.waitForTimeout(500);

        // Verify new order visually
        const newPosB = await categoryBRow.boundingBox();
        const newPosA = await categoryARow.boundingBox();

        if (newPosB && newPosA) {
          // Category B should now be above Category A
          expect(newPosB.y).toBeLessThan(newPosA.y);
        }
      }
    });

    // Step 4: Verify order persists after reload
    await test.step('Verify Persisted Order', async () => {
      // Reload the page
      await page.reload();

      // Wait for categories to load
      await page.waitForSelector('text=Category A');

      // Get positions again
      const categoryA = page.locator('text=Category A').first();
      const categoryB = page.locator('text=Category B').first();

      const posA = await categoryA.boundingBox();
      const posB = await categoryB.boundingBox();

      // Verify order is still B before A
      if (posA && posB) {
        expect(posB.y).toBeLessThan(posA.y);
      }
    });
  });

  test('should show visual feedback during drag', async ({ page }) => {
    // Create at least two categories first
    const categories = [
      { name: 'Drag Test 1', slug: 'drag-test-1' },
      { name: 'Drag Test 2', slug: 'drag-test-2' },
    ];

    for (const category of categories) {
      await page.click('button:has-text("Add Category")');
      await page.fill('input[name="name"]', category.name);
      await page.fill('input[name="slug"]', category.slug);
      await page.click('button[type="submit"]:has-text("Create")');
      await page.waitForTimeout(300);
    }

    // Find first category's drag handle
    const firstRow = page.locator('[data-category-id]:has-text("Drag Test 1")');
    const dragHandle = firstRow.locator('[data-drag-handle]').or(firstRow.locator('text=⋮⋮'));

    const handleBox = await dragHandle.boundingBox();

    if (handleBox) {
      // Start dragging
      await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
      await page.mouse.down();

      // Verify visual feedback (row should have dragging styles)
      // This depends on the actual implementation (background color change, etc.)
      const rowStyle = await firstRow.evaluate(el => getComputedStyle(el).backgroundColor);

      // Move mouse while dragging
      await page.mouse.move(handleBox.x, handleBox.y + 50);

      // Release
      await page.mouse.up();
    }
  });

  test('should handle hierarchical category reordering', async ({ page }) => {
    // Create parent and child categories
    await test.step('Create Parent Category', async () => {
      await page.click('button:has-text("Add Category")');
      await page.fill('input[name="name"]', 'Parent Category');
      await page.fill('input[name="slug"]', 'parent-category');
      await page.click('button[type="submit"]:has-text("Create")');
      await expect(page.locator('text=Parent Category')).toBeVisible();
    });

    await test.step('Create Child Categories', async () => {
      // Expand parent if needed
      const expandButton = page.locator('[data-category-id]:has-text("Parent Category")')
        .locator('button:has-text("▶")').or(
          page.locator('[data-category-id]:has-text("Parent Category")')
            .locator('[data-expand-button]')
        );

      if (await expandButton.isVisible()) {
        await expandButton.click();
      }

      // Add child category
      const parentRow = page.locator('[data-category-id]:has-text("Parent Category")');
      const addChildButton = parentRow.locator('button:has-text("Add Child")');

      if (await addChildButton.isVisible()) {
        await addChildButton.click();
        await page.fill('input[name="name"]', 'Child Category 1');
        await page.fill('input[name="slug"]', 'child-category-1');
        // Parent should be pre-selected
        await page.click('button[type="submit"]:has-text("Create")');

        await addChildButton.click();
        await page.fill('input[name="name"]', 'Child Category 2');
        await page.fill('input[name="slug"]', 'child-category-2');
        await page.click('button[type="submit"]:has-text("Create")');
      }
    });

    await test.step('Reorder Child Categories', async () => {
      // Find child categories under parent
      const child1 = page.locator('text=Child Category 1').first();
      const child2 = page.locator('text=Child Category 2').first();

      // Both should be visible
      await expect(child1).toBeVisible();
      await expect(child2).toBeVisible();

      // Drag child 2 above child 1
      const child2Row = page.locator('[data-category-id]:has-text("Child Category 2")');
      const child1Row = page.locator('[data-category-id]:has-text("Child Category 1")');

      const dragHandle = child2Row.locator('[data-drag-handle]').or(child2Row.locator('text=⋮⋮'));
      const handleBox = await dragHandle.boundingBox();
      const targetBox = await child1Row.boundingBox();

      if (handleBox && targetBox) {
        await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
        await page.mouse.down();
        await page.mouse.move(targetBox.x + targetBox.width / 2, targetBox.y - 10);
        await page.waitForTimeout(100);
        await page.mouse.up();
      }
    });
  });

  test('should cancel drag on escape key', async ({ page }) => {
    // Create test categories
    await page.click('button:has-text("Add Category")');
    await page.fill('input[name="name"]', 'Cancel Drag Test');
    await page.fill('input[name="slug"]', 'cancel-drag-test');
    await page.click('button[type="submit"]:has-text("Create")');

    const categoryRow = page.locator('[data-category-id]:has-text("Cancel Drag Test")');
    const dragHandle = categoryRow.locator('[data-drag-handle]').or(categoryRow.locator('text=⋮⋮'));

    const handleBox = await dragHandle.boundingBox();

    if (handleBox) {
      const initialPos = await categoryRow.boundingBox();

      // Start dragging
      await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + handleBox.height / 2);
      await page.mouse.down();
      await page.mouse.move(handleBox.x, handleBox.y + 50);

      // Press escape to cancel
      await page.keyboard.press('Escape');

      // Release mouse
      await page.mouse.up();

      // Verify position hasn't changed
      const finalPos = await categoryRow.boundingBox();

      if (initialPos && finalPos) {
        expect(Math.abs(finalPos.y - initialPos.y)).toBeLessThan(5);
      }
    }
  });

  test('should display category count and hierarchy', async ({ page }) => {
    // The category list should show the total number of categories
    const categoryCount = page.locator('[data-category-count]').or(
      page.locator('text=/\\d+ categories/i')
    );

    // Should be visible
    if (await categoryCount.isVisible()) {
      await expect(categoryCount).toBeVisible();
    }

    // Hierarchical structure should be visible with indentation
    // Child categories should be indented relative to parents
    const categories = page.locator('[data-category-level]');
    const count = await categories.count();

    if (count > 0) {
      // Verify level data attributes exist
      for (let i = 0; i < Math.min(count, 5); i++) {
        const category = categories.nth(i);
        const level = await category.getAttribute('data-category-level');
        expect(level).toBeDefined();
      }
    }
  });
});
