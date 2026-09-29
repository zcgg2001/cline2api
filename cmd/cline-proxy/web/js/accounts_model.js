// Shared by the embedded UI and Node's regression tests; no browser dependencies.
const AccountListModel = (() => {
  function timestamp(value) {
    const time = Date.parse(value || '');
    return Number.isFinite(time) && time > 0 ? time : 0;
  }
  function cooldowns(account, now = Date.now()) {
    return Object.entries(account.modelCooldowns || {}).filter(([, until]) => timestamp(until) > now);
  }
  function ready(account) {
    return account.status === 'active' && ['free', 'pass'].includes(account.subscription);
  }
  function attention(account, now = Date.now()) {
    return !ready(account) || cooldowns(account, now).length > 0;
  }
  function filter(accounts, options = {}, now = Date.now()) {
    const query = (options.search || '').trim().toLocaleLowerCase();
    const result = accounts.filter(a => {
      if (query && !(String(a.email || '') + ' ' + String(a.accountId || '')).toLocaleLowerCase().includes(query)) return false;
      if (options.group && (a.subscription || 'unknown') !== options.group) return false;
      switch (options.status) {
        case 'ready': return ready(a);
        case 'attention': return attention(a, now);
        case 'model-cooldown': return cooldowns(a, now).length > 0;
        case 'cooldown': case 'expired': return a.status === options.status;
        default: return true;
      }
    });
    const sort = options.sort || 'recent';
    return result.sort((a, b) => {
      let diff = 0;
      if (sort === 'email') diff = String(a.email || '').localeCompare(String(b.email || ''));
      else if (sort === 'tokens') diff = (Number(b.totalTokens) || 0) - (Number(a.totalTokens) || 0);
      else if (sort === 'requests') diff = (Number(b.usageCount) || 0) - (Number(a.usageCount) || 0);
      else diff = timestamp(b[sort === 'used' ? 'lastUsed' : 'createdAt']) - timestamp(a[sort === 'used' ? 'lastUsed' : 'createdAt']);
      return diff || String(a.accountId).localeCompare(String(b.accountId));
    });
  }
  function page(accounts, requested, size) {
    size = [12, 24, 48].includes(Number(size)) ? Number(size) : 12;
    const pages = Math.max(1, Math.ceil(accounts.length / size));
    const current = Math.max(1, Math.min(pages, Number(requested) || 1));
    return { current, pages, items: accounts.slice((current - 1) * size, current * size), size };
  }
  function reconcileSelection(selected, accounts) {
    const existing = new Set(accounts.map(a => a.accountId));
    return new Set([...selected].filter(id => existing.has(id)));
  }
  function quotaDisplay(value) {
    const known=typeof value==='number'&&Number.isFinite(value)&&value>=0;
    return {known,percent:known?value:null,barValue:known?Math.min(100,value):0,tone:known?(value>=100?'exhausted':value>=75?'warning':'normal'):'unknown'};
  }
  return { timestamp, cooldowns, ready, attention, filter, page, reconcileSelection, quotaDisplay };
})();
if (typeof module !== 'undefined' && module.exports) module.exports = AccountListModel;
