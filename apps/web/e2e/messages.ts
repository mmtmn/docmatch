// The e2e suite reads the same locale file the app uses, so selectors never retype copy.
// Playwright runs this under Node ESM, which requires the import attribute for JSON.
import messages from '@docmatch/locales/pt-BR.json' with { type: 'json' };

export const m = messages;
