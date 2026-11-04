import { act, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { t } from '../lib/i18n';
import { CharCounter } from './CharCounter';
import { InfoTip } from './InfoTip';
import { RulePicker, type RuleOption } from './RulePicker';
import { RulesBox } from './RulesBox';
import { RunButton } from './RunButton';
import { Switch } from './Switch';
import { charLimitState } from '../hooks/useCharLimit';

describe('Switch', () => {
  it('is a real switch with a visible label', async () => {
    const onChange = vi.fn();
    render(<Switch label={t('form.use_rules')} checked={false} onChange={onChange} />);
    const control = screen.getByRole('switch', { name: t('form.use_rules') });
    expect(control).toHaveAttribute('aria-checked', 'false');
    await userEvent.click(control);
    expect(onChange).toHaveBeenCalledWith(true);
    await userEvent.keyboard('[Space]');
    expect(onChange).toHaveBeenCalledTimes(2);
  });
});

describe('InfoTip', () => {
  it('opens on hover, focus and tap; closes with Esc and when the pointer leaves', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<InfoTip text={t('tooltips.web_search')} />);
    const trigger = screen.getByRole('button', { name: t('tooltips.more_info') });
    const tip = screen.getByRole('tooltip', { hidden: true });
    expect(tip).not.toBeVisible();
    expect(trigger).toHaveAttribute('aria-describedby', tip.id);

    await user.hover(trigger);
    expect(tip).toBeVisible();
    await user.unhover(trigger);
    await act(async () => {
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });
    expect(tip).not.toBeVisible();

    await user.tab();
    expect(tip).toBeVisible();
    await user.keyboard('{Escape}');
    expect(tip).not.toBeVisible();
    expect(trigger).toHaveFocus();

    // Mouse click (hover already open) keeps it open; a touch tap toggles.
    await user.click(trigger);
    expect(tip).toBeVisible();
    await user.keyboard('{Escape}');
    expect(tip).not.toBeVisible();
    await user.pointer({ keys: '[TouchA]', target: trigger });
    expect(tip).toBeVisible();
    await user.pointer({ keys: '[TouchA]', target: trigger });
    expect(tip).not.toBeVisible();
    vi.useRealTimers();
  });

  it('stays open while the pointer is over the tooltip itself', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<InfoTip text="dica" />);
    const trigger = screen.getByRole('button');
    await user.hover(trigger);
    const tip = screen.getByRole('tooltip');
    fireEvent.mouseLeave(trigger);
    fireEvent.mouseEnter(tip.parentElement!);
    await act(async () => {
      vi.advanceTimersByTime(500);
      await Promise.resolve();
    });
    expect(tip).toBeVisible();
    vi.useRealTimers();
  });
});

describe('RunButton', () => {
  it('stays focusable when disabled and explains why', () => {
    render(<RunButton disabled reason={t('form.disabled_no_files')} />);
    const button = screen.getByRole('button', { name: t('form.submit') });
    expect(button).toHaveAttribute('aria-disabled', 'true');
    expect(button).toHaveAccessibleDescription(t('form.disabled_no_files'));
  });

  it('shows the estimate when a quote is present', () => {
    render(<RunButton disabled={false} quote={{ expected: 120, cap: 180 }} />);
    expect(screen.getByText('≈ 120 tokens (máx. 180)')).toBeInTheDocument();
  });

  it('shows the busy state', () => {
    render(<RunButton disabled={false} busy />);
    expect(screen.getByRole('button', { name: t('form.submitting') })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
  });
});

describe('CharCounter', () => {
  it('is empty below 80%, counts from 80% and warns at the limit', () => {
    const { rerender } = render(<CharCounter id="c" state={charLimitState('a'.repeat(10), 100)} />);
    expect(document.getElementById('c')).toHaveTextContent('');
    rerender(<CharCounter id="c" state={charLimitState('a'.repeat(80), 100)} />);
    expect(document.getElementById('c')).toHaveTextContent(
      t('form.counter', { count: 80, max: 100 }),
    );
    rerender(<CharCounter id="c" state={charLimitState('a'.repeat(100), 100)} />);
    expect(document.getElementById('c')).toHaveTextContent(t('form.counter_limit', { max: 100 }));
    expect(document.getElementById('c')).toHaveAttribute('data-at-limit', 'true');
  });
});

const options: RuleOption[] = [
  { id: 'a', title: 'Ignorar centavos', usedBefore: true },
  { id: 'b', title: 'Tarifa bancária como despesa' },
  { id: 'c', title: 'Juros só com nota' },
];

describe('RulePicker', () => {
  it('opens, searches without diacritics and toggles options', async () => {
    const onChange = vi.fn();
    render(<RulePicker options={options} selected={['a']} onChange={onChange} searchMax={20} />);
    await userEvent.click(
      screen.getByRole('button', { name: t('picker.trigger_with_count', { count: 1 }) }),
    );
    const search = screen.getByRole('searchbox', { name: t('picker.search_label') });
    expect(search).toHaveFocus();
    expect(search).toHaveAttribute('maxlength', '20');
    expect(screen.getByRole('group', { name: t('picker.group_recent') })).toBeInTheDocument();
    await userEvent.type(search, 'bancaria');
    const visible = screen.getAllByRole('option');
    expect(visible).toHaveLength(1);
    expect(visible[0]).toHaveTextContent('Tarifa bancária como despesa');
    await userEvent.click(visible[0]!);
    expect(onChange).toHaveBeenCalledWith(['a', 'b']);
  });

  it('is keyboard operable and closes with Esc returning focus', async () => {
    const onChange = vi.fn();
    render(<RulePicker options={options} selected={[]} onChange={onChange} searchMax={100} />);
    const trigger = screen.getByRole('button', { name: t('picker.trigger') });
    trigger.focus();
    await userEvent.keyboard('{Enter}');
    await userEvent.keyboard('{ArrowDown}');
    const listbox = screen.getByRole('listbox');
    expect(listbox).toHaveFocus();
    await userEvent.keyboard('{ArrowDown}{ArrowDown}');
    await userEvent.keyboard(' ');
    expect(onChange).toHaveBeenCalledWith(['c']);
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it('shows the empty state and clears the selection', async () => {
    const onChange = vi.fn();
    render(
      <RulePicker options={options} selected={['a', 'b']} onChange={onChange} searchMax={100} />,
    );
    await userEvent.click(screen.getByRole('button'));
    await userEvent.type(screen.getByRole('searchbox'), 'zzz');
    expect(screen.getByText(t('picker.no_results'))).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: t('picker.clear') }));
    expect(onChange).toHaveBeenCalledWith([]);
  });
});

describe('RulesBox', () => {
  function Box({ withOptions }: { withOptions: boolean }) {
    return (
      <RulesBox
        value=""
        onChange={vi.fn()}
        useRules
        onUseRulesChange={vi.fn()}
        webSearch={false}
        onWebSearchChange={vi.fn()}
        ruleOptions={withOptions ? options : []}
        selectedRules={[]}
        onSelectedRulesChange={vi.fn()}
      />
    );
  }

  it('renders the label row in order and hides the picker without history', () => {
    const { rerender } = render(<Box withOptions={false} />);
    expect(screen.queryByRole('button', { name: t('picker.trigger') })).not.toBeInTheDocument();
    const textarea = screen.getByLabelText(t('form.rules_label'));
    expect(textarea).toHaveAttribute('maxlength');
    expect(textarea).toHaveAttribute('rows', '1');
    const switches = screen.getAllByRole('switch');
    expect(switches.map((s) => s.textContent)).toEqual([t('form.use_rules'), t('form.web_search')]);
    rerender(<Box withOptions />);
    expect(screen.getByRole('button', { name: t('picker.trigger') })).toBeInTheDocument();
  });
});
