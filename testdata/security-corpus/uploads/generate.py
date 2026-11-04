#!/usr/bin/env python3
"""Gera o corpus de ataque para o scanner de uploads. Nada daqui é commitado: os arquivos vão
para a pasta `generated/` (ignorada). Cada caso tem um nome, o arquivo e o veredito esperado.

Uso: python3 -I testdata/security-corpus/uploads/generate.py [pasta-de-saída]
Saída: <pasta>/<caso>.<ext> + <pasta>/manifest.json
"""
from __future__ import annotations

import io
import json
import os
import struct
import sys
import zipfile
import zlib

OUT = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "generated")

# String de teste EICAR (padrão da indústria para testar antivírus; não é malware).
EICAR = (
    "X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*"
)

cases: list[dict] = []


def put(name: str, data: bytes, expect: str, reason: str, mime_claimed: str, note: str = "") -> None:
    path = os.path.join(OUT, name)
    with open(path, "wb") as f:
        f.write(data)
    cases.append({"file": name, "expect": expect, "reason": reason, "mime_claimed": mime_claimed, "note": note})


def png_bomb(width: int = 60000, height: int = 60000) -> bytes:
    """PNG pequeno em bytes com dimensões enormes (bomba de descompressão)."""
    def chunk(tag: bytes, body: bytes) -> bytes:
        return struct.pack(">I", len(body)) + tag + body + struct.pack(">I", zlib.crc32(tag + body) & 0xFFFFFFFF)

    ihdr = struct.pack(">IIBBBBB", width, height, 8, 0, 0, 0, 0)  # grayscale 8 bits
    # Uma linha = 1 byte de filtro + width bytes; comprimimos muitos zeros.
    raw = io.BytesIO()
    comp = zlib.compressobj(9)
    row = b"\x00" + b"\x00" * width
    data = b""
    for _ in range(min(height, 2000)):  # o suficiente para o decodificador inocente alocar w*h
        data += comp.compress(row)
    data += comp.flush()
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", ihdr) + chunk(b"IDAT", data) + chunk(b"IEND", b"")


def main() -> None:
    os.makedirs(OUT, exist_ok=True)

    # 1. EICAR puro e dentro de zip, com extensão inocente.
    put("eicar.csv", EICAR.encode(), "reject", "malware", "text/csv", "ClamAV deve detectar")
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("extrato.csv", EICAR)
    put("eicar-in-zip.zip", buf.getvalue(), "reject", "malware", "application/zip")

    # 2. Bomba zip: 100 MB de zeros em poucos KB (ratio >> 100:1), e zip aninhado além da profundidade 1.
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        z.writestr("zeros.csv", b"0" * (100 * 1024 * 1024))
    put("zip-bomb-ratio.zip", buf.getvalue(), "reject", "zip_ratio", "application/zip", "ratio > 100:1")
    inner = io.BytesIO()
    with zipfile.ZipFile(inner, "w") as z:
        z.writestr("a.csv", b"a;b\n1;2\n")
    outer = io.BytesIO()
    with zipfile.ZipFile(outer, "w") as z:
        z.writestr("inner.zip", inner.getvalue())
    put("zip-nested.zip", outer.getvalue(), "reject", "zip_depth", "application/zip", "profundidade 2")
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        for i in range(1500):
            z.writestr(f"f{i}.csv", b"x")
    put("zip-too-many-entries.zip", buf.getvalue(), "reject", "zip_entries", "application/zip", "> 1000 entradas")
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("../../etc/passwd", b"root:x:0:0")
    put("zip-traversal.zip", buf.getvalue(), "reject", "path_traversal", "application/zip")
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("planilha.xlsm", b"PK\x03\x04vbaProject.bin")
    put("zip-with-macro.zip", buf.getvalue(), "reject", "file_type", "application/zip", "xlsm dentro do zip")

    # 3. XML: billion laughs e XXE.
    laughs = '<?xml version="1.0"?>\n<!DOCTYPE lolz [\n <!ENTITY lol "lol">\n'
    prev = "lol"
    for i in range(1, 10):
        laughs += f' <!ENTITY lol{i} "' + "".join(f"&{prev};" for _ in range(10)) + '">\n'
        prev = f"lol{i}"
    laughs += "]>\n<nfeProc><lolz>&lol9;</lolz></nfeProc>\n"
    put("xml-billion-laughs.xml", laughs.encode(), "reject", "xml_entities", "application/xml")
    xxe = '<?xml version="1.0"?><!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><nfeProc><x>&xxe;</x></nfeProc>'
    put("xml-xxe.xml", xxe.encode(), "reject", "xml_entities", "application/xml")

    # 4. Tipo mentiroso: executável com extensão .csv/.pdf, HTML/SVG com script, poliglota GIF+HTML.
    put("renamed-exe.csv", b"MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff" + b"\x00" * 200, "reject", "file_type", "text/csv")
    put("renamed-elf.pdf", b"\x7fELF\x02\x01\x01" + b"\x00" * 200, "reject", "file_type", "application/pdf")
    put("script.svg", b'<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>', "reject", "file_type", "image/svg+xml")
    put("page.html", b"<html><script>fetch('/x')</script></html>", "reject", "file_type", "text/html")
    put("polyglot-gif-html.gif", b"GIF89a<html><script>alert(1)</script></html>", "reject", "file_type", "image/gif", "GIF que também é HTML")
    put("macro.docm", b"PK\x03\x04" + b"word/vbaProject.bin" + b"\x00" * 64, "reject", "file_type", "application/vnd.ms-word.document.macroEnabled.12")
    put("pdf-with-js.pdf", b"%PDF-1.7\n1 0 obj << /OpenAction << /S /JavaScript /JS (app.alert(1)) >> >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF", "reject", "pdf_active_content", "application/pdf", "desarmar e reconstruir deve remover; se não houver texto, rejeitar")
    put("encrypted.zip", b"PK\x03\x04\x14\x00\x01\x00" + b"\x00" * 64, "reject", "encrypted_archive", "application/zip", "flag de criptografia ligada")

    # 5. Imagens: bomba PNG, EXIF com GPS (deve ser removido, não rejeitado).
    put("png-bomb.png", png_bomb(), "reject", "image_bomb", "image/png", "60000x60000 em poucos KB")
    put("tiny-gps.jpg", b"\xff\xd8\xff\xe1\x00\x1cExif\x00\x00MM\x00*\x00\x00\x00\x08\x00\x01\x88%\x00\x04\x00\x00\x00\x01\x00\x00\x00\x1a" + b"\xff\xd9", "accept_sanitized", "exif_stripped", "image/jpeg", "aceita após remover EXIF/GPS")

    # 6. Mídia: playlists e listas concat (SSRF/leitura local via ffmpeg) são sempre recusadas.
    put("playlist.m3u8", b"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"file:///etc/passwd\"\n#EXTINF:1,\nhttp://127.0.0.1/x.ts\n", "reject", "media_playlist", "application/vnd.apple.mpegurl")
    put("concat.txt", b"ffconcat version 1.0\nfile '/etc/passwd'\n", "reject", "media_playlist", "video/mp4", "lista concat com extensão trocada")
    put("playlist-as-mp4.mp4", b"#EXTM3U\n#EXTINF:1,\nfile:///etc/shadow\n", "reject", "media_playlist", "video/mp4")

    # 7. Nomes de arquivo hostis (o nome vai no manifesto; o conteúdo é inofensivo).
    names = ["../../../etc/passwd", "..\\..\\windows\\win.ini", "extrato.csv\x00.exe", "a" * 300 + ".csv", "CON", "extrato.csv;rm -rf /", "=cmd|' /C calc'!A0.csv", "‮exe.csv"]
    with open(os.path.join(OUT, "hostile-names.json"), "w", encoding="utf-8") as f:
        json.dump({"names": names, "expect": "sanitize_or_reject"}, f, ensure_ascii=False, indent=2)

    # 8. CSV com injeção de fórmula (aceito; a exportação deve escapar).
    put("formula-injection.csv", "cliente;valor\n=HYPERLINK(\"http://evil.example\";\"x\");1\n+cmd|' /C calc'!A0;2\n-2+3;3\n@SUM(1,1);4\n".encode(), "accept", "formula_cells_escaped_on_export", "text/csv")

    # 9. Benignos de controle (devem ser aceitos).
    put("ok.csv", "data;historico;valor\n01/07/2026;PIX RECEBIDO;1.000,00\n".encode("utf-8"), "accept", "ok", "text/csv")
    put("ok-win1252.csv", "cliente;valor\nPadaria São João;1.000,00\n".encode("cp1252"), "accept", "ok", "text/csv", "Windows-1252")
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w") as z:
        z.writestr("a.csv", b"a;b\n1;2\n")
        z.writestr("b.csv", b"a;b\n3;4\n")
    put("ok.zip", buf.getvalue(), "accept", "ok", "application/zip")

    with open(os.path.join(OUT, "manifest.json"), "w", encoding="utf-8") as f:
        json.dump({"cases": cases}, f, ensure_ascii=False, indent=2)
    print(f"{len(cases)} casos gerados em {OUT}")


if __name__ == "__main__":
    main()
