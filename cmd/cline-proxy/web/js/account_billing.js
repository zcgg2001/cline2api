const billingTranslations = {
  '官方额度与费用':'Official quota & billing','同步额度与费用':'Sync quota & billing','同步中':'Syncing',
  '正在读取官方数据':'Fetching official data','尚未同步':'Not synced','查询失败':'Could not fetch data',
  '未开通 ClinePass':'No active ClinePass subscription','5 小时':'5 hours','本周':'Weekly','本月':'Monthly',
  '已用':'used','重置':'Resets','重置时间已到，等待同步':'Reset time reached; sync to update',
  '剩余':'Remaining','未知':'Unknown','个人余额':'Personal balance','最近记录实际扣费':'Recent recorded charges',
  '订阅标价':'Listed subscription price','月':'month','年':'year','订阅周期结束':'Billing period ends',
  '订阅费用与按量扣费分别计算':'Subscription price is separate from usage charges',
  '获取失败，保留上次数据':'Fetch failed; showing previous data','上次成功同步':'Last successful sync',
  '重新认证账号后再同步':'Re-authenticate this account, then sync again',
  '账号无权读取此数据':'This account cannot access this data',
  '查询频率受限，请稍后重试':'Rate limited; try again later',
  '上游查询超时':'Upstream request timed out','上游暂不可用或数据格式变化':'Upstream unavailable or response format changed',
  '凭据刷新失败，请检查账号登录状态':'Credential refresh failed; check account authentication',
  '最近账单':'Recent usage records','实际扣费':'Actual charge','暂无消费记录':'No usage records',
  '笔记录':'records','仅汇总返回的最近记录，不代表完整月账单':'Sum of returned recent records, not the full monthly bill',
  '官方个人账户数据，包含其他客户端产生的消费':'Official personal account data, including usage from other clients',
  '仍有更早记录':'Earlier records are available','钱包余额不代表 ClinePass 剩余额度':'Wallet balance is separate from remaining ClinePass quota',
  '刷新额度':'Refresh quota','美元':'USD','部分记录缺少扣费字段':'Some records lack charge data',
  '此处为上次结果，尚未确认当前订阅状态':'Previous result; current subscription status is not confirmed',
};
Object.assign(I18N,billingTranslations);
Object.entries(billingTranslations).forEach(([zh,en])=>{I18N_REV[en]=zh;});

const billingView = {records:new Map(),pending:new Map(),queue:[],active:0};

function billingMoney(value) {return typeof value==='number'&&Number.isFinite(value)?'$'+value.toLocaleString('en-US',{minimumFractionDigits:2,maximumFractionDigits:6}):t('未知');}
function billingError(code) {
  const messages={unauthorized:'重新认证账号后再同步',forbidden:'账号无权读取此数据',rate_limited:'查询频率受限，请稍后重试',timeout:'上游查询超时',credential_refresh_failed:'凭据刷新失败，请检查账号登录状态'};
  return t(messages[code]||'上游暂不可用或数据格式变化');
}
function billingSectionError(section) {
  if(!section||!section.errorCode)return '';
  return '<p class="ac-billing-error">'+esc(t(section.state==='stale'?'获取失败，保留上次数据':'查询失败'))+' · '+esc(billingError(section.errorCode))+'</p>';
}
function billingReset(value) {
  const until=Date.parse(value||'');
  if(!Number.isFinite(until))return t('未知');
  if(until<=Date.now())return t('重置时间已到，等待同步');
  const minutes=Math.ceil((until-Date.now())/60000);
  const days=Math.floor(minutes/1440),hours=Math.floor((minutes%1440)/60),mins=minutes%60;
  return t('重置')+' '+[days?days+'d':'',hours?hours+'h':'',mins?mins+'m':''].filter(Boolean).join(' ');
}
function billingLimitsHTML(snapshot,detail) {
  const limits=snapshot.limits||{};
  if(limits.state==='not_subscribed')return '<p class="ac-billing-unavailable">'+esc(t('未开通 ClinePass'))+'</p>';
  if(!limits.items?.length)return billingSectionError(limits)||'<p class="ac-muted">'+t('未知')+'</p>';
  const names={five_hour:'5 小时',weekly:'本周',monthly:'本月'};
  return limits.items.map(item=>{
    const state=AccountListModel.quotaDisplay(item.percentUsed);
    const label=names[item.type];if(!label)return '';
    const reset=billingReset(item.resetsAt);
    const percent=state.known?state.percent.toLocaleString(LC(),{maximumFractionDigits:1})+'%':t('未知');
    return '<div class="ac-quota"><div class="ac-quota-label"><span>'+esc(t(label))+'</span><strong>'+esc(percent)+(state.known?' '+t('已用'):'')+'</strong></div>'+
      (state.known?'<progress class="ac-quota-bar '+state.tone+'" value="'+state.barValue+'" max="100" aria-label="'+accountAttr('ClinePass '+t(label)+' '+percent)+'"></progress>':'<div class="ac-quota-unknown"></div>')+
      '<span class="ac-quota-reset" title="'+accountAttr(item.resetsAt?accountDate(item.resetsAt):t('未知'))+'">'+esc(reset)+'</span></div>';
  }).join('')+billingSectionError(limits);
}
function billingPlanHTML(plan) {
  if(!plan)return '';
  if(plan.state==='not_subscribed')return '';
  const interval=plan.interval==='Monthly'?t('月'):plan.interval==='Annual'?t('年'):plan.interval||'';
  return (plan.name?'<p class="ac-billing-plan">'+esc(plan.name)+'</p>':'')+
    (plan.priceUsd!=null?'<p class="ac-muted">'+t('订阅标价')+' '+billingMoney(plan.priceUsd)+(interval?' / '+esc(interval):'')+'</p>':'')+
    (plan.periodEnd?'<p class="ac-muted">'+t('订阅周期结束')+' '+esc(accountDate(plan.periodEnd))+'</p>':'')+billingSectionError(plan);
}
function accountBillingHTML(id,detail=false) {
  if(!isAdmin())return '';
  const record=billingView.records.get(id),snapshot=record?.data,pending=billingView.pending.has(id);
  const refresh='<button class="ac-link" data-ac-action="billing" data-ac-id="'+accountAttr(id)+'"'+(pending?' disabled':'')+'>'+t(pending?'同步中':'刷新额度')+'</button>';
  let content='';
  if(!snapshot)content='<p class="ac-muted">'+t(pending?'正在读取官方数据':record?.error?'查询失败':'尚未同步')+'</p>'+(record?.error?'<p class="ac-billing-error">'+esc(record.error)+'</p>':'');
  else {
    const usage=snapshot.usage||{},wallet=snapshot.wallet||{};
    content=billingLimitsHTML(snapshot,detail)+
      '<div class="ac-billing-money"><div><span>'+t('个人余额')+'</span><strong>'+esc(billingMoney(wallet.balanceUsd))+'</strong></div><div><span>'+t('最近记录实际扣费')+' · '+(usage.items?.length??'—')+'</span><strong>'+esc(billingMoney(usage.chargedUsd))+'</strong></div></div>'+
      billingSectionError(wallet)+billingSectionError(usage)+
      '<p class="ac-billing-note">'+t('钱包余额不代表 ClinePass 剩余额度')+'</p>';
    if(detail) {
      content+=billingPlanHTML(snapshot.plan)+'<p class="ac-billing-note">'+t('订阅费用与按量扣费分别计算')+'</p>'+
        '<div class="ac-billing-ledger"><h3>'+t('最近账单')+' · '+(usage.items?.length??0)+' '+t('笔记录')+'</h3>'+
        '<p class="ac-billing-note">'+t('仅汇总返回的最近记录，不代表完整月账单')+'</p><p class="ac-billing-note">'+t('官方个人账户数据，包含其他客户端产生的消费')+'</p>'+
        ((usage.items||[]).length?(usage.items||[]).map(item=>'<div class="ac-ledger-row"><div><strong>'+esc(item.model||'—')+'</strong><span>'+esc(item.createdAt?accountDate(item.createdAt):t('未知'))+'</span><span>'+formatTokenCount(item.inputTokens)+' / '+formatTokenCount(item.outputTokens)+' tokens</span></div><div><span>'+t('实际扣费')+'</span><strong>'+esc(billingMoney(item.chargedUsd))+'</strong></div></div>').join(''):
          '<p class="ac-muted">'+t(usage.state==='ready'?'暂无消费记录':'查询失败')+'</p>')+
        (usage.hasMore?'<p class="ac-billing-note">'+t('仍有更早记录')+'</p>':'')+'</div>';
    }
    const dates=[snapshot.limits?.updatedAt,wallet.updatedAt,usage.updatedAt].filter(Boolean).map(value=>Date.parse(value)).filter(Number.isFinite);
    if(dates.length)content+='<p class="ac-billing-time">'+t('上次成功同步')+' '+esc(new Date(Math.min(...dates)).toLocaleTimeString(LC()))+'</p>';
    if(record.error)content+='<p class="ac-billing-error">'+t('获取失败，保留上次数据')+' · '+esc(record.error)+'</p>';
  }
  return '<div class="ac-billing-head"><span>'+t('官方额度与费用')+'</span>'+refresh+'</div>'+content;
}
function accountBillingCard(id){return isAdmin()?'<div class="ac-billing" data-billing-id="'+accountAttr(id)+'">'+accountBillingHTML(id)+'</div>':'';}
function paintAccountBilling(id) {
  for(const element of document.querySelectorAll('[data-billing-id]')){
    if(element.dataset.billingId===id)element.innerHTML=accountBillingHTML(id,element.dataset.billingDetail==='true');
  }
}
function loadAccountBilling(id,force=false) {
  if(!isAdmin()||billingView.pending.has(id))return;
  const cached=billingView.records.get(id);
  if(!force&&cached&&Date.now()-cached.at<120000)return;
  billingView.pending.set(id,true);billingView.queue.push({id,force});paintAccountBilling(id);drainBillingQueue();
}
function loadVisibleBilling(force=false) {
  if(!isAdmin()||_('tab-accounts').style.display==='none')return;
  for(const account of accountPage().items)loadAccountBilling(account.accountId,force);
}
async function drainBillingQueue() {
  while(billingView.active<3 && billingView.queue.length){
    const {id,force}=billingView.queue.shift();
    if(!isAdmin()||!accountView.accounts.some(a=>a.accountId===id)){billingView.pending.delete(id);continue;}
    billingView.active++;
    (async()=>{
      try {
        const result=await api('GET','/accounts/billing?accountId='+encodeURIComponent(id)+(force?'&refresh=1':''));
        if(!isAdmin())return;
        billingView.records.set(id,{data:result.data,at:Date.now()});
      }catch(e){
        const previous=billingView.records.get(id);
        billingView.records.set(id,{data:previous?.data,error:e.message,at:Date.now()-90000});
      }finally{billingView.pending.delete(id);billingView.active--;paintAccountBilling(id);drainBillingQueue();}
    })();
  }
}
_('acRefreshBilling').addEventListener('click',()=>loadVisibleBilling(true));
