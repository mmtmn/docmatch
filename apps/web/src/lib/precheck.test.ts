import { describe, expect, it } from 'vitest';
import { t } from './i18n';
import { fileCaps } from './limits';
import {
  classify,
  extensionOf,
  looksLikeEmail,
  looksLikeOfx,
  precheckFiles,
  sniffKind,
} from './precheck';

type Bytes = Uint8Array<ArrayBuffer>;
const bytes = (...values: number[]): Bytes => Uint8Array.from(values);
const text = (value: string): Bytes => new TextEncoder().encode(value);

function file(name: string, content: Bytes | string, lastModified = 1): File {
  const data = typeof content === 'string' ? text(content) : content;
  return new File([data], name, { lastModified });
}

const PDF = text('%PDF-1.7\n');
const PNG = bytes(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2);
const ZIP = bytes(0x50, 0x4b, 0x03, 0x04, 0, 0);
const CFB = bytes(0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1);
const MP3 = text('ID3\u0003\u0000');
const EBML = bytes(0x1a, 0x45, 0xdf, 0xa3, 0, 0);
const MP4 = Uint8Array.from([0, 0, 0, 0x18, ...text('ftypisom'), 0, 0, 0, 0]);
const M4A = Uint8Array.from([0, 0, 0, 0x18, ...text('ftypM4A '), 0, 0, 0, 0]);
const HEIC = Uint8Array.from([0, 0, 0, 0x18, ...text('ftypheic'), 0, 0, 0, 0]);
const WAV = Uint8Array.from([...text('RIFF'), 0, 0, 0, 0, ...text('WAVEfmt ')]);
const WEBP = Uint8Array.from([...text('RIFF'), 0, 0, 0, 0, ...text('WEBPVP8 ')]);
const BINARY = bytes(0x00, 0x01, 0x02, 0xff, 0xfe, 0x00, 0x10);
const EML = 'From: a@b.c\r\nTo: d@e.f\r\nSubject: Oi\r\nDate: Mon\r\n\r\nCorpo';
const OFX = 'OFXHEADER:100\nDATA:OFXSGML\n<OFX><SIGNONMSGSRSV1>';
const CSV = 'data;descricao;valor\n2026-10-01;PIX;10,00\n';

describe('sniffKind', () => {
  it('recognises binary signatures', () => {
    expect(sniffKind(PDF, 'pdf')).toBe('pdf');
    expect(sniffKind(PNG, 'png')).toBe('image');
    expect(sniffKind(WEBP, 'webp')).toBe('image');
    expect(sniffKind(HEIC, 'heic')).toBe('image');
    expect(sniffKind(MP3, 'mp3')).toBe('audio');
    expect(sniffKind(WAV, 'wav')).toBe('audio');
    expect(sniffKind(M4A, 'm4a')).toBe('audio');
    expect(sniffKind(MP4, 'mp4')).toBe('video');
    expect(sniffKind(EBML, 'webm')).toBe('video');
    expect(sniffKind(EBML, 'weba')).toBe('audio');
  });

  it('uses the extension to disambiguate zip containers', () => {
    expect(sniffKind(ZIP, 'xlsx')).toBe('xlsx');
    expect(sniffKind(ZIP, 'xlsm')).toBe('xlsx');
    expect(sniffKind(ZIP, 'zip')).toBe('zip');
    expect(sniffKind(ZIP, 'docx')).toBeNull();
    expect(sniffKind(CFB, 'xls')).toBe('xlsx');
    expect(sniffKind(CFB, 'doc')).toBeNull();
  });

  it('classifies text by content first, then by extension', () => {
    expect(sniffKind(text(EML), 'eml')).toBe('eml');
    expect(sniffKind(text(OFX), 'ofx')).toBe('ofx');
    expect(sniffKind(text(CSV), 'csv')).toBe('csv');
    expect(sniffKind(text(CSV), 'txt')).toBe('txt');
    expect(sniffKind(text(CSV), null)).toBe('txt');
    expect(sniffKind(text(CSV), 'exe')).toBeNull();
    expect(sniffKind(BINARY, 'csv')).toBeNull();
  });

  it('helpers', () => {
    expect(extensionOf('extrato.OFX')).toBe('ofx');
    expect(extensionOf('semextensao')).toBeNull();
    expect(looksLikeEmail('Subject: x\nOi')).toBe(false);
    expect(looksLikeEmail(EML)).toBe(true);
    expect(looksLikeOfx('<?OFX OFXHEADER="200"?>')).toBe(true);
  });
});

describe('classify', () => {
  it('rejects mismatches between extension and content with the right reason', () => {
    expect(classify('foto.pdf', PNG)).toEqual({
      reason: 'errors.file_extension_mismatch',
      vars: { name: 'foto.pdf', ext: 'pdf' },
    });
    expect(classify('dados.csv', BINARY)).toMatchObject({
      reason: 'errors.file_extension_mismatch',
    });
    expect(classify('planilha.xlsx', text(CSV))).toMatchObject({
      reason: 'errors.file_extension_mismatch',
    });
    expect(classify('mensagem.eml', text(CSV))).toMatchObject({
      reason: 'errors.file_extension_mismatch',
    });
  });

  it('rejects unknown formats', () => {
    expect(classify('programa.exe', BINARY)).toEqual({
      reason: 'errors.file_type_unknown',
      vars: { name: 'programa.exe' },
    });
    expect(classify('doc.docx', ZIP)).toMatchObject({ reason: 'errors.file_type_unknown' });
  });

  it('accepts unambiguous content under an unknown or missing extension', () => {
    expect(classify('scan.bin', PDF)).toEqual({ kind: 'pdf' });
    expect(classify('extrato', text(OFX))).toEqual({ kind: 'ofx' });
    expect(classify('colado-1.txt', text('qualquer texto'))).toEqual({ kind: 'txt' });
  });
});

describe('precheckFiles', () => {
  it('accepts valid files with kind and family', async () => {
    const result = await precheckFiles([
      file('extrato.ofx', OFX),
      file('nota.pdf', PDF),
      file('reuniao.mp3', MP3),
    ]);
    expect(result.rejected).toEqual([]);
    expect(result.accepted.map((f) => [f.kind, f.family])).toEqual([
      ['ofx', 'document'],
      ['pdf', 'document'],
      ['audio', 'media'],
    ]);
    expect(new Set(result.accepted.map((f) => f.id)).size).toBe(3);
  });

  it('rejects empty files, long names, duplicates and unknown formats with pt-BR reasons', async () => {
    const first = await precheckFiles([file('nota.pdf', PDF)]);
    const result = await precheckFiles(
      [
        file('vazio.csv', ''),
        file(`${'a'.repeat(fileCaps.fileNameBytes)}.pdf`, PDF),
        file('nota.pdf', PDF),
        file('x.exe', BINARY),
      ],
      { existing: first.accepted },
    );
    expect(result.accepted).toEqual([]);
    expect(result.rejected.map((r) => r.reason)).toEqual([
      t('errors.file_empty', { name: 'vazio.csv' }),
      t('errors.file_name_too_long', {
        name: `${'a'.repeat(fileCaps.fileNameBytes)}.pdf`,
        max: fileCaps.fileNameBytes,
      }),
      t('errors.file_duplicate', { name: 'nota.pdf' }),
      t('errors.file_type_unknown', { name: 'x.exe' }),
    ]);
  });

  it('enforces size caps per file and per run, by family', async () => {
    const big = new Uint8Array(40);
    const result = await precheckFiles(
      [
        file('a.pdf', Uint8Array.from([...PDF, ...big])),
        file('b.pdf', Uint8Array.from([...PDF, ...new Uint8Array(10)]), 2),
        file('c.pdf', Uint8Array.from([...PDF, ...new Uint8Array(10)]), 3),
        file('d.mp3', Uint8Array.from([...MP3, ...big])),
        file('e.mp3', Uint8Array.from([...MP3, ...new Uint8Array(4)]), 2),
      ],
      {
        caps: {
          documentFileBytes: 30,
          documentRunBytes: 35,
          mediaFileBytes: 20,
          mediaRunBytes: 8,
        },
      },
    );
    expect(result.accepted.map((f) => f.name)).toEqual(['b.pdf']);
    expect(result.rejected.map((r) => r.name)).toEqual(['a.pdf', 'c.pdf', 'd.mp3', 'e.mp3']);
    expect(result.rejected[0]!.reason).toBe(
      t('errors.file_too_large', { name: 'a.pdf', max: '30 B' }),
    );
    expect(result.rejected[1]!.reason).toBe(t('errors.run_too_large', { max: '35 B' }));
    expect(result.rejected[2]!.reason).toBe(
      t('errors.file_too_large', { name: 'd.mp3', max: '20 B' }),
    );
    expect(result.rejected[3]!.reason).toBe(t('errors.media_run_too_large', { max: '8 B' }));
  });

  it('enforces the number of files per run, counting existing ones', async () => {
    const existing = (await precheckFiles([file('a.pdf', PDF)])).accepted;
    const result = await precheckFiles([file('b.pdf', PDF, 2), file('c.pdf', PDF, 3)], {
      existing,
      caps: { filesPerRun: 2 },
    });
    expect(result.accepted.map((f) => f.name)).toEqual(['b.pdf']);
    expect(result.rejected[0]!.reason).toBe(t('errors.too_many_files', { max: 2 }));
  });

  it('reports unreadable files instead of throwing', async () => {
    const broken = file('x.pdf', PDF);
    Object.defineProperty(broken, 'slice', {
      value: () => ({ arrayBuffer: () => Promise.reject(new Error('io')) }),
    });
    const result = await precheckFiles([broken]);
    expect(result.rejected[0]!.reason).toBe(t('errors.read_failed', { name: 'x.pdf' }));
  });
});
