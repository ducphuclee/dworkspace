import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

// An open collection kept the schema and views it loaded, and sent both back on
// every view change. An agent adds a property, somebody in an open tab renames
// a view, and the property is gone again. The tab now picks the change up as it
// happens, and a view change sends only the views.
await withFixture(async ({ page, api, base, path }) => {
  page.setDefaultTimeout(15000);
  const collectionId = path.split('/').pop();
  const { token } = await api('POST', '/api/tokens', { name: 'agent', scope: 'write' });

  await page.locator('.view-tab', { hasText: 'Table' }).waitFor();
  const res = await fetch(`${base}/mcp`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      jsonrpc: '2.0', id: 1, method: 'tools/call',
      params: { name: 'update_schema', arguments: { page_id: collectionId, properties: [{ name: 'Priority', type: 'text' }] } },
    }),
  });
  assert.equal(res.status, 200);

  // The open table shows the agent's property without a reload.
  await page.locator('th', { hasText: 'Priority' }).first().waitFor();

  // A view change from this tab leaves the schema alone.
  await page.locator('.view-tab', { hasText: 'Table' }).dblclick();
  const input = page.locator('.confirm-dialog input');
  await input.fill('Everything');
  await input.press('Enter');
  await eventually(async () => (await api('GET', path)).views.some((v) => v.name === 'Everything'));
  const stored = await api('GET', path);
  assert.ok(stored.schema.some((p) => p.name === 'Priority'), 'renaming a view took the agent\'s property away');
});
