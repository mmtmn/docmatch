# ADR-0010: AGPL-3.0 para serviços e apps, Apache-2.0 para SDKs

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

A docmatch processa documentos financeiros, de RH e jurídicos de terceiros. Código aberto é
condição de confiança: o cliente (ou o auditor dele) pode ler o que toca os dados, e a comunidade
pode encontrar falhas antes de um atacante. Ao mesmo tempo, o modelo de negócio é o serviço
hospedado; uma licença permissiva deixaria qualquer um oferecer a mesma hospedagem sem devolver
nada.

Os SDKs têm o problema oposto: precisam ser embutidos em código proprietário de clientes sem que a
licença seja motivo de veto por um departamento jurídico.

Alternativas consideradas:

- **MIT/Apache em tudo.** Máxima adoção, nenhuma proteção contra concorrente que hospeda o código
  sem contribuir.
- **Licenças "source available" (BSL, SSPL, Elastic).** Não são código aberto segundo a OSI;
  afastam contribuidores e distribuições, e o ganho sobre a AGPL é pequeno para um projeto deste
  tamanho.
- **AGPL em tudo, inclusive SDKs.** Um SDK AGPL contamina o aplicativo do cliente que o usa; na
  prática ninguém integraria.

## Decisão

| Caminho | Licença | Arquivo |
| --- | --- | --- |
| `apps/`, `services/`, `infra/`, `packages/`, `db/`, `tools/`, `testdata/`, `config/`, `docs/` | AGPL-3.0-only | `LICENSE` na raiz |
| `sdks/*` e o código de cliente gerado a partir de `packages/contracts` | Apache-2.0 | `sdks/LICENSE` |

Regras:

- Contribuições entram sob a mesma licença do caminho ("inbound = outbound"); não há CLA.
- Dependências passam por lista de permissões no CI: MIT, BSD, Apache-2.0, ISC, MPL-2.0, Zlib,
  Unicode e, nos caminhos AGPL, também GPL e LGPL. Nos SDKs só licenças permissivas. Ferramentas:
  `cargo deny`, `go-licenses`, `license-checker` (npm), `pip-licenses`.
- Dados de teste e corpora só com licença compatível e sem cláusula "não comercial".
- Nome e logotipo não são licenciados; um fork sob outro domínio usa outro nome (o código já
  permite isso: domínio e nome vêm de configuração).
- Modelos de linguagem são serviços externos; a saída deles (relatórios, regras aprendidas,
  ferramentas geradas para um tenant) pertence ao cliente, conforme os termos de uso.

## Consequências

- Qualquer um pode hospedar a própria docmatch editando `.env` e `terraform.tfvars`; se
  modificar e oferecer como serviço, precisa publicar as modificações.
- Integradores usam os SDKs sem fricção jurídica; o serviço roda separado, do outro lado da API.
- Algumas empresas proíbem código AGPL no próprio repositório. Não é um problema: elas consomem a
  API com o SDK Apache-2.0 e nunca precisam do código do serviço.
- A lista de permissões de licenças falha o CI quando uma dependência nova chega com licença fora
  da lista; a exceção, se justificada, é registrada no arquivo de configuração da ferramenta com
  comentário.
- Ferramentas geradas pelo toolsmith e promovidas para a biblioteca comum (não as de um tenant)
  entram no repositório sob AGPL, com registro de origem.
