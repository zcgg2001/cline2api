const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const root = path.resolve(__dirname, '..');
const template = fs.readFileSync(path.join(root, 'templates/admin.html'), 'utf8');
const i18n = template.split('// ===== i18n =====')[1].split('// ===== /i18n =====')[0];
const updates = template.split('// ========== 在线更新 ==========')[1].split('// ========== 仪表盘 ==========')[0];
const extensionSources = ['accounts.js', 'account_billing.js'].map(name => fs.readFileSync(path.join(root, 'js', name), 'utf8'));

function harness(initialLanguage = 'zh') {
  const elements = new Map();
  const textNodes = [];
  const attrs = [];
  function element(id) {
    if (!elements.has(id)) {
      const classes = new Set();
      elements.set(id, {
        textContent: '', style: {}, disabled: false,
        classList: {
          add: name => classes.add(name), remove: name => classes.delete(name),
          contains: name => classes.has(name),
          toggle: (name, on) => on ? classes.add(name) : classes.delete(name),
        },
      });
    }
    return elements.get(id);
  }
  const context = vm.createContext({
    document: {
      cookie: 'cline_admin_lang=' + initialLanguage, documentElement: {}, body: {},
      createTreeWalker() {
        let i = -1;
        return {nextNode() { return ++i < textNodes.length; }, get currentNode() { return textNodes[i]; }};
      },
      querySelectorAll: () => attrs,
    },
    navigator: {language: 'zh-CN'}, NodeFilter: {SHOW_TEXT: 4},
    localStorage: {getItem() {}, setItem() {}}, _: element,
    toast() {}, setTimeout() {}, clearTimeout() {},
    loadStats() {}, loadAccounts() {}, loadRequestLogs() {}, loadModels() {}, loadConfig() {},
    api: async () => ({data: {}}),
  });
  vm.runInContext(i18n + '\n' + updates, context);
  for (const source of extensionSources) {
    const dict = source.slice(0, source.indexOf('Object.assign(I18N'));
    const name = source.match(/const (\w+) =/)[1];
    vm.runInContext(dict + '\nObject.assign(I18N,' + name + '); Object.entries(' + name + ').forEach(([zh,en])=>I18N_REV[en]=zh);', context);
  }
  return {
    context, element,
    run: code => vm.runInContext(code, context),
    switch: lang => vm.runInContext('setLang(' + JSON.stringify(lang) + ')', context),
    node(value, ignored = false) {
      const node = {nodeValue: value, parentElement: {closest: () => ignored ? {} : null}};
      textNodes.push(node);
      return node;
    },
    attr(value) {
      const el = {
        value, closest: () => null, hasAttribute: name => name === 'title',
        getAttribute() { return this.value; }, setAttribute(name, next) { this.value = next; },
      };
      attrs.push(el);
      return el;
    },
  };
}

test('Chinese sources survive repeated English switches, including ambiguous translations', () => {
  for (const initial of ['zh', 'en']) {
    const h = harness(initial);
    const admin = h.node('  管理\n'), role = h.node('管理员');
    const whitespace = h.node('OAuth 或 refreshToken');
    h.run('applyLang()');
    for (let i = 0; i < 4; i++) {
      h.switch('en');
      assert.equal(admin.nodeValue, '  Admin\n');
      assert.equal(role.nodeValue, 'Admin');
      assert.equal(whitespace.nodeValue, 'OAuth or refreshToken');
      h.switch('zh');
      assert.equal(admin.nodeValue, '  管理\n');
      assert.equal(role.nodeValue, '管理员');
    }
  }
});

test('translation does not restore stale dynamic text or password visibility labels', () => {
  const h = harness();
  const count = h.node('10'), label = h.attr('显示密码');
  const script = h.node('管理', true);
  h.switch('en');
  count.nodeValue = '25';
  label.value = 'Hide password';
  h.switch('zh');
  assert.equal(count.nodeValue, '25');
  assert.equal(label.value, '隐藏密码');
  h.switch('en');
  assert.equal(label.value, 'Hide password');
  assert.equal(script.nodeValue, '管理');
  const dynamic = h.node('Cancel');
  h.switch('zh');
  assert.equal(dynamic.nodeValue, '取消');
});

test('all literal translation calls and static Chinese attributes have English entries', () => {
  const h = harness();
  const dict = h.run('I18N');
  const calls = [...[template, ...extensionSources].join('\n').matchAll(/\bt\('([^'\n]*)'\)/g)].map(m => m[1]);
  const html = [template.split('<script>')[0], ...['accounts.html', 'availability.html'].map(name => fs.readFileSync(path.join(root, 'templates', name), 'utf8'))].join('\n');
  const attributes = [...html.matchAll(/(?:title|placeholder|aria-label)=(['"])(.*?)\1/g)].map(m => m[2]);
  for (const text of new Set([...calls, ...attributes])) {
    if (/[\u3400-\u9fff]/.test(text)) assert(dict[text], 'Missing English translation: ' + text);
  }
});

test('all visible Chinese text in the embedded tab templates has an English entry', () => {
  const h = harness();
  const dict = h.run('I18N');
  const html = [
    template.split('</head>')[1].split('<script>')[0],
    fs.readFileSync(path.join(root, 'templates/accounts.html'), 'utf8'),
    fs.readFileSync(path.join(root, 'templates/availability.html'), 'utf8'),
  ].join('\n');
  const textNodes = [...html.matchAll(/>([^<>]+)</g)]
    .map(match => match[1].replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&').replace(/\s+/g, ' ').trim())
    .filter(text => /[\u3400-\u9fff]/.test(text) && text !== '中文');
  for (const text of new Set(textNodes)) {
    assert(dict[text], 'Missing visible English translation: ' + text);
  }
});

test('context-specific close labels do not collide in English', () => {
  const h = harness('en');
  const dict = h.run('I18N');
  assert.equal(dict['关闭'], 'Close');
  assert.equal(dict['停用'], 'Disabled');
  assert.match(template, /<option value="false">停用<\/option>/);
  assert.doesNotMatch(i18n, /'关闭':\s*'Off'/);
});

test('admin page declares a self-contained favicon', () => {
  assert.match(template, /<link rel="icon" type="image\/svg\+xml" href="data:image\/svg\+xml,/);
});

test('admin project links point to the maintained repository', () => {
  const projectLinks = [...template.matchAll(/openExternal\('(https:\/\/github\.com\/[^']+)'\)/g)].map(match => match[1]);
  assert.ok(projectLinks.length >= 5, 'Expected project, feedback, and release links');
  for (const link of projectLinks) assert.match(link, /^https:\/\/github\.com\/zcgg2001\/cline2api(?:\/|$)/);
  assert.doesNotMatch(template, /github\.com\/luawei1\/cline2api/);
});

test('update warnings and version titles switch language without another check', () => {
  const h = harness();
  h.context.info = {currentVersion: '1.2.0', latestVersion: '1.3.0', hasUpdate: true, supported: false, warning: 'GitHub 检查限流，请稍后重试'};
  h.run('renderUpdateInfo(info, true)');
  h.switch('en');
  assert.equal(h.element('updateTitle').textContent, 'New version v1.3.0 available');
  assert.equal(h.element('updateCheckButton').textContent, 'New version v1.3.0');
  assert.equal(h.element('updateWarning').textContent, 'GitHub rate limited the check; try again later.');
  h.switch('zh');
  assert.equal(h.element('updateTitle').textContent, '新版本 v1.3.0 可用');
  assert.equal(h.element('updateWarning').textContent, h.context.info.warning);
});

test('an unavailable release never reports that this build is up to date', () => {
  const h = harness('en');
  h.context.info = {currentVersion: '1.2.0', checkStatus: 'error', hasUpdate: false, supported: false, warning: 'GitHub 检查限流，请稍后重试'};
  h.run('renderUpdateInfo(info, true)');
  assert.match(h.element('updateStatus').textContent, /Unable to determine/);
  assert.equal(h.element('applyUpdateButton').style.display, 'none');
  h.switch('zh');
  assert.equal(h.element('updateStatus').textContent, '无法确认最新版本，请稍后重试。');
});

test('pending and failed update checks remain localized and release the disabled button', async () => {
  const h = harness('zh');
  let reject;
  h.context.api = () => new Promise((_, fail) => { reject = fail; });
  const pending = h.run('checkForUpdates(true)');
  h.switch('en');
  assert.equal(h.element('updateCheckButton').disabled, true);
  assert.equal(h.element('updateCheckButton').textContent, 'Checking…');
  assert.equal(h.element('updateStatus').textContent, 'Checking GitHub Releases…');
  reject(new Error('GitHub 检查限流，请稍后重试'));
  await pending;
  assert.equal(h.element('updateCheckButton').disabled, false);
  assert.equal(h.element('updateCheckButton').textContent, 'Check for updates');
  assert.match(h.element('updateStatus').textContent, /^Check failed: GitHub rate limited/);
  h.switch('zh');
  assert.equal(h.element('updateStatus').textContent, '检查失败：GitHub 检查限流，请稍后重试');
});

test('changing language while installing cannot re-enable the install button', () => {
  const h = harness();
  h.context.info = {currentVersion:'1.2.0', latestVersion:'1.3.0', hasUpdate:true, supported:true};
  h.run('renderUpdateInfo(info, true)');
  h.context.confirm = () => true;
  h.context.api = () => new Promise(() => {});
  h.run('applyOnlineUpdate()');
  h.switch('en');
  assert.equal(h.element('applyUpdateButton').disabled, true);
  assert.equal(h.element('applyUpdateButton').textContent, 'Updating…');
  assert.match(h.element('updateStatus').textContent, /^Downloading/);
});
