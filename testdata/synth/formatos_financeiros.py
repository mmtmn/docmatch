#!/usr/bin/env python3
"""Gera os fixtures sintéticos dos formatos financeiros (PDF, CNAB 240/400, SPED, ZIP) e a
variação do gêmeo Vantara com o extrato em PDF e a carteira de cobrança em CNAB 240.

Tudo é inventado: empresas, CNPJs, números e valores. A variação do gêmeo lê os CSVs do gêmeo
público (testdata/golden/vantara-twin) e só troca o formato: o extrato vira PDF com camada de
texto e o razão a receber principal vira um retorno CNAB 240 (segmentos T/U, movimento 02,
entrada confirmada). O gabarito é o do gêmeo com os nomes de arquivo trocados.

Só biblioteca padrão; a saída é determinística (sem datas de geração), então rodar de novo não
muda nenhum byte.

Uso: python3 -I testdata/synth/formatos_financeiros.py [raiz-do-repositório]
"""
from __future__ import annotations

import csv
import io
import os
import shutil
import sys
import unicodedata
import zipfile
import zlib
from decimal import Decimal

ROOT = sys.argv[1] if len(sys.argv) > 1 else "."
GOLDEN = os.path.join(ROOT, "testdata", "golden")
TWIN = os.path.join(GOLDEN, "vantara-twin")
VARIANT = os.path.join(GOLDEN, "vantara-twin-pdf-cnab240")

D = Decimal


def brl(v: Decimal) -> str:
    """17380.00 -> '17.380,00'."""
    q = abs(v.quantize(D("0.01")))
    inteiro, frac = f"{q:.2f}".split(".")
    grupos = []
    while len(inteiro) > 3:
        grupos.insert(0, inteiro[-3:])
        inteiro = inteiro[:-3]
    grupos.insert(0, inteiro)
    return f"{'.'.join(grupos)},{frac}"


def write(path: str, data: bytes) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)


# ----------------------------------------------------------------------------------- PDF

# Helvetica advance widths (Adobe AFM), ASCII 32..126.
HELVETICA = [
    278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, 556, 556,
    556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, 1015, 667, 667, 722,
    722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, 667, 778, 722, 667, 611, 722,
    667, 944, 667, 667, 611, 278, 278, 278, 469, 556, 333, 556, 556, 500, 556, 556, 278, 556,
    556, 222, 222, 500, 222, 833, 556, 556, 556, 556, 333, 500, 278, 556, 500, 722, 500, 500,
    500, 334, 260, 334, 584,
]


def char_width(ch: str) -> int:
    code = ord(ch)
    if 32 <= code <= 126:
        return HELVETICA[code - 32]
    base = unicodedata.normalize("NFD", ch)[0]
    if 32 <= ord(base) <= 126:
        return HELVETICA[ord(base) - 32]
    return 556


WIN_ANSI_WIDTHS = [char_width(bytes([c]).decode("cp1252", errors="replace")) for c in range(32, 256)]


def text_width(text: str, size: float) -> float:
    return sum(char_width(c) for c in text) * size / 1000


def pdf_string(text: str) -> str:
    raw = text.encode("cp1252")
    out = []
    for b in raw:
        if b in (0x28, 0x29, 0x5C):
            out.append("\\" + chr(b))
        elif b < 0x20 or b > 0x7E:
            out.append(f"\\{b:03o}")
        else:
            out.append(chr(b))
    return "(" + "".join(out) + ")"


class Pdf:
    """Minimal PDF 1.4 writer: Helvetica with /Widths, Flate content streams, gray images."""

    W, H = 595, 842

    def __init__(self) -> None:
        self.pages: list[tuple[list[str], bool]] = []

    def page(self, ops: list[str], image: bool = False) -> None:
        self.pages.append((ops, image))

    @staticmethod
    def text(x: float, y: float, text: str, size: float = 9) -> str:
        return f"BT /F1 {size:g} Tf {x:.2f} {y:.2f} Td {pdf_string(text)} Tj ET"

    @classmethod
    def right(cls, x_right: float, y: float, text: str, size: float = 9) -> str:
        return cls.text(x_right - text_width(text, size), y, text, size)

    def bytes(self) -> bytes:
        objects: list[bytes] = []

        def add(body: bytes) -> int:
            objects.append(body)
            return len(objects)

        add(b"<< /Type /Catalog /Pages 2 0 R >>")
        add(b"")  # pages, filled below
        widths = " ".join(str(w) for w in WIN_ANSI_WIDTHS)
        font = add(
            (
                "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding "
                f"/FirstChar 32 /LastChar 255 /Widths [{widths}] >>"
            ).encode()
        )
        kids = []
        for ops, image in self.pages:
            content = zlib.compress("\n".join(ops).encode("latin-1"), 9)
            content_id = add(
                f"<< /Length {len(content)} /Filter /FlateDecode >>\nstream\n".encode()
                + content
                + b"\nendstream"
            )
            resources = f"/Font << /F1 {font} 0 R >>"
            if image:
                pixels = bytes((x * 13 + y * 7) % 256 for y in range(16) for x in range(16))
                data = zlib.compress(pixels, 9)
                image_id = add(
                    (
                        "<< /Type /XObject /Subtype /Image /Width 16 /Height 16 /ColorSpace "
                        f"/DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length {len(data)} >>\nstream\n"
                    ).encode()
                    + data
                    + b"\nendstream"
                )
                resources += f" /XObject << /Im1 {image_id} 0 R >>"
            page_id = add(
                (
                    f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 {self.W} {self.H}] "
                    f"/Resources << {resources} >> /Contents {content_id} 0 R >>"
                ).encode()
            )
            kids.append(page_id)
        objects[1] = (
            f"<< /Type /Pages /Kids [{' '.join(f'{k} 0 R' for k in kids)}] /Count {len(kids)} >>"
        ).encode()
        out = bytearray(b"%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
        offsets = []
        for i, body in enumerate(objects, start=1):
            offsets.append(len(out))
            out += f"{i} 0 obj\n".encode() + body + b"\nendobj\n"
        xref = len(out)
        out += f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode()
        for off in offsets:
            out += f"{off:010d} 00000 n \n".encode()
        out += (
            f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n"
        ).encode()
        return bytes(out)


def signed_brl(v: Decimal) -> str:
    return f"{brl(v)} {'D' if v < 0 else 'C'}"


def statement_dc_suffix(rows, opening: Decimal, period: str, per_page: int) -> bytes:
    """Bank statement: Data | Histórico | Documento | Valor (R$) | Saldo (R$), C/D suffixes."""
    pdf = Pdf()
    chunks = [rows[i : i + per_page] for i in range(0, len(rows), per_page)]
    balance = opening
    for p, chunk in enumerate(chunks, start=1):
        ops = []
        y = 800
        if p == 1:
            ops.append(Pdf.text(40, y, "BANCO SINTÉTICO S.A.", 12))
            ops.append(Pdf.text(40, y - 16, "Extrato de conta corrente", 10))
            ops.append(Pdf.text(40, y - 30, "EMPRESA SINTÉTICA DE ALIMENTOS LTDA   Agência 0001   Conta 12345-6"))
            ops.append(Pdf.text(40, y - 44, f"Período: {period}"))
            y -= 70
        else:
            ops.append(Pdf.text(40, y, "BANCO SINTÉTICO S.A. - Extrato de conta corrente (continuação)", 10))
            y -= 26
        ops.append(Pdf.text(40, y, "Data"))
        ops.append(Pdf.text(100, y, "Histórico"))
        ops.append(Pdf.text(300, y, "Documento"))
        ops.append(Pdf.right(455, y, "Valor (R$)"))
        ops.append(Pdf.right(555, y, "Saldo (R$)"))
        y -= 16
        if p == 1:
            ops.append(Pdf.text(40, y, period.split(" a ")[0]))
            ops.append(Pdf.text(100, y, "SALDO ANTERIOR"))
            ops.append(Pdf.right(555, y, signed_brl(balance)))
            y -= 14
        for date, hist, doc, value in chunk:
            balance += value
            ops.append(Pdf.text(40, y, date))
            ops.append(Pdf.text(100, y, hist))
            if doc:
                ops.append(Pdf.text(300, y, doc))
            ops.append(Pdf.right(455, y, signed_brl(value)))
            ops.append(Pdf.right(555, y, signed_brl(balance)))
            y -= 14
        if p == len(chunks):
            ops.append(Pdf.text(40, y, rows[-1][0]))
            ops.append(Pdf.text(100, y, "SALDO FINAL"))
            ops.append(Pdf.right(555, y, signed_brl(balance)))
        ops.append(Pdf.text(40, 40, "Central de atendimento: 0800 000 0000 - documento sintético para testes", 7))
        ops.append(Pdf.right(555, 40, f"Página {p} de {len(chunks)}", 7))
        pdf.page(ops)
    return pdf.bytes()


def statement_credit_debit() -> bytes:
    """Data | Lançamento | Docto | Crédito | Débito | Saldo; the date printed once per day, a
    description continued on the next line, debits with a minus sign, and a third page that is
    only an image (scanned)."""
    rows = [
        ("01/07", "PIX RECEBIDO", "", D("1500.00")),
        ("", "PADARIA CENTRAL LTDA", None, None),
        ("", "TARIFA MENSALIDADE PACOTE", "", D("-45.90")),
        ("03/07", "LIQUIDACAO COBRANCA", "123456", D("2340.10")),
        ("", "PAGAMENTO BOLETO ENERGIA", "778899", D("-890.00")),
        ("04/07", "TED RECEBIDA 45723174000110", "", D("10000.00")),
    ]
    page2 = [
        ("08/07", "DEBITO AUTOMATICO SEGURO", "4455", D("-210.33")),
        ("09/07", "PIX RECEBIDO", "", D("75.00")),
        ("", "MARIA DA SILVA", None, None),
    ]
    pdf = Pdf()
    balance = D("5000.00")

    def header(ops, y):
        ops.append(Pdf.text(40, y, "Data"))
        ops.append(Pdf.text(80, y, "Lançamento"))
        ops.append(Pdf.text(290, y, "Docto"))
        ops.append(Pdf.right(400, y, "Crédito"))
        ops.append(Pdf.right(470, y, "Débito"))
        ops.append(Pdf.right(555, y, "Saldo"))

    for p, chunk in enumerate([rows, page2], start=1):
        ops = [Pdf.text(40, 800, "COOPERATIVA SINTÉTICA DE CRÉDITO - extrato de 01/07/2026 a 10/07/2026", 10)]
        header(ops, 770)
        y = 754
        if p == 1:
            ops.append(Pdf.text(80, y, "SALDO ANTERIOR"))
            ops.append(Pdf.right(555, y, brl(balance)))
            y -= 14
        for date, hist, doc, value in chunk:
            if value is None:
                ops.append(Pdf.text(80, y + 4, hist, 8))
                y -= 10
                continue
            balance += value
            if date:
                ops.append(Pdf.text(40, y, date))
            ops.append(Pdf.text(80, y, hist))
            if doc:
                ops.append(Pdf.text(290, y, doc))
            if value > 0:
                ops.append(Pdf.right(400, y, brl(value)))
            else:
                ops.append(Pdf.right(470, y, "-" + brl(value)))
            ops.append(Pdf.right(555, y, brl(balance)))
            y -= 14
        ops.append(Pdf.right(555, 40, f"Página {p} de 3", 7))
        pdf.page(ops)
    pdf.page(["q 495 0 0 742 50 50 cm /Im1 Do Q"], image=True)
    return pdf.bytes()


def scanned() -> bytes:
    pdf = Pdf()
    pdf.page(["q 495 0 0 742 50 50 cm /Im1 Do Q"], image=True)
    return pdf.bytes()


def letter() -> bytes:
    pdf = Pdf()
    pdf.page(
        [
            Pdf.text(40, 800, "DISTRIBUIDORA SINTÉTICA LTDA - CNPJ 11.222.333/0001-81", 11),
            Pdf.text(40, 770, "Prezados, segue a cobrança da nota fiscal 088231 referente ao pedido 7310."),
            Pdf.text(40, 756, "Pedimos a gentileza de confirmar o recebimento deste aviso."),
            Pdf.text(40, 720, "05/07/2026"),
            Pdf.text(110, 720, "Parcela única"),
            Pdf.right(400, 720, "17.380,00"),
            Pdf.text(40, 680, "Atenciosamente,"),
            Pdf.text(40, 666, "Departamento financeiro"),
        ]
    )
    return pdf.bytes()


# ----------------------------------------------------------------------------------- CNAB


class Rec:
    """A fixed-width record; positions are 1-based and inclusive, as in the manuals."""

    def __init__(self, length: int) -> None:
        self.chars = [" "] * length

    def put(self, start: int, value: str) -> "Rec":
        for i, c in enumerate(value):
            self.chars[start - 1 + i] = c
        return self

    def alpha(self, start: int, end: int, value: str) -> "Rec":
        width = end - start + 1
        return self.put(start, value[:width].ljust(width))

    def num(self, start: int, end: int, value) -> "Rec":
        width = end - start + 1
        return self.put(start, str(value).rjust(width, "0")[-width:])

    def money(self, start: int, end: int, value: Decimal) -> "Rec":
        return self.num(start, end, int((value * 100).quantize(D("1"))))

    def __str__(self) -> str:
        return "".join(self.chars)


def ddmmaaaa(dmy: str) -> str:
    d, m, y = dmy.split("/")
    return f"{d}{m}{y}"


def ddmmaa(dmy: str) -> str:
    d, m, y = dmy.split("/")
    return f"{d}{m}{y[2:]}"


def cnab240_file(bank: str, company: str, details: list[Rec], service: str = "01", op: str = "T", ret: bool = True) -> bytes:
    """Header de arquivo, um lote, os detalhes, trailers (FEBRABAN 240 v10)."""
    header = (
        Rec(240).put(1, bank).put(4, "0000").put(8, "0").put(18, "2").num(19, 32, "11222333000181")
        .alpha(33, 52, "CONVENIO SINTETICO").num(53, 57, 1).put(58, "0").num(59, 70, 123456).put(71, "7")
        .alpha(73, 102, company).alpha(103, 132, "BANCO SINTETICO").put(143, "2" if ret else "1")
        .put(144, "10072026").put(152, "070000").num(158, 163, 123).put(164, "103")
    )
    lot = (
        Rec(240).put(1, bank).put(4, "0001").put(8, "1").put(9, op).put(10, service).put(14, "060")
        .put(18, "2").num(19, 33, "11222333000181").alpha(74, 103, company)
    )
    for i, d in enumerate(details, start=1):
        d.put(1, bank).put(4, "0001").put(8, "3").num(9, 13, i)
    lot_trailer = Rec(240).put(1, bank).put(4, "0001").put(8, "5").num(18, 23, len(details) + 2)
    trailer = Rec(240).put(1, bank).put(4, "9999").put(8, "9").num(18, 23, 1).num(24, 29, len(details) + 4)
    lines = [header, lot, *details, lot_trailer, trailer]
    return ("\r\n".join(str(r) for r in lines) + "\r\n").encode("ascii")


def segment_t(movement: str, nosso: str, seu: str, due: str, value: Decimal, kind: str, tax: str, name: str, company_ref: str = "", fee: Decimal = D(0)) -> Rec:
    return (
        Rec(240).put(14, "T").put(16, movement).num(18, 22, 1).num(24, 35, 123456).put(36, "7")
        .alpha(38, 57, nosso).put(58, "1").alpha(59, 73, seu).put(74, ddmmaaaa(due)).money(82, 96, value)
        .put(97, "999").num(100, 104, 1).alpha(106, 130, company_ref).put(131, "09").put(133, kind)
        .num(134, 148, tax).alpha(149, 188, name).money(199, 213, fee)
    )


def segment_u(movement: str, occurred: str, paid: Decimal = D(0), interest: Decimal = D(0), credit: str = "", net: Decimal | None = None) -> Rec:
    r = (
        Rec(240).put(14, "U").put(16, movement).money(18, 32, interest).money(33, 47, D(0))
        .money(48, 62, D(0)).money(63, 77, D(0)).money(78, 92, paid)
        .money(93, 107, paid if net is None else net).money(108, 122, D(0)).money(123, 137, D(0))
        .put(138, ddmmaaaa(occurred))
    )
    r.put(146, ddmmaaaa(credit) if credit else "00000000")
    return r


def twin_cnab240(ledger_csv: str) -> bytes:
    """The twin's main receivables ledger as the bank reports it: one T/U pair per title, movement
    02 (entrada confirmada) on the issue date."""
    rows = list(csv.DictReader(io.StringIO(ledger_csv), delimiter=";"))
    details = []
    for r in rows:
        value = D(r["valor"].replace(".", "").replace(",", "."))
        tax = "".join(c for c in r["cnpj"] if c.isdigit())
        doc = r["documento"]
        details.append(
            segment_t("02", r["nosso_numero"], doc, r["vencimento"], value, "2", tax, r["cliente"], doc.replace(" ", ""))
        )
        details.append(segment_u("02", r["emissao"]))
    return cnab240_file("999", "EMPRESA SINTETICA DE ALIMENTOS", details)


def cnab240_return() -> bytes:
    """Collection return with every kind of movement."""
    d = []
    d += [segment_t("02", "000000777001", "DM 1001", "15/07/2026", D("1500.00"), "2", "45723174000110", "PADARIA CENTRAL LTDA", "PED1001", D("1.50")), segment_u("02", "01/07/2026")]
    d += [segment_t("06", "000000777002", "DM 1002", "10/07/2026", D("2340.10"), "2", "11222333000181", "DISTRIBUIDORA SINTETICA LTDA", "PED1002", D("2.10")),
          segment_u("06", "12/07/2026", paid=D("2390.00"), interest=D("49.90"), credit="13/07/2026", net=D("2387.90"))]
    d += [segment_t("09", "000000777003", "DM 1003", "05/07/2026", D("99.90"), "1", "12345678909", "MARIA DA SILVA"), segment_u("09", "08/07/2026")]
    d += [segment_t("03", "000000777004", "DM 1004", "20/07/2026", D("10.00"), "2", "45723174000110", "PADARIA CENTRAL LTDA").put(214, "0809"), segment_u("03", "02/07/2026")]
    d += [segment_t("28", "000000777001", "DM 1001", "15/07/2026", D("1500.00"), "2", "45723174000110", "PADARIA CENTRAL LTDA", "", D("1.50")), segment_u("28", "02/07/2026")]
    return cnab240_file("999", "EMPRESA SINTETICA DE ALIMENTOS", d)


def cnab240_remittance() -> bytes:
    def p(nosso, seu, due, value, species, issued):
        return (
            Rec(240).put(14, "P").put(16, "01").num(18, 22, 1).num(24, 35, 123456).alpha(38, 57, nosso)
            .put(58, "1").put(59, "1").put(60, "1").put(61, "2").put(62, "2").alpha(63, 77, seu)
            .put(78, ddmmaaaa(due)).money(86, 100, value).put(107, species).put(109, "N")
            .put(110, ddmmaaaa(issued)).put(118, "1").put(221, "3").put(228, "09")
        )

    def q(kind, tax, name):
        return Rec(240).put(14, "Q").put(16, "01").put(18, kind).num(19, 33, tax).alpha(34, 73, name).alpha(74, 113, "RUA SINTETICA 100")

    d = [
        p("000000888001", "NF 5001", "20/07/2026", D("12400.00"), "04", "07/07/2026"), q("2", "45723174000110", "PADARIA CENTRAL LTDA"),
        Rec(240).put(14, "R").put(16, "01"),
        p("000000888002", "NF 5002", "25/07/2026", D("870.00"), "02", "08/07/2026"), q("1", "12345678909", "MARIA DA SILVA"),
    ]
    return cnab240_file("999", "EMPRESA SINTETICA DE ALIMENTOS", d, op="R", ret=False)


def cnab240_payments() -> bytes:
    barcode = "34191790010104351004791020150008291070026000"
    a = (
        Rec(240).put(14, "A").put(15, "0").put(16, "00").put(18, "018").put(21, "341").num(24, 28, 987)
        .put(29, "1").num(30, 41, 12345).put(42, "6").alpha(44, 73, "CAIXAS BOA VISTA LTDA")
        .alpha(74, 93, "NF 55120").put(94, "20072026").put(102, "BRL").money(120, 134, D("14700.00"))
        .alpha(135, 154, "SISPAG0001").put(155, "20072026").money(163, 177, D("14700.00")).put(231, "00")
    )
    b = Rec(240).put(14, "B").put(18, "2").num(19, 32, "11222333000181").alpha(33, 62, "RUA SINTETICA")
    j = (
        Rec(240).put(14, "J").put(15, "0").put(16, "00").put(18, barcode).alpha(62, 91, "ENERGIA SINTETICA SA")
        .put(92, "25072026").money(100, 114, D("890.00")).money(115, 129, D(0)).money(130, 144, D(0))
        .put(145, "24072026").money(153, 167, D("890.00")).alpha(183, 202, "CONTA JULHO").put(231, "BD")
    )
    j52 = (
        Rec(240).put(14, "J").put(16, "00").put(18, "52").put(20, "2").num(21, 35, "11222333000181")
        .alpha(36, 75, "EMPRESA SINTETICA DE ALIMENTOS").put(76, "2").num(77, 91, "45723174000110")
        .alpha(92, 131, "ENERGIA SINTETICA SA")
    )
    o = Rec(240).put(14, "O").put(18, "8" + "1" * 43)
    return cnab240_file("999", "EMPRESA SINTETICA DE ALIMENTOS", [a, b, j, j52, o], service="20", op="C")


def cnab400_file(bank: str, bank_name: str, details: list[Rec]) -> bytes:
    header = (
        Rec(400).put(1, "02RETORNO01").alpha(12, 26, "COBRANCA").num(27, 46, 4321)
        .alpha(47, 76, "EMPRESA SINTETICA DE ALIMENTOS").put(77, bank).alpha(80, 94, bank_name)
        .put(95, "100726").num(395, 400, 1)
    )
    for i, d in enumerate(details, start=2):
        d.num(395, 400, i)
    trailer = Rec(400).put(1, "9").put(2, "2").put(3, "01").put(5, bank).num(395, 400, len(details) + 2)
    return ("\r\n".join(str(r) for r in [header, *details, trailer]) + "\r\n").encode("ascii")


def detail400(kind: str, occurrence: str, occurred: str, seu: str, due: str, value: Decimal, paid: Decimal, interest: Decimal) -> Rec:
    """Common 400 return fields (109-279), the same in every consulted manual."""
    return (
        Rec(400).put(1, kind).put(2, "02").num(4, 17, "11222333000181").put(109, occurrence)
        .put(111, ddmmaa(occurred)).alpha(117, 126, seu).put(147, ddmmaa(due)).money(153, 165, value)
        .put(166, "999").money(228, 240, D(0)).money(241, 253, D(0)).money(254, 266, paid)
        .money(267, 279, interest)
    )


def cnab400_returns() -> dict[str, bytes]:
    out = {}
    # Itaú 341: nosso número 63-70, tarifa 176-188, data do crédito 296-301, pagador 325-354.
    itau = [
        detail400("1", "06", "10/07/2026", "NF 200311", "10/07/2026", D("24300.00"), D("24300.00"), D(0))
        .alpha(38, 62, "PED200311").put(63, "98765001").money(176, 188, D("1.90")).put(296, "110726")
        .alpha(325, 354, "PAO E ARTE PANIFICADORA LTDA"),
        detail400("1", "02", "01/07/2026", "NF 200312", "10/07/2026", D("2700.00"), D(0), D(0))
        .put(63, "98765002").alpha(325, 354, "PAO E ARTE PANIFICADORA LTDA"),
    ]
    out["retorno_itau_341.ret"] = cnab400_file("341", "BANCO ITAU SA", itau)
    # Bradesco 237: nosso número 71-82, tarifa 176-188, data do crédito 296-301, motivos 319-328.
    bradesco = [
        detail400("1", "06", "13/07/2026", "NF 200328", "13/07/2026", D("8450.00"), D("8450.00"), D(0))
        .put(71, "000098765004").money(176, 188, D("2.45")).put(296, "140726").put(319, "00"),
        detail400("1", "28", "13/07/2026", "NF 200328", "13/07/2026", D("8450.00"), D(0), D(0))
        .put(71, "000098765004").money(176, 188, D("2.45")),
    ]
    out["retorno_bradesco_237.ret"] = cnab400_file("237", "BRADESCO", bradesco)
    # Santander 033: nosso número 63-70, tarifa 176-188, data do crédito 296-301.
    santander = [
        detail400("1", "06", "15/07/2026", "NF 200335", "15/07/2026", D("71900.00"), D("71900.00"), D(0))
        .put(63, "98765005").money(176, 188, D("3.10")).put(296, "160726"),
    ]
    out["retorno_santander_033.ret"] = cnab400_file("033", "SANTANDER", santander)
    # Banco do Brasil 001, CBR643 convênio de 7 posições: detalhe tipo 7, nosso número 64-80,
    # data do crédito 176-181, tarifa 182-188; e um detalhe tipo 1 (convênio de 6 posições).
    bb = [
        detail400("7", "06", "16/07/2026", "NF 200340", "16/07/2026", D("98000.00"), D("49000.00"), D(0))
        .put(64, "12345670098765007").put(176, "170726").num(182, 188, 350),
        detail400("1", "02", "06/07/2026", "NF 200341", "30/07/2026", D("1000.00"), D(0), D(0)),
    ]
    out["retorno_bb_001.ret"] = cnab400_file("001", "BANCO DO BRASIL", bb)
    for bank, name in [("104", "CAIXA"), ("756", "SICOOB"), ("998", "BANCO SINTETICO")]:
        details = [
            detail400("1", "06", "21/07/2026", "NF 200360", "20/07/2026", D("27850.00"), D("27850.00"), D(0))
            .put(63, "98765009"),
        ]
        out[f"retorno_{name.lower().replace(' ', '_')}_{bank}.ret"] = cnab400_file(bank, name, details)
    return out


# ----------------------------------------------------------------------------------- SPED


def sped(lines: list[str], signature: bytes = b"") -> bytes:
    body = "".join(f"|{'|'.join(fields)}|\r\n" for fields in lines)
    return body.encode("latin-1") + signature


def efd_icms_ipi() -> bytes:
    recs = [
        ["0000", "017", "0", "01072026", "31072026", "EMPRESA SINTÉTICA DE ALIMENTOS LTDA", "11222333000181", "", "SP", "110042490114", "3550308", "", "", "A", "1"],
        ["0001", "0"],
        ["0150", "P001", "PADARIA CENTRAL LTDA", "01058", "45723174000110", "", "110042490118", "3550308", "", "RUA SINTETICA", "100", "", "CENTRO"],
        ["0150", "P002", "MARIA DA SILVA", "01058", "", "12345678909", "", "3550308", "", "RUA DOIS", "2", "", "CENTRO"],
        ["0990", "5"],
        ["C001", "0"],
        ["C100", "1", "0", "P001", "55", "00", "1", "088231", "35260711222333000181550010000882311000882310", "05072026", "05072026", "17380,00", "1", "0,00", "0,00", "17380,00", "9", "0,00", "0,00", "0,00", "17380,00", "3128,40", "0,00", "0,00", "0,00", "286,77", "1320,88", "", ""],
        ["C170", "1", "FAR01", "FARINHA DE TRIGO 25KG", "100,00000", "SC", "17380,00", "0,00", "0", "000", "5102", "", "17380,00", "18,00", "3128,40", "0,00", "0,00", "0,00", "0", "", "", "0,00", "0,00", "0,00", "01", "17380,00", "1,65", "", "", "286,77", "01", "17380,00", "7,60", "", "", "1320,88", "3.01.01"],
        ["C190", "000", "5102", "18,00", "17380,00", "17380,00", "3128,40", "0,00", "0,00", "0,00", "0,00", ""],
        ["C100", "0", "1", "P002", "55", "00", "1", "000512", "35260745723174000110550010000005121000005120", "06072026", "08072026", "870,00", "0", "0,00", "0,00", "870,00", "9", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "", ""],
        ["C990", "6"],
        ["E001", "0"],
        ["E100", "01072026", "31072026"],
        ["E110", "3128,40", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "0,00", "3128,40", "0,00", "3128,40", "0,00", "0,00"],
        ["E990", "4"],
        ["9001", "0"],
        ["9900", "0000", "1"], ["9900", "0150", "2"], ["9900", "C100", "2"], ["9900", "C170", "1"],
        ["9900", "C190", "1"], ["9900", "E110", "1"],
        ["9990", "9"],
        ["9999", "25"],
    ]
    return sped(recs, b"SBRCAAEPDR\x00\x01\x02 assinatura sintetica \xff\xfe")


def efd_contribuicoes() -> bytes:
    recs = [
        ["0000", "006", "0", "", "", "01072026", "31072026", "EMPRESA SINTÉTICA DE ALIMENTOS LTDA", "11222333000181", "SP", "3550308", "", "00", "1"],
        ["0001", "0"],
        ["0150", "P001", "PADARIA CENTRAL LTDA", "01058", "45723174000110", "", "110042490118", "3550308", "", "RUA SINTETICA", "100", "", "CENTRO"],
        ["0990", "4"],
        ["C001", "0"],
        ["C010", "11222333000181", "2"],
        ["C100", "1", "0", "P001", "55", "00", "1", "088231", "35260711222333000181550010000882311000882310", "05072026", "05072026", "17380,00", "1", "0,00", "0,00", "17380,00", "9", "0,00", "0,00", "0,00", "17380,00", "3128,40", "0,00", "0,00", "0,00", "286,77", "1320,88", "", ""],
        ["C170", "1", "FAR01", "FARINHA DE TRIGO 25KG", "100,00000", "SC", "17380,00", "0,00", "0", "000", "5102", "", "17380,00", "18,00", "3128,40", "0,00", "0,00", "0,00", "0", "", "", "0,00", "0,00", "0,00", "01", "17380,00", "1,65", "", "", "286,77", "01", "17380,00", "7,60", "", "", "1320,88", "3.01.01"],
        ["C990", "5"],
        ["M001", "0"],
        ["M200", "286,77", "0,00", "0,00", "286,77", "0,00", "0,00", "286,77", "0,00", "0,00", "0,00", "0,00", "286,77"],
        ["M990", "3"],
        ["9001", "0"],
        ["9900", "C100", "1"],
        ["9990", "3"],
        ["9999", "17"],
    ]
    return sped(recs)


def ecd() -> bytes:
    recs = [
        ["0000", "LECD", "01012025", "31122025", "EMPRESA SINTÉTICA DE ALIMENTOS LTDA", "11222333000181", "SP", "110042490114", "3550308", "", "", "0", "0", "0", "", "0", "0", "", "N", "N", "0", "0", "1"],
        ["0001", "0"],
        ["0990", "3"],
        ["I001", "0"],
        ["I010", "G", "9.00"],
        ["I200", "L000123", "15032025", "17380,00", "N", ""],
        ["I250", "1.1.02.001", "", "17380,00", "D", "", "", "RECEBIMENTO NF 088231 PADARIA CENTRAL", "P001"],
        ["I250", "1.1.01.003", "", "17380,00", "C", "", "", "BAIXA DUPLICATA NF 088231", "P001"],
        ["I200", "L000124", "20032025", "45,90", "N", ""],
        ["I250", "3.2.01.010", "CC01", "45,90", "D", "", "", "TARIFA BANCARIA MARCO", ""],
        ["I250", "1.1.02.001", "", "45,90", "C", "", "", "TARIFA BANCARIA MARCO", ""],
        ["I990", "8"],
        ["9001", "0"],
        ["9900", "I200", "2"], ["9900", "I250", "4"],
        ["9990", "4"],
        ["9999", "17"],
    ]
    return sped(recs)


# ----------------------------------------------------------------------------------- ZIP


def zipped(entries: list[tuple[str, bytes]]) -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", compression=zipfile.ZIP_DEFLATED) as z:
        for name, data in entries:
            info = zipfile.ZipInfo(name, date_time=(2026, 7, 31, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED if not name.endswith("/") else zipfile.ZIP_STORED
            info.external_attr = 0o644 << 16
            z.writestr(info, data, compresslevel=9)
    return buf.getvalue()


# ----------------------------------------------------------------------------------- twin variant


def twin_variant() -> None:
    with open(os.path.join(TWIN, "01_contas_a_receber_protheus_jul2026.csv"), encoding="utf-8") as f:
        ledger = f.read()
    with open(os.path.join(TWIN, "03_extrato_bancario_jul2026.csv"), encoding="utf-8") as f:
        bank = list(csv.DictReader(f, delimiter=";"))
    rows = []
    for r in bank:
        value = D(r["valor"].replace(".", "").replace(",", "."))
        rows.append((r["data"], r["historico"], r["documento"], value if r["tipo"] == "C" else -value))
    if os.path.isdir(VARIANT):
        shutil.rmtree(VARIANT)
    os.makedirs(VARIANT)
    ret = "01_retorno_cobranca_jul2026.ret"
    pdf = "03_extrato_bancario_jul2026.pdf"
    write(os.path.join(VARIANT, ret), twin_cnab240(ledger))
    write(os.path.join(VARIANT, pdf), statement_dc_suffix(rows, D("150000.00"), "01/07/2026 a 31/07/2026", 10))
    for name in ["02_titulos_unidade_norte_omie_jul2026.csv", "04_caixa_de_entrada_contas_a_pagar.txt"]:
        shutil.copyfile(os.path.join(TWIN, name), os.path.join(VARIANT, name))
    with open(os.path.join(TWIN, "expected.yaml"), encoding="utf-8") as f:
        expected = f.read()
    expected = expected.replace(
        "# Gabarito do gêmeo sintético público do caso Vantara. Gerado por testdata/synth/vantara_twin.py.",
        "# Gabarito da variação do gêmeo com extrato em PDF e carteira em CNAB 240: o mesmo do gêmeo\n"
        "# (testdata/golden/vantara-twin/expected.yaml) com os nomes de arquivo trocados.\n"
        "# Gerado por testdata/synth/formatos_financeiros.py.",
    )
    expected = expected.replace("protheus: 01_contas_a_receber_protheus_jul2026.csv", f"protheus: {ret}")
    expected = expected.replace("bank: 03_extrato_bancario_jul2026.csv", f"bank: {pdf}")
    expected = expected.replace("case: vantara-twin-jul-2026", "case: vantara-twin-pdf-cnab240-jul-2026")
    write(os.path.join(VARIANT, "expected.yaml"), expected.encode("utf-8"))
    readme = f"""# Variação do gêmeo: extrato em PDF e carteira em CNAB 240

Gerada por `testdata/synth/formatos_financeiros.py` a partir do gêmeo público
(`testdata/golden/vantara-twin`). Mesmos títulos, mesmas linhas de extrato, mesmo gabarito; só os
formatos mudam:

| Arquivo | O que é |
|---|---|
| `{ret}` | Os 12 títulos da operação principal como o banco os devolve: retorno CNAB 240 (FEBRABAN), segmentos T e U, movimento 02 (entrada confirmada). Sem espécie do título: o segmento T não a traz. |
| `02_titulos_unidade_norte_omie_jul2026.csv` | Cópia do gêmeo. |
| `{pdf}` | O extrato de julho em PDF com camada de texto, duas páginas, saldo anterior e final, valores com sufixo C/D. |
| `04_caixa_de_entrada_contas_a_pagar.txt` | Cópia do gêmeo. |

O harness atinge 100% quando reproduz as mesmas 18 linhas, 17 títulos e 23 achados do gêmeo.
"""
    write(os.path.join(VARIANT, "00_LEIA-ME.md"), readme.encode("utf-8"))


def main() -> None:
    write(os.path.join(GOLDEN, "pdf", "extrato_credito_debito.pdf"), statement_credit_debit())
    write(os.path.join(GOLDEN, "pdf", "extrato_digitalizado.pdf"), scanned())
    write(os.path.join(GOLDEN, "pdf", "carta_cobranca.pdf"), letter())
    write(os.path.join(GOLDEN, "cnab240", "retorno_cobranca.ret"), cnab240_return())
    write(os.path.join(GOLDEN, "cnab240", "remessa_cobranca.rem"), cnab240_remittance())
    write(os.path.join(GOLDEN, "cnab240", "retorno_pagamentos.ret"), cnab240_payments())
    for name, data in cnab400_returns().items():
        write(os.path.join(GOLDEN, "cnab400", name), data)
    write(os.path.join(GOLDEN, "sped", "efd_icms_ipi_jul2026.txt"), efd_icms_ipi())
    write(os.path.join(GOLDEN, "sped", "efd_contribuicoes_jul2026.txt"), efd_contribuicoes())
    write(os.path.join(GOLDEN, "sped", "ecd_2025.txt"), ecd())
    twin_variant()
    with open(os.path.join(TWIN, "02_titulos_unidade_norte_omie_jul2026.csv"), "rb") as f:
        omie = f.read()
    write(
        os.path.join(GOLDEN, "zip", "pacote_jul2026.zip"),
        zipped(
            [
                ("cobranca/", b""),
                ("cobranca/retorno_cobranca.ret", cnab240_return()),
                ("extratos/extrato_credito_debito.pdf", statement_credit_debit()),
                ("titulos_unidade_norte_omie_jul2026.csv", omie),
                ("__MACOSX/._titulos_unidade_norte_omie_jul2026.csv", b"\x00\x05\x16\x07"),
                ("vazio.txt", b""),
            ]
        ),
    )
    print(f"fixtures gerados em {GOLDEN} (pdf, cnab240, cnab400, sped, zip) e {VARIANT}")


if __name__ == "__main__":
    main()
