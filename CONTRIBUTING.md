# Contribuindo

Obrigado por ajudar a docmatch. Este guia é curto de propósito: o `make ci` diz o resto.

## Fluxo

1. Crie um branch curto a partir de `main`: `feat/...`, `fix/...`, `chore/...`, `docs/...`, `security/...`.
2. Commits pequenos, conforme avança, no formato [Conventional Commits](https://www.conventionalcommits.org/pt-br/):
   `feat(api): orçamento assinado com HMAC`. O hook `commit-msg` recusa o que não seguir.
3. Rode `make ci` antes do push (o hook `pre-push` faz isso por você). Nada sobe quebrado.
4. Abra um PR usando o template: o que mudou, por quê, como testou (cole o resumo do `make ci`), capturas de tela se houver UI, impacto de segurança.
5. Um PR é mesclado com *squash* em `main`.

Pushes e PRs não iniciam GitHub Actions. Os testes obrigatórios rodam nesta máquina,
inclusive os quatro SDKs e o launcher npm. As receitas hospedadas só podem ser iniciadas
manualmente; não são requisito para mesclar. `make -C apps/ios app` valida a interface
SwiftUI em um Mac com Xcode; no Linux, o portão valida o DocmatchKit.

## Regras que o CI aplica

- **Nada hardcoded.** URLs, e-mails, IPs, ARNs, contas e chaves vêm do ambiente ou de `config/`. O lint `tools/lint/hardcode.sh` falha a build; exceções justificadas vão em `tools/lint/hardcode-allow.txt`.
- **Nenhum segredo, nunca.** `.env`, `terraform.tfvars` e `testdata/private/` são recusados pelo hook mesmo com `--force`. `gitleaks` roda nos arquivos staged.
- **Textos de interface só em pt-BR e só em `packages/locales`.** String literal na UI falha o lint.
- **Limites de caracteres vêm de `packages/contracts/limits.yaml`.** Cliente e servidor são gerados do mesmo arquivo.
- **Sem linhas de atribuição a assistentes de IA** em commits ou PRs. O hook `commit-msg` recusa.
- **Cobertura mínima da API:** 85% geral nos pacotes incluídos, 95% em créditos/configuração e 90% em autenticação/sessão. O portão combina testes unitários e de fluxos de conta no PostgreSQL; `ci-full` inclui os adaptadores de banco.
- **SQL parametrizado e isolamento por organização.** Consultas geradas usam `sqlc`; transações de serviços também usam consultas parametrizadas. Testes de RLS e de `EXPLAIN` verificam os caminhos críticos, não todas as consultas possíveis.

## Ambiente local

```bash
cp .env.example .env
make setup
make dev
```

`make dev` sobe Postgres, MinIO, Mailpit, stripe-mock e um modelo local via Docker Compose. Para rodar o caso Vantara privado, aponte `DM_VANTARA_PATH` para a pasta; sem ele, o gêmeo sintético público em `testdata/golden/vantara-twin/` é usado.

## Dúvidas

Abra uma issue. Para vulnerabilidades, siga o [SECURITY.md](SECURITY.md) e não abra issue pública.
