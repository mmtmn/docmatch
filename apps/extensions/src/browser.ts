export interface MessageSender {
  id?: string;
  url?: string;
}
interface StorageArea {
  get(key: string): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
  remove(key: string): Promise<void>;
  setAccessLevel?(options: { accessLevel: 'TRUSTED_CONTEXTS' }): Promise<void>;
}
export interface ExtensionBrowser {
  runtime: {
    id: string;
    getURL(path: string): string;
    sendMessage(message: unknown): Promise<unknown>;
    onMessage: {
      addListener(
        listener: (
          message: unknown,
          sender: MessageSender,
          respond: (response: unknown) => void,
        ) => boolean,
      ): void;
    };
    sendNativeMessage?(application: string, message: unknown): Promise<unknown>;
  };
  storage: { local: StorageArea };
  identity?: {
    getRedirectURL(path?: string): string;
    launchWebAuthFlow(options: { url: string; interactive: boolean }): Promise<string | undefined>;
  };
  tabs: { create(options: { url: string }): Promise<unknown> };
}
const globals = globalThis as typeof globalThis & {
  browser?: ExtensionBrowser;
  chrome?: ExtensionBrowser;
};
export const browser = (globals.browser ?? globals.chrome)!;
