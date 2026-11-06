# Logo para consentimento e perfis

Os PNGs são exportações do [favicon existente](../favicon.svg), sem redesenhar a marca:

- `docmatch-google-120.png`: 120 × 120, para o consentimento do Google.
- `docmatch-avatar-512.png`: 512 × 512, para perfis e avatares.

Para regenerar a partir da raiz do repositório com ImageMagick:

```bash
convert -background none -density 576 apps/web/public/favicon.svg -resize 120x120 -strip apps/web/public/brand/docmatch-google-120.png
convert -background none -density 576 apps/web/public/favicon.svg -resize 512x512 -strip apps/web/public/brand/docmatch-avatar-512.png
```

Instruções de OAuth em [docs/runbooks/google-sign-in.md](../../../../docs/runbooks/google-sign-in.md).
