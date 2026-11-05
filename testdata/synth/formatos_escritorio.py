#!/usr/bin/env python3
"""Gera os fixtures sintéticos dos formatos de escritório e de e-mail (DOCX, ODT, MSG do
Outlook, JSON, JSON Lines, XML genérico, HTML e 7z) e duas variações do gêmeo Vantara:

- vantara-twin-escritorio: carteira principal numa tabela de ODT, carteira da unidade Norte
  num JSON com chaves aninhadas, extrato numa página HTML salva do internet banking e a caixa
  de entrada colada num DOCX;
- vantara-twin-msg-7z: as duas carteiras (XML e JSON Lines) dentro de um 7z, o extrato como
  anexo CSV de um e-mail .msg do Outlook e a caixa de entrada em texto, como no gêmeo.

Tudo é inventado: empresas, CNPJs, números, valores e endereços (domínio .invalid). As variações
leem os arquivos do gêmeo público (testdata/golden/vantara-twin) e só trocam o formato; o
gabarito é o do gêmeo com os nomes de arquivo trocados.

Só biblioteca padrão: os contêineres (CFB do .msg, 7z) são escritos aqui, byte a byte, a
partir das especificações ([MS-CFB], [MS-OXMSG], 7zFormat.txt); a compressão do 7z é o LZMA2
do módulo lzma. A saída é determinística (sem datas de geração), então rodar de novo não muda
nenhum byte.

Uso: python3 -I testdata/synth/formatos_escritorio.py [raiz-do-repositório]
"""
from __future__ import annotations

import csv
import io
import json
import lzma
import os
import shutil
import struct
import sys
import zipfile
import zlib
from xml.sax.saxutils import escape

ROOT = sys.argv[1] if len(sys.argv) > 1 else "."
GOLDEN = os.path.join(ROOT, "testdata", "golden")
TWIN = os.path.join(GOLDEN, "vantara-twin")
OUT = os.path.join(GOLDEN, "escritorio")
VARIANT_OFFICE = os.path.join(GOLDEN, "vantara-twin-escritorio")
VARIANT_MSG = os.path.join(GOLDEN, "vantara-twin-msg-7z")

ZIP_DATE = (2026, 7, 31, 0, 0, 0)


def write(path: str, data: bytes) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)


def twin_text(name: str) -> str:
    with open(os.path.join(TWIN, name), encoding="utf-8") as f:
        return f.read()


def twin_rows(name: str, delimiter: str) -> list[dict[str, str]]:
    return list(csv.DictReader(io.StringIO(twin_text(name)), delimiter=delimiter))


# ----------------------------------------------------------------------------------- ZIP


def zipped(entries: list[tuple[str, bytes]], stored_first: bool = False) -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        for i, (name, data) in enumerate(entries):
            info = zipfile.ZipInfo(name, date_time=ZIP_DATE)
            stored = name.endswith("/") or (stored_first and i == 0)
            info.compress_type = zipfile.ZIP_STORED if stored else zipfile.ZIP_DEFLATED
            info.external_attr = 0o644 << 16
            z.writestr(info, data, compresslevel=9)
    return buf.getvalue()


# ----------------------------------------------------------------------------------- DOCX

W_NS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"


def w_par(text: str) -> str:
    if not text:
        return "<w:p/>"
    runs = []
    for i, part in enumerate(text.split("\t")):
        if i:
            runs.append("<w:r><w:tab/></w:r>")
        if part:
            runs.append(f'<w:r><w:t xml:space="preserve">{escape(part)}</w:t></w:r>')
    return "<w:p>" + "".join(runs) + "</w:p>"


def w_table(rows: list[list[str]], spans: dict[tuple[int, int], int] | None = None) -> str:
    spans = spans or {}
    out = ['<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="0" w:type="auto"/></w:tblPr>']
    for r, row in enumerate(rows):
        out.append("<w:tr>")
        for c, cell in enumerate(row):
            span = spans.get((r, c))
            pr = f'<w:tcPr><w:gridSpan w:val="{span}"/></w:tcPr>' if span else '<w:tcPr><w:tcW w:w="0" w:type="auto"/></w:tcPr>'
            paras = "".join(w_par(line) for line in cell.split("\n"))
            out.append(f"<w:tc>{pr}{paras}</w:tc>")
        out.append("</w:tr>")
    out.append("</w:tbl>")
    return "".join(out)


def docx(body: str) -> bytes:
    document = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        f'<w:document xmlns:w="{W_NS}" '
        'xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006">'
        f'<w:body>{body}<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:body></w:document>'
    )
    content_types = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
        '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
        '<Default Extension="xml" ContentType="application/xml"/>'
        '<Override PartName="/word/document.xml" '
        'ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>'
        "</Types>"
    )
    rels = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" '
        'Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" '
        'Target="word/document.xml"/></Relationships>'
    )
    return zipped(
        [
            ("[Content_Types].xml", content_types.encode()),
            ("_rels/.rels", rels.encode()),
            ("word/document.xml", document.encode("utf-8")),
        ]
    )


def payment_instruction_docx() -> bytes:
    """An e-mail pasted into Word: headers, body, an installments table, a signature grid."""
    lines = [
        "De: financeiro@caixasboavista.invalid",
        "Para: pagar@exemplo-sintetico.invalid",
        "Data: 18/07/2026 16:52",
        "Assunto: Instrução de pagamento - NF 55120",
        "",
        "Bom dia,",
        "",
        "Conforme combinado, segue a instrução de pagamento da NF 55120 (pedido PC 7310).",
        "Valor total R$ 14.994,00, em duas parcelas:",
    ]
    body = "".join(w_par(x) for x in lines)
    body += w_table(
        [
            ["Parcela", "Vencimento", "Documento", "Valor"],
            ["1/2", "25/07/2026", "NF 55120-1", "7.497,00"],
            ["2/2", "25/08/2026", "NF 55120-2", "7.497,00"],
        ]
    )
    body += w_par("")
    body += w_par("Dados bancários: Banco 341 / ag 0987 / cc 12345-6")
    # A tracked deletion is not text of the document.
    body += (
        '<w:p><w:del w:id="1" w:author="Ana"><w:r><w:delText>Valor antigo R$ 14.700,00</w:delText></w:r></w:del>'
        '<w:r><w:t xml:space="preserve">Att,</w:t></w:r></w:p>'
    )
    body += w_table([["Ana Lima\nFinanceiro - Caixas Boa Vista\t(11) 0000-0000"]])
    return docx(body)


def inbox_docx(text: str) -> bytes:
    """Every line of a text dump as a paragraph, as Word keeps a pasted e-mail."""
    return docx("".join(w_par(line) for line in text.rstrip("\n").split("\n")))


# ----------------------------------------------------------------------------------- ODT

ODT_MIME = b"application/vnd.oasis.opendocument.text"


def o_par(text: str, tag: str = "text:p") -> str:
    if not text:
        return f"<{tag}/>"
    # Runs of spaces are <text:s/> in OpenDocument.
    out = []
    i = 0
    while i < len(text):
        if text[i] == " " and i + 1 < len(text) and text[i + 1] == " ":
            j = i
            while j < len(text) and text[j] == " ":
                j += 1
            out.append(f'<text:s text:c="{j - i}"/>')
            i = j
        elif text[i] == "\t":
            out.append("<text:tab/>")
            i += 1
        else:
            out.append(escape(text[i]))
            i += 1
    return f"<{tag}>" + "".join(out) + f"</{tag}>"


def o_table(name: str, rows: list[list[str]], pad: int = 0) -> str:
    out = [f'<table:table table:name="{escape(name)}">']
    width = max(len(r) for r in rows)
    out.append(f'<table:table-column table:number-columns-repeated="{width + pad}"/>')
    for r, row in enumerate(rows):
        if r == 0:
            out.append("<table:table-header-rows>")
        out.append("<table:table-row>")
        for cell in row:
            out.append('<table:table-cell office:value-type="string">' + o_par(cell) + "</table:table-cell>")
        if pad:
            # Spreadsheet-like exports pad the rows with empty repeated cells.
            out.append(f'<table:table-cell table:number-columns-repeated="{pad}"/>')
        out.append("</table:table-row>")
        if r == 0:
            out.append("</table:table-header-rows>")
    out.append("</table:table>")
    return "".join(out)


def odt(body: str) -> bytes:
    content = (
        '<?xml version="1.0" encoding="UTF-8"?>'
        '<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" '
        'xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" '
        'xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" '
        'xmlns:dc="http://purl.org/dc/elements/1.1/" office:version="1.3">'
        "<office:automatic-styles/>"
        f"<office:body><office:text>{body}</office:text></office:body></office:document-content>"
    )
    manifest = (
        '<?xml version="1.0" encoding="UTF-8"?>'
        '<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3">'
        '<manifest:file-entry manifest:full-path="/" manifest:media-type="application/vnd.oasis.opendocument.text"/>'
        '<manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>'
        "</manifest:manifest>"
    )
    return zipped(
        [
            ("mimetype", ODT_MIME),
            ("META-INF/manifest.xml", manifest.encode()),
            ("content.xml", content.encode("utf-8")),
        ],
        stored_first=True,
    )


def titles_odt() -> bytes:
    rows = [["Cliente", "CNPJ", "Documento", "Vencimento", "Valor", "Situação"]]
    rows += [
        ["Padaria Central Ltda", "11.222.333/0001-81", "NF 088231", "10/07/2026", "17.380,00", "Aberto"],
        ["Empório Bela Vista", "34.567.890/0001-45", "NF 088240", "15/07/2026", "2.450,90", "Aberto"],
        ["Mercearia Sol Nascente", "56.789.012/0001-67", "NF 088251", "20/07/2026", "980,00", "Pago"],
    ]
    body = o_par("Títulos em aberto - julho/2026", "text:h")
    body += o_par("Relatório  gerado\tpelo financeiro.")[: -len("</text:p>")]
    body += "<office:annotation><text:p>comentário do revisor</text:p></office:annotation></text:p>"
    body += o_table("Titulos", rows, pad=3)
    body += o_par("")
    body += o_par("Total em aberto: R$ 19.830,90")
    return odt(body)


def ledger_odt(ledger_csv: str) -> bytes:
    rows = [r.split(";") for r in ledger_csv.strip("\n").split("\n")]
    body = o_par("Contas a receber em aberto - julho/2026 (TOTVS Protheus)", "text:h")
    body += o_par("Posição em 31/07/2026, operação principal.")
    body += o_table("Titulos", rows)
    return odt(body)


# ----------------------------------------------------------------------------------- CFB / MSG

SECTOR = 512
MINI = 64
CUTOFF = 4096
FREESECT, ENDOFCHAIN, FATSECT = 0xFFFFFFFF, 0xFFFFFFFE, 0xFFFFFFFD
NOSTREAM = 0xFFFFFFFF


class Node:
    def __init__(self, name: str, data: bytes | None = None) -> None:
        self.name = name
        self.data = data  # None for a storage
        self.children: list[Node] = []

    def storage(self, name: str) -> "Node":
        n = Node(name)
        self.children.append(n)
        return n

    def stream(self, name: str, data: bytes) -> None:
        self.children.append(Node(name, data))


def cfb_key(name: str) -> tuple[int, str]:
    return (len(name.encode("utf-16-le")), name.upper())


def cfb(root: Node) -> bytes:
    """A version 3 Compound File: 512-byte sectors, small streams in the mini stream."""
    entries: list[Node] = []

    def collect(n: Node) -> None:
        entries.append(n)
        for c in n.children:
            collect(c)

    collect(root)
    ids = {id(n): i for i, n in enumerate(entries)}
    left = [NOSTREAM] * len(entries)
    right = [NOSTREAM] * len(entries)
    child = [NOSTREAM] * len(entries)

    def tree(nodes: list[Node]) -> int:
        """Siblings as a balanced binary search tree (all nodes black); returns the root id."""
        if not nodes:
            return NOSTREAM
        mid = len(nodes) // 2
        me = ids[id(nodes[mid])]
        left[me] = tree(nodes[:mid])
        right[me] = tree(nodes[mid + 1:])
        return me

    for n in entries:
        if n.data is None:
            child[ids[id(n)]] = tree(sorted(n.children, key=lambda c: cfb_key(c.name)))

    # Mini stream: every small stream, 64-byte aligned.
    mini = bytearray()
    minifat: list[int] = []
    start = [ENDOFCHAIN] * len(entries)
    big: list[tuple[int, bytes]] = []
    for i, n in enumerate(entries):
        if n.data is None or len(n.data) == 0:
            continue
        if len(n.data) < CUTOFF:
            first = len(mini) // MINI
            count = (len(n.data) + MINI - 1) // MINI
            start[i] = first
            minifat.extend(range(first + 1, first + count))
            minifat.append(ENDOFCHAIN)
            mini += n.data + b"\0" * (count * MINI - len(n.data))
        else:
            big.append((i, n.data))

    sectors: list[bytes] = []
    fat: list[int] = []

    def place(data: bytes) -> int:
        count = max(1, (len(data) + SECTOR - 1) // SECTOR)
        first = len(sectors)
        for k in range(count):
            sectors.append(data[k * SECTOR:(k + 1) * SECTOR].ljust(SECTOR, b"\0"))
            fat.append(first + k + 1 if k + 1 < count else ENDOFCHAIN)
        return first

    for i, data in big:
        start[i] = place(data)
    root_start = place(bytes(mini)) if mini else ENDOFCHAIN
    minifat_bytes = b"".join(struct.pack("<I", x) for x in minifat)
    minifat_sectors = (len(minifat_bytes) + SECTOR - 1) // SECTOR
    # Unused mini FAT entries are free sectors.
    minifat_bytes = minifat_bytes.ljust(minifat_sectors * SECTOR, b"\xff")
    minifat_start = place(minifat_bytes) if minifat else ENDOFCHAIN

    def entry(i: int, n: Node) -> bytes:
        name = n.name.encode("utf-16-le") + b"\0\0"
        kind = 5 if i == 0 else (1 if n.data is None else 2)
        if i == 0:
            first, size = root_start, len(mini)
        elif n.data is None:
            first, size = 0, 0
        else:
            first, size = (start[i] if n.data else ENDOFCHAIN), len(n.data)
        return (
            name.ljust(64, b"\0")
            + struct.pack("<HBB", len(name), kind, 1)
            + struct.pack("<III", left[i], right[i], child[i])
            + b"\0" * 16  # CLSID
            + struct.pack("<I", 0)  # state bits
            + b"\0" * 16  # creation and modification times
            + struct.pack("<IQ", first, size)
        )

    directory = b"".join(entry(i, n) for i, n in enumerate(entries))
    directory += b"".join(
        (b"\0" * 64 + struct.pack("<HBB", 0, 0, 0) + struct.pack("<III", NOSTREAM, NOSTREAM, NOSTREAM) + b"\0" * 36 + struct.pack("<IQ", 0, 0))
        for _ in range((-len(entries)) % 4)
    )
    dir_start = place(directory)

    # FAT sectors describe themselves too: grow until they fit.
    n_fat = 1
    while (len(fat) + n_fat) > n_fat * (SECTOR // 4):
        n_fat += 1
    fat_ids = list(range(len(sectors), len(sectors) + n_fat))
    fat.extend([FATSECT] * n_fat)
    fat_bytes = b"".join(struct.pack("<I", x) for x in fat).ljust(n_fat * SECTOR, b"\xff")
    for k in range(n_fat):
        sectors.append(fat_bytes[k * SECTOR:(k + 1) * SECTOR])
    if n_fat > 109:
        raise SystemExit("arquivo grande demais para este gerador (sem DIFAT)")
    difat = fat_ids + [FREESECT] * (109 - n_fat)
    header = (
        b"\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1"
        + b"\0" * 16
        + struct.pack("<HHHHH", 0x003E, 0x0003, 0xFFFE, 9, 6)
        + b"\0" * 6
        + struct.pack("<I", 0)  # directory sectors (0 for version 3)
        + struct.pack("<I", n_fat)
        + struct.pack("<I", dir_start)
        + struct.pack("<I", 0)  # transaction signature
        + struct.pack("<I", CUTOFF)
        + struct.pack("<I", minifat_start)
        + struct.pack("<I", minifat_sectors)
        + struct.pack("<I", ENDOFCHAIN)  # first DIFAT sector
        + struct.pack("<I", 0)
        + b"".join(struct.pack("<I", x) for x in difat)
    )
    assert len(header) == SECTOR
    return header + b"".join(sectors)


PT_LONG, PT_BOOLEAN, PT_SYSTIME = 0x0003, 0x000B, 0x0040


def unicode(text: str) -> bytes:
    return text.encode("utf-16-le")


def props_stream(head: bytes, fixed: list[tuple[int, int, bytes]], strings: list[tuple[int, int]]) -> bytes:
    """[MS-OXMSG] 2.4: a header, then 16 bytes per property (type, id, flags, value; for a
    variable-length property the value is its size, the data lives in its own stream)."""
    out = bytearray(head)
    for kind, pid, value in fixed:
        out += struct.pack("<HHI", kind, pid, 6) + value.ljust(8, b"\0")
    for pid, size in strings:
        out += struct.pack("<HHIII", 0x001F, pid, 6, size, 0)
    return bytes(out)


ATTACHMENT_HEAD = b"\0" * 8


def filetime(y: int, mo: int, d: int, h: int, mi: int) -> bytes:
    import datetime

    t = datetime.datetime(y, mo, d, h, mi, tzinfo=datetime.timezone.utc)
    ticks = (int(t.timestamp()) + 11_644_473_600) * 10_000_000
    return struct.pack("<Q", ticks)


def put_strings(node: Node, values: dict[int, str]) -> list[tuple[int, int]]:
    out = []
    for pid, text in values.items():
        data = unicode(text)
        node.stream(f"__substg1.0_{pid:04X}001F", data)
        out.append((pid, len(data) + 2))
    return out


def message(
    node: Node,
    header: int,
    subject: str,
    sender: tuple[str, str],
    recipients: list[tuple[str, str, int]],
    body: str,
    headers: str | None,
    submit: bytes,
    attachments: list[dict],
) -> None:
    strings = put_strings(
        node,
        {
            0x001A: "IPM.Note",
            0x0037: subject,
            0x0E1D: subject,
            0x0C1A: sender[0],
            0x0C1E: "SMTP",
            0x0C1F: sender[1],
            0x5D01: sender[1],
            0x0042: sender[0],
            0x0065: sender[1],
            0x0E04: "; ".join(n for n, _, t in recipients if t == 1),
            0x0E03: "; ".join(n for n, _, t in recipients if t == 2),
            0x1000: body,
            **({0x007D: headers} if headers else {}),
        },
    )
    fixed = [
        (PT_SYSTIME, 0x0039, submit),
        (PT_SYSTIME, 0x0E06, submit),
        (PT_LONG, 0x3FFD, struct.pack("<I", 65001)),
    ]
    for i, (name, address, kind) in enumerate(recipients):
        r = node.storage(f"__recip_version1.0_#{i:08X}")
        s = put_strings(r, {0x3001: name, 0x3003: address, 0x39FE: address, 0x3002: "SMTP"})
        r.stream("__properties_version1.0", props_stream(ATTACHMENT_HEAD, [(PT_LONG, 0x0C15, struct.pack("<I", kind))], s))
    for i, a in enumerate(attachments):
        st = node.storage(f"__attach_version1.0_#{i:08X}")
        names = {0x3707: a["name"], 0x3704: a["name"][:12], 0x3001: a["name"]}
        if a.get("mime"):
            names[0x370E] = a["mime"]
        if a.get("content_id"):
            names[0x3712] = a["content_id"]
        s = put_strings(st, names)
        fixed_a = [(PT_LONG, 0x3705, struct.pack("<I", 5 if "message" in a else 1))]
        if a.get("hidden"):
            fixed_a.append((PT_BOOLEAN, 0x7FFE, struct.pack("<I", 1)))
        if "message" in a:
            inner = st.storage("__substg1.0_3701000D")
            message(inner, 24, **a["message"])
        else:
            st.stream("__substg1.0_37010102", a["data"])
        st.stream("__properties_version1.0", props_stream(ATTACHMENT_HEAD, fixed_a, s))
    # Top-level message: 32-byte header; an attached message: 24 bytes ([MS-OXMSG] 2.4.1).
    counts = (len(recipients), len(attachments), len(recipients), len(attachments))
    head = struct.pack("<8xIIII8x", *counts) if header == 32 else struct.pack("<8xIIII", *counts)
    node.stream("__properties_version1.0", props_stream(head, fixed, strings))


def msg(**kwargs) -> bytes:
    root = Node("Root Entry")
    message(root, 32, **kwargs)
    # Named properties map (empty): Outlook always writes it.
    nameid = root.storage("__nameid_version1.0")
    for pid in ("00020102", "00030102", "00040102"):
        nameid.stream(f"__substg1.0_{pid}", b"")
    return cfb(root)


PNG = bytes.fromhex("89504e470d0a1a0a0000000d4948445200000001000000010806000000")


def boleto_msg(statement_csv: bytes) -> bytes:
    """A supplier's reply with the updated boleto data, a CSV statement, a signature image
    (hidden, inline) and the message it answers (attached, with an attachment of its own)."""
    return msg(
        subject="RE: cobrança em aberto - Caixas Boa Vista",
        sender=("Ana Lima", "ana.lima@caixasboavista.invalid"),
        recipients=[
            ("Contas a Pagar", "pagar@exemplo-sintetico.invalid", 1),
            ("Compras", "compras@exemplo-sintetico.invalid", 2),
        ],
        body=(
            "Pessoal, não identificamos o pagamento da nota abaixo.\r\n\r\n"
            "Nota fiscal 055120 - valor atualizado R$ 14.994,00 (venc. 25/07/2026).\r\n"
            "Linha digitável: 03399.11223 34400.556677 88990.011223 8 91230000014994\r\n\r\n"
            "Att,\r\nAna"
        ),
        headers=(
            "Received: from mx.example.com\r\n"
            "From: Ana Lima <ana.lima@caixasboavista.invalid>\r\n"
            "Date: Sat, 18 Jul 2026 16:52:00 -0300\r\n"
            "Message-ID: <boleto-55120@caixasboavista.invalid>\r\n"
            "Subject: RE: cobranca em aberto\r\n"
        ),
        submit=filetime(2026, 7, 18, 19, 52),
        attachments=[
            {"name": "extrato_jul2026.csv", "data": statement_csv, "mime": "text/csv"},
            {"name": "image001.png", "data": PNG, "mime": "image/png", "content_id": "image001.png@01DC", "hidden": True},
            {
                "name": "cobranca em aberto",
                "message": dict(
                    subject="cobrança em aberto - Caixas Boa Vista",
                    sender=("Contas a Pagar", "pagar@exemplo-sintetico.invalid"),
                    recipients=[("Ana Lima", "ana.lima@caixasboavista.invalid", 1)],
                    body="Ana, a NF 55120 consta como paga em 20/07/2026. Pode conferir?",
                    headers=None,
                    submit=filetime(2026, 7, 18, 14, 10),
                    attachments=[{"name": "comprovante.pdf", "data": b"%PDF-1.4\n%sintetico\n"}],
                ),
            },
        ],
    )


def statement_msg(statement_csv: bytes) -> bytes:
    """The bank statement sent by the account manager as an attachment."""
    return msg(
        subject="Extrato da conta de cobrança - julho/2026",
        sender=("Gerente PJ", "gerente.pj@banco-sintetico.invalid"),
        recipients=[("Financeiro", "financeiro@exemplo-sintetico.invalid", 1)],
        body="Bom dia,\r\n\r\nsegue o extrato de julho da conta de cobrança, em CSV.\r\n\r\nAtenciosamente,\r\nGerente PJ",
        headers="Date: Mon, 3 Aug 2026 09:05:00 -0300\r\n",
        submit=filetime(2026, 8, 3, 12, 5),
        attachments=[
            {"name": "extrato_conta_jul2026.csv", "data": statement_csv, "mime": "text/csv"},
            {"name": "image001.png", "data": PNG, "mime": "image/png", "content_id": "logo@01DC", "hidden": True},
        ],
    )


# ----------------------------------------------------------------------------------- 7z


def number(v: int) -> bytes:
    """7z variable-length number (7zFormat.txt, REAL_UINT64)."""
    for n in range(9):
        if n == 8 or v < (1 << (7 * (n + 1))):
            break
    if n == 8:
        return b"\xFF" + struct.pack("<Q", v)
    high = v >> (8 * n)
    mask = (0xFF << (8 - n)) & 0xFF
    return bytes([mask | high]) + v.to_bytes(8, "little")[:n]


def bits(flags: list[bool]) -> bytes:
    out = bytearray((len(flags) + 7) // 8)
    for i, f in enumerate(flags):
        if f:
            out[i // 8] |= 0x80 >> (i % 8)
    return bytes(out)


def lzma2(data: bytes) -> bytes:
    return lzma.compress(data, format=lzma.FORMAT_RAW, filters=[{"id": lzma.FILTER_LZMA2, "preset": 9, "dict_size": 1 << 20}])


def sevenz(entries: list[tuple[str, bytes | None]], encoded_header: bool = False) -> bytes:
    """A solid 7z archive, one LZMA2 folder. `None` data is a directory. With
    `encoded_header`, the header itself is compressed, as 7-Zip writes it by default."""
    with_data = [(n, d) for n, d in entries if d]
    stream = b"".join(d for _, d in with_data)
    packed = lzma2(stream)
    prop = 16  # dictionary of 2^20 bytes: (2 | (p & 1)) << (p // 2 + 11)
    h = bytearray(b"\x01")  # Header
    h += b"\x04"  # MainStreamsInfo
    h += b"\x06" + number(0) + number(1) + b"\x09" + number(len(packed)) + b"\x00"  # PackInfo
    h += b"\x07\x0B" + number(1) + b"\x00"  # UnpackInfo, one folder, not external
    h += number(1) + bytes([0x21, 0x21]) + number(1) + bytes([prop])  # one coder: LZMA2
    h += b"\x0C" + number(len(stream)) + b"\x00"
    h += b"\x08\x0D" + number(len(with_data))  # SubStreamsInfo
    if len(with_data) > 1:
        h += b"\x09" + b"".join(number(len(d)) for _, d in with_data[:-1])
    h += b"\x0A\x01" + b"".join(struct.pack("<I", zlib.crc32(d)) for _, d in with_data)
    h += b"\x00\x00"
    h += b"\x05" + number(len(entries))  # FilesInfo
    empty = [not d for _, d in entries]
    if any(empty):
        v = bits(empty)
        h += b"\x0E" + number(len(v)) + v
        files = bits([d is not None for _, d in entries if not d])
        h += b"\x0F" + number(len(files)) + files
    names = b"\x00" + b"".join(n.encode("utf-16-le") + b"\0\0" for n, _ in entries)
    h += b"\x11" + number(len(names)) + names
    h += b"\x00\x00"
    header = bytes(h)
    if encoded_header:
        packed_header = lzma2(header)
        e = bytearray(b"\x17")  # EncodedHeader: a StreamsInfo for the compressed header
        e += b"\x06" + number(len(packed)) + number(1) + b"\x09" + number(len(packed_header)) + b"\x00"
        e += b"\x07\x0B" + number(1) + b"\x00"
        e += number(1) + bytes([0x21, 0x21]) + number(1) + bytes([prop])
        e += b"\x0C" + number(len(header))
        e += b"\x0A\x01" + struct.pack("<I", zlib.crc32(header)) + b"\x00"
        e += b"\x00"
        packed += packed_header
        header = bytes(e)
    start = struct.pack("<QQI", len(packed), len(header), zlib.crc32(header))
    return b"7z\xBC\xAF\x27\x1C\x00\x04" + struct.pack("<I", zlib.crc32(start)) + start + packed + header


# ----------------------------------------------------------------------------------- JSON / XML / HTML


def titles_json() -> bytes:
    doc = {
        "empresa": {"nome": "Exemplo Sintético Ltda", "cnpj": "11.222.333/0001-81"},
        "pagina": 1,
        "titulos": [
            {
                "cliente": {"nome": "MOINHO AZUL COM LTDA", "cnpj": "90123456000134"},
                "documento": "FAT-2026-0411",
                "vencimento": "2026-07-20",
                "valor": 27850.00,
                "pago": False,
                "tags": ["royalty", "sul"],
                "parcelas": [{"numero": 1, "valor": 27850.00}],
            },
            {
                "cliente": {"nome": "NORTE PAES EIRELI", "cnpj": "35791357000190"},
                "documento": "FAT-2026-0412",
                "vencimento": "2026-07-14",
                "valor": 10450,
                "pago": True,
                "observacao": None,
            },
        ],
    }
    return (json.dumps(doc, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


def bank_jsonl(rows: list[dict[str, str]]) -> bytes:
    lines = [json.dumps({"data": r["data"], "historico": r["historico"], "documento": r["documento"], "valor": r["valor"], "tipo": r["tipo"]}, ensure_ascii=False) for r in rows[:4]]
    lines.insert(2, "linha corrompida pela exportação")
    return ("\n".join(lines) + "\n").encode("utf-8")


def ledger_jsonl(rows: list[dict[str, str]]) -> bytes:
    return "".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows).encode("utf-8")


def bank_xml(rows: list[dict[str, str]]) -> bytes:
    out = ['<?xml version="1.0" encoding="ISO-8859-1"?>', '<extrato xmlns="urn:exemplo:extrato:1">']
    out.append('  <conta banco="999" agencia="0001" numero="12345-6" titular="Exemplo Sintético Ltda"/>')
    out.append("  <lancamentos>")
    for i, r in enumerate(rows[:6], start=1):
        doc = f"<documento>{r['documento']}</documento>" if r["documento"] else ""
        out.append(
            f'    <lancamento seq="{i}"><data>{r["data"]}</data><historico>{escape(r["historico"])}</historico>'
            f'{doc}<valor moeda="BRL">{r["valor"]}</valor><tipo>{r["tipo"]}</tipo></lancamento>'
        )
    out.append("  </lancamentos>")
    out.append("  <!-- saldo calculado pelo banco -->")
    out.append("  <saldo><![CDATA[Saldo & conferência: 150.000,00]]></saldo>")
    out.append("</extrato>")
    return ("\n".join(out) + "\n").encode("iso-8859-1")


def ledger_xml(rows: list[dict[str, str]]) -> bytes:
    out = ['<?xml version="1.0" encoding="UTF-8"?>', "<titulos_a_receber>"]
    for r in rows:
        fields = "".join(f"<{k}>{escape(v)}</{k}>" for k, v in r.items())
        out.append(f"  <titulo>{fields}</titulo>")
    out.append("</titulos_a_receber>")
    return ("\n".join(out) + "\n").encode("utf-8")


def statement_html(rows: list[dict[str, str]], charset: str, title: str) -> bytes:
    body_rows = "\n".join(
        f"<tr><td>{r['data']}</td><td>{escape(r['historico'])}</td><td>{r['documento']}</td>"
        f"<td align=right>{r['valor']}</td><td>{r['tipo']}</td></tr>"
        for r in rows
    )
    page = f"""<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta http-equiv="Content-Type" content="text/html; charset={charset}">
<title>{escape(title)}</title>
<style>table.extrato td {{ font-family: Arial; }}</style>
<script>var linhas = "<table><tr><td>Data</td><td>Valor</td></tr></table>";</script>
</head>
<body>
<div class="cabecalho"><p>Banco Sintético S.A. &middot; Internet Banking Empresas<br>
Agência 0001 &middot; Conta 12345-6 &middot; Exemplo Sintético Ltda</p></div>
<p style="display:none">sessão 8f2a</p>
<table width="100%" border="0"><tr><td>
<table class="extrato" border="1">
<thead><tr><th>Data</th><th>Histórico</th><th>Documento</th><th>Valor (R$)</th><th>Tipo</th></tr></thead>
<tbody>
{body_rows}
</tbody>
</table>
</td></tr></table>
<p>Extrato emitido em 31/07/2026 às 23:59. Valores sujeitos a alteração.</p>
</body>
</html>
"""
    return page.encode(charset)


# ----------------------------------------------------------------------------------- variants


def answer_key(case: str, header: str, sources: dict[str, str]) -> bytes:
    with open(os.path.join(TWIN, "expected.yaml"), encoding="utf-8") as f:
        expected = f.read()
    expected = expected.replace(
        "# Gabarito do gêmeo sintético público do caso Vantara. Gerado por testdata/synth/vantara_twin.py.",
        header,
    )
    defaults = {
        "protheus": "01_contas_a_receber_protheus_jul2026.csv",
        "omie": "02_titulos_unidade_norte_omie_jul2026.csv",
        "bank": "03_extrato_bancario_jul2026.csv",
        "ap_inbox": "04_caixa_de_entrada_contas_a_pagar.txt",
    }
    for key, file in sources.items():
        old = f"{key}: {defaults[key]}"
        assert old in expected, old
        expected = expected.replace(old, f"{key}: {file}")
    expected = expected.replace("case: vantara-twin-jul-2026", f"case: {case}")
    return expected.encode("utf-8")


def reset(path: str) -> None:
    if os.path.isdir(path):
        shutil.rmtree(path)
    os.makedirs(path)


def office_variant(protheus_csv: str, omie: list[dict[str, str]], bank: list[dict[str, str]], inbox: str) -> None:
    reset(VARIANT_OFFICE)
    files = {
        "protheus": "01_contas_a_receber_protheus_jul2026.odt",
        "omie": "02_titulos_unidade_norte_omie_jul2026.json",
        "bank": "03_extrato_bancario_jul2026.html",
        "ap_inbox": "04_caixa_de_entrada_contas_a_pagar.docx",
    }
    write(os.path.join(VARIANT_OFFICE, files["protheus"]), ledger_odt(protheus_csv))
    doc = {
        "pagina": 1,
        "total_de_paginas": 1,
        "registros": len(omie),
        "titulos": [
            {
                "customer": {"name": r["customer_name"], "tax_id": r["tax_id"]},
                "invoice_ref": r["invoice_ref"],
                "due_date": r["due_date"],
                "amount": float(r["amount"]),
                "paid_flag": r["paid_flag"],
            }
            for r in omie
        ],
    }
    write(os.path.join(VARIANT_OFFICE, files["omie"]), (json.dumps(doc, ensure_ascii=False, indent=2) + "\n").encode("utf-8"))
    write(os.path.join(VARIANT_OFFICE, files["bank"]), statement_html(bank, "windows-1252", "Extrato de conta corrente - julho/2026"))
    write(os.path.join(VARIANT_OFFICE, files["ap_inbox"]), inbox_docx(inbox))
    header = (
        "# Gabarito da variação do gêmeo em formatos de escritório (ODT, JSON, HTML e DOCX): o\n"
        "# mesmo do gêmeo (testdata/golden/vantara-twin/expected.yaml) com os nomes de arquivo\n"
        "# trocados. Gerado por testdata/synth/formatos_escritorio.py."
    )
    write(os.path.join(VARIANT_OFFICE, "expected.yaml"), answer_key("vantara-twin-escritorio-jul-2026", header, files))
    readme = f"""# Variação do gêmeo: formatos de escritório

Gerada por `testdata/synth/formatos_escritorio.py` a partir do gêmeo público
(`testdata/golden/vantara-twin`). Mesmos títulos, mesmas linhas de extrato, mesma caixa de
entrada, mesmo gabarito; só os formatos mudam:

| Arquivo | O que é |
|---|---|
| `{files["protheus"]}` | Os 12 títulos da operação principal numa tabela de documento OpenDocument, com título e parágrafo acima. |
| `{files["omie"]}` | Os 5 títulos da unidade Norte como uma API devolve: JSON com paginação e cliente aninhado (`customer.name`, `customer.tax_id`), valor numérico. |
| `{files["bank"]}` | O extrato de julho como página salva do internet banking (windows-1252): tabela de layout com a tabela do extrato dentro, script, estilo e texto escondido. |
| `{files["ap_inbox"]}` | A caixa de entrada do gêmeo colada num documento Word, uma linha por parágrafo. |

O harness atinge 100% quando reproduz as mesmas 18 linhas, 17 títulos e 23 achados do gêmeo.
"""
    write(os.path.join(VARIANT_OFFICE, "00_LEIA-ME.md"), readme.encode("utf-8"))


def msg_variant(protheus: list[dict[str, str]], omie: list[dict[str, str]], bank_csv: bytes) -> None:
    reset(VARIANT_MSG)
    archive = "01_carteira_jul2026.7z"
    xml_name = "01_contas_a_receber_protheus_jul2026.xml"
    jsonl_name = "02_titulos_unidade_norte_omie_jul2026.jsonl"
    statement = "03_extrato_bancario_jul2026.msg"
    attached = "extrato_conta_jul2026.csv"
    write(
        os.path.join(VARIANT_MSG, archive),
        sevenz(
            [
                ("carteiras", None),
                (f"carteiras/{xml_name}", ledger_xml(protheus)),
                (f"carteiras/{jsonl_name}", ledger_jsonl(omie)),
            ],
            encoded_header=True,
        ),
    )
    write(os.path.join(VARIANT_MSG, statement), statement_msg(bank_csv))
    shutil.copyfile(os.path.join(TWIN, "04_caixa_de_entrada_contas_a_pagar.txt"), os.path.join(VARIANT_MSG, "04_caixa_de_entrada_contas_a_pagar.txt"))
    files = {
        "protheus": f"{archive}/carteiras/{xml_name}",
        "omie": f"{archive}/carteiras/{jsonl_name}",
        "bank": f"{statement}/{attached}",
    }
    header = (
        "# Gabarito da variação do gêmeo com as carteiras (XML e JSON Lines) num 7z e o extrato\n"
        "# como anexo de um .msg do Outlook: o mesmo do gêmeo (testdata/golden/vantara-twin/\n"
        "# expected.yaml) com os nomes de arquivo trocados. Gerado por\n"
        "# testdata/synth/formatos_escritorio.py."
    )
    write(os.path.join(VARIANT_MSG, "expected.yaml"), answer_key("vantara-twin-msg-7z-jul-2026", header, files))
    readme = f"""# Variação do gêmeo: carteiras num 7z, extrato num e-mail do Outlook

Gerada por `testdata/synth/formatos_escritorio.py` a partir do gêmeo público
(`testdata/golden/vantara-twin`). Mesmo gabarito; só os formatos mudam:

| Arquivo | O que é |
|---|---|
| `{archive}` | Arquivo 7z (LZMA2, sólido, cabeçalho compactado como o 7-Zip grava) com a pasta `carteiras/`: os 12 títulos da operação principal em XML (`<titulo>` repetido) e os 5 da unidade Norte em JSON Lines. |
| `{statement}` | E-mail do gerente do banco (Outlook, `.msg`) com o extrato de julho anexado em CSV (`{attached}`, cópia do gêmeo) e o logotipo da assinatura como imagem embutida. |
| `04_caixa_de_entrada_contas_a_pagar.txt` | Cópia do gêmeo. |

O e-mail do gerente também é uma caixa de entrada (uma mensagem); a do arquivo 04 tem mais
mensagens e é a que entra, com um aviso para o `.msg`. O harness atinge 100% quando reproduz as
mesmas 18 linhas, 17 títulos e 23 achados do gêmeo.
"""
    write(os.path.join(VARIANT_MSG, "00_LEIA-ME.md"), readme.encode("utf-8"))


def main() -> None:
    protheus_csv = twin_text("01_contas_a_receber_protheus_jul2026.csv")
    protheus = twin_rows("01_contas_a_receber_protheus_jul2026.csv", ";")
    omie = twin_rows("02_titulos_unidade_norte_omie_jul2026.csv", ",")
    bank = twin_rows("03_extrato_bancario_jul2026.csv", ";")
    with open(os.path.join(TWIN, "03_extrato_bancario_jul2026.csv"), "rb") as f:
        bank_csv = f.read()
    inbox = twin_text("04_caixa_de_entrada_contas_a_pagar.txt")

    reset(OUT)
    write(os.path.join(OUT, "instrucao_pagamento.docx"), payment_instruction_docx())
    write(os.path.join(OUT, "titulos_em_aberto.odt"), titles_odt())
    write(os.path.join(OUT, "boleto_atualizado.msg"), boleto_msg(bank_csv))
    write(os.path.join(OUT, "titulos_api.json"), titles_json())
    write(os.path.join(OUT, "lancamentos_jul2026.jsonl"), bank_jsonl(bank))
    write(os.path.join(OUT, "extrato_jul2026.xml"), bank_xml(bank))
    write(os.path.join(OUT, "extrato_internet_banking.html"), statement_html(bank[:5], "windows-1252", "Extrato - Internet Banking"))
    write(
        os.path.join(OUT, "pacote_jul2026.7z"),
        sevenz(
            [
                ("extratos", None),
                ("extratos/extrato_jul2026.csv", bank_csv),
                ("notas/vazio.txt", b""),
                ("titulos_api.json", titles_json()),
            ]
        ),
    )
    # Entry names that must be cleaned: control characters, a name far past 200 characters
    # and two names that are the same once cleaned.
    write(
        os.path.join(OUT, "nomes_estranhos.7z"),
        sevenz(
            [
                ("extrato\x1b[31m.csv", bank_csv),
                ("extrato[31m.csv", bank_csv),
                ("pasta/" + "n" * 300 + ".csv", bank_csv),
            ]
        ),
    )
    office_variant(protheus_csv, omie, bank, inbox)
    msg_variant(protheus, omie, bank_csv)
    print(f"fixtures gerados em {OUT}, {VARIANT_OFFICE} e {VARIANT_MSG}")


if __name__ == "__main__":
    main()
