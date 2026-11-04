import { useId, useRef } from 'react';
import { useAutosize } from '../hooks/useAutosize';
import { useCharLimit } from '../hooks/useCharLimit';
import { t } from '../lib/i18n';
import { textLimits } from '../lib/limits';
import { CharCounter } from './CharCounter';
import { InfoTip } from './InfoTip';
import { RulePicker, type RuleOption } from './RulePicker';
import { Switch } from './Switch';
import styles from './RulesBox.module.css';

export interface RulesBoxProps {
  value: string;
  onChange: (value: string) => void;
  useRules: boolean;
  onUseRulesChange: (value: boolean) => void;
  webSearch: boolean;
  onWebSearchChange: (value: boolean) => void;
  /** Saved rules; the picker is not rendered at all when empty (no history). */
  ruleOptions: readonly RuleOption[];
  selectedRules: readonly string[];
  onSelectedRulesChange: (ids: string[]) => void;
  /** Textarea grows from 1 to `maxLines` lines, then scrolls internally. */
  maxLines?: number;
  /** Keyboard-open mode on a phone: only the label and the textarea stay visible. */
  compact?: boolean;
  disabled?: boolean;
}

export function RulesBox({
  value,
  onChange,
  useRules,
  onUseRulesChange,
  webSearch,
  onWebSearchChange,
  ruleOptions,
  selectedRules,
  onSelectedRulesChange,
  maxLines = 6,
  compact = false,
  disabled = false,
}: RulesBoxProps) {
  const id = useId();
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const max = textLimits.rules_text;
  const limit = useCharLimit(value, max);
  useAutosize(textareaRef, value, { minLines: 1, maxLines });

  const textareaId = `${id}-rules`;
  const counterId = `${id}-counter`;
  const helpId = `${id}-help`;

  return (
    <div
      className={styles.box}
      data-rules-off={useRules ? undefined : 'true'}
      data-compact={compact ? 'true' : undefined}
    >
      <div className={styles.labelRow}>
        <label htmlFor={textareaId} className={styles.label}>
          {t('form.rules_label')}
        </label>
        {ruleOptions.length > 0 && (
          <div className={styles.picker}>
            <RulePicker
              options={ruleOptions}
              selected={selectedRules}
              onChange={onSelectedRulesChange}
              searchMax={textLimits.rule_picker_search}
              disabled={disabled || !useRules}
            />
          </div>
        )}
        <div className={styles.switches}>
          <Switch
            label={t('form.use_rules')}
            checked={useRules}
            onChange={onUseRulesChange}
            disabled={disabled}
          >
            <InfoTip text={t('tooltips.use_rules')} />
          </Switch>
          <Switch
            label={t('form.web_search')}
            checked={webSearch}
            onChange={onWebSearchChange}
            disabled={disabled}
          >
            <InfoTip text={t('tooltips.web_search')} />
          </Switch>
        </div>
      </div>

      <textarea
        ref={textareaRef}
        id={textareaId}
        className={styles.textarea}
        value={value}
        rows={1}
        maxLength={max}
        placeholder={t('form.rules_placeholder')}
        aria-describedby={`${helpId} ${counterId}`}
        disabled={disabled}
        spellCheck
        onChange={(event) => onChange(event.target.value)}
      />
      <p id={helpId} className="visually-hidden">
        {useRules ? t('form.rules_help') : t('form.rules_disabled_hint')}
      </p>
      <CharCounter id={counterId} state={limit} />
    </div>
  );
}
