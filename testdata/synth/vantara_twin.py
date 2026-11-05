#!/usr/bin/env python3
"""Gera o gêmeo sintético público do caso Vantara.

Reproduz a MESMA estrutura de armadilhas (23 achados, 18 linhas bancárias, 17 títulos) com
empresas, CNPJs, números e valores inventados. Nenhum valor do caso privado aparece aqui.

Uso: python3 -I testdata/synth/vantara_twin.py [pasta-de-saída]
Padrão: testdata/golden/vantara-twin/
"""
from __future__ import annotations

import datetime as dt
import os
import sys
from decimal import Decimal, ROUND_HALF_UP

OUT = sys.argv[1] if len(sys.argv) > 1 else os.path.join("testdata", "golden", "vantara-twin")

D = Decimal


def brl(v: Decimal) -> str:
    """17380.00 -> '17.380,00' (formato Protheus/Itaú)."""
    q = v.quantize(D("0.01"), rounding=ROUND_HALF_UP)
    sign = "-" if q < 0 else ""
    inteiro, frac = f"{abs(q):.2f}".split(".")
    grupos = []
    while len(inteiro) > 3:
        grupos.insert(0, inteiro[-3:])
        inteiro = inteiro[:-3]
    grupos.insert(0, inteiro)
    return f"{sign}{'.'.join(grupos)},{frac}"


def dmy(d: dt.date) -> str:
    return d.strftime("%d/%m/%Y")


def iso(d: dt.date) -> str:
    return d.isoformat()


def cnpj_valid(digits: str) -> bool:
    d = [int(c) for c in digits if c.isdigit()]
    if len(d) != 14:
        return False
    w1 = [5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2]
    w2 = [6] + w1

    def dv(ds, w):
        s = sum(a * b for a, b in zip(ds, w))
        r = s % 11
        return 0 if r < 2 else 11 - r

    return dv(d[:12], w1) == d[12] and dv(d[:13], w2) == d[13]


def mask_cnpj(digits: str) -> str:
    return f"{digits[0:2]}.{digits[2:5]}.{digits[5:8]}/{digits[8:12]}-{digits[12:14]}"


def mod10(s: str) -> int:
    tot, w = 0, 2
    for ch in reversed(s):
        p = int(ch) * w
        tot += p // 10 + p % 10
        w = 1 if w == 2 else 2
    r = tot % 10
    return 0 if r == 0 else 10 - r


def mod11(s: str) -> int:
    tot, w = 0, 2
    for ch in reversed(s):
        tot += int(ch) * w
        w = 2 if w == 9 else w + 1
    r = 11 - (tot % 11)
    return 1 if r in (0, 10, 11) else r


def boleto_check(line: str) -> dict:
    f = line.replace(".", " ").split()
    f1, f2, f3, dvg, f5 = f[0] + f[1], f[2] + f[3], f[4] + f[5], f[6], f[7]
    free = f1[4:9] + f2[:10] + f3[:10]
    bar_wo = f1[:4] + f5 + free
    return {
        "dv1": mod10(f1[:9]) == int(f1[9]),
        "dv2": mod10(f2[:10]) == int(f2[10]),
        "dv3": mod10(f3[:10]) == int(f3[10]),
        "dvg": mod11(bar_wo) == int(dvg),
        "factor": int(f5[:4]),
        "value": D(f5[4:]) / 100,
    }


# ------------------------------------------------------------------ dados inventados
Y = 2026
M = 7


def d(day: int) -> dt.date:
    return dt.date(Y, M, day)


# Clientes da operação principal (Protheus). Todos os CNPJs são inválidos de propósito (armadilha F23).
CUST = {
    "paoarte": ("Pao & Arte Panificadora Ltda", "21456789000123", "SP-Capital"),
    "luacheia": ("Confeitaria Lua Cheia Comercio de Alimentos", "34567890000145", "SP-Interior"),
    "fornoserra": ("Forno da Serra Franqueado ME", "56789012000167", "MG"),
    "trigodourado": ("Rede Trigo Dourado Ltda", "67890123000189", "RJ"),
    "colinaverde": ("Mercearia Colina Verde Alimentos SA", "78901234000101", "SP-Capital"),
    "saborroca": ("Sabor da Roca Franquias Ltda", "89012345000112", "MG"),
    "moinhoazul": ("Moinho Azul Comercio Ltda", "90123456000134", "SUL"),
    "trigoexpress": ("Trigo Express Delivery ME", "13579246000156", "SP-Capital"),
    "beirario": ("Padaria Beira Rio Ltda", "24680135000178", "SP-Interior"),
}

# (id, cliente, documento, nosso_numero, tipo, emissao, vencimento, valor)
PROTHEUS = [
    ("T01", "paoarte", "NF 200311", "000098765001", "Produto", 1, 10, D("24300.00")),
    ("T02", "paoarte", "NF 200312", "000098765002", "Royalty", 1, 10, D("2700.00")),
    ("T03", "luacheia", "NF 200320", "000098765003", "Produto", 2, 12, D("38640.50")),
    ("T04", "fornoserra", "NF 200328", "000098765004", "Produto", 3, 13, D("8450.00")),
    ("T05", "trigodourado", "NF 200335", "000098765005", "Produto", 5, 15, D("71900.00")),
    ("T06", "trigodourado", "NF 200336", "000098765006", "Royalty", 5, 15, D("6300.00")),
    ("T07", "colinaverde", "NF 200340", "000098765007", "Produto", 6, 16, D("98000.00")),
    ("T08", "saborroca", "NF 200351", "000098765008", "Servico", 7, 17, D("12400.00")),
    ("T09", "moinhoazul", "NF 200360", "000098765009", "Produto", 8, 20, D("27850.00")),
    ("T10", "luacheia", "NF 200372", "000098765010", "Royalty", 8, 20, D("5120.00")),
    ("T11", "trigoexpress", "NF 200377", "000098765011", "Produto", 9, 21, D("6210.00")),
    ("T12", "beirario", "NF 200383", "000098765012", "Produto", 10, 22, D("19500.00")),
]

# Unidade adquirida (Omie): (id, nome, cnpj, ref, vencimento, valor)
OMIE = [
    ("N01", "MOINHO AZUL COM LTDA", "90123456000134", "FAT-2026-0411", 20, D("27850.00")),
    ("N02", "NORTE PAES EIRELI", "35791357000190", "FAT-2026-0412", 14, D("10450.00")),
    ("N03", "COLINA VERDE MERCEARIA S/A", "78901234000101", "FAT-2026-0415", 16, D("2900.00")),
    ("N04", "GRAOS DO NORTE LTDA", "46802468000112", "FAT-2026-0420", 24, D("4380.00")),
    ("N05", "NORTE PAES EIRELI", "35791357000190", "FAT-2026-0421", 28, D("7650.00")),
]

AP_NF = "55120"
AP_PC = "7310"
AP_VALUE = D("14700.00")
AP_RECHARGE = (AP_VALUE * D("1.02")).quantize(D("0.01"))
AP_VOLUMES = (9, 12)
CTE_COUNT = 38
CTE_TOTAL = D("29480.15")
# Linha digitável propositalmente inválida: dígitos verificadores errados, valor 149,94 em vez de
# 14.994,00 e fator 9123 (vencimento em 2022 no ciclo antigo).
BOLETO = "03399.11223 34400.556677 88990.011223 8 91230000014994"

# ------------------------------------------------------------------ extrato (18 linhas)
BANK = [
    (10, "LIQUIDACAO COBRANCA SIMPLES", "000098765001", D("24300.00"), "C"),
    (10, "PIX RECEBIDO PAO E ARTE PANIFICAD", "", D("2700.00"), "C"),
    (13, "LIQUIDACAO COBRANCA SIMPLES", "000098765004", D("8450.00"), "C"),
    (14, "TED RECEBIDA CONFEITARIA LUA CHEI", "", D("43760.50"), "C"),
    (15, "LIQUIDACAO COBRANCA SIMPLES", "000098765005", D("71900.00"), "C"),
    (15, "TARIFA PACOTE COBRANCA JUL", "", D("980.00"), "D"),
    (16, "LIQUIDACAO COBRANCA SIMPLES", "000098765007", D("49000.00"), "C"),
    (17, "PIX RECEBIDO SABOR DA ROCA FRANQU", "", D("11780.00"), "C"),
    (20, f"PAGAMENTO FORNECEDOR NF {AP_NF}", "", AP_VALUE, "D"),
    (21, "LIQUIDACAO COBRANCA SIMPLES", "000098765009", D("27850.00"), "C"),
    (22, "PIX RECEBIDO 13579246000156", "", D("6210.00"), "C"),
    (23, "TED RECEBIDA CNPJ 24680135000178", "", D("19890.00"), "C"),
    (24, "PIX RECEBIDO REDE TRIGO", "", D("6300.00"), "C"),
    (27, "PIX RECEBIDO NORTE PAES EIRELI", "", D("10450.00"), "C"),
    (28, "DEPOSITO NAO IDENTIFICADO", "", D("4380.00"), "C"),
    (29, "PIX RECEBIDO MERCEARIA COLINA VER", "", D("2900.00"), "C"),
    (30, "PIX RECEBIDO PAO E ARTE PANIFICAD", "", D("1150.00"), "C"),
    (31, "TARIFA PIX EMISSAO JUL", "", D("62.00"), "D"),
]


def late_fee(amount: D, days: int) -> D:
    return (amount * (D("0.02") + D("0.01") * D(days) / D(30))).quantize(D("0.01"), rounding=ROUND_HALF_UP)


def main() -> None:
    os.makedirs(OUT, exist_ok=True)

    # ---- 00_LEIA-ME.md
    with open(os.path.join(OUT, "00_LEIA-ME.md"), "w", encoding="utf-8") as f:
        f.write(
            "# Gêmeo sintético do caso Vantara (amostra de julho/2026)\n\n"
            "Dados 100% inventados, gerados por `testdata/synth/vantara_twin.py`. Reproduzem a estrutura e as\n"
            "armadilhas do caso privado com outras empresas, CNPJs, números e valores. Formatos preservados como os\n"
            "sistemas exportam (datas pt-BR, vírgula decimal, ponto e vírgula, nomes inconsistentes).\n\n"
            "| Arquivo | O que é | Sistema de origem |\n|---|---|---|\n"
            "| `01_contas_a_receber_protheus_jul2026.csv` | Títulos a receber em aberto, operação principal (12 linhas) | TOTVS Protheus |\n"
            "| `02_titulos_unidade_norte_omie_jul2026.csv` | Títulos a receber em aberto, unidade adquirida (5 linhas) | Omie |\n"
            "| `03_extrato_bancario_jul2026.csv` | Extrato da conta principal de cobrança (18 linhas) | Banco |\n"
            "| `04_caixa_de_entrada_contas_a_pagar.txt` | Amostra da caixa de entrada de contas a pagar + thread de aprovação | E-mail / WhatsApp |\n\n"
            "O gabarito está em `expected.yaml`. O harness atinge 100% quando reproduz as 18 linhas, os 17 títulos e os 23 achados.\n"
        )

    # ---- 01 Protheus
    with open(os.path.join(OUT, "01_contas_a_receber_protheus_jul2026.csv"), "w", encoding="utf-8") as f:
        f.write("cliente;cnpj;unidade;documento;nosso_numero;tipo;emissao;vencimento;valor;status\n")
        for _, ck, doc, nn, tipo, em, ve, val in PROTHEUS:
            name, cnpj, unit = CUST[ck]
            f.write(f"{name};{mask_cnpj(cnpj)};{unit};{doc};{nn};{tipo};{dmy(d(em))};{dmy(d(ve))};{brl(val)};Aberto\n")

    # ---- 02 Omie
    with open(os.path.join(OUT, "02_titulos_unidade_norte_omie_jul2026.csv"), "w", encoding="utf-8") as f:
        f.write("customer_name,tax_id,invoice_ref,due_date,amount,paid_flag\n")
        for _, name, cnpj, ref, ve, val in OMIE:
            f.write(f"{name},{cnpj},{ref},{iso(d(ve))},{val:.2f},N\n")

    # ---- 03 extrato
    with open(os.path.join(OUT, "03_extrato_bancario_jul2026.csv"), "w", encoding="utf-8") as f:
        f.write("data;historico;documento;valor;tipo\n")
        for day, hist, doc, val, side in BANK:
            f.write(f"{dmy(d(day))};{hist};{doc};{brl(val)};{side}\n")

    # ---- 04 caixa de entrada
    txt = f"""========================================================================
ARQUIVO 04 — AMOSTRA DA CAIXA DE ENTRADA DE CONTAS A PAGAR (pagar@exemplo-sintetico.com.br)
Amostra de 3 itens de uma media de ~340 e-mails/mes. Formatos preservados.
========================================================================

--- E-MAIL 1 ---------------------------------------------------------
De: financeiro@caixasboavista.com.br
Para: pagar@exemplo-sintetico.com.br
Data: 06/07/2026 09:14
Assunto: NF {AP_NF} - Fatura de embalagens

Bom dia,

Segue em anexo a NF {AP_NF} referente ao pedido de caixas (PC {AP_PC}).
Valor: R$ {brl(AP_VALUE)} - vencimento 20/07.
Dados bancarios: Banco 341 / ag 0987 / cc 12345-6
Qualquer duvida estou a disposicao.

Att,
Ana

[anexo: NF{AP_NF}.pdf]


--- E-MAIL 2 ---------------------------------------------------------
De: ana.lima@caixasboavista.com.br
Para: pagar@exemplo-sintetico.com.br; compras@exemplo-sintetico.com.br
Data: 18/07/2026 16:52
Assunto: RE: cobranca em aberto - Caixas Boa Vista

Pessoal, nao identificamos o pagamento da nota abaixo. Reenvio o boleto
atualizado com juros.

Nota fiscal 0{AP_NF} - emissao 06/07/2026
Valor original R$ {brl(AP_VALUE)}
Valor atualizado R$ {brl(AP_RECHARGE)} (venc. 25/07/2026)
Linha digitavel: {BOLETO}

Obs: o pedido PC {AP_PC} foi entregue parcialmente ({AP_VOLUMES[0]} de {AP_VOLUMES[1]} volumes), o
restante sai na proxima semana.

[anexo: boleto_{AP_NF}_atualizado.pdf]


--- E-MAIL 3 ---------------------------------------------------------
De: contato@transportadoravianorte.com.br
Para: pagar@exemplo-sintetico.com.br
Data: 09/07/2026 07:41
Assunto: Faturas semana 27

Segue planilha com as {CTE_COUNT} CT-e da semana. Total R$ {brl(CTE_TOTAL)}.
Vencimento 5 dias apos o recebimento conforme contrato.
Favor confirmar recebimento.

[anexo: fretes_sem27.xlsx]


--- TRECHO DO GRUPO DE WHATSAPP "APROVACOES FIN" ----------------------
[06/07 11:02] Compras - Diego: pessoal, NF {AP_NF} Caixas Boa Vista, {brl(AP_VALUE)[:-3]}, ok pra pagar?
[06/07 11:40] Controladoria - Lucia: entrou o material completo?
[06/07 14:18] Compras - Diego: acho que sim, o Pedro confirmou
[06/07 14:19] Controladoria - Lucia: ok, aprovado
[19/07 08:33] Fin - Renata: gente a Boa Vista cobrando de novo a {AP_NF}, ja pagamos?
[19/07 08:35] Fin - Renata: achei, pagou dia 20. era outra nota entao?
[19/07 09:02] Controladoria - Lucia: nao sei, alguem confere?
"""
    with open(os.path.join(OUT, "04_caixa_de_entrada_contas_a_pagar.txt"), "w", encoding="utf-8") as f:
        f.write(txt)

    # ------------------------------------------------------------------ gabarito
    pro = {t[0]: t for t in PROTHEUS}
    amt = {t[0]: t[7] for t in PROTHEUS}
    amt.update({t[0]: t[5] for t in OMIE})
    protheus_open = sum(amt[t[0]] for t in PROTHEUS)
    omie_open = sum(t[5] for t in OMIE)
    credits = sum(v for _, _, _, v, s in BANK if s == "C")
    debits = sum(v for _, _, _, v, s in BANK if s == "D")
    fine = D("19890.00") - amt["T12"]
    unapplied = D("1150.00")
    applied = credits - fine - unapplied
    fees = D("980.00") + D("62.00")
    withholding = amt["T08"] - D("11780.00")
    partial_open = amt["T07"] - D("49000.00")
    true_open = partial_open + amt["N05"] + withholding
    naive_open = true_open + amt["N01"]
    undelivered = (AP_VALUE * (AP_VOLUMES[1] - AP_VOLUMES[0]) / AP_VOLUMES[1]).quantize(D("0.01"))

    late = {
        "T03": late_fee(amt["T03"], 1),
        "T06": late_fee(amt["T06"], 9),
        "T11": late_fee(amt["T11"], 1),
        "N02": late_fee(amt["N02"], 13),
        "N03": late_fee(amt["N03"], 13),
        "N04": late_fee(amt["N04"], 4),
    }
    late_total = sum(late.values())

    bc = boleto_check(BOLETO)
    assert not any(bc[k] for k in ("dv1", "dv2", "dv3", "dvg")), "o boleto sintético precisa ser inválido"
    for _, _, cnpj, *_ in OMIE:
        assert not cnpj_valid(cnpj)
    for _, cnpj, _ in CUST.values():
        assert not cnpj_valid(cnpj)
    assert amt["T03"] + amt["T10"] == D("43760.50")
    assert amt["T07"] / 2 == D("49000.00")
    assert (amt["T08"] * D("0.95")).quantize(D("0.01")) == D("11780.00")
    assert (amt["T12"] * D("1.02")).quantize(D("0.01")) == D("19890.00")
    assert amt["T09"] == amt["N01"]
    assert d(12).weekday() == 6, "T03 vence em domingo"

    def title(id_, src, ref, cust, cnpj, due, amount, status, **kw):
        base = {
            "id": id_, "source": src, "ref": ref, "customer": cust, "tax_id": cnpj,
            "due": iso(due), "amount": f"{amount:.2f}", "status": status,
        }
        base.update({k: v for k, v in kw.items()})
        return base

    def y(v):
        if isinstance(v, bool):
            return "true" if v else "false"
        if isinstance(v, list):
            return "[" + ", ".join(y(x) for x in v) + "]"
        if isinstance(v, (int, D)):
            return str(v)
        if v is None:
            return "null"
        s = str(v)
        if any(c in s for c in ":#{}[],&*?|<>=!%@`'\"") or s != s.strip():
            return '"' + s.replace('"', '\\"') + '"'
        return s

    def row(dct):
        return "  - { " + ", ".join(f"{k}: {y(v)}" for k, v in dct.items()) + " }\n"

    titles = []
    cust = lambda k: CUST[k][0]  # noqa: E731
    cnpj = lambda k: CUST[k][1]  # noqa: E731
    titles.append(title("T01", "protheus", "NF 200311", cust("paoarte"), cnpj("paoarte"), d(10), amt["T01"], "paid", nosso_numero="000098765001", paid_on=iso(d(10)), paid_amount=f"{amt['T01']:.2f}", open_amount=0, days_late=0, flags=[]))
    titles.append(title("T02", "protheus", "NF 200312", cust("paoarte"), cnpj("paoarte"), d(10), amt["T02"], "paid", nosso_numero="000098765002", paid_on=iso(d(10)), paid_amount=f"{amt['T02']:.2f}", open_amount=0, days_late=0, flags=[]))
    titles.append(title("T03", "protheus", "NF 200320", cust("luacheia"), cnpj("luacheia"), d(12), amt["T03"], "paid", nosso_numero="000098765003", effective_due=iso(d(13)), paid_on=iso(d(14)), paid_amount=f"{amt['T03']:.2f}", open_amount=0, days_late=2, business_days_late=1, flags=["late"]))
    titles.append(title("T04", "protheus", "NF 200328", cust("fornoserra"), cnpj("fornoserra"), d(13), amt["T04"], "paid", nosso_numero="000098765004", paid_on=iso(d(13)), paid_amount=f"{amt['T04']:.2f}", open_amount=0, days_late=0, flags=[]))
    titles.append(title("T05", "protheus", "NF 200335", cust("trigodourado"), cnpj("trigodourado"), d(15), amt["T05"], "paid", nosso_numero="000098765005", paid_on=iso(d(15)), paid_amount=f"{amt['T05']:.2f}", open_amount=0, days_late=0, flags=[]))
    titles.append(title("T06", "protheus", "NF 200336", cust("trigodourado"), cnpj("trigodourado"), d(15), amt["T06"], "paid", nosso_numero="000098765006", paid_on=iso(d(24)), paid_amount=f"{amt['T06']:.2f}", open_amount=0, days_late=9, flags=["late"]))
    titles.append(title("T07", "protheus", "NF 200340", cust("colinaverde"), cnpj("colinaverde"), d(16), amt["T07"], "partial", nosso_numero="000098765007", paid_on=iso(d(16)), paid_amount="49000.00", open_amount=f"{partial_open:.2f}", days_late=15, flags=["overdue"]))
    titles.append(title("T08", "protheus", "NF 200351", cust("saborroca"), cnpj("saborroca"), d(17), amt["T08"], "paid", nosso_numero="000098765008", paid_on=iso(d(17)), paid_amount="11780.00", open_amount=f"{withholding:.2f}", days_late=0, flags=["withholding_pending"]))
    titles.append(title("T09", "protheus", "NF 200360", cust("moinhoazul"), cnpj("moinhoazul"), d(20), amt["T09"], "paid", nosso_numero="000098765009", paid_on=iso(d(21)), paid_amount=f"{amt['T09']:.2f}", open_amount=0, days_late=1, flags=["late"]))
    titles.append(title("T10", "protheus", "NF 200372", cust("luacheia"), cnpj("luacheia"), d(20), amt["T10"], "paid", nosso_numero="000098765010", paid_on=iso(d(14)), paid_amount=f"{amt['T10']:.2f}", open_amount=0, days_late=-6, flags=["early"]))
    titles.append(title("T11", "protheus", "NF 200377", cust("trigoexpress"), cnpj("trigoexpress"), d(21), amt["T11"], "paid", nosso_numero="000098765011", paid_on=iso(d(22)), paid_amount=f"{amt['T11']:.2f}", open_amount=0, days_late=1, flags=["late"]))
    titles.append(title("T12", "protheus", "NF 200383", cust("beirario"), cnpj("beirario"), d(22), amt["T12"], "paid", nosso_numero="000098765012", paid_on=iso(d(23)), paid_amount="19890.00", open_amount=0, days_late=1, flags=["late", "fine_received"]))
    titles.append(title("N01", "omie", "FAT-2026-0411", "MOINHO AZUL COM LTDA", "90123456000134", d(20), amt["N01"], "duplicate", duplicate_of="T09", open_amount=0, flags=[]))
    titles.append(title("N02", "omie", "FAT-2026-0412", "NORTE PAES EIRELI", "35791357000190", d(14), amt["N02"], "paid", paid_on=iso(d(27)), paid_amount=f"{amt['N02']:.2f}", open_amount=0, days_late=13, flags=["late", "intercompany"]))
    titles.append(title("N03", "omie", "FAT-2026-0415", "COLINA VERDE MERCEARIA S/A", "78901234000101", d(16), amt["N03"], "paid", paid_on=iso(d(29)), paid_amount=f"{amt['N03']:.2f}", open_amount=0, days_late=13, flags=["late", "intercompany"]))
    titles.append(title("N04", "omie", "FAT-2026-0420", "GRAOS DO NORTE LTDA", "46802468000112", d(24), amt["N04"], "paid", paid_on=iso(d(28)), paid_amount=f"{amt['N04']:.2f}", open_amount=0, days_late=4, flags=["late", "proposed", "intercompany"]))
    titles.append(title("N05", "omie", "FAT-2026-0421", "NORTE PAES EIRELI", "35791357000190", d(28), amt["N05"], "open", open_amount=f"{amt['N05']:.2f}", days_late=3, flags=["overdue"]))

    lines = [
        (1, "receipt", ["T01"], "nosso_numero_exact", [], False),
        (2, "receipt", ["T02"], "name_amount_due", ["F15"], False),
        (3, "receipt", ["T04"], "nosso_numero_exact", [], False),
        (4, "receipt", ["T03", "T10"], "subset_sum_same_customer", ["F01", "F02", "F03"], False),
        (5, "receipt", ["T05"], "nosso_numero_exact", [], False),
        (6, "fee", [], "bank_fee_pattern", ["F13"], False),
        (7, "receipt", ["T07"], "nosso_numero_partial", ["F04"], False),
        (8, "receipt", ["T08"], "name_amount_withholding", ["F05"], False),
        (9, "supplier_payment", [f"AP-{AP_NF}"], "document_in_text", ["F16"], False),
        (10, "receipt", ["T09"], "nosso_numero_exact", ["F06"], False),
        (11, "receipt", ["T11"], "tax_id_in_text", [], False),
        (12, "receipt", ["T12"], "tax_id_amount_fine", ["F07"], False),
        (13, "receipt", ["T06"], "fuzzy_name_unique_amount", ["F08"], False),
        (14, "receipt", ["N02"], "name_amount", ["F09"], False),
        (15, "receipt", ["N04"], "amount_only_unique", ["F10"], True),
        (16, "receipt", ["N03"], "name_amount", ["F11"], False),
        (17, "unapplied", [], "none", ["F12"], False),
        (18, "fee", [], "bank_fee_pattern", ["F13"], False),
    ]

    findings = [
        ("F01", "one_payment_many_titles", "info", D(0), ["bank:4", "title:T03", "title:T10"], "Uma TED paga duas notas"),
        ("F02", "early_payment", "info", D(0), ["bank:4", "title:T10"], "Nota paga 6 dias antes do vencimento"),
        ("F03", "paid_late_no_fee", "low", late["T03"], ["bank:4", "title:T03"], "Vencimento em domingo; paga 1 dia útil atrasada sem encargos"),
        ("F04", "partial_payment", "high", partial_open, ["bank:7", "title:T07"], "Pagamento parcial de exatamente 50%"),
        ("F05", "withholding_suspected", "medium", withholding, ["bank:8", "title:T08"], "Diferença de 5% em nota de serviço: provável ISS retido"),
        ("F06", "duplicate_title_across_erps", "high", amt["T09"], ["title:T09", "title:N01", "bank:10"], "Mesmo título nos dois ERPs; contas a receber superestimado"),
        ("F07", "overpayment_fine_received", "low", fine, ["bank:12", "title:T12"], "Recebido 2% a mais: multa por atraso a contabilizar como receita financeira"),
        ("F08", "ambiguous_name_match", "low", late["T06"], ["bank:13", "title:T06", "title:T11"], "Nome ambíguo resolvido pelo valor; 9 dias de atraso sem encargos"),
        ("F09", "intercompany_receipt", "medium", amt["N02"], ["bank:14", "title:N02"], "Recebível da unidade Norte caiu na conta principal"),
        ("F10", "amount_only_proposal", "medium", amt["N04"], ["bank:15", "title:N04"], "Depósito não identificado: proposta por valor único, precisa de confirmação"),
        ("F11", "customer_master_duplicate", "low", D(0), ["title:T07", "title:N03", "bank:16"], "Mesmo CNPJ com nomes diferentes nos dois cadastros"),
        ("F12", "unapplied_cash", "medium", unapplied, ["bank:17"], "Recebimento sem título em aberto: adiantamento de cliente"),
        ("F13", "unbooked_bank_fees", "low", fees, ["bank:6", "bank:18"], "Tarifas bancárias sem lançamento"),
        ("F14", "overdue_open", "medium", amt["N05"], ["title:N05"], "Título vencido há 3 dias sem cobrança"),
        ("F15", "paid_without_nosso_numero", "info", D(0), ["bank:2", "title:T02"], "Pago por Pix, sem nosso número no extrato"),
        ("F16", "ap_invoice_already_paid", "high", AP_RECHARGE, ["bank:9", "email:1", "email:2", "chat:2026-07-19T08:33"], f"NF 0{AP_NF} é a NF {AP_NF}, já paga no vencimento"),
        ("F17", "ap_premature_recharge", "medium", AP_RECHARGE - AP_VALUE, ["email:2", "bank:9"], "Recobrança com 2% enviada antes do vencimento"),
        ("F18", "ap_boleto_invalid", "critical", AP_RECHARGE, ["email:2"], "Linha digitável inválida: possível fraude de boleto"),
        ("F19", "ap_sender_or_bank_details_changed", "high", AP_RECHARGE, ["email:1", "email:2"], "Remetente e forma de pagamento mudaram entre os e-mails"),
        ("F20", "ap_three_way_match_failed", "high", undelivered, ["email:2", "chat:2026-07-06T14:18", "bank:9"], f"Pedido entregue {AP_VOLUMES[0]} de {AP_VOLUMES[1]} volumes, pago integralmente"),
        ("F21", "ap_invoice_without_payment_or_approval", "high", CTE_TOTAL, ["email:3"], f"{CTE_COUNT} CT-e vencidos sem pagamento nesta conta e sem aprovação"),
        ("F22", "uncharged_late_fees", "low", late_total, ["title:T03", "title:T06", "title:T11", "title:N02", "title:N03", "title:N04"], "Seis títulos pagos com atraso sem multa nem juros"),
        ("F23", "invalid_tax_ids", "info", D(0), ["title:T01", "title:N02"], "Todos os 11 CNPJs falham no dígito verificador"),
    ]

    out = []
    out.append("# Gabarito do gêmeo sintético público do caso Vantara. Gerado por testdata/synth/vantara_twin.py.\n")
    out.append("version: 1\ncase: vantara-twin-jul-2026\nperiod: { start: 2026-07-01, end: 2026-07-31 }\n\n")
    out.append("sources:\n  protheus: 01_contas_a_receber_protheus_jul2026.csv\n  omie: 02_titulos_unidade_norte_omie_jul2026.csv\n  bank: 03_extrato_bancario_jul2026.csv\n  ap_inbox: 04_caixa_de_entrada_contas_a_pagar.txt\n\n")
    out.append("totals_brl:\n")
    for k, v in [
        ("protheus_open", protheus_open), ("omie_open", omie_open), ("bank_credits", credits), ("bank_debits", debits),
        ("applied_principal", applied), ("fines_received", fine), ("unapplied_cash", unapplied),
        ("true_open_balance", true_open), ("naive_open_balance", naive_open), ("unbooked_bank_fees", fees),
        ("uncharged_late_fees_estimate", late_total),
    ]:
        out.append(f"  {k}: {v:.2f}\n")
    out.append("\n# status: paid | partial | open | duplicate\n# flags: late | early | fine_received | withholding_pending | proposed | intercompany | overdue\ntitles:\n")
    for t in titles:
        out.append(row(t))
    out.append("\n# kind: receipt | fee | supplier_payment | unapplied\nbank_lines:\n")
    for n, kind, matched, rule, fcodes, proposed in lines:
        day, _, _, val, side = BANK[n - 1]
        r = {"n": n, "date": iso(d(day)), "amount": f"{val:.2f}", "side": side, "kind": kind, "matched": matched, "rule": rule}
        if proposed:
            r["proposed"] = True
        r["findings"] = fcodes
        out.append(row(r))
    out.append("\nfindings:\n")
    for code, kind, sev, amount, ev, ttl in findings:
        out.append(row({"code": code, "kind": kind, "severity": sev, "amount_at_risk_brl": f"{amount:.2f}", "evidence": ev, "title_ptbr": ttl}))
    out.append(f"\nboleto:\n  digit_line: \"{BOLETO}\"\n  check_digits_valid: false\n  encoded_value_brl: {bc['value']:.2f}\n  claimed_value_brl: {AP_RECHARGE:.2f}\n  due_factor: {bc['factor']}\n  due_factor_date: 2022-09-29\n  expected_factor_for_2026_07_25: 1518\n")
    out.append("\nlate_fee_estimates_brl:\n")
    for k, v in late.items():
        out.append(f"  {k}: {v:.2f}\n")
    with open(os.path.join(OUT, "expected.yaml"), "w", encoding="utf-8") as f:
        f.write("".join(out))

    print(f"gerado em {OUT}: créditos {credits:.2f}, débitos {debits:.2f}, saldo real em aberto {true_open:.2f}, ingênuo {naive_open:.2f}")


if __name__ == "__main__":
    main()
