import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const call = vi.hoisted(() => vi.fn());
vi.mock('../../lib/api', () => ({ api: call }));
import { BillingPanel } from './BillingPanel';
import type { RequestOptions } from '../../lib/api';
const lastOptions = () => call.mock.calls.at(-1)?.[1] as RequestOptions;
const initial = {
  enabled: true,
  test_mode: true,
  can_manage: true,
  has_customer: false,
  blocked: false,
  minimum_topup_credits: 50,
  maximum_topup_credits: 1000000,
  token_brl: 0.05,
  subscription: null,
};
const location = window.location;
let assign: ReturnType<typeof vi.fn>;
beforeEach(() => {
  vi.clearAllMocks();
  assign = vi.fn();
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...location, search: '', assign },
  });
  call.mockResolvedValue(initial);
});
afterEach(() => {
  Object.defineProperty(window, 'location', { configurable: true, value: location });
});
describe('BillingPanel', () => {
  it('sends only the selected plan and keeps the idempotency key for a retry', async () => {
    render(<BillingPanel onRefresh={vi.fn()} />);
    const button = await screen.findByRole('button', { name: 'Assinar Essencial' });
    expect(screen.getByText(/nenhum valor real/)).toBeInTheDocument();
    call.mockRejectedValueOnce(new Error('Tente novamente'));
    await userEvent.setup().click(button);
    expect(await screen.findByText('Tente novamente')).toBeInTheDocument();
    const first = lastOptions();
    expect(first.body).toEqual({ plan: 'starter' });
    call.mockResolvedValueOnce({ checkout_url: 'https://checkout.stripe.com/c/pay/fixture' });
    await userEvent.setup().click(button);
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://checkout.stripe.com/c/pay/fixture'),
    );
    expect(lastOptions().idempotencyKey).toBe(first.idempotencyKey);
  });
  it('shows no purchase controls for an account outside the allowlist', async () => {
    call.mockResolvedValue({ ...initial, enabled: false, can_manage: false });
    render(<BillingPanel onRefresh={vi.fn()} />);
    expect(await screen.findByText(/compras ainda não estão disponíveis/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Assinar Essencial' })).not.toBeInTheDocument();
  });
  it('offers topups and portal for an active subscription, and blocks an unsafe redirect', async () => {
    call.mockResolvedValue({
      ...initial,
      has_customer: true,
      subscription: {
        plan: 'starter',
        status: 'active',
        current_period_end: null,
        cancel_at: null,
      },
    });
    render(<BillingPanel onRefresh={vi.fn()} />);
    const quantity = await screen.findByRole('spinbutton', { name: 'Quantidade de tokens' });
    await userEvent.setup().clear(quantity);
    await userEvent.setup().type(quantity, '100');
    call.mockResolvedValueOnce({ checkout_url: 'https://evil.invalid/checkout' });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Comprar tokens' }));
    expect(await screen.findByText(/endereço de pagamento inválido/)).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
    expect(lastOptions().body).toEqual({ credits: 100 });
    expect(
      screen.getByRole('button', { name: 'Gerenciar assinatura e faturas' }),
    ).toBeInTheDocument();
  });
  it('keeps portal access while a refunded account is frozen', async () => {
    call.mockResolvedValue({ ...initial, blocked: true, has_customer: true });
    render(<BillingPanel onRefresh={vi.fn()} />);
    expect(await screen.findByRole('alert')).toHaveTextContent(/reembolso/);
    expect(screen.queryByRole('button', { name: 'Assinar Essencial' })).not.toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Gerenciar assinatura e faturas' }),
    ).toBeInTheDocument();
  });
  it('does not claim a payment succeeded from a URL parameter', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...location, search: '?pagamento=sucesso', assign },
    });
    render(<BillingPanel onRefresh={vi.fn()} />);
    expect(await screen.findByText(/aguardando a confirmação/)).toBeInTheDocument();
    expect(call).toHaveBeenCalledTimes(1);
  });
});
