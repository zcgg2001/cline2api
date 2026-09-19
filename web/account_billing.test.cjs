const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const model=require('./accounts_model.js');

function renderer(admin=true){
  const ctx=vm.createContext({
    I18N:{},I18N_REV:{},AccountListModel:model,isAdmin:()=>admin,LC:()=> 'en-US',t:s=>s,
    esc:s=>String(s).replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;'),
    accountAttr:s=>String(s).replaceAll('"','&quot;'),accountDate:s=>s,formatTokenCount:n=>String(n||0),
    _:()=>({addEventListener(){}}),console,
  });
  vm.runInContext(fs.readFileSync(require.resolve('./account_billing.js'),'utf8'),ctx);
  return (data,detail=false)=>{ctx.fixture=data;ctx.detail=detail;return vm.runInContext("billingView.records.set('one',{data:fixture,at:Date.now()});accountBillingHTML('one',detail)",ctx);};
}
test('free usage displays actual zero charges separately from missing Pass quota',()=>{
  const html=renderer()({plan:{state:'not_subscribed'},limits:{state:'not_subscribed',items:[]},wallet:{state:'ready',balanceUsd:.5},usage:{state:'ready',chargedUsd:0,items:[{model:'Free <model>',chargedUsd:0}]}} ,true);
  assert(html.includes('未开通 ClinePass'));
  assert(html.includes('$0.50'));
  assert(html.includes('$0.00'));
  assert(!html.includes('0%'));
  assert(html.includes('Free &lt;model&gt;'));
  assert(html.includes('不代表完整月账单'));
});
test('zero and over-limit percentages render while missing values remain unknown',()=>{
  const html=renderer()({limits:{state:'ready',items:[{type:'five_hour',percentUsed:0},{type:'weekly',percentUsed:null},{type:'monthly',percentUsed:112.5}]},wallet:{state:'error',errorCode:'timeout'},usage:{state:'error',chargedUsd:null}});
  assert(html.includes('0% 已用'));
  assert(html.includes('112.5% 已用'));
  assert(html.includes('value="100"'));
  assert(html.includes('ac-quota-unknown'));
  assert(!html.includes('$0.00'));
  assert(html.includes('上游查询超时'));
});
test('stale results are labeled and readers cannot render billing',()=>{
  const data={limits:{state:'error',errorCode:'forbidden'},wallet:{state:'stale',balanceUsd:.5,errorCode:'timeout',updatedAt:'2026-09-19T08:00:00Z'},usage:{items:[]}};
  assert(renderer()(data).includes('获取失败，保留上次数据'));
  assert.equal(renderer(false)(data),'');
});
