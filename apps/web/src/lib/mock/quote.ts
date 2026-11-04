// Demo providers for the hero form while the API is not wired (behind PUBLIC_DM_FEATURE_RESULTS).
// The numbers are illustrative; the real quote is signed by the API from config/pricing.yaml.
import pricing from 'virtual:pricing';
import type { QuoteProvider } from '../../components/HeroForm';
import type { RuleOption } from '../../components/RulePicker';
import type { Quote } from '../../components/RunButton';
import { MB } from '../limits';

export const mockRuleOptions: RuleOption[] = [
  { id: 'r-centavos', title: 'Ignorar diferenças de até R$ 0,05', usedBefore: true },
  { id: 'r-tarifa', title: 'Tarifa bancária entra como despesa operacional', usedBefore: true },
  { id: 'r-juros', title: 'Juros só conciliam com nota de débito' },
  { id: 'r-pix', title: 'PIX recebido casa pelo nome do pagador e valor' },
  { id: 'r-data', title: 'Tolerar até 2 dias úteis entre extrato e razão' },
];

export function estimateTokens(totalBytes: number, fileCount: number, webSearch: boolean): Quote {
  const perMb = 4;
  const perFile = 2;
  const expected = Math.max(
    pricing.min_tokens_per_run,
    Math.ceil((totalBytes / MB) * perMb) + fileCount * perFile + (webSearch ? 20 : 0),
  );
  return { expected, cap: Math.ceil(expected * 1.5) };
}

export const mockQuoteProvider: QuoteProvider = async (request, signal) => {
  await new Promise<void>((resolve, reject) => {
    const handle = setTimeout(resolve, 150);
    signal.addEventListener('abort', () => {
      clearTimeout(handle);
      reject(new DOMException('aborted', 'AbortError'));
    });
  });
  const totalBytes = request.files.reduce((sum, file) => sum + file.size, 0);
  return estimateTokens(totalBytes, request.files.length, request.webSearch);
};
