/**
 * Hands the manifest to GitHub the way its manifest flow requires: a top-level browser form POST
 * with one field named `manifest`. A fetch cannot do it, GitHub shows its own confirmation page
 * and redirects the browser back to us.
 */
export function submitManifestForm(postUrl: string, manifest: Record<string, unknown>): void {
  const form = document.createElement('form');
  form.method = 'POST';
  form.action = postUrl;
  form.style.display = 'none';
  const field = document.createElement('input');
  field.type = 'hidden';
  field.name = 'manifest';
  field.value = JSON.stringify(manifest);
  form.append(field);
  document.body.append(form);
  form.submit();
}

/** The current page without query and hash, which is where GitHub should send the browser back. */
export function currentPageUrl(): string {
  const url = new URL(window.location.href);
  url.search = '';
  url.hash = '';
  return url.toString();
}
