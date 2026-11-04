# ADR-0009: Produto somente em pt-BR

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

O mercado inicial é o Brasil: extratos de bancos brasileiros, boletos, Pix, NF-e, exportações de
Protheus e Omie, CNPJ e CPF, datas `dd/mm/aaaa`, vírgula decimal, nomes de empresa com "Ltda" e
"ME". Os problemas mais difíceis de ingestão são locais, não genéricos. Ao mesmo tempo, o
repositório é aberto e quer contribuições de fora, o que pede código legível
internacionalmente.

Internacionalização dobra a superfície de texto (interface, e-mails, relatórios, páginas legais,
blog, mensagens de erro, SDKs), dobra a revisão de tom e exige decisões de formato por locale em
cada ponto do pipeline. Sem cliente pagante pedindo outro idioma, é custo sem retorno.

Alternativas consideradas:

- **pt-BR e en-US desde o início.** Dobra o trabalho de texto e divide a atenção de qualidade.
- **Interface em inglês, dados em pt-BR.** Afasta o usuário-alvo (financeiro de pequena e média
  empresa) sem ganhar o usuário internacional.
- **Tradução automática da interface.** Qualidade imprevisível justamente onde o produto
  precisa soar confiável.

## Decisão

- Todo texto voltado ao usuário é em português do Brasil: interface web, admin, apps móveis,
  e-mails, relatórios do harness, mensagens de erro da API (campo `message`), páginas legais,
  blog, READMEs e exemplos dos SDKs, esta documentação.
- Texto de interface vive só em `packages/locales`, com chave por contexto; string literal na
  interface falha o lint. Web, Android e iOS consomem o mesmo pacote.
- Identificadores de código, comentários em código, mensagens de commit no corpo técnico, nomes
  de variáveis de ambiente e de rotas são em inglês. O resumo do commit é em pt-BR.
- Prompts de modelo são em inglês (melhor desempenho), com instrução explícita de que os campos de
  texto livre da saída estruturada sejam em pt-BR; o relatório é montado por template em pt-BR a
  partir da saída estruturada, não pelo texto corrido do modelo.
- O pipeline assume locale pt-BR por padrão na ingestão (vírgula decimal, `dd/mm/aaaa`, fuso
  `America/Sao_Paulo`) e detecta exceções por arquivo em vez de perguntar.
- `lang="pt-BR"` em todas as páginas; `Accept-Language` é ignorado; não há seletor de idioma.
- Códigos de erro (`packages/contracts`) são estáveis e em inglês (`quote_expired`), para que
  SDKs e integradores programem contra eles; a mensagem ao lado é em pt-BR.

## Consequências

- Uma única voz para revisar: tom, acentuação, vocabulário financeiro brasileiro.
- Ingestão "nativa" em pt-BR, que é onde a concorrência genérica erra.
- Usuários fora do Brasil não são atendidos; o site diz isso. Não há SEO internacional.
- Adicionar um idioma depois é trabalho aditivo: `packages/locales` já é indexado por chave, e
  os templates de relatório são arquivos por idioma. O que não é aditivo (detecção de locale na
  ingestão) já existe por arquivo.
- Contribuidores que não leem português conseguem trabalhar no código; os textos e esta
  documentação pedem revisão de quem lê.
