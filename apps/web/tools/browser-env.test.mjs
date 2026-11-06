import { describe, expect, it } from 'vitest';
import { browserEnv } from './browser-env.mjs';

describe('browser environment', () => {
  it('removes inherited Snap libraries without changing the parent environment', () => {
    const source = {
      GIO_MODULE_DIR: '/home/test/snap/code/common/gio-modules',
      GTK_PATH: '/snap/code/current/gtk',
      GTK_EXE_PREFIX: '/usr',
      GSETTINGS_SCHEMA_DIR: '/usr/share/glib-2.0/schemas',
      PATH: '/usr/bin',
      UNSET: undefined,
    };
    expect(browserEnv(source)).toEqual({
      GTK_EXE_PREFIX: '/usr',
      GSETTINGS_SCHEMA_DIR: '/usr/share/glib-2.0/schemas',
      PATH: '/usr/bin',
    });
    expect(source.GTK_PATH).toBe('/snap/code/current/gtk');
  });
});
