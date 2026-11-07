declare module 'virtual:limits' {
  const limits: import('../../web/vite/limits-schema').Limits;
  export default limits;
}
declare module 'virtual:pricing' {
  const pricing: import('../../web/vite/pricing-schema').Pricing;
  export default pricing;
}
declare module 'astro:env/client' {
  export const PUBLIC_DM_API_BASE_URL: string | undefined;
  export const PUBLIC_DM_MEDIA_BASE_URL: string | undefined;
  export const PUBLIC_DM_FEATURE_RESULTS: boolean;
}
