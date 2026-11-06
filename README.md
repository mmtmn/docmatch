# docmatch

**Transforme documentos em resultados.** Cruze informações, encontre inconsistências com
suas evidências e baixe os dados organizados em XLSX, CSV ou PDF.

Use o [site](https://docmatch.com.br), os apps [Android](apps/android/README.md) e
[iOS](apps/ios/README.md), o terminal ou os [SDKs](sdks/README.md).

## Terminal

```sh
npm install -g --allow-remote=root https://docmatch.com.br/install/docmatch.tgz
docmatch
```

O menu cuida da configuração: escolha Ollama ou OpenRouter, prepare os serviços locais
ou conecte uma API existente. Selecione vários arquivos ou arraste seus caminhos para
o terminal, confira a estimativa e acompanhe o processamento. Sem variáveis de ambiente
para copiar e colar. Requer Node 20+; o assistente verifica os demais requisitos.

[Guia do terminal](tools/terminal/README.md) · [Instalação e comandos avançados](tools/installer/README.md)

## Veja funcionando

[Site](docs/demos/website.gif) · [Terminal](docs/demos/terminal.gif) · [Android](docs/demos/android.gif)

As três gravações usam o gêmeo público Vantara: **100% dos itens avaliados**, com dados,
achados, evidências e totais conferidos. [GIFs e relatórios](docs/demos/README.md).
A nota vale para esse caso de teste; sugestões de IA continuam sujeitas à revisão humana.

## O que você recebe

- Um documento consolidado para baixar, com achados e evidências abaixo.
- Valores confirmados separados de correções propostas; os originais são preservados.
- Modelos locais ou OpenRouter, com orçamento aprovado antes de processar.
- Personalização da interface com prévia, aprovação e reversão para contas pagas/QA.
- API em todos os planos pagos: **R$ 100, R$ 500 e R$ 1.000/mês**.

## Desenvolver e contribuir

[Rodar o código localmente](docs/desenvolvimento.md) · [API e SDKs](docs/api.md) ·
[Arquitetura](docs/arquitetura.md) · [Estruturas, algoritmos e padrões](docs/estruturas-algoritmos-padroes.md)

`make ci` executa o portão local antes de cada push. `make ci-full` executa os testes
completos de release. PRs não iniciam runners pagos no GitHub.

[Documentação](docs/README.md) · [Contribuindo](CONTRIBUTING.md) · [Segurança](SECURITY.md)

Licença [AGPL-3.0](LICENSE) para apps/serviços e [Apache-2.0](sdks/LICENSE) para SDKs.
