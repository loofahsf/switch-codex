interface TauriEvent<T> {
  payload: T;
}

type UnlistenFn = () => void;

interface ConfirmOptions {
  title?: string;
  kind?: 'info' | 'warning' | 'error';
}

declare global {
  interface Window {
    __TAURI__: {
      core: {
        invoke<T>(command: string, args?: Record<string, unknown>): Promise<T>;
      };
      event: {
        listen<T>(event: string, handler: (event: TauriEvent<T>) => void): Promise<UnlistenFn>;
      };
      dialog: {
        confirm(message: string, options?: ConfirmOptions): Promise<boolean>;
      };
    };
  }
}

export function invoke<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  return window.__TAURI__.core.invoke<T>(command, args);
}

export function listen<T>(event: string, handler: (payload: T) => void): Promise<UnlistenFn> {
  return window.__TAURI__.event.listen<T>(event, ({ payload }) => handler(payload));
}

export function confirm(message: string, options?: ConfirmOptions): Promise<boolean> {
  return window.__TAURI__.dialog.confirm(message, options);
}

export function getErrorMessage(error: unknown, fallback: string): string {
  if (typeof error === 'string' && error.trim()) {
    return error;
  }
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return fallback;
}
