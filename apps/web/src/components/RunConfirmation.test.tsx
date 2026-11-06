import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { t } from '../lib/i18n';
import { RunConfirmation } from './RunConfirmation';
const quote = {
  id: 'q1',
  expected_credits: 1200,
  cap_credits: 2000,
  signature: 'example',
  requires_approval: false,
};
describe('RunConfirmation', () => {
  it('shows the estimate and cap before explicitly confirming', async () => {
    const answer = vi.fn();
    render(<RunConfirmation quote={quote} exempt={false} onAnswer={answer} />);
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-modal', 'true');
    expect(screen.getByText(t('app.confirm_tokens', { count: 1200 }))).toBeVisible();
    expect(screen.getByText(t('app.confirm_tokens', { count: 2000 }))).toBeVisible();
    expect(answer).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: t('app.confirm_start') }));
    expect(answer).toHaveBeenCalledWith(true);
  });
  it('cancels on dismissal without confirming', async () => {
    const answer = vi.fn();
    render(<RunConfirmation quote={quote} exempt={false} onAnswer={answer} />);
    fireEvent(screen.getByRole('dialog'), new Event('cancel', { cancelable: true }));
    expect(answer).toHaveBeenCalledWith(false);
    await userEvent.click(screen.getByRole('button', { name: t('app.confirm_cancel') }));
    expect(answer).not.toHaveBeenCalledWith(true);
  });
  it('explains the test exemption instead of displaying a charge', () => {
    render(<RunConfirmation quote={quote} exempt onAnswer={vi.fn()} />);
    expect(screen.getByText(t('app.confirm_exempt'))).toBeVisible();
    expect(screen.queryByText(t('app.confirm_cap'))).toBeNull();
  });
});
