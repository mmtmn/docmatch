// Mocked run for the results screen (behind PUBLIC_DM_FEATURE_RESULTS). No API wiring yet.
// Every counterparty, document number and amount below is invented for the demo: nothing here may
// come from a real customer case or from testdata/private (which is never committed or published).
import type { CleanMatch, Finding, RunResults } from '../../components/results/types';

const BRL = 'BRL';

/** A one-second silent WAV, built in the browser so the player has something real to seek. */
export function silentWav(seconds = 4, sampleRate = 8000): Blob {
  const samples = seconds * sampleRate;
  const buffer = new ArrayBuffer(44 + samples * 2);
  const view = new DataView(buffer);
  const write = (offset: number, text: string): void => {
    for (let i = 0; i < text.length; i += 1) view.setUint8(offset + i, text.charCodeAt(i));
  };
  write(0, 'RIFF');
  view.setUint32(4, 36 + samples * 2, true);
  write(8, 'WAVE');
  write(12, 'fmt ');
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, sampleRate * 2, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  write(36, 'data');
  view.setUint32(40, samples * 2, true);
  return new Blob([buffer], { type: 'audio/wav' });
}

let audioUrl: string | null = null;
/** Blob URL for the demo audio (allowed by the CSP media-src blob:). Browser only. */
export function mockAudioUrl(): string | null {
  if (typeof URL === 'undefined' || typeof URL.createObjectURL !== 'function') return null;
  audioUrl ??= URL.createObjectURL(silentWav());
  return audioUrl;
}

export const mockFindings: Finding[] = [
  {
    id: 'f-001',
    title: 'Transferência sem contrapartida no razão',
    matchType: 'missing',
    amountAtRisk: 9730,
    currency: BRL,
    confidence: 'high',
    status: 'open',
    explanation:
      'O extrato mostra uma TED recebida de Serra Azul Transportes em 12/10 que não aparece em nenhum título do razão. Nenhuma regra de tolerância explica a diferença.',
    rule: 'Chave exata: valor + data ± 2 dias úteis + contraparte',
    left: {
      kind: 'table',
      sourceName: 'extrato-outubro.ofx',
      rows: [
        {
          id: 'l1',
          date: '2026-10-11',
          description: 'PIX RECEBIDO ALFA COM',
          amount: 2300,
          document: 'E-8812',
        },
        {
          id: 'l2',
          date: '2026-10-12',
          description: 'TED RECEBIDA SERRA AZUL TRANSPORTES',
          amount: 9730,
          document: 'E-8813',
          cited: true,
        },
        {
          id: 'l3',
          date: '2026-10-12',
          description: 'TARIFA TED',
          amount: -9.9,
          document: 'E-8814',
        },
      ],
    },
    right: {
      kind: 'table',
      sourceName: 'razao-outubro.xlsx',
      rows: [
        {
          id: 'r1',
          date: '2026-10-11',
          description: 'Recebimento Alfa Comércio',
          amount: 2300,
          document: 'NF 4410',
        },
        {
          id: 'r2',
          date: '2026-10-13',
          description: 'Tarifa bancária',
          amount: -9.9,
          document: 'LC 120',
        },
      ],
    },
  },
  {
    id: 'f-002',
    title: 'Nota fiscal 4421 com valor diferente do pagamento',
    matchType: 'amount',
    amountAtRisk: 1275.5,
    currency: BRL,
    confidence: 'medium',
    status: 'open',
    explanation:
      'O pagamento de R$ 11.224,50 casa com a NF 4421 de R$ 12.500,00 pela data e pelo fornecedor, mas falta R$ 1.275,50. Pode ser retenção de ISS não lançada.',
    rule: 'Mesma contraparte e data; diferença acima da tolerância de R$ 0,05',
    left: {
      kind: 'table',
      sourceName: 'extrato-outubro.ofx',
      rows: [
        {
          id: 'l1',
          date: '2026-10-15',
          description: 'PAGTO FORNECEDOR BETA SERVICOS',
          amount: -11224.5,
          document: 'E-8830',
          cited: true,
        },
      ],
    },
    right: {
      kind: 'table',
      sourceName: 'notas-outubro.pdf',
      rows: [
        {
          id: 'r1',
          date: '2026-10-15',
          description: 'NF 4421 Beta Serviços',
          amount: -12500,
          document: 'NF 4421',
          cited: true,
        },
        {
          id: 'r2',
          date: '2026-10-15',
          description: 'NF 4422 Beta Serviços',
          amount: -980,
          document: 'NF 4422',
        },
      ],
    },
  },
  {
    id: 'f-003',
    title: 'Pagamento em duplicidade para Gama Materiais',
    matchType: 'duplicate',
    amountAtRisk: 3890,
    currency: BRL,
    confidence: 'high',
    status: 'open',
    explanation:
      'Dois débitos de R$ 3.890,00 para a mesma contraparte em 48 horas, e uma única nota no período.',
    rule: 'Mesmo valor, mesma contraparte, intervalo menor que 3 dias',
    left: {
      kind: 'table',
      sourceName: 'extrato-outubro.ofx',
      rows: [
        {
          id: 'l1',
          date: '2026-10-20',
          description: 'PAGTO GAMA MATERIAIS',
          amount: -3890,
          document: 'E-8851',
          cited: true,
        },
        {
          id: 'l2',
          date: '2026-10-22',
          description: 'PAGTO GAMA MATERIAIS',
          amount: -3890,
          document: 'E-8857',
          cited: true,
        },
      ],
    },
    right: {
      kind: 'table',
      sourceName: 'razao-outubro.xlsx',
      rows: [
        {
          id: 'r1',
          date: '2026-10-20',
          description: 'Gama Materiais NF 771',
          amount: -3890,
          document: 'NF 771',
          cited: true,
        },
      ],
    },
  },
  {
    id: 'f-004',
    title: 'Acordo verbal de desconto citado em reunião',
    matchType: 'amount',
    amountAtRisk: 640,
    currency: BRL,
    confidence: 'low',
    status: 'open',
    explanation:
      'Na gravação da reunião de 18/10, o fornecedor Delta menciona um desconto de 5% que não consta na nota nem no pagamento. Confiança baixa: depende da transcrição.',
    left: {
      kind: 'audio',
      sourceName: 'reuniao-delta-18-10.m4a',
      src: null,
      mimeType: 'audio/wav',
      timestamp: 2,
      transcript: '"…fechamos com cinco por cento de desconto nessa remessa, como combinado."',
    },
    right: {
      kind: 'table',
      sourceName: 'notas-outubro.pdf',
      rows: [
        {
          id: 'r1',
          date: '2026-10-25',
          description: 'NF 4430 Delta Insumos',
          amount: -12800,
          document: 'NF 4430',
          cited: true,
        },
      ],
    },
  },
  {
    id: 'f-005',
    title: 'Data de competência diferente em aluguel',
    matchType: 'date',
    amountAtRisk: 0,
    currency: BRL,
    confidence: 'medium',
    status: 'open',
    explanation:
      'O débito do aluguel caiu em 05/10, mas o razão registra a competência em 30/09. Valor igual; só a data difere.',
    rule: 'Tolerância de 2 dias úteis excedida',
    left: {
      kind: 'table',
      sourceName: 'extrato-outubro.ofx',
      rows: [
        {
          id: 'l1',
          date: '2026-10-05',
          description: 'DEB AUTOMATICO ALUGUEL',
          amount: -7200,
          document: 'E-8790',
          cited: true,
        },
      ],
    },
    right: {
      kind: 'table',
      sourceName: 'razao-outubro.xlsx',
      rows: [
        {
          id: 'r1',
          date: '2026-09-30',
          description: 'Aluguel sede',
          amount: -7200,
          document: 'LC 98',
          cited: true,
        },
      ],
    },
  },
];

export const mockCleanMatches: CleanMatch[] = Array.from({ length: 18 }, (_, i) => ({
  id: `c-${String(i + 1).padStart(3, '0')}`,
  date: `2026-10-${String(1 + ((i * 3) % 28)).padStart(2, '0')}`,
  description: [
    'Folha de pagamento',
    'Energia elétrica',
    'Assinatura SaaS',
    'Recebimento cliente',
    'Frete',
  ][i % 5]!,
  amount: [-42800, -1320.45, -890, 15230, -2140.9][i % 5]!,
  currency: BRL,
  leftSource: 'extrato-outubro.ofx',
  rightSource: 'razao-outubro.xlsx',
}));

export const mockResults: RunResults = {
  runId: 'demo',
  fileCount: 4,
  findings: mockFindings,
  cleanMatches: mockCleanMatches,
  demo: true,
};
