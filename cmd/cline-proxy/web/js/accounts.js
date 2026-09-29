const accountTranslations = {
  '查看可用状态，管理分组与账号用量':'Monitor availability, groups and account usage',
  '刷新列表':'Refresh list','搜索账号':'Search accounts','搜索邮箱或账号 ID':'Search email or account ID',
  '全部分组':'All groups','全部状态':'All statuses','可参与调度':'Available for routing','需要处理':'Needs attention',
  '账号冷却':'Account cooldown','模型冷却':'Model cooldown','凭据过期':'Credentials expired','排序方式':'Sort by',
  '最近添加':'Recently added','最近使用':'Recently used','Token 用量':'Token usage','请求次数':'Requests','邮箱名称':'Email',
  '重置筛选':'Reset filters','用量为累计统计':'Lifetime usage','显示方式':'Display mode','卡片':'Cards','列表':'Table',
  '选择本页':'Select page','选择全部筛选结果':'Select all matches','清除选择':'Clear selection','批量操作':'Bulk action',
  '测试连接':'Test connection','刷新凭据':'Refresh credentials','设为 Free':'Set Free','设为 Pass':'Set Pass','设为待确认':'Mark unconfirmed',
  '导出所选':'Export selected','删除所选':'Delete selected','执行':'Apply','停止后续任务':'Stop pending tasks',
  '查看逐项结果':'View individual results','每页':'Per page','上一页':'Previous','下一页':'Next',
  '待确认账号不参与调度；模型冷却仅影响对应模型。批量操作最多选择 1000 个账号。':'Unconfirmed accounts are excluded from routing. Model cooldowns affect only those models. Select up to 1,000 accounts per operation.',
  '账号详情':'Account details','关闭详情':'Close details','全部账号':'All accounts','账号池总量':'Accounts in the pool','选择账号':'Select account',
  '已确认订阅且状态活跃':'Confirmed subscription and active status','过期、冷却或订阅待确认':'Expired, cooling or unconfirmed',
  '确认后才能参与调度':'Confirm before routing','已选择':'Selected','个账号':'accounts','已更新':'Updated',
  '没有符合条件的账号':'No matching accounts','试试调整搜索关键词或筛选条件。':'Try a different search or filter.',
  '还没有账号':'No accounts yet','添加 Cline 账号后，即可查看状态与用量。':'Add a Cline account to view its status and usage.',
  '请联系管理员添加账号。':'Ask an administrator to add accounts.','详情':'Details','模型受限':'models cooling',
  '输入 / 输出':'Input / output','累计请求':'Lifetime requests','累计 Token':'Lifetime tokens','缓存 Token':'Cached tokens',
  '尚未使用':'Never used','订阅待确认':'Subscription unconfirmed','恢复时间':'Recovery time','等待恢复检查':'Awaiting recovery check',
  '账号 ID':'Account ID','账号状态':'Account status','创建时间':'Created','最后使用':'Last used','按模型用量与冷却':'Usage and cooldowns by model',
  '暂无模型用量记录':'No model usage recorded','变更订阅':'Change subscription','删除账号':'Delete account',
  '测试会向上游发送一条短请求，可能消耗少量额度。继续？':'Testing sends a short upstream request and may consume quota. Continue?',
  '刷新凭据会连接上游，保留历史统计。继续？':'Refresh credentials through the upstream, preserving usage history. Continue?',
  '确认变更这些账号的订阅分组？':'Change the subscription group for these accounts?',
  '将永久删除所选账号，无法撤销。继续？':'Permanently delete these accounts? This cannot be undone.',
  '导出文件包含账号凭据，请妥善保存。继续？':'The exported file contains credentials. Keep it private. Continue?',
  '正在执行':'Running','操作完成':'Operation complete','已停止':'Stopped','成功':'Succeeded','失败':'Failed','未执行':'Not run',
  '当前任务结束后停止':'Stop after the current request','没有选择账号':'No accounts selected','最多选择 1000 个账号':'Select up to 1,000 accounts',
  '测试通过':'Connection successful','凭据已刷新':'Credentials refreshed','未返回测试结果':'No test result returned',
  '账号列表加载失败，已保留上次结果。':'Could not refresh accounts. Previous results are retained.',
  '导出完成':'Export complete','删除完成':'Deletion complete','订阅已更新':'Subscription updated','个结果':'matches',
  '筛选变化已清除勾选':'Selection cleared after filters changed','已选账号包含其他分页':'Selection includes other pages',
  '详情中的凭据均已隐藏':'Credentials are hidden in this view','关闭':'Close',
};
Object.assign(I18N, accountTranslations);
Object.entries(accountTranslations).forEach(([zh, en]) => { I18N_REV[en] = zh; });

const accountView = {
  accounts: [], selected: new Set(), page: 1, size: 12, view: 'cards', loading: false,
  loaded: false, busy: false, cancelled: false, results: new Map(), detailID: null,
};
try {
  const pref = JSON.parse(localStorage.getItem('cline_account_view') || '{}');
  if (['cards', 'table'].includes(pref.view)) accountView.view = pref.view;
  if ([12, 24, 48].includes(pref.size)) accountView.size = pref.size;
} catch (_) {}

function accountAttr(value) { return esc(String(value == null ? '' : value)).replace(/"/g, '&quot;').replace(/'/g, '&#39;'); }
function accountDate(value) { return AccountListModel.timestamp(value) ? new Date(value).toLocaleString(LC()) : t('尚未使用'); }
function subscriptionLabel(value) { return value === 'free' ? 'Free' : value === 'pass' ? 'Pass' : t('待确认'); }
function accountOptions() { return { search:_('acSearch').value, group:_('accountGroupFilter').value, status:_('acStatus').value, sort:_('acSort').value }; }
function accountFiltered() { return AccountListModel.filter(accountView.accounts, accountOptions()); }
function accountPage() { return AccountListModel.page(accountFiltered(), accountView.page, accountView.size); }
function accountName(a) { return a.email || a.accountId; }
function accountActionButton(action, id, label, extra = '') {
  return '<button type="button" class="btn btn-sm '+extra+'" data-ac-action="'+action+'" data-ac-id="'+accountAttr(id)+'"'+(accountView.busy && action !== 'detail' ? ' disabled' : '')+'>'+esc(t(label))+'</button>';
}
function accountBadge(a) {
  let kind = a.status === 'active' ? 'ready' : a.status === 'cooldown' ? 'cooldown' : 'expired';
  let label = a.status === 'active' ? '活跃' : a.status === 'cooldown' ? '账号冷却' : '凭据过期';
  if (a.status === 'active' && !AccountListModel.ready(a)) { kind = 'attention'; label = '订阅待确认'; }
  const cooling = AccountListModel.cooldowns(a).length;
  return '<span class="ac-badge '+kind+'">'+esc(t(label))+'</span>' +
    (cooling ? '<span class="ac-badge cooldown">'+cooling+' '+esc(t('模型受限'))+'</span>' : '');
}
function accountGroupBadge(a) {
  const group = ['free','pass'].includes(a.subscription) ? a.subscription : 'unknown';
  return '<span class="ac-badge '+group+'">'+esc(subscriptionLabel(group))+'</span>';
}
function accountCheckbox(a) {
  if (!isAdmin()) return '';
  return '<input type="checkbox" data-ac-select="'+accountAttr(a.accountId)+'" aria-label="'+accountAttr(t('选择账号')+' '+accountName(a))+'"'+(accountView.selected.has(a.accountId)?' checked':'')+(accountView.busy?' disabled':'')+'>';
}
function accountIdentity(a) {
  return '<div class="ac-identity"><button class="ac-name" data-ac-action="detail" data-ac-id="'+accountAttr(a.accountId)+'">'+esc(accountName(a))+'</button><div class="ac-id" title="'+accountAttr(a.accountId)+'">'+esc(a.accountId)+'</div></div>';
}
function accountResult(a) {
  const r = accountView.results.get(a.accountId);
  return r ? '<div class="ac-card-result'+(r.ok?'':' failed')+'">'+esc(r.message)+'</div>' : '';
}
function accountCard(a) {
  const lastUsed = AccountListModel.timestamp(a.lastUsed) ? new Date(a.lastUsed).toLocaleDateString(LC()) : t('尚未使用');
  return '<article class="ac-card" data-selected="'+accountView.selected.has(a.accountId)+'">'+
    '<div class="ac-card-head"><div class="ac-avatar">'+esc(accountName(a).charAt(0).toUpperCase())+'</div>'+accountIdentity(a)+accountCheckbox(a)+'</div>'+
    '<div class="ac-card-state">'+accountGroupBadge(a)+accountBadge(a)+'</div>'+
    '<div class="ac-card-metrics"><div><span>'+t('累计请求')+'</span><strong>'+formatNumber(a.usageCount)+'</strong></div><div><span>'+t('累计 Token')+'</span><strong>'+formatTokenCount(a.totalTokens)+'</strong></div><div><span>'+t('缓存 Token')+'</span><strong>'+formatTokenCount(a.cachedTokens)+'</strong></div></div>'+accountBillingCard(a.accountId)+accountResult(a)+
    '<div class="ac-card-footer"><span class="ac-muted" title="'+accountAttr(accountDate(a.lastUsed))+'">'+t('最后使用')+' · '+esc(lastUsed)+'</span><div class="ac-actions">'+(isAdmin()?accountActionButton('test',a.accountId,'测试连接'):'')+accountActionButton('detail',a.accountId,'详情')+'</div></div></article>';
}
function accountTable(accounts) {
  return '<div class="ac-table-wrap"><table class="ac-table"><thead><tr>'+(isAdmin()?'<th><span class="ac-sr-only">'+t('选择账号')+'</span></th>':'')+
    ['账号','订阅分组','账号状态','累计请求','累计 Token',...(isAdmin()?['官方额度与费用']:[]),'最后使用','操作'].map(label=>'<th>'+esc(t(label))+'</th>').join('')+'</tr></thead><tbody>'+accounts.map(a=>
    '<tr data-selected="'+accountView.selected.has(a.accountId)+'">'+(isAdmin()?'<td>'+accountCheckbox(a)+'</td>':'')+'<td>'+accountIdentity(a)+'</td><td>'+accountGroupBadge(a)+'</td><td>'+accountBadge(a)+'</td><td class="ac-numeric">'+formatNumber(a.usageCount)+'</td><td class="ac-numeric">'+formatTokenCount(a.totalTokens)+'</td>'+(isAdmin()?'<td class="ac-billing-cell">'+accountBillingCard(a.accountId)+'</td>':'')+'<td>'+esc(accountDate(a.lastUsed))+'</td><td><div class="ac-actions">'+(isAdmin()?accountActionButton('test',a.accountId,'测试连接'):'')+accountActionButton('detail',a.accountId,'详情')+'</div></td></tr>'
  ).join('')+'</tbody></table></div>';
}
function renderAccountSummary() {
  const all = accountView.accounts;
  const options = accountOptions();
  for (const option of _('accountGroupFilter').options) {
    const group=option.value;
    option.textContent=group?subscriptionLabel(group)+' ('+all.filter(a=>(a.subscription||'unknown')===group).length+')':t('全部分组');
  }
  const stats = [
    ['all','全部账号',all.length,'账号池总量', !options.status && !options.group],
    ['ready','可参与调度',all.filter(AccountListModel.ready).length,'已确认订阅且状态活跃',options.status==='ready'],
    ['attention','需要处理',all.filter(a=>AccountListModel.attention(a)).length,'过期、冷却或订阅待确认',options.status==='attention'],
    ['unknown','待确认',all.filter(a=>!['free','pass'].includes(a.subscription)).length,'确认后才能参与调度',options.group==='unknown'],
  ];
  _('acSummary').innerHTML = stats.map(([key,label,count,note,active])=>'<button data-summary="'+key+'" aria-pressed="'+active+'"><span class="ac-summary-label">'+esc(t(label))+'</span><strong class="ac-summary-value">'+formatNumber(count)+'</strong><span class="ac-summary-note">'+esc(t(note))+'</span></button>').join('');
}
function renderAccountSelection() {
  const page = accountPage();
  const selected = accountView.selected.size;
  _('acSelectedCount').textContent = t('已选择')+' '+selected+' '+t('个账号')+(selected>page.items.filter(a=>accountView.selected.has(a.accountId)).length?' · '+t('已选账号包含其他分页'):'');
  const checkbox = _('acSelectPage');
  const count = page.items.filter(a=>accountView.selected.has(a.accountId)).length;
  checkbox.checked = page.items.length > 0 && count === page.items.length;
  checkbox.indeterminate = count > 0 && count < page.items.length;
  checkbox.disabled = accountView.busy || !page.items.length;
  _('acRunBulk').disabled = accountView.busy || !selected;
  _('acBulkAction').disabled = accountView.busy;
  _('acSelectFiltered').disabled = accountView.busy || !accountFiltered().length;
  _('acClearSelection').disabled = accountView.busy || !selected;
  document.querySelectorAll('[data-ac-select]').forEach(el=>{ el.checked = accountView.selected.has(el.dataset.acSelect); el.disabled = accountView.busy; el.closest('[data-selected]').dataset.selected = String(el.checked); });
}
function renderAccounts() {
  const filtered = accountFiltered();
  const page = AccountListModel.page(filtered, accountView.page, accountView.size);
  accountView.page = page.current;
  renderAccountSummary();
  _('acCount').textContent = filtered.length+' / '+accountView.accounts.length+' '+t('个账号');
  _('acViewCards').setAttribute('aria-pressed',String(accountView.view==='cards'));
  _('acViewTable').setAttribute('aria-pressed',String(accountView.view==='table'));
  _('acPageSize').value = String(accountView.size);
  if (!page.items.length) {
    const empty = accountView.accounts.length === 0;
    _('acContent').innerHTML = '<div class="ac-empty"><strong>'+t(empty?'还没有账号':'没有符合条件的账号')+'</strong><p>'+t(empty?(isAdmin()?'添加 Cline 账号后，即可查看状态与用量。':'请联系管理员添加账号。'):'试试调整搜索关键词或筛选条件。')+'</p>'+(!empty?'<button class="btn" data-ac-action="clear">'+t('重置筛选')+'</button>':isAdmin()?'<button class="btn btn-primary" data-ac-action="import">'+t('添加账号')+'</button>':'')+'</div>';
  } else {
    _('acContent').innerHTML = accountView.view === 'cards' ? '<div class="ac-grid">'+page.items.map(accountCard).join('')+'</div>' : accountTable(page.items);
  }
  _('acRange').textContent = (filtered.length?(page.current-1)*page.size+1:0)+'–'+Math.min(page.current*page.size,filtered.length)+' / '+filtered.length;
  _('acPageLabel').textContent = page.current+' / '+page.pages;
  _('acPrev').disabled = page.current<=1;
  _('acNext').disabled = page.current>=page.pages;
  renderAccountSelection();
  loadVisibleBilling();
}
async function loadAccounts() {
  if (accountView.loading || !currentUser || currentUser.mustChangePassword) return;
  accountView.loading = true;
  _('acContent').setAttribute('aria-busy','true');
  _('acReload').disabled = true;
  try {
    const result = await api('GET','/accounts');
    accountView.accounts = result.data.accounts || [];
    accountView.selected = AccountListModel.reconcileSelection(accountView.selected,accountView.accounts);
    accountView.loaded = true;
    _('acLoadError').hidden = true;
    _('acUpdated').textContent = t('已更新')+' '+new Date().toLocaleTimeString(LC(),{hour:'2-digit',minute:'2-digit'});
    renderAccounts();
    if (_('acDetail').open) renderAccountDetail();
  } catch (e) {
    _('acLoadError').textContent = t('账号列表加载失败，已保留上次结果。')+' '+e.message;
    _('acLoadError').hidden = false;
    if (!accountView.loaded) { _('acContent').innerHTML = ''; }
  } finally {
    accountView.loading = false;
    _('acContent').setAttribute('aria-busy','false');
    _('acReload').disabled = false;
  }
}
function resetAccountFilters() {
  _('acSearch').value = ''; _('accountGroupFilter').value = ''; _('acStatus').value = ''; _('acSort').value = 'recent';
  accountFiltersChanged();
}
function accountFiltersChanged() {
  if (accountView.selected.size) toast(t('筛选变化已清除勾选'),'info');
  accountView.selected.clear(); accountView.page = 1; renderAccounts();
}
function selectAccountIDs(ids, selected) {
  if (!isAdmin() || accountView.busy) return;
  const next = new Set(accountView.selected);
  ids.forEach(id=>selected?next.add(id):next.delete(id));
  if (next.size>1000) { toast(t('最多选择 1000 个账号'),'error'); return; }
  accountView.selected = next;
  renderAccountSelection();
}
function accountShowDetail(id) {
  accountView.detailID = id;
  renderAccountDetail();
  if (!_('acDetail').open) _('acDetail').showModal();
  loadAccountBilling(id);
}
function renderAccountDetail() {
  const a = accountView.accounts.find(a=>a.accountId===accountView.detailID);
  if (!a) { _('acDetail').close(); return; }
  _('acDetailTitle').textContent = accountName(a);
  const cooled = new Map(AccountListModel.cooldowns(a));
  const models = [...new Set([...Object.keys(a.modelStats||{}),...cooled.keys()])].sort();
  const fields = [['账号 ID',a.accountId],['创建时间',accountDate(a.createdAt)],['最后使用',accountDate(a.lastUsed)],['输入 / 输出',formatTokenCount(a.promptTokens)+' / '+formatTokenCount(a.completionTokens)]];
  if (a.status==='cooldown') fields.push(['恢复时间',AccountListModel.timestamp(a.cooldownUntil)>Date.now()?accountDate(a.cooldownUntil):t('等待恢复检查')]);
  _('acDetailBody').innerHTML = '<section><div class="ac-actions">'+accountGroupBadge(a)+accountBadge(a)+'</div><p class="ac-footnote">'+t('详情中的凭据均已隐藏')+'</p><dl>'+fields.map(([key,val])=>'<dt>'+esc(t(key))+'</dt><dd>'+esc(val)+'</dd>').join('')+'</dl></section>'+
    '<section><div class="ac-card-metrics"><div><span>'+t('累计请求')+'</span><strong>'+formatNumber(a.usageCount)+'</strong></div><div><span>'+t('累计 Token')+'</span><strong>'+formatTokenCount(a.totalTokens)+'</strong></div><div><span>'+t('缓存 Token')+'</span><strong>'+formatTokenCount(a.cachedTokens)+'</strong></div></div></section>'+
    (isAdmin()?'<section class="ac-billing-detail" data-billing-id="'+accountAttr(a.accountId)+'" data-billing-detail="true">'+accountBillingHTML(a.accountId,true)+'</section>':'')+
    (isAdmin()?'<section><h3>'+t('变更订阅')+'</h3><div class="ac-actions"><select id="acDetailGroup" aria-label="'+t('订阅分组')+'"'+(accountView.busy?' disabled':'')+'>'+['unknown','free','pass'].map(g=>'<option value="'+g+'"'+((a.subscription||'unknown')===g?' selected':'')+'>'+subscriptionLabel(g)+'</option>').join('')+'</select>'+accountActionButton('group',a.accountId,'保存')+'</div><div class="ac-actions">'+accountActionButton('test',a.accountId,'测试连接')+accountActionButton('refresh',a.accountId,'刷新凭据')+accountActionButton('export',a.accountId,'导出')+accountActionButton('delete',a.accountId,'删除账号','btn-danger')+'</div></section>':'')+
    '<section><h3>'+t('按模型用量与冷却')+'</h3>'+(models.length?models.map(id=>{const m=(a.modelStats||{})[id]||{};return '<div class="ac-detail-model"><strong>'+esc(id)+'</strong><p>'+formatNumber(m.usageCount)+' req · '+formatTokenCount(m.totalTokens)+' tok</p><p>'+t('输入 / 输出')+' '+formatTokenCount(m.promptTokens)+' / '+formatTokenCount(m.completionTokens)+' · '+t('缓存 Token')+' '+formatTokenCount(m.cachedTokens)+'</p>'+(cooled.has(id)?'<span class="ac-badge cooldown">'+t('恢复时间')+' '+esc(accountDate(cooled.get(id)))+'</span>':'')+'</div>';}).join(''):'<p class="ac-muted">'+t('暂无模型用量记录')+'</p>')+'</section>'+accountResult(a);
}
function operationLabel(action) { return {test:'测试连接',refresh:'刷新凭据',delete:'删除所选',export:'导出所选',free:'设为 Free',pass:'设为 Pass',unknown:'设为待确认'}[action]; }
async function runAccountOperation(action, requestedIDs) {
  if (!isAdmin() || accountView.busy || !operationLabel(action)) return;
  const existing = new Map(accountView.accounts.map(a=>[a.accountId,a]));
  const ids = [...new Set(requestedIDs)].filter(id=>existing.has(id));
  if (!ids.length) { toast(t('没有选择账号'),'error'); return; }
  if (ids.length>1000) { toast(t('最多选择 1000 个账号'),'error'); return; }
  const confirmations = {test:'测试会向上游发送一条短请求，可能消耗少量额度。继续？',refresh:'刷新凭据会连接上游，保留历史统计。继续？',delete:'将永久删除所选账号，无法撤销。继续？',export:'导出文件包含账号凭据，请妥善保存。继续？'};
  const sample = ids.slice(0,3).map(id=>accountName(existing.get(id))).join('\n');
  if (!confirm(t(operationLabel(action))+' · '+ids.length+' '+t('个账号')+'\n'+sample+(ids.length>3?'\n…':'')+'\n\n'+t(confirmations[action]||'确认变更这些账号的订阅分组？'))) return;
  accountView.busy = true; accountView.cancelled = false;
  renderAccounts();
  if (_('acDetail').open) renderAccountDetail();
  const box = _('acOperation'); box.hidden = false;
  const cancel = _('acCancelOperation'); cancel.hidden = !['test','refresh'].includes(action); cancel.disabled = false; cancel.textContent = t('停止后续任务');
  _('acOperationTitle').textContent = t(operationLabel(action))+' · '+t('正在执行');
  _('acOperationResults').replaceChildren();
  _('acProgress').value = 0; _('acProgress').max = ids.length;
  let success = 0, failed = 0, done = 0;
  const progress = () => { _('acProgress').value = done; _('acOperationSummary').textContent = done+' / '+ids.length+' · '+t('成功')+' '+success+' · '+t('失败')+' '+failed+' · '+t('未执行')+' '+(ids.length-done); };
  progress();
  const record = (id,ok,message) => {
    const result = {ok,message}; accountView.results.set(id,result); done++; if(ok)success++;else failed++;
    const row = document.createElement('div'); row.className='ac-result'+(ok?'':' failed');
    const name = document.createElement('span'); name.textContent=accountName(existing.get(id));
    const detail = document.createElement('span'); detail.textContent=message;
    row.append(name,detail); _('acOperationResults').append(row); progress();
  };
  try {
    if (action==='test'||action==='refresh') {
      for (const id of ids) {
        if (accountView.cancelled || !isAdmin()) break;
        try {
          const result = await api('POST',action==='test'?'/accounts/test':'/accounts/reset',{accountId:id});
          if (action==='test') {
            const test = (result.data.results||[])[0];
            record(id,!!(test&&test.ok),test&&test.ok?t('测试通过')+' · '+formatDuration(test.durationMs):test?test.error:t('未返回测试结果'));
          } else record(id,true,t('凭据已刷新'));
        } catch(e) { record(id,false,e.message); }
      }
    } else if (action==='export') {
      const response = await fetch(API+'/accounts/export',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({accountIds:ids})});
      if(!response.ok){if(response.status===401)showLogin();const error=await response.json();throw new Error(error.error||String(response.status));}
      const blob = await response.blob(); const url=URL.createObjectURL(blob); const link=document.createElement('a');
      link.href=url;link.download='cline-accounts-selected.json';document.body.append(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);
      ids.forEach(id=>record(id,true,t('导出完成')));
    } else if (action==='delete') {
      await api('POST','/accounts/batch-delete',{accountIds:ids});
      ids.forEach(id=>{accountView.selected.delete(id);record(id,true,t('删除完成'));});
      if(ids.includes(accountView.detailID)) _('acDetail').close();
    } else if (['free','pass','unknown'].includes(action)) {
      await api('POST','/accounts/subscription',{accountIds:ids,subscription:action});
      ids.forEach(id=>record(id,true,t('订阅已更新')));
    }
  } catch(e) { ids.forEach(id=>record(id,false,e.message)); }
  finally {
    accountView.busy = false; cancel.hidden=true;
    _('acOperationTitle').textContent = t(operationLabel(action))+' · '+t(accountView.cancelled?'已停止':'操作完成');
    await loadAccounts(); loadStats(); renderAccounts(); if(_('acDetail').open)renderAccountDetail();
  }
}

// Keep dashboard/settings entry points; all operations use explicit snapshots.
async function testAllAccounts() { await loadAccounts(); switchTab('accounts'); await runAccountOperation('test',accountView.accounts.map(a=>a.accountId)); }
async function refreshAllTokens() { await loadAccounts(); switchTab('accounts'); await runAccountOperation('refresh',accountView.accounts.map(a=>a.accountId)); }
async function deleteAllAccounts() { await loadAccounts(); switchTab('accounts'); await runAccountOperation('delete',accountView.accounts.map(a=>a.accountId)); }

_('acSearch').addEventListener('input',accountFiltersChanged);
['accountGroupFilter','acStatus','acSort'].forEach(id=>_(id).addEventListener('change',accountFiltersChanged));
_('acClearFilters').addEventListener('click',resetAccountFilters);
_('acSummary').addEventListener('click',e=>{
  const b=e.target.closest('[data-summary]');if(!b)return;
  _('acStatus').value=['ready','attention'].includes(b.dataset.summary)?b.dataset.summary:'';
  _('accountGroupFilter').value=b.dataset.summary==='unknown'?'unknown':'';
  _('acSearch').value='';accountFiltersChanged();
});
function saveAccountViewPreference(){try{localStorage.setItem('cline_account_view',JSON.stringify({view:accountView.view,size:accountView.size}));}catch(_){} }
['cards','table'].forEach(view=>_('acView'+(view==='cards'?'Cards':'Table')).addEventListener('click',()=>{accountView.view=view;saveAccountViewPreference();renderAccounts();}));
_('acPageSize').addEventListener('change',e=>{accountView.size=Number(e.target.value);accountView.page=1;saveAccountViewPreference();renderAccounts();});
_('acPrev').addEventListener('click',()=>{accountView.page--;renderAccounts();});
_('acNext').addEventListener('click',()=>{accountView.page++;renderAccounts();});
_('acSelectPage').addEventListener('change',e=>selectAccountIDs(accountPage().items.map(a=>a.accountId),e.target.checked));
_('acSelectFiltered').addEventListener('click',()=>selectAccountIDs(accountFiltered().map(a=>a.accountId),true));
_('acClearSelection').addEventListener('click',()=>{if(!accountView.busy){accountView.selected.clear();renderAccountSelection();}});
_('acRunBulk').addEventListener('click',()=>runAccountOperation(_('acBulkAction').value,[...accountView.selected]));
_('acCancelOperation').addEventListener('click',()=>{accountView.cancelled=true;_('acCancelOperation').disabled=true;_('acCancelOperation').textContent=t('当前任务结束后停止');});
_('acCloseDetail').addEventListener('click',()=>_('acDetail').close());
_('acDetail').addEventListener('click',e=>{if(e.target===_('acDetail') && e.clientX<_('acDetail').getBoundingClientRect().left)_('acDetail').close();});
_('acContent').addEventListener('change',e=>{if(e.target.matches('[data-ac-select]'))selectAccountIDs([e.target.dataset.acSelect],e.target.checked);});
function handleAccountAction(e) {
  const button=e.target.closest('[data-ac-action]'); if(!button)return;
  const {acAction:action,acId:id}=button.dataset;
  if(action==='detail')accountShowDetail(id);
  else if(action==='billing')loadAccountBilling(id,true);
  else if(action==='clear')resetAccountFilters();
  else if(action==='import')switchTab('import');
  else if(action==='group')runAccountOperation(_('acDetailGroup').value,[id]);
  else runAccountOperation(action,[id]);
}
_('acContent').addEventListener('click',handleAccountAction);
_('acDetailBody').addEventListener('click',handleAccountAction);
