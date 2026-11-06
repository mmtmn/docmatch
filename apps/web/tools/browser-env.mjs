// VS Code's Snap exports GTK/GIO paths for its own runtime. Passing those to a system
// Playwright browser mixes glibc versions and can crash WebKit's network process.
// Remove only Snap paths, and only from the child browser's environment.
/** @param {NodeJS.ProcessEnv} [source] @returns {Record<string, string>} */
export function browserEnv(source = process.env) {
  const result = Object.fromEntries(
    Object.entries(source).filter((entry) => entry[1] !== undefined),
  );
  for (const key of ['GIO_MODULE_DIR', 'GTK_PATH', 'GTK_EXE_PREFIX', 'GSETTINGS_SCHEMA_DIR']) {
    if (result[key]?.includes('/snap/')) delete result[key];
  }
  return /** @type {Record<string, string>} */ (result);
}
