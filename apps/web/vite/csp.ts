/** HTTPS upgrades remain mandatory everywhere except an explicit HTTP loopback build. */
export function transportDirectives(site: string): Array<'upgrade-insecure-requests'> {
  const url = new URL(site);
  const localHTTP =
    url.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
  return localHTTP ? [] : ['upgrade-insecure-requests'];
}
