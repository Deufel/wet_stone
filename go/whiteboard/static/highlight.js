// v1.3 🎨 highlight.js — Custom Highlight API syntax highlighting
// Pattern: <pre><code class="language">…</code></pre>
// Languages registered: css, html, python, javascript, go, sqlite
// By: Michael Deufel

if (typeof window !== 'undefined' && window.__highlightLoaded) {
  /* already evaluated in this realm — the helmet <script> can re-run on
     re-render/hot-reload; a second top-level `const` would throw. No-op. */
} else {
if (typeof window !== 'undefined') window.__highlightLoaded = true;

const CSS_LANG = [
  ['css-comment',     /\/\*[\s\S]*?\*\//g],
  ['css-string',      /"[^"]*"|'[^']*'/g],
  ['css-atrule',      /@[\w-]+/g],
  ['css-var-name',    /--[\w-]+/g],
  ['css-unit',        /\b\d*\.?\d+(?:px|rem|em|%|vw|vh|svh|svw|dvh|dvw|ch|ex|fr|deg|rad|turn|ms|s)\b/g],
  ['css-number',      /\b\d*\.?\d+\b/g],
  ['css-property',    /(?:^|(?<=[{;])\s*)[\w-]+(?=\s*:)/gm],
  ['css-selector',    /^[ \t]*([^{};@/][^{};]*?)(?=\s*\{)/gm],
  ['css-punctuation', /[{}();]/g],
];

const HTML_LANG = [
  ['html-comment',   /<!--[\s\S]*?-->/g],
  ['html-doctype',   /<!DOCTYPE[^>]*>/gi],
  ['html-entity',    /&[#\w]+;/g],
  ['html-value',     /(?<==\s*)"[^"]*"|(?<==\s*)'[^']*'/g],
  ['html-tag',       /(?<=<\/?)[\w-]+/g],
  ['html-attribute', /\b[\w-]+(?=\s*=|\s*\/?>)/g],
  ['html-bracket',   /<\/?|\/?>/g],
];

const PY_KEYWORDS =
  'False|None|True|and|as|assert|async|await|break|class|continue|def|del|'
  + 'elif|else|except|finally|for|from|global|if|import|in|is|lambda|'
  + 'nonlocal|not|or|pass|raise|return|try|while|with|yield|match|case';
const PY_BUILTINS =
  'abs|all|any|ascii|bin|bool|bytearray|bytes|callable|chr|classmethod|'
  + 'compile|complex|delattr|dict|dir|divmod|enumerate|eval|exec|filter|'
  + 'float|format|frozenset|getattr|globals|hasattr|hash|help|hex|id|input|'
  + 'int|isinstance|issubclass|iter|len|list|locals|map|max|memoryview|min|'
  + 'next|object|oct|open|ord|pow|print|property|range|repr|reversed|round|'
  + 'set|setattr|slice|sorted|staticmethod|str|sum|super|tuple|type|vars|zip|'
  + 'self|cls';

const PYTHON_LANG = [
  ['python-string',     /(?:[fFrRbB]{1,2})?(?:"""[\s\S]*?"""|'''[\s\S]*?''')/g],
  ['python-string',     /(?:[fFrRbB]{1,2})?(?:"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*')/g],
  ['python-comment',    /#[^\n]*/g],
  ['python-decorator',  /@[\w.]+/g],
  ['python-function',   /(?<=\bdef\s+)[a-zA-Z_]\w*/g],
  ['python-class',      /(?<=\bclass\s+)[a-zA-Z_]\w*/g],
  ['python-keyword',    new RegExp(`\\b(?:${PY_KEYWORDS})\\b`, 'g')],
  ['python-builtin',    new RegExp(`\\b(?:${PY_BUILTINS})\\b`, 'g')],
  ['python-number',     /\b(?:0[xX][\da-fA-F_]+|0[oO][0-7_]+|0[bB][01_]+|\d[\d_]*(?:\.\d[\d_]*)?(?:[eE][+-]?\d+)?)\b/g],
  ['python-operator',   /->|:=|==|!=|<=|>=|\*\*|\/\/|<<|>>|[+\-*\/%<>=&|^~]/g],
  ['python-punctuation', /[{}()[\],;]/g],
];

const JS_KEYWORDS =
  'as|async|await|break|case|catch|class|const|continue|debugger|default|'
  + 'delete|do|else|export|extends|finally|for|from|function|if|import|in|'
  + 'instanceof|let|new|of|return|static|super|switch|this|throw|try|'
  + 'typeof|var|void|while|with|yield';

const JS_BUILTINS =
  'true|false|null|undefined|NaN|Infinity|globalThis|'
  + 'Array|Boolean|Date|Error|JSON|Map|Math|Number|Object|Promise|RegExp|'
  + 'Set|String|Symbol|WeakMap|WeakSet|'
  + 'Function|Reflect|Proxy|Intl|'
  + 'console|document|window|self';

const JAVASCRIPT_LANG = [
  ['javascript-string',     /`(?:\\.|[^`\\])*`/g],
  ['javascript-string',     /"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'/g],
  ['javascript-comment',    /\/\/[^\n]*|\/\*[\s\S]*?\*\//g],
  ['javascript-decorator',  /@[\w.]+/g],
  ['javascript-function',   /(?<=\bfunction\s+)[a-zA-Z_$][\w$]*/g],
  ['javascript-class',      /(?<=\bclass\s+)[a-zA-Z_$][\w$]*/g],
  ['javascript-keyword',    new RegExp(`\\b(?:${JS_KEYWORDS})\\b`, 'g')],
  ['javascript-builtin',    new RegExp(`\\b(?:${JS_BUILTINS})\\b`, 'g')],
  ['javascript-number',     /\b(?:0[xX][\da-fA-F_]+n?|0[oO][0-7_]+n?|0[bB][01_]+n?|\d[\d_]*n?(?:\.\d[\d_]*)?(?:[eE][+-]?\d+)?)\b/g],
  ['javascript-operator',   /=>|\?\.|\?\?|===|!==|<=|>=|\*\*|<<|>>>|>>|&&|\|\||\.\.\.|[+\-*\/%<>=&|^~!?]/g],
  ['javascript-punctuation', /[{}()[\],;:]/g],
];

const GO_KEYWORDS =
  'break|case|chan|const|continue|default|defer|else|fallthrough|for|'
  + 'func|go|goto|if|import|interface|map|package|range|return|select|'
  + 'struct|switch|type|var';

const GO_BUILTINS =
  'true|false|nil|iota|'
  + 'bool|byte|complex64|complex128|error|float32|float64|'
  + 'int|int8|int16|int32|int64|rune|string|'
  + 'uint|uint8|uint16|uint32|uint64|uintptr|any|comparable|'
  + 'append|cap|close|complex|copy|delete|imag|len|make|new|'
  + 'panic|print|println|real|recover|min|max|clear';

const GO_LANG = [
  // Raw strings first (backticks can contain newlines).
  ['go-string',      /`[\s\S]*?`/g],
  // Interpreted strings and rune literals.
  ['go-string',      /"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'/g],
  // Comments (line + block).
  ['go-comment',     /\/\/[^\n]*|\/\*[\s\S]*?\*\//g],
  // Function declarations — name after `func`, optionally with a receiver.
  // Pattern matches `func Name(` and `func (r *T) Name(`.
  ['go-function',    /(?<=\bfunc\s+(?:\([^)]*\)\s+)?)[a-zA-Z_]\w*/g],
  // Type declarations — name after `type`.
  ['go-class',       /(?<=\btype\s+)[a-zA-Z_]\w*/g],
  // Keywords.
  ['go-keyword',     new RegExp(`\\b(?:${GO_KEYWORDS})\\b`, 'g')],
  // Builtins and primitive types.
  ['go-builtin',     new RegExp(`\\b(?:${GO_BUILTINS})\\b`, 'g')],
  // Numbers — int, float, hex, octal, binary, with optional underscores and imaginary suffix.
  ['go-number',      /\b(?:0[xX][\da-fA-F_]+|0[oO]?[0-7_]+|0[bB][01_]+|\d[\d_]*(?:\.\d[\d_]*)?(?:[eE][+-]?\d+)?i?)\b/g],
  // Operators — note <- (channel) and := (short var decl) are Go-distinctive.
  ['go-operator',    /:=|<-|\.\.\.|==|!=|<=|>=|&&|\|\||<<|>>|&\^|\+\+|--|[+\-*\/%&|^<>=!~]/g],
  ['go-punctuation', /[{}()[\],;.:]/g],
];

// SQL is case-insensitive, so keyword/type/function patterns carry the `i` flag.
const SQLITE_KEYWORDS =
  'ABORT|ACTION|ADD|AFTER|ALL|ALTER|ALWAYS|ANALYZE|AND|AS|ASC|ATTACH|'
  + 'AUTOINCREMENT|BEFORE|BEGIN|BETWEEN|BY|CASCADE|CASE|CAST|CHECK|COLLATE|'
  + 'COLUMN|COMMIT|CONFLICT|CONSTRAINT|CREATE|CROSS|CURRENT|CURRENT_DATE|'
  + 'CURRENT_TIME|CURRENT_TIMESTAMP|DATABASE|DEFAULT|DEFERRABLE|DEFERRED|'
  + 'DELETE|DESC|DETACH|DISTINCT|DO|DROP|EACH|ELSE|END|ESCAPE|EXCEPT|'
  + 'EXCLUDE|EXCLUSIVE|EXISTS|EXPLAIN|FAIL|FALSE|FILTER|FIRST|FOLLOWING|'
  + 'FOR|FOREIGN|FROM|FULL|GENERATED|GLOB|GROUP|GROUPS|HAVING|IF|IGNORE|'
  + 'IMMEDIATE|IN|INDEX|INDEXED|INITIALLY|INNER|INSERT|INSTEAD|INTERSECT|'
  + 'INTO|IS|ISNULL|JOIN|KEY|LAST|LEFT|LIKE|LIMIT|MATCH|MATERIALIZED|'
  + 'NATURAL|NO|NOT|NOTHING|NOTNULL|NULL|NULLS|OF|OFFSET|ON|OR|ORDER|'
  + 'OTHERS|OUTER|OVER|PARTITION|PLAN|PRAGMA|PRECEDING|PRIMARY|QUERY|RAISE|'
  + 'RANGE|RECURSIVE|REFERENCES|REGEXP|REINDEX|RELEASE|RENAME|REPLACE|'
  + 'RESTRICT|RETURNING|RIGHT|ROLLBACK|ROW|ROWS|SAVEPOINT|SELECT|SET|TABLE|'
  + 'TEMP|TEMPORARY|THEN|TIES|TO|TRANSACTION|TRIGGER|TRUE|UNBOUNDED|UNION|'
  + 'UNIQUE|UPDATE|USING|VACUUM|VALUES|VIEW|VIRTUAL|WHEN|WHERE|WINDOW|WITH|'
  + 'WITHOUT';

// Type-affinity names (not reserved words, but conventional to colour in DDL).
const SQLITE_TYPES =
  'INTEGER|INT2|INT8|INT|TINYINT|SMALLINT|MEDIUMINT|BIGINT|UNSIGNED|'
  + 'CHARACTER|VARCHAR|NVARCHAR|NCHAR|CHAR|CLOB|TEXT|'
  + 'BLOB|REAL|DOUBLE|FLOAT|NUMERIC|DECIMAL|BOOLEAN';

// Core scalar / aggregate / date / JSON built-in functions.
const SQLITE_FUNCTIONS =
  'abs|avg|changes|coalesce|concat_ws|concat|count|datetime|date|'
  + 'format|group_concat|hex|ifnull|iif|instr|json_array_length|json_array|'
  + 'json_extract|json_group_array|json_group_object|json_insert|json_object|'
  + 'json_patch|json_quote|json_remove|json_replace|json_set|json_type|'
  + 'json_valid|jsonb|json|julianday|last_insert_rowid|length|likelihood|'
  + 'likely|load_extension|lower|ltrim|max|min|nullif|octet_length|printf|'
  + 'quote|randomblob|random|round|rtrim|sign|string_agg|strftime|substring|'
  + 'substr|sum|timediff|time|total_changes|total|trim|typeof|unhex|unicode|'
  + 'unixepoch|unlikely|upper|zeroblob';

const SQLITE_LANG = [
  // Comments first: SQL comments routinely contain apostrophes ("-- don't"),
  // and `--`/`/* */` inside a string literal is rare — so claiming comment
  // ranges before strings is the more robust order for SQL (the imperative
  // languages above do the reverse, where the opposite tradeoff holds).
  ['sqlite-comment',    /--[^\n]*|\/\*[\s\S]*?\*\//g],
  // Single-quoted string literals; '' is the escaped quote (no backslash escapes).
  // Line-bounded ([^'\n]) so a stray apostrophe in a comment can't desync pairing
  // and swallow the rest of the file (multi-line literals are rare in hand SQL).
  ['sqlite-string',     /'(?:''|[^'\n])*'/g],
  // Quoted identifiers: "id", `id`, [id] — distinct from string literals.
  ['sqlite-identifier', /"(?:""|[^"\n])*"|`(?:``|[^`\n])*`|\[[^\]\n]*\]/g],
  ['sqlite-keyword',    new RegExp(`\\b(?:${SQLITE_KEYWORDS})\\b`, 'gi')],
  ['sqlite-type',       new RegExp(`\\b(?:${SQLITE_TYPES})\\b`, 'gi')],
  ['sqlite-function',   new RegExp(`\\b(?:${SQLITE_FUNCTIONS})\\b`, 'gi')],
  // Bind parameters: ?, ?NNN, :name, @name, $name.
  ['sqlite-parameter',  /\?\d*|[:@$]\w+/g],
  ['sqlite-number',     /\b0[xX][\da-fA-F]+\b|\b\d+(?:\.\d*)?(?:[eE][+-]?\d+)?\b|\.\d+(?:[eE][+-]?\d+)?/g],
  // Operators — || (concat) and ->/->> (JSON) are SQLite-distinctive.
  ['sqlite-operator',   /->>|->|\|\||<<|>>|<=|>=|==|!=|<>|[-+*\/%&|~<>=]/g],
  ['sqlite-punctuation', /[(),;.]/g],
];

const languages = new Map([
  ['css',        CSS_LANG],
  ['html',       HTML_LANG],
  ['python',     PYTHON_LANG],
  ['javascript', JAVASCRIPT_LANG],
  ['go',         GO_LANG],
  ['sqlite',     SQLITE_LANG],
]);

function allTokenNames() {
  const names = new Set();
  for (const tokens of languages.values())
    for (const [name] of tokens) names.add(name);
  return names;
}

function detectLang(codeEl) {
  for (const name of languages.keys())
    if (codeEl.classList.contains(name)) return name;
  return null;
}

function overlapsAny(claims, start, end) {
  for (const [s, e] of claims) if (start < e && end > s) return true;
  return false;
}

function highlightAll(root = document) {
  if (!window.CSS?.highlights || typeof Highlight === 'undefined') return;

  for (const name of allTokenNames()) window.CSS.highlights.delete(name);

  const byLang = new Map();
  for (const code of root.querySelectorAll('pre > code')) {
    const lang = detectLang(code);
    if (!lang) continue;
    const node = code.firstChild;
    if (node?.nodeType !== Node.TEXT_NODE) continue;
    if (!byLang.has(lang)) byLang.set(lang, []);
    byLang.get(lang).push({ src: node.textContent, textNode: node, claims: [] });
  }

  for (const [lang, blocks] of byLang) {
    const tokens = languages.get(lang);
    for (const [name, pattern] of tokens) {
      const highlight = new Highlight();
      for (const block of blocks) {
        const re = new RegExp(pattern.source, pattern.flags);
        for (const match of block.src.matchAll(re)) {
          const start = match.index;
          const end   = start + match[0].length;
          if (overlapsAny(block.claims, start, end)) continue;
          const range = new Range();
          range.setStart(block.textNode, start);
          range.setEnd(block.textNode, end);
          highlight.add(range);
          block.claims.push([start, end]);
        }
      }
      if (highlight.size > 0) window.CSS.highlights.set(name, highlight);
    }
  }
}

// Expose for dynamically-rendered content (the docs render after DOMContentLoaded).
if (typeof window !== 'undefined') window.highlightAll = highlightAll;

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', () => highlightAll());
} else {
  highlightAll();
}

}  // end idempotent load guard
