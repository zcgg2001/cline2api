const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const model = require('../js/accounts_model.js');

function harness({admin = true, confirmed = true, error = ''} = {}) {
  const elements = new Map(), calls = [], confirmations = [];
  const accounts = Array.from({length: 13}, (_, i) => ({accountId: String(i), email: 'user' + i, status: 'active', subscription: 'free'}));
  function element(id) {
    if (!elements.has(id)) elements.set(id, {
      value: '', open: false, textContent: '', children: [],
      addEventListener() {}, setAttribute() {}, close() { this.open = false; },
      append(...items) { this.children.push(...items); }, replaceChildren() { this.children = []; },
    });
    return elements.get(id);
  }
  const context = vm.createContext({
    I18N: {}, I18N_REV: {}, AccountListModel: model,
    isAdmin: () => admin, currentUser: {}, _: element,
    localStorage: {getItem() { return null; }},
    document: {createElement: () => element(Symbol()), querySelectorAll: () => []},
    confirm: message => { confirmations.push(message); return confirmed; },
    t: s => s, esc: s => String(s), toast() {}, loadStats() {},
    api: async (method, endpoint, body) => {
      calls.push({method, endpoint, body: body && JSON.parse(JSON.stringify(body))});
      if (method === 'GET') return {data: {accounts}};
      if (error) throw new Error(error);
      for (const a of accounts) if (body.accountIds.includes(a.accountId)) a.disabled = endpoint === '/accounts/disable';
      return {success: true};
    },
  });
  vm.runInContext(fs.readFileSync(require.resolve('../js/accounts.js'), 'utf8'), context);
  context.fixture = accounts;
  // Renderers are tested through the model and translation suites. Exercise the real operation and reload flow here.
  vm.runInContext('accountView.accounts = fixture; renderAccounts = () => {}; renderAccountDetail = () => {};', context);
  return {calls, confirmations, accounts, element, run: code => vm.runInContext(code, context)};
}

test('bulk disable and enable send explicit cross-page selections and report each result', async () => {
  const h = harness();
  h.run("accountView.selected = new Set(['0', '12']);");
  await h.run("runAccountOperation('disable', [...accountView.selected, '0'])");
  assert.deepEqual(h.calls[0], {method: 'POST', endpoint: '/accounts/disable', body: {accountIds: ['0', '12']}});
  assert(h.accounts[0].disabled && h.accounts[12].disabled);
  assert(!h.accounts[1].disabled);
  assert.match(h.confirmations[0], /保留/);
  assert.equal(h.element('acOperationResults').children.length, 2);
  assert.match(h.element('acOperationSummary').textContent, /成功 2.*失败 0/);
  assert.match(h.run('accountBadge(accountView.accounts[0])'), /ac-badge disabled.*已禁用/);
  await h.run("runAccountOperation('enable', [...accountView.selected])");
  assert.equal(h.calls[2].endpoint, '/accounts/enable');
  assert(!h.accounts[0].disabled && !h.accounts[12].disabled);
  assert.equal(h.run('accountView.busy'), false);
});

test('cancelled confirmations and read-only users never mutate accounts', async () => {
  for (const options of [{confirmed: false}, {admin: false}]) {
    const h = harness(options);
    await h.run("runAccountOperation('disable', ['0', '12'])");
    assert.equal(h.calls.length, 0);
    assert(!h.accounts[0].disabled);
  }
});

test('a rejected batch reports failures and releases the busy state without changing accounts', async () => {
  const h = harness({error: 'save failed'});
  await h.run("runAccountOperation('disable', ['0', '12'])");
  assert(!h.accounts[0].disabled && !h.accounts[12].disabled);
  assert.equal(h.element('acOperationResults').children.length, 2);
  assert.match(h.element('acOperationSummary').textContent, /成功 0.*失败 2/);
  assert.equal(h.run('accountView.busy'), false);
  assert.equal(h.calls[1].endpoint, '/accounts');
});
