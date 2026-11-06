import { expect, it } from 'vitest';
import { previewDocument } from './WorkspacePanel';
import { defaultManifest } from '../../lib/workspace';
it('renders labels as text and restricts preview resources', () => {
  const markup = previewDocument({ ...defaultManifest, findings_label: '<script>bad()</script>' });
  expect(markup).not.toContain('<script>');
  expect(markup).toContain('&lt;script&gt;bad()&lt;/script&gt;');
  expect(markup).toContain("default-src 'none'");
  expect(markup).toContain("form-action 'none'");
  expect(markup).toContain('/workspace-preview.css');
});
