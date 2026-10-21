import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

// #68: looking at a page's sharing state must not publish it. The globe used to
// create a link the moment its menu opened, so checking whether Stop sharing
// had worked published the page again under a fresh address, and every page
// somebody looked at this way ended up public.
await withFixture(async ({ page, api, base, workspace }) => {
  page.setDefaultTimeout(15000);
  const doc = await api('POST', '/api/pages', { title: 'Quarterly notes', workspaceId: workspace.id });
  const shared = async () => (await api('GET', `/api/pages/${doc.id}`)).shared === true;
  const status = async (url) => (await fetch(url)).status;

  await page.goto(`${base}/p/${doc.id}`);
  const globe = page.locator('.share-wrap .icon-btn').first();
  const menu = page.locator('.share-menu');

  // Opening the menu shows the state and publishes nothing.
  await globe.click();
  await menu.getByText('This page is not public.', { exact: false }).waitFor();
  assert.equal(await shared(), false, 'opening the menu published the page');

  // Create link publishes, and the address works.
  await menu.getByRole('button', { name: 'Create link', exact: true }).click();
  const field = menu.locator('input.share-input[readonly]');
  await eventually(async () => (await field.count()) === 1 && (await field.inputValue()).includes('/public/'));
  const first = await field.inputValue();
  assert.equal(await status(first), 200);
  assert.equal(await shared(), true);

  // A password typed and left behind does not replace the address on screen;
  // it waits for New link.
  await menu.locator('input[type=password]').fill('correct horse');
  await menu.locator('.share-hint').first().click();
  await menu.getByRole('button', { name: 'New link', exact: true }).waitFor();
  assert.equal(await field.inputValue(), first);
  assert.equal(await status(first), 200, 'leaving the password field replaced the link');

  // Stop sharing ends the address, and opening the menu again to check does not
  // bring the page back.
  await menu.getByRole('button', { name: 'Stop sharing', exact: true }).click();
  await eventually(async () => (await menu.count()) === 0);
  assert.equal(await status(first), 404);
  assert.equal(await shared(), false);
  await globe.click();
  await menu.getByText('This page is not public.', { exact: false }).waitFor();
  assert.equal(await shared(), false, 'checking the menu after Stop sharing published the page again');
  await page.keyboard.press('Escape');
  await eventually(async () => (await menu.count()) === 0);
  assert.equal(await shared(), false);
});
