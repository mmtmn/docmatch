# Desenvolvimento local

É necessário acesso ao repositório enquanto ele permanecer privado. Use um checkout
atualizado e siga os requisitos da sua plataforma no README. A preparação básica é:

```sh
make setup-local
make local
```

O comando local sobe banco, serviços e site e informa os endereços. O ambiente de
desenvolvimento tem um login próprio para testes. Ele não deve ser ativado em produção.
Comece com os [dados sintéticos](../../usuario/src/primeiro-projeto.md), sem credenciais
ou arquivos de clientes.

```sh
make ci
make ci-full
make local-down
```

`ci` é o portão habitual de alterações; `ci-full` inclui verificações de release e pode
exigir navegadores, simuladores ou ferramentas adicionais. Um teste pulado não demonstra
compatibilidade com um dispositivo real.

Para editar estes guias, instale `mdbook` 0.5.4 e execute `make -C apps/web build`.
O preview do site serve os dois livros com as mesmas regras de URL da produção.

As extensões têm pacotes próprios por navegador e ambiente. O teste local usa apenas
origens locais explícitas. A versão de produção exige IDs e retornos de autorização
registrados, sem liberar acesso genérico à rede local.
