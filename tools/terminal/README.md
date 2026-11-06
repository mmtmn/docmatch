# docmatch no terminal

```sh
npm install -g --allow-remote=root https://docmatch.com.br/install/docmatch.tgz
docmatch
```

O menu configura o modelo (Ollama ou OpenRouter), a instalação local ou a conexão com
uma API existente. Depois, selecione vários documentos, arraste caminhos para o terminal,
confirme a estimativa e baixe os dados corrigidos. As chaves ficam somente na sua máquina.

Requer Node 20 ou posterior. Linux, macOS e Windows, em x64 e ARM64. A primeira execução
baixa o binário da versão instalada por HTTPS e verifica o SHA-256 incluído no pacote.
Não há dependências npm nem scripts de pós-instalação. Para rodar o servidor local, o
assistente verifica Docker e orienta a instalação do que faltar. Conectar a uma API
existente não exige Docker nem compiladores.

`--allow-remote=root` autoriza somente a URL pedida nessa instalação, como exige o npm 12;
não altera a configuração global do npm. O pacote não executa scripts de instalação.

`docmatch config` altera a conexão; `docmatch workspace` abre os documentos diretamente.
Para atualizar: `npm install -g --allow-remote=root https://docmatch.com.br/install/docmatch.tgz`. Para remover o launcher:
`npm uninstall -g docmatch`. Os dados e configurações locais são preservados.
