import { test, expect } from '@playwright/test';

test('debug console errors', async ({ page }) => {
  // Capture console messages
  const messages: string[] = [];
  const errors: string[] = [];

  page.on('console', msg => {
    const text = `[${msg.type()}] ${msg.text()}`;
    messages.push(text);
    if (msg.type() === 'error') {
      errors.push(text);
    }
    console.log(text);
  });

  // Capture page errors
  page.on('pageerror', error => {
    const text = `[PAGE ERROR] ${error.message}`;
    errors.push(text);
    console.log(text);
  });

  // Navigate to login
  console.log('=== Navigating to /login ===');
  await page.goto('/login');
  await page.waitForLoadState('networkidle');

  // Login
  console.log('=== Logging in ===');
  await page.fill('input[name="username"]', process.env.E2E_ADMIN_USERNAME ?? 'admin');
  await page.fill('input[name="password"]', process.env.E2E_ADMIN_PASSWORD ?? 'admin123');
  await page.click('button[type="submit"]');

  // Wait for redirect
  console.log('=== Waiting for products page ===');
  await page.waitForURL('**/products/**', { timeout: 10000 }).catch(e => {
    console.log('Failed to redirect to /products:', e.message);
  });

  await page.waitForLoadState('networkidle');

  // Wait a bit for any async errors
  await page.waitForTimeout(3000);

  console.log('\n=== SUMMARY ===');
  console.log(`Total console messages: ${messages.length}`);
  console.log(`Total errors: ${errors.length}`);

  if (errors.length > 0) {
    console.log('\n=== ERRORS ===');
    errors.forEach(err => console.log(err));
  }

  // Take screenshot
  await page.screenshot({ path: 'test-results/debug-products-page.png', fullPage: true });

  console.log('\nCurrent URL:', page.url());
  console.log('Page title:', await page.title());
});
