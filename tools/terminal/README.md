# docmatch no terminal

```sh
npm install -g docmatch
docmatch
```

O menu configura o modelo (Ollama ou OpenRouter), a instalação local ou a conexão com
uma API existente. Depois, escolha ou crie um projeto com nome. Selecione vários documentos, arraste caminhos para o terminal,
confirme a estimativa e baixe os dados corrigidos. As chaves ficam somente na sua máquina.

Requer Node 20 ou posterior. Linux, macOS e Windows, em x64 e ARM64. A primeira execução
baixa o binário da versão instalada por HTTPS e verifica o SHA-256 incluído no pacote.
Não há dependências npm nem scripts de pós-instalação. Para rodar o servidor local, o
assistente verifica Docker e orienta a instalação do que faltar. Conectar a uma API
existente não exige Docker nem compiladores.

`docmatch config` altera a conexão; `docmatch workspace` abre os documentos diretamente.
Para atualizar: `npm install -g docmatch`. Para remover o launcher:
`npm uninstall -g docmatch`. Os dados e configurações locais são preservados.

O projeto fica salvo na conta. Para continuar depois:

```sh
docmatch workspace --list-projects --plain
docmatch workspace --saved-project ID
```

Para enviar sem iniciar uma conciliação, acrescente `--upload-only`. Se nada mudou,
o terminal abre o resultado existente sem uma nova cobrança. A chave da API permanece
vinculada à conta em que foi criada.
