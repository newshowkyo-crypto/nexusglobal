/* Presentation helpers only. API field names, permissions and monetary values stay authoritative. */
(function () {
  'use strict';
  var pages = {
    overview: ['资金总览', '查看交易处理、结算进度与待处理异常。', '⌂', '运营工作台 / 资金概览'],
    'merchant-onboarding': ['商户与站点', '维护商户资料、收款规则和外部站点的授权记录。', '◇'],
    'acquirer-channels': ['收单通道', '管理机构账户、凭证与通道启停。', '⇄'],
    'payment-orders': ['支付订单', '查询每次支付的状态。结果待确认时，先核对机构记录。', '▤'],
    'payment-refunds': ['退款与争议', '跟进退款状态，按原订单核对金额。', '↶'],
    'payment-settlements': ['结算管理', '按状态查阅结算批次及时间窗口。', '⇄', '运营工作台 / 结算'],
    'payment-payouts': ['出金', '查询出金申请及机构处理状态。', '▱'],
    'bank-vba': ['银行账户与入账', '管理银行收款账户、查看到账进度与对账结果。', '▥'],
    'approval-inbox': ['审批与审计', '复核重要变更，查看申请原因、变更内容和执行进度。', '✓'],
    'risk-ops': ['待办与风险', '查看风险提醒、冻结状态和待执行任务。', '⚑', '运营工作台 / 待办与风险'],
    'channel-risk': ['收款风险规则', '设置支付通道的风险规则，查看人工调整记录。', '⊙'],
    'merchant-reserve': ['商户准备金', '设置商户预留资金比例，查看持有和释放情况。', '◴'],
    'fee-allocation': ['费用明细', '查看支付、换汇和资金流转产生的费用。', '≋'],
    'pipeline-isolation': ['业务风险隔离', '按业务组隔离风险，查看冻结状态和跨组操作。', '⊞'],
    'compliance-rfi': ['合规资料', '准备订单、发票、合同和履约证明，跟进资料审核。', '▤'],
    'issuing-pools': ['虚拟卡管理', '管理发卡机构、卡池及充值方式。', '▱'],
    'universal-adapter': ['支付机构与接入', '管理支付服务机构和接入通道。首次接入通常需要技术人员配置。', '⌘'],
    'corporate-entity': ['经营主体', '维护实际开展业务的公司及其注册地区。', '▧'],
    'asset-bridge': ['币种兑换', '管理币种间的兑换规则、价格偏差和执行方式。', '↔'],
    'wallet-policy': ['收款钱包', '管理允许接收资金的钱包地址和用途。', '▣'],
    'design-system': ['组件规范', '确认颜色、文字层级与按钮行为后，再应用到实际后台。', '◇', '设计评审 / 蓝白金融风']
  };
  var words = {
    USD: '美元（USD）', SGD: '新加坡元（SGD）', HKD: '港币（HKD）', EUR: '欧元（EUR）', GBP: '英镑（GBP）', AED: '阿联酋迪拉姆（AED）',
    SG: '新加坡', HK: '中国香港', CN: '中国', US: '美国', GB: '英国', AE: '阿联酋',
    ACTIVE: '启用', DISABLED: '停用', PENDING: '待处理', APPLYING: '执行中', APPLIED: '已生效',
    REJECTED: '已拒绝', FAILED: '失败', FAILED_STALE: '变更冲突，需重新申请', RISK_HOLD: '风险冻结',
    COMPLETED: '已完成', PROCESSING: '处理中', AUTHORIZED: '已授权', CAPTURED: '收款成功',
    SETTLED: '已结算', REFUNDED: '已退款', PARTIALLY_REFUNDED: '部分退款', CANCELED: '已取消',
    EXPIRED: '已过期', CONFIRMED: '已确认', RECONCILED: '已对账', SUBMITTED: '已提交',
    UNKNOWN_EXTERNAL_STATE: '结果待确认', REQUIRES_ACTION: '等待付款验证', DEADLETTER: '需人工处理',
    TIER_2_COMPLIANT: '标准收款通道', TIER_1_HIGH_RISK: '需额外风控的通道',
    TIER_1_AGGREGATOR: '通过合作平台发卡', TIER_2_DIRECT: '直接连接发卡机构',
    CRYPTO_DESENSITIZED: '加密资产资金池', FIAT_DIRECT: '银行资金直接充值',
    DEFAULT: '标准处理', BURN_MODE: '专用隔离处理', STRICT: '严格处理', MANUAL_REVIEW: '人工复核',
    UNSUPPORTED: '尚未接通', NEXUS_ISSUING_HTTP_V1: '机构接口接入', MOCK_LOCAL: '本地模拟接入',
    'MOCK_LOCAL (仅本地)': '本地模拟接入（仅供演示）',
    HEALTHY: '正常', DEGRADED: '服务受限', DOWN: '不可用', UNKNOWN: '待确认',
    online: '实时提醒已连接', connecting: '正在连接实时提醒', disconnected: '实时提醒未连接',
    reconnecting: '正在重连实时提醒', local: '本地演示', production: '正式环境', staging: '测试环境',
    LOW: '低风险', MEDIUM: '中等风险', HIGH: '高风险', CRITICAL: '紧急',
    ALLOW: '放行', BLOCK: '拦截', COOLDOWN: '暂缓处理', MONITOR: '持续观察',
    LP_FEE: '资金服务费', FX_SLIPPAGE: '兑换价格偏差', GAS_COST: '链上手续费', GAS_FEE: '链上手续费', BANK_FEE: '银行手续费', NETWORK_FEE: '支付网络费',
    ALLOCATED: '已分摊', ROLLING_RESERVE: '滚动准备金', DISPUTE_RESERVE: '争议准备金', FORFEITED: '已扣除',
    CARD_ACQUIRER: '银行卡收款', BANK_COLLECTION: '银行转账收款', CRYPTO_PAY: '加密资产收款',
    CARD_ISSUER: '虚拟卡发卡', BANK_RAIL: '银行转账', ONCHAIN_WALLET: '链上钱包',
    ENABLE_ACQUIRER_CHANNEL: '启用收款通道', DISABLE_ACQUIRER_CHANNEL: '停用收款通道',
    ENABLE_ISSUING_POOL: '启用发卡卡池', DISABLE_ISSUING_POOL: '停用发卡卡池',
    SYSTEM_LOCKDOWN: '暂停系统资金操作', SYSTEM_RESUME: '恢复系统资金操作',
    FREEZE: '冻结业务组', UNFREEZE: '解除业务组冻结', RFI_APPROVE: '通过合规资料', RFI_REJECT: '拒绝合规资料'
  };
  function node(tag, cls, text) {
    var el = document.createElement(tag);
    if (cls) el.className = cls;
    if (text !== undefined) el.textContent = text;
    return el;
  }
  function translated(value) { return words[value] || value; }
  var atlasPaths = {
    mark:['M5 18V6l14 12V6','M5 12h3m8 0h3'],
    overview:['M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z'],
    'payment-settlements':['M4 8h16l-4-4M20 16H4l4 4'],
    'risk-ops':['M5 21V4m0 0c5-3 9 4 14 1v10c-5 3-9-4-14-1'],
    'design-system':['m12 3 9 5-9 5-9-5 9-5zM3 12l9 5 9-5M3 16l9 5 9-5'],
    calendar:['M4 6h16v15H4zM8 3v6m8-6v6M4 11h16'],
    chevron:['m9 5l7 7-7 7'],
    download:['M12 3v12m-4-4 4 4 4-4M4 16v4h16v-4'],
    search:['M11 18a7 7 0 1 1 0-14 7 7 0 0 1 0 14zM21 21l-4.3-4.3'],
    warning:['M12 3 2 20h20L12 3zM12 9v4m0 3h.01']
  };
  function atlasIcon(name) {
    var paths = atlasPaths[name];
    if (!paths) return null;
    var ns = 'http://www.w3.org/2000/svg', svg = document.createElementNS(ns,'svg');
    svg.setAttribute('viewBox','0 0 24 24'); svg.setAttribute('aria-hidden','true'); svg.classList.add('atlas-icon');
    paths.forEach(function (data) { var path = document.createElementNS(ns,'path'); path.setAttribute('d',data); svg.appendChild(path); });
    return svg;
  }
  window.atlasIcon = atlasIcon;
  function messageRow(id, text, columns) {
    var tbody = document.getElementById(id); if (!tbody) return;
    var tr = node('tr'), td = node('td', 'empty', text); td.colSpan = columns; tr.appendChild(td); tbody.replaceChildren(tr);
  }
  function detailDialog(title, pairs) {
    var overlay = node('div', 'modal'); overlay.setAttribute('role', 'dialog'); overlay.setAttribute('aria-modal', 'true'); overlay.setAttribute('aria-label', title);
    var panel = node('div', 'modal-content'); panel.appendChild(node('h3', '', title));
    pairs.forEach(function (pair) {
      var field = node('div', 'form-group'); field.appendChild(node('label', '', pair[0]));
      var value = node('div', 'detail-value', translated(String(pair[1] === undefined || pair[1] === null || pair[1] === '' ? '未提供' : pair[1])));
      field.appendChild(value); panel.appendChild(field);
    });
    var close = node('button', 'submit-btn', '关闭'); close.type = 'button';
    var previous = document.activeElement;
    function dismiss() { overlay.remove(); if (previous) previous.focus(); }
    close.addEventListener('click', dismiss); panel.appendChild(close); overlay.appendChild(panel); document.body.appendChild(overlay); close.focus();
    overlay.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') dismiss();
      if (e.key === 'Tab') { e.preventDefault(); close.focus(); }
    });
  }
  function renderList(id, items, columns, values, detail) {
    var tbody = document.getElementById(id); tbody.replaceChildren();
    if (!items.length) { messageRow(id, '暂无记录', columns); return; }
    items.forEach(function (item) {
      var row = node('tr'); values(item).forEach(function (value) { row.appendChild(node('td', '', translated(String(value === undefined || value === null || value === '' ? '未提供' : value)))); });
      if (detail) {
        var td = node('td'), btn = node('button', 'action-btn', '详情'); btn.type = 'button'; btn.addEventListener('click', function () { detail(item); }); td.appendChild(btn); row.appendChild(td);
      }
      tbody.appendChild(row);
    }); beautifyTables();
  }
  function loadList(path, id, columns, render) {
    messageRow(id, '正在加载…', columns);
    authFetch(API_BASE + path).then(function (r) { if (!r.ok) throw new Error('加载失败'); return r.json(); }).then(function (data) { render(Array.isArray(data) ? data : (data && data.items) || []); }).catch(function () { messageRow(id, '暂时无法加载，请刷新重试。', columns); });
  }
  window.loadFeeAllocations = function () {
    loadList('/v3/fee-allocations', 'fee-allocations-table', 8, function (items) {
      renderList('fee-allocations-table', items, 8, function (i) { return [i.transaction_id, referenceNames[i.merchant_id] || i.merchant_id, translated(i.fee_type), i.amount, i.currency, translated(i.status), ft(i.created_at)]; }, function (i) {
        detailDialog('费用详情', [['交易编号', i.transaction_id], ['商户', referenceNames[i.merchant_id] || i.merchant_id], ['费用类型', translated(i.fee_type)], ['金额', i.amount], ['币种', translated(i.currency)], ['状态', translated(i.status)], ['创建时间', ft(i.created_at)]]);
      });
    });
  };
  window.loadRollingReserves = function () {
    loadList('/v3/rolling-reserves', 'reserve-entries-table', 8, function (items) {
      renderList('reserve-entries-table', items, 8, function (i) { return [referenceNames[i.merchant_id] || i.merchant_id, i.transaction_id, i.amount, reserveTypeCN[i.reserve_type] || i.reserve_type, ft(i.release_date), reserveStatusCN[i.status] || translated(i.status), ft(i.created_at)]; }, function (i) {
        detailDialog('准备金详情', [['商户', referenceNames[i.merchant_id] || i.merchant_id], ['交易编号', i.transaction_id], ['金额（原账本单位）', i.amount], ['准备金类型', reserveTypeCN[i.reserve_type] || i.reserve_type], ['预计释放日期', ft(i.release_date)], ['状态', reserveStatusCN[i.status] || translated(i.status)]]);
      });
    });
  };
  window.loadRiskEvents = function () {
    loadList('/risk/events', 'risk-events-table', 8, function (items) {
      renderList('risk-events-table', items, 8, function (i) { return [i.id, referenceNames[i.merchant_id] || i.merchant_id, i.site_id, i.channel_id, translated(i.decision), i.reason, ft(i.created_at)]; }, function (i) {
        detailDialog('风险提醒详情', [['商户', referenceNames[i.merchant_id] || i.merchant_id], ['站点', i.site_id], ['处理结果', translated(i.decision)], ['原因', i.reason], ['发生时间', ft(i.created_at)]]);
      });
    });
  };
  window.loadRiskOverrides = function () {
    loadList('/v3/override-audit-logs', 'risk-overrides-table', 8, function (items) {
      renderList('risk-overrides-table', items, 8, function (i) { return [i.id, i.entity_type, i.entity_id, overrideTypeCN[i.override_type] || i.override_type, i.reason, i.operator, ft(i.expires_at), ft(i.created_at)]; });
    });
  };
  function directText(el, value) {
    Array.from(el.childNodes).forEach(function (part) {
      if (part.nodeType === Node.TEXT_NODE && part.textContent.trim()) part.textContent = value + ' ';
    });
  }
  function setHelp(input, text) {
    if (!input) return;
    var help = node('small', 'field-help', text);
    help.id = input.id + '-help';
    input.setAttribute('aria-describedby', help.id);
    input.parentElement.appendChild(help);
  }
  var referenceCache = {};
  var referenceNames = {};
  var referenceLoaders = {};
  function records(path, refresh) {
    if (!referenceCache[path] || refresh) {
      referenceCache[path] = authFetch(API_BASE + path).then(function (r) { if (!r.ok) throw new Error('加载失败'); return r.json(); }).then(function (d) {
        var items = Array.isArray(d) ? d : (d && d.items) || [];
        items.forEach(function (item) {
          var name = item.merchant_name || item.name || item.channel_name || item.display_name;
          if (name && item.id) referenceNames[item.id] = name;
          if (name && item.tenant_id) referenceNames[item.tenant_id] = name;
        });
        return items;
      }).catch(function (error) { delete referenceCache[path]; throw error; });
    }
    return referenceCache[path];
  }
  function selector(id, path, key, label, optional) {
    var original = document.getElementById(id);
    if (!original) return;
    if (original.dataset.businessReference && referenceLoaders[id]) { referenceLoaders[id](false); return; }
    var select;
    if (original.tagName === 'SELECT') select = original;
    else {
      select = node('select'); select.id = id; select.value = original.value;
      original.replaceWith(select);
    }
    select.dataset.businessReference = 'true';
    var note = node('small', 'reference-note'); select.parentElement.appendChild(note);
    function load(refresh) {
      var previous = select.value;
      select.replaceChildren(new Option('正在加载可选项目…', ''));
      select.disabled = true;
      records(path, refresh).then(function (items) {
        select.replaceChildren(new Option(optional ? '暂不关联（可选）' : '请选择' + label, ''));
        var seen = {};
        items.forEach(function (item) {
          var value = item[key];
          if (!value || seen[value]) return;
          seen[value] = true;
          var name = item.merchant_name || item.name || item.channel_name || item.display_name || item.mae_id || label;
          var suffix = items.filter(function (other) {
            return (other.merchant_name || other.name || other.channel_name || other.display_name) === name;
          }).length > 1 ? ' · ' + String(value).slice(0, 8) : '';
          select.add(new Option(name + suffix, value));
        });
        if (previous && seen[previous]) select.value = previous;
        select.disabled = items.length === 0;
        note.textContent = items.length ? '按名称选择，系统会自动关联对应记录。' : '暂无可选项目，请先添加' + label + '。';
        note.className = 'reference-note';
        beautifyTables();
      }).catch(function () {
        select.replaceChildren(new Option('加载失败，请重新打开此页面', ''));
        select.disabled = true;
        note.textContent = '未能取得列表，请检查权限或稍后重试。';
        note.className = 'reference-note field-error';
        select.dataset.businessReference = '';
      });
    }
    referenceLoaders[id] = load;
    load(false);
    select.addEventListener('focus', function () { if (!select.options.length) load(true); });
  }
  function refreshReferences(tab) {
    var section = document.getElementById('tab-' + tab);
    section.querySelectorAll('input[id$="-tenant"], input[id$="-tenant-id"], select[data-tenant-reference]').forEach(function (el) {
      el.dataset.tenantReference = 'true'; selector(el.id, '/api/v3/merchants', 'tenant_id', '商户', false);
    });
    if (tab === 'merchant-reserve') selector('new-reserve-merchant', '/api/v3/merchants', 'id', '商户', false);
    if (tab === 'issuing-pools') selector('new-iss-entity-id', '/entities', 'id', '经营主体', true);
    if (tab === 'merchant-onboarding') selector('new-merchant-mae-config', '/mae-list', 'id', '收款路由配置', true);
    if (tab === 'channel-risk') selector('new-risk-channel', '/v3/channel-risk', 'id', '收款通道', false);
  }
  function translateNodeText(root) {
    var targets = [];
    if (root.nodeType === 1 && root.matches('option,.badge,.sse-status,.radio-card-desc,.radio-card-title,td')) targets.push(root);
    if (root.querySelectorAll) targets = targets.concat(Array.from(root.querySelectorAll('option,.badge,.sse-status,.radio-card-desc,.radio-card-title,td')));
    targets.forEach(function (el) {
      if (!el.children.length) {
        var text = el.textContent.trim();
        if (words[text]) el.textContent = words[text];
      }
    });
  }
  function beautifyTables() {
    document.querySelectorAll('.tab table').forEach(function (table) {
      var headers = Array.from(table.querySelectorAll('thead th'));
      headers.forEach(function (th, index) {
        var technical = ['记录编号', '账户编号', '源数据哈希', '支付机构代码', '通道代码', '上次重载', '卡池代码'].indexOf(th.textContent.trim()) !== -1;
        th.classList.toggle('technical-column', technical);
        table.querySelectorAll('tbody tr').forEach(function (row) {
          if (row.cells.length !== headers.length) return;
          var td = row.cells[index];
          td.classList.toggle('technical-column', technical);
          if (!td.children.length && (td.dataset.referenceId || /^[a-f\d]{8}-[a-f\d-]{27}$/i.test(td.textContent.trim()))) {
            var value = td.dataset.referenceId || td.textContent.trim();
            td.dataset.referenceId = value;
            var display = !technical && referenceNames[value] ? referenceNames[value] : value.slice(0, 8) + '…';
            if (td.textContent !== display) td.textContent = display;
            td.title = '完整编号：' + value;
            td.classList.toggle('technical-id', display !== referenceNames[value]);
          }
        });
      });
    });
  }
  function initialize() {
    var nav = document.querySelector('.nav');
    function navButton(key) { return Array.from(nav.querySelectorAll('[data-tab]')).find(function (btn) { return btn.dataset.tab === key; }); }
    var groups = [
      ['工作台', ['overview', 'payment-settlements', 'risk-ops']],
      ['设计评审', ['design-system']],
      ['支付运营', ['merchant-onboarding', 'payment-orders', 'acquirer-channels', 'payment-refunds', 'payment-payouts', 'approval-inbox']],
      ['资金与风险', ['bank-vba', 'merchant-reserve', 'fee-allocation', 'channel-risk', 'pipeline-isolation', 'compliance-rfi']],
      ['业务配置', ['issuing-pools', 'universal-adapter', 'corporate-entity', 'asset-bridge', 'wallet-policy']]
    ];
    var operationMenu = node('details','atlas-operations-nav');
    operationMenu.appendChild(node('summary','','全部业务功能'));
    groups.forEach(function (group,index) {
      var container = index < 2 ? nav : operationMenu;
      container.appendChild(node('div', 'nav-group-label' + (index === 1 ? ' atlas-nav-divider' : ''), group[0]));
      group[1].forEach(function (key) {
        var btn = navButton(key);
        btn.textContent = pages[key][0];
        var icon = node('span', 'nav-icon'); icon.setAttribute('aria-hidden', 'true');
        icon.append(atlasIcon(key) || document.createTextNode(pages[key][2]));
        btn.prepend(icon); container.appendChild(btn);
      });
    });
    nav.appendChild(operationMenu);
    var logo = nav.querySelector('.logo');
    var wordmark = node('span', 'atlas-wordmark');
    wordmark.append(node('strong', '', 'NexusGlobal'), node('small', '', '资金运营台'));
    var mark = node('span', 'atlas-brand-mark'); mark.appendChild(atlasIcon('mark'));
    logo.replaceChildren(mark, wordmark);
    var footer = node('div', 'nav-footer'), avatar = node('span', 'atlas-avatar', 'NG');
    avatar.setAttribute('aria-hidden', 'true');
    var footerLabel = node('span'); footerLabel.append(node('strong', '', '运营工作台'), node('small', '', '验收环境 · 隔离数据库'));
    var logout = document.getElementById('logout-btn');
    if (logout) {
      logout.classList.add('atlas-footer-logout'); logout.textContent = '退出登录';
      if (logout.parentElement && logout.parentElement.tagName === 'SPAN') logout.parentElement.remove();
    }
    footer.append(avatar, footerLabel); if (logout) footer.appendChild(logout); nav.appendChild(footer);
    var main = document.querySelector('.main');
    var top = node('header', 'workspace-top');
    var crumb = node('div', 'workspace-breadcrumb', '机构运营');
    var separator = atlasIcon('chevron'); separator.setAttribute('aria-hidden', 'true');
    var current = node('strong', '', '资金总览'); current.id = 'workspace-current'; crumb.append(separator, current); top.appendChild(crumb);
    var meta = node('div', 'workspace-meta');
    var chip = node('span', 'environment-chip', '演示数据');
    var environment = document.querySelector('#tab-overview .stat:nth-child(4) .value');
    if (environment) chip.title = '运行环境：' + environment.textContent.trim();
    meta.appendChild(chip);
    var kitLink = node('button','atlas-button atlas-top-kit atlas-compact','查看规范'); kitLink.type = 'button';
    kitLink.addEventListener('click',function () { navButton('design-system').click(); }); meta.appendChild(kitLink);
    top.appendChild(meta); main.prepend(top);
    var mobileNav = node('select', 'mobile-navigation'); mobileNav.setAttribute('aria-label', '切换管理功能');
    groups.forEach(function (group) {
      var optgroup = node('optgroup'); optgroup.label = group[0];
      group[1].forEach(function (key) { optgroup.appendChild(new Option(pages[key][0], key)); }); mobileNav.appendChild(optgroup);
    });
    var mobileShell = node('div','atlas-mobile-shell');
    var mobileQuick = node('div','atlas-mobile-quick');
    var mobileEntries = groups[0][1].concat(groups[1][1]).map(function (key) {
      var control = node('button','',pages[key][0]); control.type = 'button';
      control.prepend(atlasIcon(key)); control.setAttribute('aria-current',key === 'overview' ? 'page' : 'false');
      control.addEventListener('click',function () { navButton(key).click(); }); mobileQuick.appendChild(control);
      return [key,control];
    });
    var mobileMore = node('details','atlas-mobile-more'); mobileMore.append(node('summary','','全部业务功能'),mobileNav);
    mobileShell.append(logo.cloneNode(true),mobileQuick,mobileMore);
    if (logout) { var mobileLogout = logout.cloneNode(true); mobileLogout.removeAttribute('id'); mobileLogout.classList.add('atlas-mobile-logout'); mobileShell.appendChild(mobileLogout); }
    mobileShell.addEventListener('click', function (event) {
      if (event.target.closest('.atlas-mobile-logout') && window.doLogout) { event.preventDefault(); window.doLogout(); }
    });
    main.prepend(mobileShell);
    mobileNav.addEventListener('change', function () { navButton(mobileNav.value).click(); });
    Object.keys(pages).forEach(function (key) {
      var tab = document.getElementById('tab-' + key);
      var heading = node('div', 'page-heading');
      var headingCopy = node('div');
      headingCopy.appendChild(node('div', 'eyebrow', pages[key][3] || ('运营工作台 / ' + pages[key][0])));
      var h1 = node('h1', '', pages[key][0]); h1.tabIndex = -1; headingCopy.appendChild(h1);
      if (pages[key][1]) headingCopy.appendChild(node('p', '', pages[key][1]));
      heading.appendChild(headingCopy);
      if (key === 'design-system') {
        var review = node('span', 'environment-chip', 'V2 · 审阅稿');
        review.title = '当前审阅稿版本';
        heading.appendChild(review);
      }
      tab.prepend(heading);
      var options = node('div', 'display-options');
      var label = node('label'); var checkbox = node('input'); checkbox.type = 'checkbox';
      checkbox.addEventListener('change', function () { tab.classList.toggle('show-technical', checkbox.checked); });
      label.appendChild(checkbox); label.appendChild(document.createTextNode('显示技术编号')); options.appendChild(label);
      if (key !== 'overview' && key !== 'design-system' && key !== 'risk-ops' && !key.startsWith('payment-')) heading.after(options);
    });
    var overview = document.getElementById('tab-overview');
    var systemGrid = overview.querySelector('.grid');
    var system = node('details', 'system-details'); system.appendChild(node('summary', '', '系统运行详情 · 数据连接、运行环境与资源使用'));
    systemGrid.replaceWith(system); system.appendChild(systemGrid);
    var risk = overview.querySelector('.section');
    var safety = node('details', 'safety-details'); safety.appendChild(node('summary', '', '紧急安全操作 · 暂停资金操作、冻结虚拟卡'));
    risk.replaceWith(safety); safety.appendChild(risk);
    var stats = document.getElementById('stats-content').closest('.section');
    directText(stats.querySelector('h2'), '业务规模');
    system.appendChild(stats);
    overview.append(system, safety);
    directText(risk.querySelector('h2'), '紧急安全操作');
    risk.querySelector('[data-action="triggerSystemLockdown"]').textContent = '暂停全部资金操作';
    risk.querySelector('[data-action="suspendActiveCards"]').textContent = '冻结全部在用虚拟卡';
    var technicalBanners = {
      'universal-adapter': '先添加支付机构，再为商户配置接入通道。银行收款账户请在「银行账户与入账」中管理。',
      'acquirer-channels': '选择适合商户业务的场景模板，再配置支付机构。部分规则会随模板固定；重要变更需要复核。',
      'approval-inbox': '重要变更需由另一位管理员复核。申请人不能审批自己的申请；批准后可在详情中跟进生效与同步进度。'
    };
    Object.keys(technicalBanners).forEach(function (key) {
      var banner = document.querySelector('#tab-' + key + ' > .alert-banner, #tab-' + key + ' .section > .alert-banner');
      if (banner) banner.textContent = technicalBanners[key];
    });
    document.querySelectorAll('.card').forEach(function (card) {
      if (!card.querySelector('input,select') || !card.querySelector('h3')) return;
      var details = node('details', 'form-disclosure'); details.appendChild(node('summary', '', card.querySelector('h3').textContent));
      card.replaceWith(details); details.appendChild(card);
      details.addEventListener('toggle', function () {
        if (!details.open) return;
        var key = details.closest('.tab').id.replace('tab-', ''); refreshReferences(key);
      });
    });
    var advanced = ['new-merchant-webhook-url','new-merchant-webhook-secret','new-merchant-mae-config','new-merchant-bank-account','new-acq-vault-key','new-iss-provider-code','new-iss-pool-code','new-iss-vault-key','new-iss-adapter-type','new-iss-adapter-provider-id','new-iss-base-url'];
    var advancedGroups = new Map();
    advanced.forEach(function (id) {
      var field = document.getElementById(id); if (!field) return;
      var grid = field.closest('.form-grid'); if (!grid) return;
      var details = advancedGroups.get(grid);
      if (!details) {
        details = node('details', 'advanced-settings'); details.appendChild(node('summary', '', '技术接入设置 · 连接支付机构或系统通知时填写'));
        details.appendChild(node('div', 'form-grid')); grid.appendChild(details); advancedGroups.set(grid, details);
      }
      details.querySelector('.form-grid').appendChild(field.closest('.form-group'));
    });
    document.querySelectorAll('.form-group label').forEach(function (label) {
      var control = label.parentElement.querySelector('input,select,textarea');
      if (control && control.id) label.htmlFor = control.id;
      if (control && /万分比/.test(label.textContent)) {
        var help = node('small', 'field-help');
        function update() { var value = Number(control.value); help.textContent = Number.isFinite(value) ? '当前比例：' + (value / 100).toFixed(2) + '%。例如填写 1000 表示 10%。' : '请填写有效比例。'; }
        control.addEventListener('input', update); label.parentElement.appendChild(help); update();
      }
    });
    var riskLabel = document.getElementById('new-merchant-risk-label');
    if (riskLabel) {
      var riskSelect = node('select'); riskSelect.id = riskLabel.id;
      [['', '暂不设置'], ['LOW', '低风险'], ['MEDIUM', '中等风险'], ['HIGH', '高风险']].forEach(function (option) { riskSelect.add(new Option(option[1], option[0])); });
      riskLabel.replaceWith(riskSelect);
    }
    setHelp(document.getElementById('new-provider-code'), '由支付机构提供的接入代码，首次接入时填写。');
    setHelp(document.getElementById('new-merchant-jurisdiction'), '填写注册地区代码，例如：中国 CN、新加坡 SG、美国 US。');
    setHelp(document.getElementById('new-sa-accumulated-minor'), '这里按最小货币单位录入：美元 100 表示 1 美元。币种不同，最小单位可能不同。');
    setHelp(document.getElementById('new-rfi-order'), '填写系统中的订单编号，用于关联对应的业务资料。');
    document.querySelectorAll('.tab table').forEach(function (table) { var wrap = node('div', 'table-scroll'); table.replaceWith(wrap); wrap.appendChild(table); });
    var feeSummary = document.getElementById('fee-stats-content');
    feeSummary.className = 'empty'; feeSummary.textContent = '全量费用汇总尚未接入。上方显示最近的费用记录，金额需结合币种查看。';
    messageRow('cross-group-operations-table', '跨组操作记录查询尚未接入。请先查看上方业务组状态。', 5);
    var originalShowTab = window.showTab;
    window.showTab = function (key, button) {
      originalShowTab(key, button); current.textContent = pages[key][0];
      mobileNav.value = key;
      mobileEntries.forEach(function (entry) { entry[1].setAttribute('aria-current',entry[0] === key ? 'page' : 'false'); });
      if (operationMenu.contains(button)) operationMenu.open = true;
      if (key === 'approval-inbox') loadApprovalInbox();
      if (key === 'channel-risk') loadRiskOverrides();
      nav.querySelectorAll('[data-tab]').forEach(function (btn) { btn.setAttribute('aria-current', btn === button ? 'page' : 'false'); });
      refreshReferences(key); document.getElementById('tab-' + key).querySelector('h1').focus({preventScroll:true});
      window.scrollTo({top:0,behavior:'instant'});
    };
    var scheduled = false;
    new MutationObserver(function (mutations) {
      mutations.forEach(function (m) {
        if (m.target.matches && m.target.matches('.sse-status,option,.badge')) translateNodeText(m.target);
        m.addedNodes.forEach(function (el) { if (el.nodeType === 1) translateNodeText(el); });
      });
      if (!scheduled) { scheduled = true; requestAnimationFrame(function () { scheduled = false; beautifyTables(); }); }
    }).observe(main, {childList:true, subtree:true});
    translateNodeText(document); beautifyTables();
    var originalLoadMerchants = window.loadMerchants;
    window.loadMerchants = function () { delete referenceCache['/api/v3/merchants']; originalLoadMerchants(); };
  }
  // Percentages are a display convention. The API continues to receive integer basis points.
  window.readRateBps = function (id) {
    var input = document.getElementById(id), value = input.value.trim();
    if (!/^\d{1,3}(\.\d{1,2})?$/.test(value) || Number(value) > 100) {
      input.setAttribute('aria-invalid', 'true'); input.focus(); alert('比例需填写 0 到 100 之间的百分数，最多保留两位小数。'); return null;
    }
    input.removeAttribute('aria-invalid');
    var parts = value.split('.');
    return Number(parts[0]) * 100 + Number(((parts[1] || '') + '00').slice(0, 2));
  };
  window.formatRate = function (value) { return Number.isFinite(Number(value)) ? (Number(value) / 100).toFixed(2) + '%' : '未设置'; };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initialize);
  else initialize();
}());
