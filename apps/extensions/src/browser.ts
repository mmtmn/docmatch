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
export interface MessagePort {
  name: string;
  sender?: MessageSender;
  postMessage(message: unknown): void;
  disconnect(): void;
  onMessage: { addListener(listener: (message: unknown) => void): void };
  onDisconnect: { addListener(listener: () => void): void };
}
export interface ExtensionBrowser {
  runtime: {
    id: string;
    getURL(path: string): string;
    sendMessage(message: unknown): Promise<unknown>;
    connect(options: { name: string }): MessagePort;
    onConnect: { addListener(listener: (port: MessagePort) => void): void };
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
  permissions?: { request(options: { origins: string[] }): Promise<boolean> };
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
