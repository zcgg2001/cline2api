
(function(){
  const STATUS_LABEL = {
    operational: ['服务可用','Operational'],
    degraded: ['部分受限','Degraded'],
    unavailable: ['检测失败','Unavailable'],
    unknown: ['尚未检测','Not checked'],
    available: ['协议已支持','Supported'],
    partial: ['协议部分支持','Partial support'],
    unconfirmed: ['待确认','Unconfirmed']
  };
  const PROTOCOL_LABEL = {
    chat: 'OpenAI Chat',
    messages: 'Anthropic Messages',
    responses: 'OpenAI Responses'
  };
  let availabilityDays = 7;
  let availabilityData = null;

  function labelOf(map, key){
    const pair = map[key] || STATUS_LABEL.unknown;
    return (typeof LANG !== 'undefined' && LANG === 'en') ? pair[1] : pair[0];
  }
  function fmtLatency(ms){
    if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) return '—';
    return ms < 1000 ? Math.round(ms) + ' ms' : (ms/1000).toFixed(2) + ' s';
  }
  function fmtRate(success, total){
    return total ? ((success/total*100).toFixed(1).replace(/\.0$/,'')) + '%' : '—';
  }
  function fmtWhen(value){
    if (!value) return t('暂无记录');
    const d = new Date(value);
    if (!Number.isFinite(d.getTime())) return t('暂无记录');
    return d.toLocaleString((typeof LANG !== 'undefined' && LANG === 'en') ? 'en-US' : 'zh-CN', {month:'short', day:'numeric', hour:'2-digit', minute:'2-digit'});
  }
  function kindOf(model){
    const caps = (model.capabilities||[]).join(' ');
    if (/video/.test(caps) || /video/.test(model.id||'')) return t('视频生成');
    if (/image/.test(caps) || /image/.test(model.id||'')) return t('图像创作');
    return t('文本与推理');
  }
  function summarize(points){
    let success = 0, total = 0, latencySum = 0, latencyN = 0;
    const observed = (points||[]).filter(p => p.status && p.status !== 'unknown');
    for (const p of observed){
      const counted = Number.isInteger(p.total) && p.total > 0;
      const n = counted ? p.total : 1;
      const s = counted ? p.success : (p.status === 'operational' ? 1 : 0);
      total += n;
      success += s;
      if (Number.isFinite(p.latency_ms) && p.latency_ms >= 0){
        latencySum += p.latency_ms * n;
        latencyN += n;
      }
    }
    let status = 'unknown';
    if (total){
      if (success === total && observed.every(p => p.status === 'operational')) status = 'operational';
      else if (success === 0 && observed.every(p => p.status === 'unavailable')) status = 'unavailable';
      else status = 'degraded';
    }
    return {status, success, total, latency_ms: latencyN ? latencySum/latencyN : null};
  }
  function bucketsFor(model, now, days){
    const count = days === 1 ? 24 : 7;
    const edges = [];
    if (days === 1){
      const hour = 60*60*1000;
      const end = Math.floor(now/hour)*hour + hour;
      for (let i=0;i<=count;i++) edges.push(end - (count-i)*hour);
    } else {
      const start = new Date(now);
      start.setHours(0,0,0,0);
      start.setDate(start.getDate()-6);
      for (let i=0;i<=count;i++){
        edges.push(start.getTime());
        start.setDate(start.getDate()+1);
      }
    }
    const from = edges[0];
    const hist = [...(model.history||[])];
    if (!hist.length && model.checked_at && model.status !== 'unknown'){
      hist.push({checked_at: model.checked_at, status: model.status, latency_ms: model.latency_ms, success: model.status==='operational'?1:0, total:1});
    }
    const valid = hist.filter(p => {
      const ts = Date.parse(p.checked_at);
      return Number.isFinite(ts) && ts >= from && ts <= now && ['operational','degraded','unavailable'].includes(p.status);
    }).sort((a,b)=>Date.parse(a.checked_at)-Date.parse(b.checked_at));
    const bins = Array.from({length:count}, ()=>[]);
    for (const p of valid){
      const idx = edges.findIndex((edge,i)=> i>0 && Date.parse(p.checked_at) < edge) - 1;
      if (idx >= 0) bins[idx].push(p);
    }
    return {
      buckets: bins.map((items,i)=>({start:edges[i], end:edges[i+1], ...summarize(items)})),
      summary: summarize(valid)
    };
  }
  function copyText(value){
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(value);
    toast(t('已复制'));
  }
  function tooltipHTML(bucket){
    if (!bucket) return '';
    const when = availabilityDays===7 ? new Date(bucket.start).toLocaleDateString((LANG==='en')?'en-US':'zh-CN',{month:'short',day:'numeric'}) : fmtWhen(bucket.start);
    return `<div class="history-tooltip"><div class="history-tooltip-heading">${esc(when)}<span>${esc(labelOf(STATUS_LABEL,bucket.status))}</span></div><dl><div><dt>${esc(t('检测次数'))}</dt><dd>${bucket.total||0}</dd></div><div><dt>${esc(t('成功率'))}</dt><dd>${esc(fmtRate(bucket.success,bucket.total))}</dd></div><div><dt>${esc(t('平均耗时'))}</dt><dd>${esc(fmtLatency(bucket.latency_ms))}</dd></div></dl></div>`;
  }
  function renderHistory(modelId, pack){
    const bars = pack.buckets.map((b,i)=>`<span class="history-bar" data-state="${esc(b.status)}" data-idx="${i}"></span>`).join('');
    return `<div class="status-history" data-model="${esc(modelId)}"><div class="history-track ${availabilityDays===7?'history-daily':''}" style="--history-count:${pack.buckets.length}">${bars}</div></div>`;
  }
  function bindHistory(root){
    root.querySelectorAll('.status-history').forEach(el => {
      const track = el.querySelector('.history-track');
      if (!track) return;
      const model = (availabilityData.groups||[]).flatMap(g=>g.models||[]).find(m=>m.id===el.dataset.model);
      if (!model) return;
      const pack = bucketsFor(model, Date.now(), availabilityDays);
      const paint = idx => {
        [...track.children].forEach((bar,i)=>bar.classList.toggle('is-selected', i===idx));
        const tip = el.querySelector('.history-tooltip');
        if (tip) tip.remove();
        if (idx == null) return;
        el.insertAdjacentHTML('beforeend', tooltipHTML(pack.buckets[idx]));
      };
      track.addEventListener('pointermove', ev => {
        const rect = track.getBoundingClientRect();
        paint(Math.max(0, Math.min(pack.buckets.length-1, Math.floor((ev.clientX-rect.left)/rect.width*pack.buckets.length))));
      });
      track.addEventListener('pointerleave', () => paint(null));
    });
  }
  function renderModel(model, now){
    const pack = bucketsFor(model, now, availabilityDays);
    return `<article class="model-status-card" data-status="${esc(model.status)}">
      <header class="model-card-heading"><span class="model-kind-icon"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0L6.41 10.54A2 2 0 0 1 4.49 12H2"/></svg></span><span class="health-badge ${esc(model.status)}">${esc(labelOf(STATUS_LABEL, model.status))}</span></header>
      <div class="model-card-identity"><p>${esc(kindOf(model))}</p><h4>${esc(model.name||model.id)}</h4></div>
      <div class="model-card-metrics"><div><span>${esc(t('最近检测耗时'))}</span><strong>${esc(fmtLatency(model.latency_ms))}</strong></div><div><span>${esc(t('检测成功率'))}</span><strong>${esc(fmtRate(pack.summary.success, pack.summary.total))}</strong></div></div>
      <div class="model-history-heading"><span>${esc(t('检测历史'))}</span><span>${pack.summary.total? pack.summary.success+' / '+pack.summary.total+' '+t('检测通过') : t('等待首次记录')}</span></div>
      ${renderHistory(model.id, pack)}
      <p class="model-last-check">${esc(fmtWhen(model.checked_at))}</p>
      <details class="model-api-details"><summary><span>${esc(t('API 模型标识'))}</span></summary><div><code>${esc(model.id)}</code><button class="btn btn-sm" data-copy="${esc(model.id)}">${esc(t('复制'))}</button></div><p>${esc((model.capabilities||[]).join(' · '))}</p></details>
    </article>`;
  }
  function renderAvailability(){
    const root = document.getElementById('availabilityRoot');
    if (!root) return;
    if (!availabilityData){
      root.innerHTML = `<div class="status-empty">${esc(t('正在获取最近检测记录…'))}</div>`;
      return;
    }
    const now = Date.now();
    const groups = availabilityData.groups || [];
    const models = groups.flatMap(g => g.models||[]);
    const passing = models.filter(m => m.status === 'operational').length;
    const latencies = models.map(m => m.latency_ms).filter(v => typeof v === 'number' && v >= 0);
    const checks = models.reduce((n,m)=> n + bucketsFor(m, now, availabilityDays).summary.total, 0);
    const avg = latencies.length ? latencies.reduce((a,b)=>a+b,0)/latencies.length : null;
    root.innerHTML = `
      <div class="status-hero">
        <div>
          <div class="status-kicker"><span class="status-orb"></span> SYSTEM STATUS</div>
          <h2>${esc(t('每一次响应，清晰可见。'))}</h2>
          <p>${esc(t('从对话到创作，查看模型最近实测结果与检测历史。'))}</p>
        </div>
        <div class="status-overview">
          <div><span>${esc(t('最近检测可用'))}</span><strong>${passing}<small> / ${models.length}</small></strong></div>
          <div><span>${esc(t('平均检测耗时'))}</span><strong>${esc(fmtLatency(avg))}</strong></div>
          <div><span>${esc(t('窗口内实测次数'))}</span><strong>${checks || '—'}</strong></div>
        </div>
      </div>
      <div class="status-toolbar">
        <span>${esc(t('最近检测'))} · ${esc(fmtWhen(availabilityData.checked_at))}</span>
        <div class="status-toolbar-actions">
          <div class="history-window-switch" role="group">
            <button type="button" aria-pressed="${availabilityDays===1}" onclick="setAvailabilityDays(1)">${esc(t('24 小时'))}</button>
            <button type="button" aria-pressed="${availabilityDays===7}" onclick="setAvailabilityDays(7)">${esc(t('7 天'))}</button>
          </div>
          <button class="btn btn-sm" onclick="loadAvailability(true)">${esc(t('刷新状态'))}</button>
        </div>
      </div>
      <div class="status-reading-key">
        <div class="history-legend">${['operational','degraded','unavailable','unknown'].map(s=>`<span><i data-state="${s}"></i>${esc(labelOf(STATUS_LABEL,s))}</span>`).join('')}</div>
        <p>${esc(t('刷新仅读取已有记录，不会发起新的模型调用。'))}</p>
      </div>
      ${groups.length ? groups.map(g => `<section class="status-group"><header><div><p class="eyebrow">MODEL COLLECTION</p><h3>${esc(g.name)}</h3></div><span class="health-badge ${esc(g.status)}">${esc(labelOf(STATUS_LABEL,g.status))}</span></header><div class="model-health-list">${(g.models||[]).map(m=>renderModel(m, now)).join('')}</div></section>`).join('') : `<div class="status-empty">${esc(t('尚无模型检测记录，请稍后回来。'))}</div>`}
      <p class="history-disclosure">${esc(t('默认按天查看最近 7 天，可切换 24 小时查看小时记录。灰色表示没有检测数据，成功率只计算真实检测。'))}</p>
      <div class="page-header"><div><div class="large-title" style="font-size:22px">${esc(t('接口能力，一眼了解。'))}</div></div></div>
      <div class="protocol-directory">${(availabilityData.protocols||[]).map(p=>`<article class="protocol-card"><div><h4>${esc(PROTOCOL_LABEL[p.id]||p.id)}</h4><span class="health-badge ${esc(p.status)}">${esc(labelOf(STATUS_LABEL,p.status))}</span></div><code>${esc(p.endpoint)}</code><p>${esc(p.description||'')}</p></article>`).join('')}</div>`;
    bindHistory(root);
    root.querySelectorAll('[data-copy]').forEach(btn => {
      btn.addEventListener('click', () => copyText(btn.getAttribute('data-copy')||''));
    });
  }
  window.setAvailabilityDays = function(days){
    availabilityDays = days === 1 ? 1 : 7;
    renderAvailability();
  };
  window.loadAvailability = async function(force){
    const root = document.getElementById('availabilityRoot');
    if (!root) return;
    if (!availabilityData) root.innerHTML = `<div class="status-loading">${esc(t('正在获取最近检测记录…'))}</div>`;
    try {
      const data = await api('GET', '/availability');
      availabilityData = data.data || data;
      renderAvailability();
    } catch (err) {
      root.innerHTML = `<div class="status-empty">${esc(err.message || t('暂时无法获取检测记录，请稍后重试。'))}</div>`;
    }
  };
  window.renderAvailability = renderAvailability;
})();
