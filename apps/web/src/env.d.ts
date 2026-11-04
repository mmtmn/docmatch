/// <reference types="astro/client" />
/// <reference types="vite/client" />

declare module 'virtual:limits' {
  const limits: import('../vite/limits-schema').Limits;
  export default limits;
}

declare module 'virtual:pricing' {
  const pricing: import('../vite/pricing-schema').Pricing;
  export default pricing;
}
