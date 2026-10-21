import assert from 'node:assert/strict';
import { withFixture } from './fixture.mjs';

// The slash menu keys its headings by group, and our own entries came after
// BlockNote's, so a group could appear twice: "Basic blocks" three times on
// 1.1.1, and after typing "ch" and clearing it, headings that were left behind.
// Each group is one run of entries now, in the order it first appears.
await withFixture(async ({ page, api, base, workspace }) => {
  page.setDefaultTimeout(15000);
  const doc = await api('POST', '/api/pages', { title: 'Slash', workspaceId: workspace.id });
  await api('PATCH', `/api/pages/${doc.id}`, { content: [{ id: 'intro', type: 'paragraph', props: {}, content: [], children: [] }] });
  await page.goto(`${base}/p/${doc.id}`);
  await page.locator('[data-id="intro"] .bn-inline-content').click();
  await page.keyboard.type('/');
  const menu = page.locator('.bn-suggestion-menu');
  await menu.waitFor();
  const headings = () => menu.locator('.bn-suggestion-menu-label').allTextContents();
  const unique = (list) => new Set(list).size === list.length;

  const full = await headings();
  assert.ok(full.length > 1, `no groups at all: ${full}`);
  assert.ok(unique(full), `a group heading repeats: ${full}`);
  await page.keyboard.type('ch');
  await page.waitForTimeout(400);
  assert.ok(unique(await headings()), 'a heading repeats while filtering');
  await page.keyboard.press('Backspace');
  await page.keyboard.press('Backspace');
  await page.waitForTimeout(400);
  assert.deepEqual(await headings(), full, 'clearing the query left other headings than before');
});
