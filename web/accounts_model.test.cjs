const test = require('node:test');
const assert = require('node:assert/strict');
const model = require('./accounts_model.js');
const now = Date.parse('2026-09-19T00:00:00Z');
const accounts = [
  {accountId:'a',email:'ALICE@example.test',subscription:'free',status:'active',createdAt:'2026-09-01',totalTokens:100},
  {accountId:'b',email:'bob@example.test',subscription:'pass',status:'active',createdAt:'2026-09-02',totalTokens:300,modelCooldowns:{glm:'2026-09-20',old:'2020-01-01'}},
  {accountId:'c',email:'carol@example.test',subscription:'unknown',status:'active',totalTokens:200},
  {accountId:'d',email:'d@example.test',subscription:'free',status:'expired',lastUsed:'0001-01-01T00:00:00Z'},
];
test('search, group and status filters combine without changing source order',()=>{
  assert.deepEqual(model.filter(accounts,{search:'  alice ',group:'free',status:'ready'},now).map(a=>a.accountId),['a']);
  assert.equal(model.filter(accounts,{search:'alice',group:'pass'},now).length,0);
  assert.deepEqual(model.filter(accounts,{sort:'tokens'},now).map(a=>a.accountId),['b','c','a','d']);
  assert.deepEqual(accounts.map(a=>a.accountId),['a','b','c','d']);
});
test('unknown subscriptions never show as available; model cooldown does not disable other models',()=>{
  assert.deepEqual(model.filter(accounts,{status:'ready'},now).map(a=>a.accountId),['b','a']);
  assert.deepEqual(model.filter(accounts,{status:'model-cooldown'},now).map(a=>a.accountId),['b']);
  assert.deepEqual(model.cooldowns(accounts[1],now),[['glm','2026-09-20']]);
  assert.equal(model.attention(accounts[2],now),true);
  assert.equal(model.timestamp(accounts[3].lastUsed),0);
});
test('pagination clamps after removals and handles empty results',()=>{
  const many=Array.from({length:25},(_,i)=>({accountId:String(i)}));
  assert.equal(model.page(many,3,12).items[0].accountId,'24');
  assert.equal(model.page(many.slice(0,12),3,12).current,1);
  assert.deepEqual(model.page([],99,12),{current:1,pages:1,items:[],size:12});
});
test('refresh preserves cross-page selection but removes accounts no longer present',()=>{
  const selected=new Set(['a','c','gone']);
  assert.deepEqual([...model.reconcileSelection(selected,accounts)],['a','c']);
  assert.deepEqual([...selected],['a','c','gone']);
});
test('official quota percentages distinguish zero, missing and exhausted values',()=>{
  assert.deepEqual(model.quotaDisplay(0),{known:true,percent:0,barValue:0,tone:'normal'});
  assert.equal(model.quotaDisplay(null).known,false);
  assert.equal(model.quotaDisplay(undefined).known,false);
  assert.equal(model.quotaDisplay(-1).known,false);
  assert.equal(model.quotaDisplay('0').known,false);
  assert.equal(model.quotaDisplay(75).tone,'warning');
  assert.deepEqual(model.quotaDisplay(112.5),{known:true,percent:112.5,barValue:100,tone:'exhausted'});
});
