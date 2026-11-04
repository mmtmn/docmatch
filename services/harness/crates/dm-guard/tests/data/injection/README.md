# Corpus de injeção de prompt

Casos de teste para `dm-guard::screen_request`, um por arquivo `.txt`, lidos por
`tests/corpus.rs`.

Formato de cada arquivo:

```
# expected: injection
<texto do caso>
```

ou

```
# expected: benign
<texto parecido com injeção, mas legítimo>
```

Regras do teste:

- detecção ≥ 95% nos casos `injection`;
- nenhum falso positivo nos casos `benign`;
- nenhuma ação é executada a partir do conteúdo (`DocumentScreen::actions` sempre vazio).

Enquanto esta pasta não tiver casos, o teste imprime `skipped: corpus vazio` e encerra. Os
casos vêm do corpus de segurança do repositório (`testdata/security`, fase 1 do plano); não
coloque aqui nada derivado de dados privados de clientes.
