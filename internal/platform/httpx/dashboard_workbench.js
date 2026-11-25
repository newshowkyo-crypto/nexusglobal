(function () {
  'use strict';
  var statuses = {
    ACTIVE:'已启用', INACTIVE:'已停用', PENDING:'待处理', PROCESSING:'处理中', REQUIRES_ACTION:'待完成验证',
    UNKNOWN_EXTERNAL_STATE:'结果待确认', UNKNOWN:'结果待确认', FAILED:'失败', SUCCEEDED:'成功',
    AUTHORIZED:'已授权', CAPTURED:'已收款', SETTLED:'已结算', PARTIALLY_CAPTURED:'部分收款',
    PARTIALLY_REFUNDED:'部分退款', REFUNDED:'已退款', CHARGED_BACK:'已拒付', CANCELED:'已取消',
    UNCONFIGURED:'待授权', EXPIRED:'已过期', REVOKED:'已撤销', SUSPENDED:'已暂停', SCHEDULED:'待生效',
    AMOUNT_MISMATCH:'金额不匹配', CURRENCY_MISMATCH:'币种不匹配', REFERENCE_MISMATCH:'参考号不匹配',
    Draft:'草稿', BalanceFrozen:'余额已冻结', FxLocked:'汇率已锁定', LPProcessing:'资金服务处理中',
    Confirmed:'已确认', Reconciled:'已对账', Failed:'失败', BalanceReleased:'余额已释放', UnknownExternalState:'结果待确认'
  };
  var dateFormat = new Intl.DateTimeFormat('zh-CN', {
    timeZone:'Asia/Shanghai', month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit', hourCycle:'h23'
  });
  var resources = {
    orders: {tab:'payment-orders', title:'支付记录', ref:'外部订单号', states:['UNKNOWN_EXTERNAL_STATE','FAILED','PROCESSING','REQUIRES_ACTION','AUTHORIZED','CAPTURED','PARTIALLY_CAPTURED','SETTLED','PARTIALLY_REFUNDED','REFUNDED','CANCELED','CHARGED_BACK']},
    refunds: {tab:'payment-refunds', title:'退款记录', ref:'退款单号', states:[['PENDING,PROCESSING,UNKNOWN','待处理或待确认'],'PENDING','PROCESSING','UNKNOWN','SUCCEEDED','FAILED']},
    settlements: {tab:'payment-settlements', title:'结算批次', ref:'批次 ID 或业务日期', states:['SETTLED']},
    payouts: {tab:'payment-payouts', title:'出金申请', ref:'申请编号', states:['Draft','BalanceFrozen','FxLocked','LPProcessing','Confirmed','Reconciled','Failed','BalanceReleased','UnknownExternalState']},
    authorizations: {tab:'merchant-onboarding', title:'站点 / 通道授权记录', ref:'站点代码', states:['ACTIVE','PENDING','UNCONFIGURED','SCHEDULED','EXPIRED','REVOKED']}
  };
  var settlementDetails = {
    'unsettled-orders':{title:'待结算收款',ref:'外部订单号'},
    'reconciliation-exceptions':{title:'对账异常',ref:'对账日期'}
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
  function amountText(amount, currency, minor) {
    if (!currency || typeof amount !== 'string' || !/^-?\d+(?:\.\d+)?$/.test(amount)) return '—';
    var digits = /^[A-Z]{3}$/.test(currency) ? new Intl.NumberFormat('en', {style:'currency', currency:currency}).resolvedOptions().maximumFractionDigits : null;
    if (minor && digits === null) return '—';
    var negative = amount.startsWith('-'), value = amount.replace(/^-/, ''), parts = value.split('.');
    if (minor && digits) {
      var integer = parts[0].padStart(digits + 1, '0');
      parts = [integer.slice(0, -digits), integer.slice(-digits) + (parts[1] || '')];
    }
    var fraction = (parts[1] || '').replace(/0+$/, '').padEnd(digits || 0, '0');
    return (negative ? '−' : '') + parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ',') + (fraction ? '.' + fraction : '');
  }
  function money(amount, currency, minor) { var value = amountText(amount,currency,minor); return value === '—' ? value : value + ' · ' + currency; }
  function currencyName(code) {
    return ({USD:'美元',CNY:'人民币',EUR:'欧元',HKD:'港元',JPY:'日元',GBP:'英镑',SGD:'新加坡元'})[code] || code || '—';
  }
  function badge(status) {
    var tone = /^(ACTIVE|CAPTURED|SETTLED|SUCCEEDED|Confirmed|Reconciled)$/.test(status) ? 'ok' :
      /^(FAILED|EXPIRED|REVOKED|CHARGED_BACK|Failed|.*_MISMATCH)$/.test(status) ? 'err' :
      /^(PROCESSING|AUTHORIZED|LPProcessing)$/.test(status) ? 'info' : 'warn';
    return make('span', 'badge badge-' + tone, label(status));
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
  function cell(row, value, cls) { var td = make('td',cls); td.append(value || '—'); row.appendChild(td); return td; }
  function amountCell(row, record, minor) {
    var text = amountText(record.amount, record.currency, minor);
    var field = cell(row,text,'atlas-amount');
    if (text !== '—') field.appendChild(make('span','atlas-currency',record.currency));
  }
  function details(resource, record, trigger) {
    var config = settlementDetails[resource] || resources[resource];
    var dialog = make('dialog', 'credentials-dialog work-dialog');
    var title = make('h2', '', config.title + '详情'); title.id = 'work-detail-title';
    dialog.setAttribute('aria-labelledby', title.id);
    var entries = [['商户',record.merchant], [config.ref,record.reference],
      ['当前状态',resource === 'unsettled-orders' ? '待结算' : label(record.status)], ['创建时间',stamp(record.created_at)]];
    if (record.amount) entries.push([resource === 'reconciliation-exceptions' ? '应收金额' : '金额',money(record.amount, record.currency, resource !== 'reconciliation-exceptions')]);
    if (resource === 'orders') entries.push(['站点',record.site], ['收款确认时间',stamp(record.captured_at)], ['内部结算时间',stamp(record.settled_at)]);
    if (resource === 'refunds') entries.push(['原订单号',record.order_reference], ['退款成功时间',stamp(record.succeeded_at)]);
    if (resource === 'settlements') entries.push(['批次笔数',String(record.item_count)], ['内部结算时间',stamp(record.settled_at)]);
    if (resource === 'payouts') entries.push(['到账资产',record.target_asset], ['网络',record.target_network], ['最近更新',stamp(record.updated_at)]);
    if (resource === 'authorizations') entries.push(['站点网址',record.origin], ['站点状态',label(record.site_status)],
      ['业务范围',record.business_scope], ['通道状态',label(record.channel_status)]);
    if (record.batch_id) entries.push([resource === 'settlements' ? '批次编号' : '记录编号',record.batch_id]);
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
    var summary = compact && resource === 'orders';
    var settlement = resource === 'settlements' || resource === 'unsettled-orders' || resource === 'reconciliation-exceptions';
    var wrapper = make('div', summary || settlement ? 'atlas-table-scroll' : 'table-scroll work-table');
    wrapper.tabIndex = 0;
    wrapper.setAttribute('role', 'region');
    wrapper.setAttribute('aria-label', settlement ? '结算明细' : resources[resource].title);
    var grid = make('table'), head = grid.createTHead().insertRow(), body = grid.createTBody();
    var headers = resource === 'authorizations' ? ['商户','站点','收单通道','授权状态','通道状态','操作'] :
      summary ? ['订单编号','类型','入口通道','金额 / 币种','状态','时间 (UTC+8)'] :
      resource === 'settlements' ? ['批次 ID','业务日 (UTC+8)','商户','金额 / 币种','状态'] :
      resource === 'unsettled-orders' ? ['支付记录 ID','外部订单号','商户','金额 / 币种','状态','操作'] :
      resource === 'reconciliation-exceptions' ? ['对账记录 ID','对账日期 (UTC+8)','商户','金额 / 币种','状态','操作'] :
      [resources[resource].ref,'商户','金额 / 币种','状态','创建时间','操作'];
    headers.forEach(function (heading) { var th = make('th', heading === '金额 / 币种' ? 'atlas-amount' : '', heading); th.scope = 'col'; head.appendChild(th); });
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
          } else if (summary) {
            var reference = button(record.reference || '查看记录',function () { details(resource,record,reference); },'atlas-button atlas-text atlas-reference');
            cell(row,reference); cell(row,'收款'); cell(row,record.entry_channel);
            amountCell(row,record,true); cell(row,badge(record.status)); cell(row,stamp(record.created_at),'atlas-number');
            return;
          } else if (settlement) {
            var recordId = record.batch_id || record.reference;
            var identifier = recordId ? button(recordId.slice(0,16) + '…',function () { details(resource,record,identifier); },'atlas-button atlas-text atlas-reference') : '—';
            cell(row,identifier,'atlas-mono');
            if (recordId) identifier.title = recordId;
            cell(row,record.reference,'atlas-number'); cell(row,record.merchant); amountCell(row,record,resource !== 'reconciliation-exceptions');
            cell(row,resource === 'unsettled-orders' ? make('span','badge badge-warn','待结算') : badge(record.status));
          } else {
            cell(row,record.reference,'atlas-mono'); cell(row,record.merchant); amountCell(row,record,true);
            cell(row,badge(record.status)); cell(row,stamp(record.created_at));
          }
          var view = button('查看', function () { details(resource,record,view); },'atlas-button atlas-text');
          if (resource !== 'settlements') cell(row,view);
        });
      }
    };
  }
  function recordsPanel(resource) {
    var config = resources[resource], settlement = resource === 'settlements';
    var section = settlement ? make('section','atlas-panel atlas-table-panel') : panel(config.title);
    section.classList.add('work-records');
    var form = make('form', 'work-filters'), query = make('input');
    query.type = 'search'; query.maxLength = 128; query.placeholder = '搜索商户或' + config.ref;
    query.setAttribute('aria-label', query.placeholder);
    var state = make('select'); state.setAttribute('aria-label', '筛选状态');
    state.appendChild(new Option('全部状态',''));
    config.states.forEach(function (entry) {
      state.appendChild(Array.isArray(entry) ? new Option(entry[1], entry[0]) : new Option(label(entry), entry));
    });
    var search = make('button', 'submit-btn', '查询'); search.type = 'submit';
    form.appendChild(query); if (!settlement && config.states.length) form.appendChild(state); form.appendChild(search);
    var grid = table(resource, false), pager = make('div', 'work-pager'), message = make('span');
    message.setAttribute('role','status');
    var cursors = [''], nextCursor = '', controller, filters = {q:'', status:''};
    var prev = button('上一页', function () { cursors.pop(); load(); });
    var next = button('下一页', function () { cursors.push(nextCursor); load(); });
    pager.append(message,prev,next);
    var filtersNav, activeResource = resource, activeFilter = '', description;
    if (settlement) {
      filtersNav = segments([['','全部'],['unsettled-orders','待复核'],['reconciliation-exceptions','异常']], '结算批次状态', switchSettlement);
      var head = panelHead(config.title,'各币种分别列示 · 不做跨币种加总',filtersNav.element);
      description = head.querySelector('p'); section.appendChild(head);
    }
    if (settlement) section.append(grid.element,pager); else section.append(form,grid.element,pager);
    if (resource === 'refunds') section.appendChild(make('p','work-note','争议工单及退款复核操作仍待接入。'));
    if (resource === 'authorizations') section.appendChild(make('p','work-note','登记站点后，还需完成授权审批、密钥发放和真实联调。'));
    function load() {
      if (controller) controller.abort();
      controller = new AbortController();
      var active = controller;
      prev.disabled = true; next.disabled = true;
      message.textContent = '读取中…'; grid.render([], '正在读取记录…');
      var params = new URLSearchParams({q:filters.q, status:filters.status, before:cursors[cursors.length-1]});
      json('/operations/records/' + activeResource + '?' + params, active.signal).then(function (data) {
        if (active !== controller) return;
        grid.render(data.items);
        nextCursor = data.next;
        var unit = activeResource === 'settlements' ? '个批次' : '条记录';
        message.textContent = (cursors.length > 1 ? '第 ' + cursors.length + ' 页 · ' : '') + data.items.length + ' ' + unit;
        prev.disabled = cursors.length === 1; next.disabled = !nextCursor;
      }).catch(function () {
        if (active !== controller) return;
        grid.render([], '当前无法读取记录，请重新查询。');
        message.textContent = '读取失败';
        prev.disabled = cursors.length === 1;
      });
    }
    function switchSettlement(filter) {
      if (filter === activeFilter) return;
      activeFilter = filter; activeResource = filter || resource;
      var nextGrid = table(activeResource,false); grid.element.replaceWith(nextGrid.element); grid = nextGrid;
      description.textContent = filter === 'unsettled-orders' ? '已收款、尚未生成内部结算批次的支付记录' :
        filter === 'reconciliation-exceptions' ? '银行入账对账记录 · 金额为应收金额' : '各币种分别列示 · 不做跨币种加总';
      filtersNav.select(filter); selectStatus('',true);
    }
    function selectStatus(status, keepQuery) {
      state.value = status;
      if (!keepQuery) query.value = '';
      filters = {q:query.value.trim(), status:state.value};
      cursors = ['']; load();
    }
    form.addEventListener('submit', function (event) {
      event.preventDefault(); filters = {q:query.value.trim(), status:state.value}; cursors = ['']; load();
    });
    return {element:section, load:load, selectStatus:selectStatus, selectSettlement:switchSettlement};
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
  function panelHead(title, description, control) {
    var head = make('div','atlas-panel-head'), copy = make('div');
    copy.appendChild(make('h2','',title));
    if (description) copy.appendChild(make('p','',description));
    head.appendChild(copy);
    if (control) head.appendChild(control);
    return head;
  }
  function segments(choices, title, change) {
    var group = make('div','atlas-segments');
    group.setAttribute('role','group'); group.setAttribute('aria-label',title);
    var controls = choices.map(function (choice) {
      var control = button(choice[1],function () { select(choice[0]); change(choice[0]); });
      control.className = '';
      group.appendChild(control);
      return [choice[0],control];
    });
    function select(value) { controls.forEach(function (entry) { entry[1].setAttribute('aria-pressed',String(entry[0] === value)); }); }
    select(choices[0][0]);
    return {element:group,select:select};
  }
  function metricCards(titles, className) {
    var grid = make('div','atlas-metrics' + (className ? ' ' + className : ''));
    var cards = titles.map(function (title) {
      var card = make('article','atlas-panel atlas-metric');
      var heading = make('h2','',title), value = make('div','atlas-metric-value','—'), note = make('p','atlas-metric-note');
      card.append(heading,value,note); grid.appendChild(card);
      return {title:heading,value:value,note:note};
    });
    return {element:grid,cards:cards};
  }
  // One primary currency per card keeps the approved single-line metric layout.
  // The currency selector remains the real switch and drives cards plus chart.
  function primaryRow(rows, currency, minor, field) {
    if (!rows.length) return null;
    var row = rows.find(function (record) { return record.currency === currency; }) ||
      rows.find(function (record) { return record.currency === 'USD'; }) || rows[0];
    return [row.currency, amountText(row[field || 'amount'],row.currency,minor)];
  }
  function fillAmount(target, rows, currency, minor, field) {
    target.replaceChildren();
    var row = primaryRow(rows,currency,minor,field);
    if (!row) { target.textContent = '—'; return; }
    target.append(make('small','atlas-currency',row[0]),document.createTextNode(' ' + row[1]));
  }
  function svgNode(parent, tag, attributes, text) {
    var node = document.createElementNS('http://www.w3.org/2000/svg',tag);
    Object.keys(attributes).forEach(function (key) { node.setAttribute(key,attributes[key]); });
    if (text !== undefined) node.textContent = text;
    parent.appendChild(node);
    return node;
  }
  function shiftDay(day, offset) {
    var date = new Date(day + 'T00:00:00Z'); date.setUTCDate(date.getUTCDate() + offset);
    return date.toISOString().slice(0,10);
  }
  function drawFlow(target, captured, paid, day, currency, length) {
    target.replaceChildren();
    var days = Array.from({length:length},function (_,index) { return shiftDay(day,index + 1 - length); });
    var incoming = new Map(captured.filter(function (item) { return item.currency === currency; }).map(function (item) { return [item.day,item.amount_minor]; }));
    var outgoing = new Map(paid.filter(function (item) { return item.currency === currency; }).map(function (item) { return [item.day,item.amount]; }));
    if (!currency || !days.some(function (date) { return incoming.has(date) || outgoing.has(date); })) {
      target.appendChild(make('p','atlas-empty','所选区间暂无资金流向记录。')); return;
    }
    var iso = /^[A-Z]{3}$/.test(currency);
    if (!iso) {
      target.appendChild(make('p','atlas-empty','该币种未配置最小货币单位，无法绘制。')); return;
    }
    var digits = new Intl.NumberFormat('en',{style:'currency',currency:currency}).resolvedOptions().maximumFractionDigits;
    var units = Math.pow(10,digits);
    var records = days.map(function (date) { return [Number(incoming.get(date) || 0) / units,Number(outgoing.get(date) || 0) / units]; });
    var peak = Math.max(0,...records.flat());
    var step = peak ? Math.pow(10,Math.floor(Math.log10(peak / 4))) : 1;
    var increment = Math.ceil(peak / 4 / step) * step || 1;
    var maximum = increment * 4, left = 0, right = 600, top = 2, bottom = 178;
    var scale = new Intl.NumberFormat('zh-CN',{notation:'compact',maximumFractionDigits:2});
    function coord(index) { return left + index * (right-left) / (days.length-1); }
    function yCoord(value) { return bottom - value/maximum*(bottom-top); }
    var plot = make('div','atlas-plot'), axis = make('div','atlas-plot-axis');
    for (var line = 4; line >= 0; line--) axis.appendChild(make('span','',scale.format(line * increment)));
    plot.appendChild(axis);
    var svg = svgNode(plot,'svg',{viewBox:'0 0 600 180',preserveAspectRatio:'none',role:'img','aria-label':length + ' 天资金流向（' + currency + '）：收款实线，出款虚线'});
    for (var grid = 0; grid <= 4; grid++) {
      var y = yCoord(grid * increment);
      svgNode(svg,'line',{x1:left,x2:right,y1:y,y2:y,class:'atlas-chart-grid'});
    }
    var paths = [0,1].map(function (series) { return records.map(function (record,index) {
      return (index === 0 ? 'M' : 'L') + coord(index) + ' ' + yCoord(record[series]);
    }).join(' '); });
    svgNode(svg,'path',{d:paths[0]+' L'+right+' '+bottom+' L'+left+' '+bottom+' Z',class:'atlas-chart-area'});
    svgNode(svg,'path',{d:paths[0],class:'atlas-chart-in'});
    svgNode(svg,'path',{d:paths[1],class:'atlas-chart-out'});
    target.appendChild(plot);
    var tickCount = length === 7 ? 7 : 6, dates = make('div','atlas-plot-dates');
    for (var tick = 0; tick < tickCount; tick++) {
      var index = Math.round(tick * (days.length-1) / (tickCount-1));
      var mark = make('span','',days[index].slice(5).replace('-','/'));
      if (tick === 0) mark.style.textAlign = 'left';
      if (tick === tickCount-1) mark.style.textAlign = 'right';
      dates.appendChild(mark);
    }
    target.appendChild(dates);
  }
  function renderDesignSystem(tab) {
    var grid = make('div','atlas-kit-grid');
    function section(title, note, wide) {
      var item = make('section','atlas-panel atlas-kit-panel' + (wide ? ' atlas-kit-wide' : ''));
      item.appendChild(panelHead(title,note)); grid.appendChild(item); return item;
    }
    var buttons = section('按钮与交互状态','桌面标准高 36 px，紧凑 32 px，移动端 44 px；字号 13 / 20 px、字重 600、圆角 6 px、图标 16 px。',true);
    var examples = make('div','atlas-kit-buttons'), response = make('p','atlas-kit-response'); response.setAttribute('role','status');
    function announce(message) { response.textContent = message; }
    function openExample(danger, opener) {
      var dialog = make('dialog','credentials-dialog work-dialog atlas-demo-dialog');
      var title = make('h2','',danger ? '确认演示冻结？' : '组件详情'); title.id = 'atlas-example-title';
      dialog.setAttribute('aria-labelledby',title.id);
      var cancel = button('返回',function () { dialog.close(); },'atlas-button');
      var actions = make('div','atlas-tools'); actions.appendChild(cancel);
      dialog.append(title,make('p','',danger ? '此弹窗只演示风险确认，不连接业务接口，也不会修改任何记录。' : '文字按钮用于查看详情、导航和低风险操作。'),actions);
      if (danger) actions.appendChild(button('确认演示',function () { dialog.close(); announce('风险确认样式演示完成。'); },'atlas-button atlas-danger'));
      dialog.addEventListener('close',function () { dialog.remove(); opener.focus(); },{once:true});
      document.body.appendChild(dialog); dialog.showModal(); cancel.focus();
    }
    [
      ['主按钮','模拟导出','atlas-primary','蓝底白字；同一操作区只设置一个主动作。',function (control) {
        control.disabled = true; control.setAttribute('aria-busy','true');
        control.replaceChildren(make('span','atlas-spinner'),'导出中');
        setTimeout(function () { control.textContent = '模拟导出'; control.disabled = false; control.removeAttribute('aria-busy'); announce('加载状态演示完成。'); },650);
      }],
      ['次按钮','取消操作','','白底描边；用于返回、取消和次要操作。',function () { announce('已取消演示操作。'); }],
      ['文字按钮','查看详情 →','atlas-text','无实色填充；用于跳转及低风险行内操作。',function (control) { openExample(false,control); }],
      ['危险按钮','演示冻结','atlas-danger','仅用于不可逆或高风险确认；先呈现影响范围。',function (control) { openExample(true,control); }]
    ].forEach(function (spec) {
      var tile = make('div');
      var control = button(spec[1],function () { spec[4](control); },'atlas-button ' + spec[2]);
      tile.append(make('h3','',spec[0]),control,make('p','',spec[3])); examples.appendChild(tile);
    });
    var states = make('div','atlas-kit-state-grid');
    states.appendChild(make('div','atlas-kit-state-head',''));
    ['默认','悬停','按下','焦点','禁用','加载'].forEach(function (name) { states.appendChild(make('div','atlas-kit-state-head',name)); });
    [['主按钮','atlas-primary'],['次按钮',''],['文字按钮','atlas-text'],['危险按钮','atlas-danger']].forEach(function (rowSpec) {
      states.appendChild(make('div','atlas-kit-state-row',rowSpec[0]));
      ['默认','悬停','按下','焦点','禁用','加载'].forEach(function (name,index) {
        var sample = make('span','atlas-button ' + rowSpec[1] + (index === 1 ? ' atlas-sample-hover' : '') + (index === 2 ? ' atlas-sample-active' : '') + (index === 3 ? ' atlas-sample-focus' : ''),index === 5 ? '提交中' : '导出报表');
        if (index === 4) { sample.classList.add('atlas-sample-disabled'); sample.setAttribute('aria-disabled','true'); }
        if (index === 5) { sample.classList.add('atlas-sample-loading'); sample.insertBefore(make('span','atlas-spinner'),sample.firstChild); }
        var cell = make('div','atlas-kit-state-cell'); cell.appendChild(sample); states.appendChild(cell);
      });
    });
    var stateWrap = make('div','atlas-kit-state-wrap'); stateWrap.appendChild(states);
    buttons.append(examples,make('h3','atlas-kit-subtitle','四类按钮状态预览'),stateWrap,response);
    var palette = make('div','atlas-kit-palette');
    [['页面背景','canvas','#F3F6FB'],['卡片 / 导航','surface','#FFFFFF'],['表头 / 悬停','subtle','#F7F9FD'],['选中背景','selected','#E9F0FE'],['面板细边框','line','#DCE4EF'],['主操作蓝','blue','#1D4FB4'],['标题 / 金额','ink','#132441'],['辅助文字','muted','#526580']].forEach(function (color) {
      var item = make('div'); item.append(make('i','atlas-swatch atlas-swatch-'+color[1]),make('span','',color[0]),make('code','',color[2])); palette.appendChild(item);
    });
    section('基础色与组件背景','状态色只承载业务含义，不给普通卡片整块上色。').appendChild(palette);
    var types = make('dl','atlas-kit-types');
    [['资金总览','标题 · 28 / 36 px · 700','atlas-type-title'],['12,842,610.24','指标 · 最大 24 / 32 px · 650','atlas-type-metric'],['结算批次','区块标题 · 16 / 24 px · 600','atlas-type-section'],['查看最新资金活动','正文 · 14 / 22 px · 400',''],['数据截至 UTC+8','标签 · 12 / 18 px · 600','atlas-type-caption'],['TX-92841-AF3','ID · 12 px · SF Mono / Consolas','atlas-mono']].forEach(function (type) {
      var row = make('div'); row.append(make('dt',type[2],type[0]),make('dd','',type[1])); types.appendChild(row);
    });
    section('字体与数字排版','拉丁：Inter → Segoe UI；中文：Noto Sans SC → 苹方 / 微软雅黑。离线稿使用本机已安装字体。').appendChild(types);
    var stateList = make('div','atlas-kit-palette');
    [['已结算','ok','#136149 / #E8F6EF'],['处理中','info','#214D99 / #EAF1FE'],['待复核','warn','#80510B / #FFF5E4'],['金额不匹配','err','#9F293E / #FDEDF0']].forEach(function (state) {
      var item = make('div'); item.append(make('span','badge badge-'+state[1],state[0]),make('code','',state[2])); stateList.appendChild(item);
    });
    section('状态标签','圆点辅助扫读，状态必须有文字；红色不用于普通趋势。').appendChild(stateList);
    var rules = make('ul','atlas-kit-rules');
    ['<strong>表格：</strong>白色行、浅蓝灰表头，正文 13 / 20 px。金额右对齐、使用等宽数字，同列显示币种。','<strong>输入框：</strong>白底、1 px 描边，焦点加 2 px 蓝框；失败提示放在字段旁，用文字说明原因。','<strong>图表：</strong>白底、细网格、蓝实线与灰蓝虚线；标明单位、区间和时区，禁用 3D 或发光。','<strong>弹窗：</strong>白底、12 px 圆角。对冻结等高风险动作展示对象、影响、审批要求，再给确认按钮。','<strong>空与错：</strong>无数据时说明可调整的条件；异常时保留筛选并提供重试，不把缺失金额显示为 0。'].forEach(function (rule) { var item = make('li'); item.innerHTML = rule; rules.appendChild(item); });
    section('表格、表单、图表与弹窗','基础间距 4 px；面板圆角 10 px；按钮和输入框圆角 6 px。').appendChild(rules);
    tab.appendChild(grid);
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
    var overview = document.getElementById('tab-overview'), settlement = document.getElementById('tab-payment-settlements');
    renderDesignSystem(document.getElementById('tab-design-system'));
    var dashboard = make('div','work-dashboard'), headingActions = make('div','atlas-heading-actions');
    var date = make('span','atlas-stamp'), dateText = make('span','','—'), zone = make('small','','UTC+8');
    date.append(window.atlasIcon('calendar'),dateText,zone);
    var summaryData, summaryRequest, period = 30, recentRequest;
    var exportButton = make('a','atlas-button atlas-primary','导出 CSV');
    exportButton.href = API_BASE+'/operations/funds.csv';
    exportButton.download = ''; exportButton.prepend(window.atlasIcon('download'));
    exportButton.title = '导出最近 90 天各币种资金流水，金额为最小货币单位';
    headingActions.append(date,exportButton); overview.querySelector('.page-heading').appendChild(headingActions);
    settlement.querySelector('.page-heading').appendChild(button('返回资金总览',function () { openTab('overview'); },'atlas-button'));
    var notice = make('div','atlas-notice'), warning = make('strong','','正在读取待办…');
    var viewRisk = button('查看待办',function () { openTab('risk-ops'); },'atlas-button atlas-text');
    var noticeCopy = make('div','atlas-notice-copy'), noticeDetails = make('span','atlas-notice-details');
    noticeCopy.append(window.atlasIcon('warning'),warning,noticeDetails);
    notice.append(noticeCopy,viewRisk); dashboard.appendChild(notice);
    var figures = metricCards(['可用余额','今日交易额','待结算金额','待处理异常']);
    figures.cards[3].value.classList.add('atlas-attention'); dashboard.appendChild(figures.element);
    var settlementFigures = metricCards(['待结算 · 美元','待确认差异','今日已结算批次'],'atlas-settlement-metrics');
    settlement.querySelector('.page-heading').after(settlementFigures.element);
    var riskPage = document.getElementById('tab-risk-ops');
    var riskPageCount = make('span','badge badge-warn','—');
    var riskPageList = make('ul','atlas-risk-list');
    var riskPageFoot = make('div','atlas-panel-foot');
    riskPageFoot.appendChild(button('查看结算批次',function () { openTab('payment-settlements'); },'atlas-button atlas-text'));
    var riskPagePanel = make('section','atlas-panel atlas-risk-page');
    riskPagePanel.append(panelHead('待办与风险','按处理优先级排序',riskPageCount),riskPageList,riskPageFoot);
    riskPage.querySelector('.page-heading').after(riskPagePanel);
    var bankLink = button('查看银行入账与对账',function () { openTab('bank-vba'); },'atlas-button atlas-text');
    var bankFooter = make('div','atlas-panel-foot'); bankFooter.appendChild(bankLink); settlement.appendChild(bankFooter);
    var columns = make('div','atlas-overview-grid'), chartPanel = make('section','atlas-panel'), risk = make('section','atlas-panel atlas-risk-panel');
    var chartControls = make('div','atlas-tools'), currencySelect = make('select');
    currencySelect.setAttribute('aria-label','资金流向币种'); currencySelect.addEventListener('change',renderFlow);
    var periods = segments([[7,'7 天'],[30,'30 天'],[90,'90 天']],'资金流向区间',function (days) { period = days; renderFlow(); });
    periods.select(period); chartControls.append(currencySelect,periods.element);
    var chartHead = panelHead('资金流向','单位：实际币种金额 · UTC+8',chartControls);
    var chartUnit = chartHead.querySelector('p');
    var chartContent = make('div','atlas-chart-content'), legend = make('div','atlas-chart-legend'), chartTarget = make('div');
    legend.append(make('span','','流入 · 已收款'), make('span','','流出 · 已确认出款'));
    chartContent.append(legend,chartTarget); chartPanel.append(chartHead,chartContent);
    var riskCount = make('span','badge badge-warn','—'), riskList = make('ul','atlas-risk-list'), riskFoot = make('div','atlas-panel-foot');
    riskFoot.appendChild(button('查看结算批次',function () { openTab('payment-settlements'); },'atlas-button atlas-text'));
    risk.append(panelHead('待办与风险','按处理优先级排序',riskCount),riskList,riskFoot); columns.append(chartPanel,risk);
    dashboard.appendChild(columns);
    var recent = make('section','atlas-panel atlas-table-panel'), tools = make('form','atlas-tools atlas-search-tools');
    var recentQuery = make('input'); recentQuery.type = 'search'; recentQuery.placeholder = '搜索订单编号或入口通道';
    recentQuery.setAttribute('aria-label',recentQuery.placeholder); recentQuery.maxLength = 128;
    var recentSearch = make('div','atlas-search');
    var searchIcon = window.atlasIcon ? window.atlasIcon('search') : null;
    if (searchIcon) recentSearch.appendChild(searchIcon);
    recentSearch.appendChild(recentQuery);
    var recentStatus = make('select'); recentStatus.setAttribute('aria-label','最近交易状态'); recentStatus.add(new Option('全部状态',''));
    resources.orders.states.forEach(function (status) { recentStatus.add(new Option(label(status),status)); });
    tools.append(recentSearch,recentStatus);
    var recentTable = table('orders',true), recentInfo = make('span'), recentFoot = make('div','atlas-panel-foot atlas-recent-foot');
    recentInfo.setAttribute('role','status');
    recentFoot.append(recentInfo);
    recent.append(panelHead('最近交易','支付记录 · 按创建时间倒序',tools),recentTable.element,recentFoot);
    dashboard.appendChild(recent); overview.querySelector('.page-heading').after(dashboard);
    var refresh = button('刷新工作台',loadDashboard,'refresh-btn');
    overview.querySelector('.system-details').appendChild(refresh);
    var riskNav = document.querySelector('.nav [data-tab="risk-ops"]');
    var riskNavCount = make('span','atlas-nav-count'); riskNavCount.hidden = true; riskNav.appendChild(riskNavCount);
    function updateMetricCurrency() {
      if (!summaryData) return;
      var metrics = summaryData.metrics, currency = currencySelect.value;
      var today = metrics.captured_flow.filter(function (item) { return item.day === summaryData.business_date; });
      fillAmount(figures.cards[0].value,metrics.available_amounts,currency,true);
      fillAmount(figures.cards[1].value,today,currency,true,'amount_minor');
      fillAmount(figures.cards[2].value,metrics.pending_amounts,currency,true);
      var settlementTitle = '待结算 · ' + currencyName(currency);
      if (settlementFigures.cards[0].title.textContent !== settlementTitle) settlementFigures.cards[0].title.textContent = settlementTitle;
      fillAmount(settlementFigures.cards[0].value,metrics.pending_amounts,currency,true);
    }
    function renderFlow() {
      if (!summaryData) return;
      chartUnit.textContent = '单位：' + (currencySelect.value || '实际币种金额') + ' · UTC+8';
      updateMetricCurrency();
      drawFlow(chartTarget,summaryData.metrics.captured_flow,summaryData.metrics.paid_flow,summaryData.business_date,currencySelect.value,period);
    }
    function updateRisk(metrics) {
      var issues = [
        ['支付结果待确认',metrics.unknown_payments,'需与提供方核对',function () { ordersWithStatus('UNKNOWN_EXTERNAL_STATE'); }],
        ['银行入账对账差异',metrics.reconciliation_exceptions,'今日未匹配记录',function () {
          openTab('payment-settlements'); pages['payment-settlements'].selectSettlement('reconciliation-exceptions');
        }],
        ['支付回调失败（今日）',metrics.webhook_failures,'检查回调重试',null],
        ['退款待处理或待确认',metrics.open_refunds,'处理中或需人工确认',function () {
          openTab('payment-refunds'); pages['payment-refunds'].selectStatus('PENDING,PROCESSING,UNKNOWN');
        }],
        ['变更待审批',metrics.pending_approvals,'需另一位管理员复核',function () {
          var filter = document.getElementById('approval-status-filter');
          if (filter) filter.value = 'PENDING';
          openTab('approval-inbox');
        }]
      ];
      var priority = metrics.unknown_payments + metrics.reconciliation_exceptions + metrics.webhook_failures;
      var total = issues.reduce(function (sum,item) { return sum + item[1]; },0);
      figures.cards[3].value.textContent = String(total).padStart(2,'0');
      figures.cards[3].note.textContent = priority + ' 项优先 · ' + (total - priority) + ' 项跟进';
      riskCount.textContent = priority + ' 项优先';
      riskPageCount.textContent = priority + ' 项优先';
      riskNavCount.textContent = String(priority); riskNavCount.hidden = !priority;
      warning.textContent = priority ? priority + ' 项异常需优先处理' : '暂无需优先处理的异常';
      noticeDetails.textContent = issues.slice(0,3).filter(function (issue) { return issue[1] > 0; }).map(function (issue) { return issue[0]; }).join('、');
      notice.classList.toggle('atlas-notice-clear',!priority);
      var ordered = issues.slice().sort(function (first,second) { return Number(Boolean(second[1])) - Number(Boolean(first[1])); });
      function renderIssues(list, items) {
        list.replaceChildren();
        items.forEach(function (issue) {
          var item = make('li');
          var row = issue[3] ? button('',issue[3],'atlas-risk-item') : make('div','atlas-risk-item');
          var copy = make('span'); copy.append(make('strong','',issue[0]),make('small','',issue[1] ? issue[1] + ' 条 · ' + issue[2] : '暂无记录'));
          var arrow = window.atlasIcon ? window.atlasIcon('chevron') : make('span','','›');
          row.append(make('span','atlas-risk-dot' + (issue[1] ? ' atlas-warning-dot' : '')),copy,arrow);
          item.appendChild(row); list.appendChild(item);
        });
      }
      renderIssues(riskList,ordered.slice(0,3));
      renderIssues(riskPageList,ordered);
    }
    function loadSummary() {
      if (summaryRequest) return summaryRequest;
      refresh.disabled = true;
      if (!summaryData) chartTarget.replaceChildren(make('p','atlas-empty','正在读取资金流向…'));
      summaryRequest = json('/operations/summary').then(function (data) {
        summaryData = data;
        var metrics = data.metrics, cards = figures.cards, settlementCards = settlementFigures.cards;
        dateText.textContent = data.business_date.replace(/-/g,'.');
        cards[0].note.textContent = '账面可出金余额 · 按币种';
        var today = metrics.captured_flow.filter(function (item) { return item.day === data.business_date; });
        cards[1].note.textContent = '今日已收款 · ' + today.reduce(function (count,item) { return count + item.count; },0) + ' 笔';
        cards[2].note.textContent = '已收款、尚未内部结算';
        settlementCards[0].note.textContent = '已收款、尚未内部结算';
        settlementCards[1].value.textContent = metrics.reconciliation_exceptions; settlementCards[1].note.textContent = '今日未匹配 · 需关注';
        settlementCards[2].value.textContent = metrics.settled_batches; settlementCards[2].note.textContent = data.business_date + ' · UTC+8';
        var currencies = Array.from(new Set(metrics.captured_flow.concat(metrics.paid_flow).map(function (item) { return item.currency; }))).sort();
        var previous = currencySelect.value;
        currencySelect.replaceChildren();
        if (!currencies.length) currencySelect.add(new Option('无币种',''));
        currencies.forEach(function (currency) { currencySelect.add(new Option(currency,currency)); });
        if (currencies.includes(previous)) currencySelect.value = previous;
        else if (currencies.includes('USD')) currencySelect.value = 'USD';
        currencySelect.disabled = currencies.length < 2;
        currencySelect.hidden = currencies.length < 2;
        renderFlow(); updateRisk(metrics);
      }).catch(function () {
        summaryData = null; dateText.textContent = '—'; warning.textContent = '无法读取统计，请刷新重试。';
        noticeDetails.textContent = '';
        figures.cards.concat(settlementFigures.cards).forEach(function (card) { card.value.textContent = '—'; card.note.textContent = '读取失败'; });
        chartTarget.replaceChildren(make('p','atlas-empty','无法读取资金流向。'));
        riskList.replaceChildren(); riskPageList.replaceChildren(); riskCount.textContent = '—'; riskPageCount.textContent = '—'; riskNavCount.hidden = true;
      }).finally(function () { summaryRequest = null; refresh.disabled = false; });
      return summaryRequest;
    }
    function loadRecent() {
      if (recentRequest) recentRequest.abort();
      var active = new AbortController(); recentRequest = active;
      recentInfo.textContent = '正在读取交易…';
      recentTable.render([],'正在读取交易…');
      var filters = new URLSearchParams({q:recentQuery.value.trim(),status:recentStatus.value});
      json('/operations/records/orders?' + filters,active.signal).then(function (data) {
        if (recentRequest !== active) return;
        recentTable.render(data.items,'暂无符合条件的交易。');
        recentInfo.textContent = '显示 ' + Math.min(data.items.length,5) + ' 条交易';
      }).catch(function () {
        if (recentRequest !== active) return;
        recentTable.render([],'无法读取交易，请刷新重试。'); recentInfo.textContent = '读取失败';
      });
    }
    var searchDelay;
    recentQuery.addEventListener('input',function () { clearTimeout(searchDelay); searchDelay = setTimeout(loadRecent,250); });
    recentStatus.addEventListener('change',function () { clearTimeout(searchDelay); loadRecent(); });
    tools.addEventListener('submit',function (event) { event.preventDefault(); clearTimeout(searchDelay); loadRecent(); });
    function loadDashboard() { loadSummary(); loadRecent(); }
    var show = window.showTab;
    window.showTab = function (key,control) {
      show(key,control);
      if (pages[key]) pages[key].load();
      if (key === 'overview') loadDashboard();
      if (key === 'payment-settlements' || key === 'risk-ops') loadSummary();
    };
    loadDashboard();
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded',start); else start();
}());
