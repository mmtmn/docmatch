// Local ESLint rule: every visible string must come from t()/tl()/tf() (packages/locales).
// Flags JSX text with letters, `{'texto'}` children, and string literals in the props that
// browsers or assistive technology show to people. Punctuation-only text (" · ", "/", "%")
// and non-string expressions are allowed; any call expression is assumed to be translated.

const UI_PROPS = new Set([
  'placeholder',
  'title',
  'alt',
  'label',
  'aria-label',
  'aria-description',
  'aria-placeholder',
  'aria-roledescription',
  'aria-valuetext',
]);

const HAS_LETTER = /\p{L}/u;

function templateHasLetters(node) {
  return node.quasis.some((quasi) => HAS_LETTER.test(quasi.value.cooked ?? quasi.value.raw));
}

function isTranslated(node) {
  if (!node) return true;
  switch (node.type) {
    case 'Literal':
      return typeof node.value !== 'string' || !HAS_LETTER.test(node.value);
    case 'TemplateLiteral':
      return !templateHasLetters(node);
    case 'ConditionalExpression':
      return isTranslated(node.consequent) && isTranslated(node.alternate);
    case 'LogicalExpression':
      return isTranslated(node.left) && isTranslated(node.right);
    case 'TSAsExpression':
    case 'TSNonNullExpression':
    case 'TSSatisfiesExpression':
      return isTranslated(node.expression);
    default:
      return true;
  }
}

function attributeName(node) {
  if (node.name.type === 'JSXIdentifier') return node.name.name;
  if (node.name.type === 'JSXNamespacedName') {
    return `${node.name.namespace.name}:${node.name.name.name}`;
  }
  return '';
}

/** @type {import('eslint').Rule.RuleModule} */
export default {
  meta: {
    type: 'problem',
    docs: {
      description: 'Textos visíveis só por t() (packages/locales/pt-BR.json)',
    },
    schema: [],
    messages: {
      jsxText:
        'Texto literal em JSX. Use t("chave") de src/lib/i18n e cadastre a chave em pt-BR.json.',
      propText:
        'Texto literal no atributo {{name}}. Use t("chave") de src/lib/i18n e cadastre a chave em pt-BR.json.',
    },
  },
  create(context) {
    return {
      JSXText(node) {
        if (HAS_LETTER.test(node.value)) {
          context.report({ node, messageId: 'jsxText' });
        }
      },
      'JSXElement > JSXExpressionContainer, JSXFragment > JSXExpressionContainer'(node) {
        const expression = node.expression;
        if (
          (expression.type === 'Literal' &&
            typeof expression.value === 'string' &&
            HAS_LETTER.test(expression.value)) ||
          (expression.type === 'TemplateLiteral' && templateHasLetters(expression))
        ) {
          context.report({ node, messageId: 'jsxText' });
        }
      },
      JSXAttribute(node) {
        const name = attributeName(node);
        if (!UI_PROPS.has(name) || !node.value) return;
        const value = node.value;
        if (value.type === 'Literal') {
          if (typeof value.value === 'string' && HAS_LETTER.test(value.value)) {
            context.report({ node, messageId: 'propText', data: { name } });
          }
          return;
        }
        if (value.type === 'JSXExpressionContainer' && !isTranslated(value.expression)) {
          context.report({ node, messageId: 'propText', data: { name } });
        }
      },
    };
  },
};
