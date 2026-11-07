// Shared views must receive the bound extension transport. Never fall back to web cookies.
import { ApiError, type RequestOptions } from '../../web/src/lib/apiTypes';
export { ApiError, type RequestOptions };
export function apiConfigured(): boolean {
  return false;
}
export function api<T>(_path: string, _options?: RequestOptions): Promise<T> {
  return Promise.reject(
    new ApiError(0, 'extension_context_required', 'extension_context_required'),
  );
}
