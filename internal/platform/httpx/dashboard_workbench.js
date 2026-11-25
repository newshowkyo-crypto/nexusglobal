(function () {
  'use strict';
  var statuses = {
    ACTIVE:'已启用', INACTIVE:'已停用', PENDING:'待处理', PROCESSING:'处理中', REQUIRES_ACTION:'待完成验证',
    UNKNOWN_EXTERNAL_STATE:'结果待确认', UNKNOWN:'结果待确认', FAILED:'失败', SUCCEEDED:'成功',
    AUTHORIZED:'已授权', CAPTURED:'已收款', SETTLED:'已结算', PARTIALLY_CAPTURED:'部分收款',
    PARTIALLY_REFUNDED:'部分退款', REFUNDED:'已退款', CHARGED_BACK:'已拒付', CANCELED:'已取消',
    UNCONFIGURED:'待授权', EXPIRED:'已过期', REVOKED:'已撤销', SUSPENDED:'已暂停', SCHEDULED:'待生效',
    Draft:'草稿', BalanceFrozen:'余额已冻结', FxLocked:'汇率已锁定', LPProcessing:'资金服务处理中',
    Confirmed:'已确认', Reconciled:'已对账', Failed:'失败', BalanceReleased:'余额已释放', UnknownExternalState:'结果待确认'
  };
  var dateFormat = new Intl.DateTimeFormat('zh-CN', {
    timeZone:'Asia/Shanghai', month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', hourCycle:'h23'
  });
  var resources = {
    orders: {tab:'payment-orders', title:'支付记录', ref:'外部订单号', states:['UNKNOWN_EXTERNAL_STATE','FAILED','PROCESSING','REQUIRES_ACTION','AUTHORIZED','CAPTURED','PARTIALLY_CAPTURED','SETTLED','PARTIALLY_REFUNDED','REFUNDED','CANCELED','CHARGED_BACK']},
    refunds: {tab:'payment-refunds', title:'退款记录', ref:'退款单号', states:['PENDING','PROCESSING','UNKNOWN','SUCCEEDED','FAILED']},
    settlements: {tab:'payment-settlements', title:'内部结算批次', ref:'业务日期', states:[]},
    payouts: {tab:'payment-payouts', title:'出金申请', ref:'申请编号', states:['Draft','BalanceFrozen','FxLocked','LPProcessing','Confirmed','Reconciled','Failed','BalanceReleased','UnknownExternalState']},
    authorizations: {tab:'merchant-onboarding', title:'站点 / 通道授权记录', ref:'站点代码', states:['ACTIVE','PENDING','UNCONFIGURED','SCHEDULED','EXPIRED','REVOKED']}
  };
  function make(tag, cls, text) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined) node.textContent = text;
    return node;
  }
  function button(label, action, cls) {
    var node = make('button', cls || 'action-btn', label);
    node.type = 'button';
    node.addEventListener('click', action);
    return node;
  }
  function label(status) { return Object.hasOwn(statuses, status) ? statuses[status] : status || '待确认'; }
  function stamp(value) { return value && Number.isFinite(Date.parse(value)) ? dateFormat.format(new Date(value)) : '—'; }
  function money(amount, currency, minor) {
    if (!currency || typeof amount !== 'string' || !/^-?\d+(?:\.\d+)?$/.test(amount)) return '—';
    if (!minor) return amount + ' · ' + currency;
    var digits = new Intl.NumberFormat('en', {style:'currency', currency:currency}).resolvedOptions().maximumFractionDigits;
    var negative = amount.startsWith('-'), value = amount.replace(/^-/, '').padStart(digits + 1, '0');
    var whole = digits ? value.slice(0, -digits) : value;
    return currency + ' ' + (negative ? '−' : '') + whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',') + (digits ? '.' + value.slice(-digits) : '');
  }
  function badge(status) {
    var tone = /^(ACTIVE|CAPTURED|SETTLED|SUCCEEDED|Confirmed|Reconciled)$/.test(status) ? 'ok' :
      /^(FAILED|EXPIRED|REVOKED|CHARGED_BACK|Failed)$/.test(status) ? 'err' : 'warn';
    return make('span', 'badge badge-' + tone, (tone === 'ok' ? '✓ ' : tone === 'err' ? '× ' : '• ') + label(status));
  }
  function json(path, signal) {
    return authFetch(API_BASE + path, {signal:signal}).then(function (response) { return response.json(); });
  }
  function openTab(key) { document.querySelector('.nav [data-tab="' + key + '"]').click(); }
  function panel(title) {
    var section = make('section', 'section work-panel');
    section.appendChild(make('h2', '', title));
    return section;
  }
  function cell(row, value) { var td = make('td'); td.append(value || '—'); row.appendChild(td); }
  function details(resource, record, trigger) {
    var dialog = make('dialog', 'credentials-dialog work-dialog');
    var title = make('h2', '', resources[resource].title + '详情'); title.id = 'work-detail-title';
    dialog.setAttribute('aria-labelledby', title.id);
    var entries = [['商户',record.merchant], [resources[resource].ref,record.reference],
      ['当前状态',label(record.status)], ['创建时间',stamp(record.created_at)]];
    if (record.amount) entries.push(['金额',money(record.amount, record.currency, resource !== 'payouts')]);
    if (resource === 'orders') entries.push(['站点',record.site], ['收款确认时间',stamp(record.captured_at)], ['内部结算时间',stamp(record.settled_at)]);
    if (resource === 'refunds') entries.push(['原订单号',record.order_reference], ['退款成功时间',stamp(record.succeeded_at)]);
    if (resource === 'settlements') entries.push(['批次笔数',String(record.item_count)], ['内部结算时间',stamp(record.settled_at)]);
    if (resource === 'payouts') entries.push(['到账资产',record.target_asset], ['网络',record.target_network], ['最近更新',stamp(record.updated_at)]);
    if (resource === 'authorizations') entries.push(['站点网址',record.origin], ['站点状态',label(record.site_status)],
      ['业务范围',record.business_scope], ['通道状态',label(record.channel_status)]);
    var list = make('dl', 'work-details');
    entries.forEach(function (entry) { list.append(make('dt', '', entry[0]), make('dd', '', entry[1] || '—')); });
    dialog.append(title, list);
    if (record.status === 'UNKNOWN_EXTERNAL_STATE' || record.status === 'UNKNOWN' || record.status === 'UnknownExternalState') {
      var instruction = resource === 'payouts' ? '请用申请编号核对机构记录，确认结果前请勿再次提交出金。' :
        '请用订单号核对机构记录或等待回调。确认结果前请勿重复扣款或退款。';
      dialog.appendChild(make('p', 'work-note', instruction));
    }
    var close = button('关闭', function () { dialog.close(); });
    dialog.appendChild(close);
    dialog.addEventListener('close', function () { dialog.remove(); trigger.focus(); }, {once:true});
    document.body.appendChild(dialog); dialog.showModal(); close.focus();
  }
  function table(resource, compact) {
    var wrapper = make('div', 'table-scroll work-table');
    wrapper.tabIndex = 0;
    wrapper.setAttribute('role', 'region');
    wrapper.setAttribute('aria-label', resources[resource].title);
    var grid = make('table'), head = grid.createTHead().insertRow(), body = grid.createTBody();
    var headers = resource === 'authorizations' ? ['商户','站点','收单通道','授权状态','通道状态','操作'] :
      [resources[resource].ref,'商户','金额','状态','创建时间','操作'];
    headers.forEach(function (heading) { var th = make('th', '', heading); th.scope = 'col'; head.appendChild(th); });
    wrapper.appendChild(grid);
    return {
      element:wrapper,
      render:function (items, message) {
        body.replaceChildren();
        if (!items.length) {
          var td = make('td', 'work-empty', message || '暂无符合条件的记录。');
          td.colSpan = headers.length;
          body.insertRow().appendChild(td);
          return;
        }
        items.slice(0, compact ? 5 : items.length).forEach(function (record) {
          var row = body.insertRow();
          if (resource === 'authorizations') {
            cell(row,record.merchant); cell(row,record.site || record.reference);
            cell(row,record.channel || '待绑定');
            cell(row,record.status === 'ACTIVE' && record.site_status !== 'ACTIVE' ?
              make('span','badge badge-warn','• 站点未启用') : badge(record.status));
            cell(row,record.channel_status ? badge(record.channel_status) : '待绑定');
          } else {
            cell(row,record.reference); cell(row,record.merchant); cell(row,money(record.amount,record.currency,resource !== 'payouts'));
            cell(row,badge(record.status)); cell(row,stamp(record.created_at));
          }
          var view = button('查看', function () { details(resource,record,view); });
          cell(row,view);
        });
      }
    };
  }
  function recordsPanel(resource) {
    var config = resources[resource], section = panel(config.title);
    section.classList.add('work-records');
    var form = make('form', 'work-filters'), query = make('input');
    query.type = 'search'; query.maxLength = 128; query.placeholder = '搜索商户或' + config.ref;
    query.setAttribute('aria-label', query.placeholder);
    var state = make('select'); state.setAttribute('aria-label', '筛选状态');
    state.appendChild(new Option('全部状态',''));
    config.states.forEach(function (code) { state.appendChild(new Option(label(code),code)); });
    var search = make('button', 'submit-btn', '查询'); search.type = 'submit';
    form.appendChild(query); if (config.states.length) form.appendChild(state); form.appendChild(search);
    var grid = table(resource, false), pager = make('div', 'work-pager'), message = make('span');
    message.setAttribute('role','status');
    var cursors = [''], nextCursor = '', controller, filters = {q:'', status:''};
    var prev = button('上一页', function () { cursors.pop(); load(); });
    var next = button('下一页', function () { cursors.push(nextCursor); load(); });
    pager.append(message,prev,next);
    section.append(form,grid.element,pager);
    if (resource === 'refunds') section.appendChild(make('p','work-note','争议工单及退款复核操作仍待接入。'));
    if (resource === 'settlements') section.appendChild(button('查看银行入账与对账',function () { openTab('bank-vba'); }));
    if (resource === 'authorizations') section.appendChild(make('p','work-note','登记站点后，还需完成授权审批、密钥发放和真实联调。'));
    function load() {
      if (controller) controller.abort();
      controller = new AbortController();
      var active = controller;
      prev.disabled = true; next.disabled = true;
      message.textContent = '读取中…'; grid.render([], '正在读取记录…');
      var params = new URLSearchParams({q:filters.q, status:filters.status, before:cursors[cursors.length-1]});
      json('/operations/records/' + resource + '?' + params, active.signal).then(function (data) {
        if (active !== controller) return;
        grid.render(data.items);
        nextCursor = data.next;
        message.textContent = '第 ' + cursors.length + ' 页 · ' + data.items.length + ' 条记录';
        prev.disabled = cursors.length === 1; next.disabled = !nextCursor;
      }).catch(function () {
        if (active !== controller) return;
        grid.render([], '当前无法读取记录，请重新查询。');
        message.textContent = '读取失败';
        prev.disabled = cursors.length === 1;
      });
    }
    function selectStatus(status) {
      state.value = status;
      query.value = '';
      filters = {q:'', status:state.value};
      cursors = ['']; load();
    }
    form.addEventListener('submit', function (event) {
      event.preventDefault(); filters = {q:query.value.trim(), status:state.value}; cursors = ['']; load();
    });
    return {element:section, load:load, selectStatus:selectStatus};
  }
  function siteDraft(onSaved) {
    var disclosure = make('details','form-disclosure');
    disclosure.id = 'site-registration';
    disclosure.appendChild(make('summary','','登记外部站点'));
    var form = make('form','card'), fields = make('div','form-grid');
    function field(name, control) {
      var box = make('div','form-group'), caption = make('label','',name);
      control.required = true;
      caption.appendChild(control); box.appendChild(caption); fields.appendChild(box);
      return control;
    }
    var merchant = field('所属商户',make('select'));
    var name = field('站点名称',make('input')); name.maxLength = 100;
    var code = field('站点代码',make('input')); code.maxLength = 64; code.pattern = '[a-zA-Z0-9][a-zA-Z0-9._\\-]{0,63}';
    code.placeholder = '如 store-hk';
    var origin = field('实际销售站点域名',make('input')); origin.type = 'url';
    origin.maxLength = 2048; origin.placeholder = 'https://shop.example.com';
    var submit = make('button','submit-btn','保存站点资料'); submit.type = 'submit'; submit.disabled = true;
    var feedback = make('p','operation-result');
    var loadingMerchants = false;
    feedback.setAttribute('role','status');
    form.append(fields,make('p','work-note','保存后站点处于待开通状态。请填写消费者实际访问的站点域名。'),submit,feedback);
    disclosure.appendChild(form);
    disclosure.addEventListener('toggle',function () {
      if (!disclosure.open || loadingMerchants) return;
      var current = merchant.value;
      loadingMerchants = true;
      merchant.replaceChildren(new Option('正在读取商户…',''));
      merchant.disabled = submit.disabled = true;
      json('/api/v3/merchants').then(function (data) {
        merchant.replaceChildren(new Option('请选择商户',''));
        var items = data.items || [];
        items.forEach(function (item) { merchant.appendChild(new Option(item.merchant_name,item.id)); });
        merchant.value = current;
        submit.disabled = merchant.disabled = !items.length;
        if (!items.length) feedback.textContent = '请先创建所属商户。';
      }).catch(function () { merchant.replaceChildren(); feedback.textContent = '无法读取商户，请重新打开此表单。'; })
        .finally(function () { loadingMerchants = false; });
    });
    form.addEventListener('submit',function (event) {
      event.preventDefault(); submit.disabled = true;
      var payload = {merchant_id:merchant.value,display_name:name.value.trim(),site_code:code.value.trim(),origin:origin.value.trim()};
      authFetch(API_BASE+'/api/v3/sites',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)})
        .then(function () {
          feedback.textContent = '站点资料已保存，状态为待开通。'; name.value = code.value = origin.value = '';
          onSaved();
        }).catch(function (error) { feedback.textContent = errMsg(error); })
        .finally(function () { submit.disabled = false; });
    });
    return disclosure;
  }
  function chart(target, hours, rate) {
    target.replaceChildren();
    if (!hours.length) { target.appendChild(make('p','work-empty','今天暂无支付记录。')); return; }
    var namespace = 'http://www.w3.org/2000/svg';
    function svgNode(parent, type, attributes, text) {
      var el = document.createElementNS(namespace, type);
      Object.keys(attributes).forEach(function (key) { el.setAttribute(key, attributes[key]); });
      if (text !== undefined) el.textContent = text;
      parent.appendChild(el);
      return el;
    }
    var svg = svgNode(target,'svg',{viewBox:'0 0 408 134',role:'img','aria-label':rate ? '按小时显示支付成功率' : '按小时显示支付笔数'});
    var max = rate ? 100 : Math.max.apply(null,hours.map(function (h) { return h.attempts; }));
    [0, .5, 1].forEach(function (part) {
      var y = 104 - part * 88;
      svgNode(svg,'line',{x1:40,x2:400,y1:y,y2:y,stroke:'#e4eaf0'});
      svgNode(svg,'text',{x:35,y:y+4,'text-anchor':'end',class:'work-axis'},rate ? Math.round(part*100) + '%' : Math.round(part*max));
    });
    var previous;
    hours.forEach(function (h) {
      var x = 47 + h.hour * 15, pct = h.captured / h.attempts, y = 104 - pct * 88;
      if (rate) {
        if (previous && previous.hour + 1 === h.hour) svgNode(svg,'line',{x1:previous.x,x2:x,y1:previous.y,y2:y,stroke:'#008a69','stroke-width':2});
        var dot = svgNode(svg,'circle',{cx:x,cy:y,r:3,fill:'#008a69'});
        svgNode(dot,'title',{},h.hour + ':00 · ' + (pct*100).toFixed(1) + '%');
        previous = {x:x,y:y,hour:h.hour};
      } else {
        var height = h.attempts/max*88, bar = svgNode(svg,'rect',{x:x-5,y:104-height,width:10,height:height,fill:'#008a69',rx:2});
        svgNode(bar,'title',{},h.hour + ':00 · ' + h.attempts + ' 笔');
      }
    });
    [0,6,12,18,23].forEach(function (hour) {
      svgNode(svg,'text',{x:47+hour*15,y:124,'text-anchor':'middle',class:'work-axis'},String(hour).padStart(2,'0')+':00');
    });
    var accessible = make('details', 'work-chart-data');
    accessible.appendChild(make('summary', '', '查看分时数据'));
    var values = make('ul');
    hours.forEach(function (h) {
      values.appendChild(make('li', '', String(h.hour).padStart(2,'0') + ':00 · ' +
        (rate ? (h.captured/h.attempts*100).toFixed(1) + '%' : h.attempts + ' 笔')));
    });
    accessible.appendChild(values); target.appendChild(accessible);
  }
  function start() {
    var pages = {};
    Object.keys(resources).forEach(function (resource) {
      var view = recordsPanel(resource), tab = document.getElementById('tab-' + resources[resource].tab);
      pages[resources[resource].tab] = view;
      if (resource === 'authorizations') tab.appendChild(siteDraft(view.load));
      tab.appendChild(view.element);
    });
    function ordersWithStatus(status) { openTab('payment-orders'); pages['payment-orders'].selectStatus(status); }
    var dashboard = make('div','work-dashboard'), mast = make('div','work-mast');
    var date = make('span','work-date','北京时间 · 今日'), refresh = button('刷新工作台',loadDashboard,'refresh-btn');
    mast.append(date,refresh); dashboard.appendChild(mast);
    var metrics = make('div','work-metrics');
    var figures = [['今日支付成功率','✓','green'],['支付结果待确认','!','red'],['待结算收款','▥','blue'],['账面可出金余额','▱','green']].map(function (spec) {
      var card = make('article','work-metric work-' + spec[2]), glyph = make('span','work-glyph',spec[1]);
      glyph.setAttribute('aria-hidden','true');
      var figure = make('div','work-number','—'), help = make('div','work-unit');
      card.append(glyph,make('h2','',spec[0]),figure,help); metrics.appendChild(card);
      return {figure:figure,help:help};
    });
    dashboard.appendChild(metrics);
    var columns = make('div','work-columns'), primary = make('div'), side = make('aside');
    var guide = panel('引导式操作'), steps = make('div','work-steps');
    [
      ['排查支付异常',function () { ordersWithStatus('UNKNOWN_EXTERNAL_STATE'); }],
      ['查看退款记录',function () { openTab('payment-refunds'); }],
      ['核对结算批次',function () { openTab('payment-settlements'); }],
      ['查看出金申请',function () { openTab('payment-payouts'); }]
    ].forEach(function (step, index) {
      var next = button('',step[1],'work-step');
      next.append(make('span','work-step-count',String(index+1)),make('strong','',step[0]),make('span','work-step-arrow','›'));
      steps.appendChild(next);
    });
    guide.appendChild(steps); primary.appendChild(guide);
    var auth = panel('商户 / 站点 / 通道授权状态'), authTable = table('authorizations',true);
    auth.querySelector('h2').appendChild(button('查看全部',function () {
      openTab('merchant-onboarding'); pages['merchant-onboarding'].element.scrollIntoView({block:'start'});
    },'refresh-btn'));
    auth.appendChild(authTable.element); primary.appendChild(auth);
    var lifecycle = panel('最近支付进度');
    lifecycle.querySelector('h2').appendChild(button('查看支付订单',function () { openTab('payment-orders'); },'refresh-btn'));
    var history = make('div'); lifecycle.appendChild(history); primary.appendChild(lifecycle);
    var charts = make('div','work-charts'), rate = panel('支付成功率趋势'), volume = panel('支付笔数（分小时）');
    var rateChart = make('div'), volumeChart = make('div');
    rate.appendChild(rateChart); volume.appendChild(volumeChart); charts.append(rate,volume); primary.appendChild(charts);
    var alerts = panel('告警与待办'), alertList = make('div','work-alerts');
    alertList.setAttribute('role','status'); alerts.appendChild(alertList); side.appendChild(alerts);
    var shortcuts = panel('常用操作'), links = make('div','work-shortcuts');
    [['商户与站点','merchant-onboarding'],['凭证管理','acquirer-channels'],['审批与审计','approval-inbox'],
      ['银行入账','bank-vba'],['风险事件','risk-ops'],['支付订单','payment-orders']].forEach(function (link) {
      links.appendChild(button(link[0],function () { openTab(link[1]); }));
    });
    shortcuts.appendChild(links); side.appendChild(shortcuts); columns.append(primary,side); dashboard.appendChild(columns);
    document.querySelector('#tab-overview .page-heading').after(dashboard);
    function loadDashboard() {
      refresh.disabled = true;
      var summary = json('/operations/summary').then(function (data) {
        var m = data.metrics;
        date.textContent = data.business_date + ' · 北京时间';
        figures[0].figure.textContent = m.attempts ? (m.captured/m.attempts*100).toFixed(2) + '%' : '—';
        figures[0].help.textContent = m.attempts ? m.captured + ' 次成功 / ' + m.attempts + ' 次支付尝试' : '今日暂无支付尝试';
        figures[1].figure.textContent = m.unknown_payments.toLocaleString('zh-CN');
        figures[1].help.textContent = '全部待核对的支付尝试';
        [m.pending_amounts,m.available_amounts].forEach(function (items, i) {
          var figure = figures[i+2].figure; figure.replaceChildren();
          if (!items.length) figure.textContent = '—';
          items.forEach(function (item) { figure.appendChild(make('div','',money(item.amount,item.currency,i === 0))); });
        });
        figures[2].help.textContent = m.pending_amounts.length ? '已收款、尚未内部结算 · 分币种' : '暂无符合条件的收款记录';
        figures[3].help.textContent = m.available_amounts.length ? '按账本原单位显示 · 分币种' : '暂无出金余额记录';
        chart(rateChart,m.hours,true); chart(volumeChart,m.hours,false);
        var issues = [
          ['支付结果待确认',m.unknown_payments,function () { ordersWithStatus('UNKNOWN_EXTERNAL_STATE'); }],
          ['退款待处理或待确认',m.open_refunds,function () { openTab('payment-refunds'); }],
          ['变更待审批',m.pending_approvals,function () { openTab('approval-inbox'); }],
          ['已关联订单的回调失败（今日）',m.webhook_failures]
        ];
        alertList.replaceChildren();
        issues.forEach(function (issue) {
          var item = issue[2] ? button('',issue[2],'work-alert') : make('div','work-alert work-alert-summary');
          item.append(make('span','',issue[0]),make('strong','',String(issue[1])));
          alertList.appendChild(item);
        });
      }).catch(function () {
        date.textContent = '统计读取失败';
        figures.forEach(function (item) { item.figure.textContent = '—'; item.help.textContent = '请刷新重试'; });
        [rateChart,volumeChart,alertList].forEach(function (target) {
          target.replaceChildren(make('p','work-empty','无法读取统计。'));
        });
      });
      var authorization = json('/operations/records/authorizations').then(function (data) {
        authTable.render(data.items,'暂无站点授权记录。');
      }).catch(function () { authTable.render([],'无法读取授权记录，请刷新重试。'); });
      var recent = json('/operations/records/orders').then(function (data) {
        var record = data.items[0]; history.replaceChildren();
        if (!record) { history.appendChild(make('p','work-empty','暂无可展示的支付记录。')); return; }
        var line = make('div','work-order-heading');
        line.append(make('strong','',record.reference),make('span','',money(record.amount,record.currency,true)),badge(record.status));
        history.appendChild(line);
        var moments = make('ol','work-timeline');
        [['创建支付',record.created_at],['确认收款',record.captured_at],['内部结算',record.settled_at]].forEach(function (step) {
          var point = make('li',step[1] ? 'recorded' : '');
          point.append(make('strong','',step[0]),make('small','',step[1] ? stamp(step[1]) : '暂无记录'));
          moments.appendChild(point);
        });
        history.appendChild(moments);
      }).catch(function () { history.replaceChildren(make('p','work-empty','无法读取支付进度，请刷新重试。')); });
      Promise.allSettled([summary,authorization,recent]).finally(function () { refresh.disabled = false; });
    }
    var show = window.showTab;
    window.showTab = function (key,btn) {
      show(key,btn);
      if (pages[key]) pages[key].load();
      if (key === 'overview' && !refresh.disabled) loadDashboard();
    };
    loadDashboard();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded',start); else start();
}());
