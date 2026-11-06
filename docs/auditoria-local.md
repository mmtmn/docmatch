# Auditoria local de release — 6 de novembro de 2026

Esta é uma verificação do código e do ambiente local, não uma certificação de ausência de
defeitos. Os resultados abaixo identificam o que foi executado, as correções encontradas e
os limites da evidência. Não houve cobrança real nem alteração de dados de clientes.

## Entrada pelo terminal

O pacote npm instala um launcher sem dependências ou pós-instalação. Na primeira execução,
ele baixa o binário da versão fixa por HTTPS e confere o SHA-256 embarcado no pacote.
Arquivos alterados no cache são rejeitados; downloads têm limite de tamanho e prazo.
As seis combinações Linux/macOS/Windows × x64/ARM64 são compiladas; a execução nativa foi
validada neste host Linux. Compilação cruzada não substitui testes nos outros sistemas.

`docmatch` abre o menu: instalação local, configuração de modelo, conexão a uma API,
documentos, diagnóstico e controle dos serviços. A configuração de conexão usa arquivo
privado escrito atomicamente. A chave salva não acompanha uma troca de origem da API.
Arquivos arrastados são dados para o parser de caminhos, nunca comandos de shell.

Uma sessão PTY real passou por menu → conexão → seleção de quatro arquivos → aprovação →
processamento → exportação XLSX → saída. A instalação npm também precisa ser verificada
com cache vazio, para exercitar download e integridade, não somente o binário compilado.

## Testes executados

| Camada | Evidência local |
| --- | --- |
| API Go | Unitários, `-race`, integração com PostgreSQL real, RLS, autenticação, créditos, cobrança, filas e pipeline com o processador Rust; 772 casos na suíte de integração, incluindo os unitários. Cobertura global de integração: 85,4%, acima do piso de 85%. |
| Fuzz Go | IDs públicos e tokens assinados, dez segundos cada, quatro workers; milhões de entradas exercitadas, sem falha. |
| Processador Rust | 369 testes de release aprovados, incluindo golden tests, propriedades com 2.000 casos, entrada malformada, limites de recursos e gRPC; um caso dependente de ambiente é opcional. |
| Web | 254 unitários; tipos, lint e build; 141 casos Playwright em Chromium, Firefox e WebKit. HTTPS local conserva a CSP de produção. Inclui WCAG/axe, teclado, telas pequenas, crescimento das regras e downloads. |
| Android | 31 testes JVM, lint, APK debug/testes, release R8 e fluxo instrumentado em emulador isolado: login, seletor, resultado, decisão e revogação da sessão por outro dispositivo. O teste destinado apenas a gravação é opt-in. |
| iOS | 43 testes DocmatchKit em Swift 6 no Linux. O build SwiftUI/Xcode precisa de um Mac e não foi validado neste host. |
| SDKs | Go com race/vet, Rust com testes/fmt/clippy, TypeScript com build/testes/catálogo, Python com testes de transporte. Todos participam do runner local. |
| Terminal | Testes Go, race, lint, vet nos três sistemas, seis builds cruzados, manifesto/checksum npm e proteção do setup local contra origens remotas. |
| Integrações externas | Planejador de personalização contra Ollama real; Stripe sandbox com Checkout, portal, fatura paga de teste, replay de webhook e cancelamento, seguido de limpeza dos recursos temporários. |
| Infra | Terraform fmt/validate, Ansible lint, testes locais e Trivy de configuração com a linha de base justificada existente. Isso não equivale a aplicar Terraform. |

O teste de carga usa quatro organizações concorrentes, duas execuções por organização,
upload, estimativa, execução, achados e XLSX reais. Na medição inicial, oito execuções
completaram em 10,8 s, p95 de 5,5 s, sem erro. As oito tentativas de ler uma execução de
outra organização receberam 404. Todos os resultados atingiram 100% do gabarito público.
Esse volume é um teste limitado de concorrência local, não uma projeção de capacidade de
produção. O orçamento padrão é p95 de 30 s; o comando aceita limites explícitos.

O gabarito nunca é enviado ao modelo. O verificador compara títulos, movimentos bancários,
achados, evidências e totais. Cinco mutações deliberadas (saldo, regra, evidência, duplicata
e total) precisam ser recusadas para que uma nota de 100% tenha valor.

O caso Vantara privado também foi reexecutado sem cache em Ollama e OpenRouter: ambos
passaram com 18/18 linhas, 17/17 títulos e 23/23 achados, preservando fatos e evidências.
Arquivos, respostas e gabarito privados não fazem parte deste relatório nem do Git.

## Segurança e dependências

`tools/ci/security.sh` integra `pnpm audit`, `govulncheck`, `cargo audit`, `pip-audit`,
Trivy para os lockfiles Android e Gitleaks para o histórico Git. Falta de scanner ou
resultado de vulnerabilidade falha o portão. Os scans usam bases atualizadas na execução;
nenhum resultado promete proteção contra vulnerabilidades ainda desconhecidas.

- Corrigidos advisories em basic-ftp, glob e minimatch com overrides limitados às versões
  afetadas; os builds e testes web foram repetidos.
- O SDK Go agora seleciona a toolchain corrigida, evitando os advisories do Go antigo do host.
- A auditoria Python usa ambiente isolado com pip corrigido.
- Lockfiles Gradle passaram a registrar também dependências transitivas. O primeiro scan
  encontrou 83 ocorrências de advisories no Unified Test Platform. Netty, Guava e Protobuf
  foram atualizados nas configurações desse tooling; o novo scan retornou zero, e o fluxo
  instrumentado foi executado novamente.
- Os scans finais não encontraram vulnerabilidades alcançáveis em Go nem advisories nos
  demais inventários examinados. `govulncheck` informa separadamente advisories em módulos
  não alcançados pelo código; isso não deve ser descrito como ausência absoluta de CVEs.
- Gitleaks não encontrou segredos nos 118 commits examinados inicialmente. Os hooks voltam
  a conferir o conteúdo antes dos commits. Credenciais, dados privados e relatórios locais
  permanecem ignorados pelo Git.

Referências de formato e funcionamento: [lockfiles Gradle](https://docs.gradle.org/current/userguide/dependency_locking.html),
[cobertura Java do Trivy](https://www.trivy.dev/docs/latest/guide/coverage/language/java/) e
[isolamento do emulador Android](https://developer.android.com/tools/variables).

## Correções e revisão de arquitetura

- A geração do documento indexa alocações por título, com vetores em ordem estável. O
  trecho deixou de repetir uma busca `O(T × A)` e passou a `O(T + L + A)`, com memória
  `O(T + A)`. Os golden tests preservam saldos, propostas, ordem e evidências.
- Permanecem adequados os decimais exatos/centavos para dinheiro, limites explícitos no
  matching combinatório, transações com RLS, reservas de crédito e idempotência financeira.
  O [guia de implementação](estruturas-algoritmos-padroes.md) descreve esses invariantes.
- O teste de migração inválida agora escolhe um identificador livre após a última migração,
  sem supor uma numeração contínua.
- O teste periódico espera os três jobs necessários, independentemente da ordem em que
  outros jobs concluem. Não se trata de aumentar um timeout para esconder a falha.
- O teste de interrupção Spot mantém seus dois trabalhadores sintéticos no mesmo protocolo;
  a integração com o processador real continua em testes separados.
- Testes do instalador respeitam canonicalização de caminhos em Windows/macOS; o empacotador
  resolve `--out` antes de mudar de diretório e preserva versões anteriores do winget.
- WebKit recebe um ambiente de processo sem bibliotecas incompatíveis herdadas do Snap.
  Todos os navegadores exigidos precisam iniciar; o portão não retorna verde pulando-os.
- O emulador Gradle usa diretório próprio do checkout, sem disputar o AVD já aberto pelo
  desenvolvedor. O teste local desativa chamadas pagas ao modelo.
- A tela de nova execução iOS foi dividida em seções menores para reduzir a expressão
  SwiftUI que excedia o type checker. A confirmação da correção exige Xcode.

Não há um algoritmo ou padrão universalmente melhor. A revisão preservou fronteiras
existentes e corrigiu um custo demonstrável, sem substituir interfaces testáveis por uma
reescrita ampla. Paginação de resultados muito grandes e teste de capacidade sustentada
continuam sendo trabalhos distintos do benchmark local acima.

## Reproduzir sem GitHub Actions

```sh
make setup
make ci
make ci-full
```

O pre-push executa `make ci`. Workflows GitHub são somente manuais; push e PR não iniciam
runners. `make ci-full` acrescenta integração completa, propriedades, fuzz, três navegadores,
Android instrumentado, scanners e carga real. Pré-requisitos extras: SDK Android/KVM,
bibliotecas dos browsers, Docker e os scanners indicados pelo runner. Para iOS, execute
`make -C apps/ios app` em macOS com Xcode. Scanners devem ser instalados no host, sem colocar
tokens ou credenciais nos arquivos do repositório.

Logs detalhados ficam em `/tmp/docmatch-ci-*.log`; carga e resultados locais em
`scratch/audit/`, ignorado pelo Git. As integrações com provedor real são opt-in para não
transformar cada push em consumo pago ou depender de uma chave externa.
