import catalog from '../../../../services/api/internal/platforms/catalog.json';

export interface Platform {
  id: 'android' | 'ios' | 'github' | 'chrome' | 'firefox' | 'safari';
  name: string;
  status: 'coming_soon' | 'beta' | 'available';
  url: string;
}
const hosts: Record<Platform['id'], string> = {
  android: 'play.google.com',
  ios: 'apps.apple.com',
  github: 'github.com',
  chrome: 'chromewebstore.google.com',
  firefox: 'addons.mozilla.org',
  safari: 'apps.apple.com',
};
export function validatePlatforms(items: unknown): Platform[] {
  if (!Array.isArray(items) || items.length !== Object.keys(hosts).length)
    throw new Error('Invalid platform catalog');
  const seen = new Set<string>();
  for (const item of items as Platform[]) {
    if (!item || !Object.hasOwn(hosts, item.id) || seen.has(item.id) || !item.name)
      throw new Error('Invalid platform');
    seen.add(item.id);
    if (item.status === 'coming_soon') {
      if (item.url !== '') throw new Error('Unpublished destination');
    } else {
      if (!['beta', 'available'].includes(item.status)) throw new Error('Invalid platform status');
      const url = new URL(item.url);
      const validHost =
        url.host === hosts[item.id] ||
        (item.status === 'beta' &&
          ['ios', 'safari'].includes(item.id) &&
          url.host === 'testflight.apple.com');
      if (
        url.protocol !== 'https:' ||
        !validHost ||
        url.username ||
        url.password ||
        url.hash ||
        url.pathname === '/'
      )
        throw new Error('Expected an official platform URL');
    }
  }
  return items as Platform[];
}
export const platforms = validatePlatforms(catalog);
