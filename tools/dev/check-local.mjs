// Fail before migrations or listeners start if a production environment was copied locally.
import { pathToFileURL } from 'node:url';

export function checkLocal(env) {
  const loopback = (host) => ['localhost', '127.0.0.1', '[::1]', '::1'].includes(host);
  for (const name of ['DATABASE_URL', 'DM_API_BASE_URL', 'DM_PUBLIC_BASE_URL']) {
    let url;
    try { url = new URL(env[name]); } catch { throw new Error(`${name}: configure um endereço local válido.`); }
    if (!loopback(url.hostname)) throw new Error(`${name}: make local recusa servidores remotos.`);
    if (name === 'DATABASE_URL' && !['postgres:', 'postgresql:'].includes(url.protocol))
      throw new Error('DATABASE_URL: use PostgreSQL local.');
    if (name !== 'DATABASE_URL' && (url.protocol !== 'http:' || url.username || url.password))
      throw new Error(`${name}: use HTTP local sem credenciais na URL.`);
  }
  for (const name of ['DM_API_LISTEN', 'DM_ADMIN_LISTEN']) {
    let address;
    try { address = new URL(`http://${env[name]}`); } catch { throw new Error(`${name}: endereço de escuta inválido.`); }
    if (!loopback(address.hostname)) throw new Error(`${name}: o login de desenvolvimento exige escuta em loopback.`);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try { checkLocal(process.env); } catch (error) { console.error(error.message); process.exitCode = 1; }
}
