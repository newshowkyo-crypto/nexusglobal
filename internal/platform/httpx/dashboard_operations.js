/* Business workflows use the existing authenticated and approved server actions. */
(function () {
  'use strict';
  function el(tag, cls, text) { var e = document.createElement(tag); if (cls) e.className = cls; if (text !== undefined) e.textContent = text; return e; }
  function request(path, options) { return authFetch(API_BASE + path, options).then(function (r) { return r.json(); }); }
  function start() {
    var page = document.getElementById('tab-overview');
    var panel = el('section', 'section opening-checks');
    panel.appendChild(el('h2', '', '收款开通检查'));
    panel.appendChild(el('p', 'field-help', '逐项确认收款条件。已填写资料与已通过真实交易核对会分别说明。'));
    var checks = el('div', 'opening-grid'); panel.appendChild(checks);
    var reload = el('button', 'refresh-btn', '重新检查'); reload.type = 'button'; panel.appendChild(reload);
    var notice = el('p', 'field-help'); panel.appendChild(notice);
    var opening = el('details', 'system-details');
    var caption = el('summary', '', '收款开通检查 · ');
    var openingState = el('span', '', '检查中'); openingState.id = 'opening-state';
    caption.appendChild(openingState); opening.append(caption, panel); page.appendChild(opening);
    function readiness() {
      reload.disabled = true; checks.replaceChildren(el('p', '', '正在检查…'));
      request('/operations/readiness').then(function (data) {
        checks.replaceChildren(); var missing = 0;
        data.checks.forEach(function (item) {
          if (!item.configured) missing++;
          var card = el('div', 'opening-item'); card.appendChild(el('span', item.configured ? 'check-dot configured' : 'check-dot', item.configured ? '✓' : '○'));
          var text = el('div'); text.appendChild(el('strong', '', item.name)); text.appendChild(el('small', '', item.status)); card.appendChild(text); checks.appendChild(card);
        });
        notice.textContent = data.notice;
        openingState.textContent = missing ? '有未配置项' : '待机构联调';
      }).catch(function () { checks.replaceChildren(el('p', 'field-error', '暂时无法检查，请稍后重试。')); openingState.textContent = '检查失败'; }).finally(function () { reload.disabled = false; });
    }
    reload.addEventListener('click', readiness); readiness();
    // Operational staff work with named businesses, not internal record identifiers.
    var section = el('section', 'section'); section.appendChild(el('h2', '', '商户收款规则'));
    section.appendChild(el('p', 'field-help', '选择商户及收款类别，提交后由另一位管理员复核。批准后即可在收款通道中选择该商户。'));
    var form = el('form', 'operations-form');
    var grid = el('div', 'form-grid'); form.appendChild(grid);
    function select(label, choices) { var wrap = el('div', 'form-group'); var name = el('label', '', label); var input = el('select'); input.required = true; choices.forEach(function (c) { input.add(new Option(c[1], c[0])); }); name.appendChild(input); wrap.appendChild(name); grid.appendChild(wrap); return input; }
    var merchant = select('商户', [['', '请选择商户']]);
    var tier = select('收款类别', [['TIER_2_COMPLIANT', '标准收款'], ['TIER_1_HIGH_RISK', '需额外审核的收款']]);
    var submit = el('button', 'submit-btn', '提交收款规则'); submit.type = 'submit'; form.appendChild(submit);
    var result = el('p', 'operation-result'); result.setAttribute('role', 'status'); form.appendChild(result); section.appendChild(form);
    document.querySelector('#tab-merchant-onboarding .section').after(section);
    function merchants() { request('/api/v3/merchants').then(function (data) { var previous = merchant.value; merchant.replaceChildren(new Option('请选择商户', '')); (data.items || []).forEach(function (m) { merchant.add(new Option(m.merchant_name, m.id)); }); merchant.value = previous; }).catch(function () { result.textContent = '商户列表暂时无法读取，请刷新后重试。'; }); }
    form.addEventListener('submit', function (event) {
      event.preventDefault(); if (!merchant.value) return; submit.disabled = true; result.textContent = '正在提交…';
      request('/api/v3/merchant-policies', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({merchant_id:merchant.value, provider_tier:tier.value, status:'ACTIVE'})}).then(function () { result.textContent = '已提交，请由另一位管理员到审批中心复核。'; }).catch(function (error) { result.textContent = plainError(error); }).finally(function () { submit.disabled = false; });
    });
    var setupPanel = el('section', 'section'); setupPanel.appendChild(el('h2', '', '机构接入资料'));
    setupPanel.appendChild(el('p', 'field-help', '选择商户后填写。资料可保存并再次修改；账户验证信息请在收款通道的“连接账户”中安全填写。'));
    var setupForm = el('form', 'operations-form'), setupGrid = el('div', 'form-grid'); setupForm.appendChild(setupGrid);
    function setupField(label, field, type) { var wrap=el('div','form-group'), name=el('label','',label), input=el(type==='select'?'select':'input'); input.name=field; if(type!=='select') input.type=type||'text'; input.maxLength=type==='url'?2048:120; name.appendChild(input); wrap.appendChild(name); setupGrid.appendChild(wrap); return input; }
    var setupMerchant=setupField('所属商户','merchant','select'); setupMerchant.required=true; setupMerchant.add(new Option('请选择商户',''));
    var providerName=setupField('支付机构名称','provider_name','text'); providerName.placeholder='填写已经申请或计划接入的机构名称';
    var setupCountry=setupField('公司注册地区','country','select');
    var serviceURL=setupField('正式服务网址','service_url','url'); serviceURL.placeholder='https://pay.example.com';
    var salesURL=setupField('实际销售网站','sales_url','url'); salesURL.placeholder='https://shop.example.com';
    var saveSetup=el('button','submit-btn','保存开通资料'); saveSetup.type='submit'; saveSetup.disabled=true; setupForm.appendChild(saveSetup);
    var setupResult=el('p','operation-result'); setupResult.setAttribute('role','status'); setupForm.appendChild(setupResult); setupPanel.appendChild(setupForm); section.after(setupPanel);
    var setupFields={provider_name:providerName,country:setupCountry,service_url:serviceURL,sales_url:salesURL};
    function readSetup() { saveSetup.disabled=true; Object.values(setupFields).forEach(function(input){input.value='';input.disabled=true;}); if(!setupMerchant.value)return; var requested=setupMerchant.value; setupResult.textContent='正在读取资料…'; request('/api/v3/merchants/'+encodeURIComponent(requested)+'/connection-setup').then(function(data){if(setupMerchant.value!==requested)return; Object.keys(setupFields).forEach(function(key){setupFields[key].value=data[key]||'';setupFields[key].disabled=false;}); saveSetup.disabled=false; setupResult.textContent='已读取保存的资料，可以继续填写。';}).catch(function(){if(setupMerchant.value===requested)setupResult.textContent='资料暂时无法读取，请稍后重新选择商户。';}); }
    setupMerchant.addEventListener('change',readSetup);
    setupForm.addEventListener('submit',function(event){event.preventDefault(); if(!setupMerchant.value)return; var data={}; Object.keys(setupFields).forEach(function(key){data[key]=setupFields[key].value.trim();}); saveSetup.disabled=true; request('/api/v3/merchants/'+encodeURIComponent(setupMerchant.value)+'/connection-setup',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)}).then(function(data){setupResult.textContent=data.message;}).catch(function(error){setupResult.textContent=plainError(error);}).finally(function(){saveSetup.disabled=false;});});
    function setupMerchants(){request('/api/v3/merchants').then(function(data){var previous=setupMerchant.value; setupMerchant.replaceChildren(new Option('请选择商户','')); (data.items||[]).forEach(function(m){setupMerchant.add(new Option(m.merchant_name,m.id));}); setupMerchant.value=previous;}).catch(function(){setupResult.textContent='商户列表暂时无法读取。';});}
    function fees() {
      var target = document.getElementById('fee-stats-content'); target.replaceChildren(el('p', '', '正在汇总…'));
      request('/v3/fee-summary').then(function (data) {
        target.className = 'fee-summary'; target.replaceChildren();
        if (!data.items.length) { target.appendChild(el('p', 'empty', '暂无费用记录')); return; }
        var titles = {LP_FEE:'资金服务费',FX_SLIPPAGE:'兑换价差',GAS_FEE:'链上手续费',BANK_FEE:'银行手续费',NETWORK_FEE:'支付网络费'};
        data.items.forEach(function (i) { var c = el('div', 'summary-card'); c.appendChild(el('strong', '', titles[i.fee_type] || '业务费用')); c.appendChild(el('div', 'summary-amount', i.amount + ' ' + i.currency)); c.appendChild(el('small', '', i.count + ' 笔记录')); target.appendChild(c); });
      }).catch(function () { target.replaceChildren(el('p', 'field-error', '费用暂时无法汇总，请稍后重试。')); });
    }
    var guide = el('section', 'section');
    guide.appendChild(el('h2', '', '商户接入向导'));
    guide.appendChild(el('p', 'field-help', '按顺序完成接入步骤；待完善的步骤不能代表已开通真实收款。'));
    var steps = el('div', 'journey');
    [
      ['1', '创建商户', '填写公司登记资料，并保存机构接入资料。', 'merchant-onboarding'],
      ['2', '申请收款规则', '提交商户收款规则，由另一位管理员复核。', 'approval-inbox'],
      ['3', '配置收款通道', '选择商户和支付机构，安全连接账户。', 'acquirer-channels'],
      ['4', '登记站点与授权', '先登记销售域名。站点凭证和通道授权流程仍在完善。', 'merchant-onboarding'],
      ['5', '核对支付与结算', '先在隔离环境联调支付、回调、退款和对账。', 'bank-vba']
    ].forEach(function (step) {
      var box = el('div', 'journey-step');
      box.appendChild(el('span', 'journey-num', step[0]));
      box.appendChild(el('strong', '', step[1]));
      box.appendChild(el('p', '', step[2]));
      if (step[3]) {
        var button = el('button', 'refresh-btn', '前往'); button.type = 'button';
        button.addEventListener('click', function () {
          var target = Array.from(document.querySelectorAll('.nav button')).find(function (button) { return button.dataset.tab === step[3]; });
          if (target) target.click();
          if (step[0] === '4') {
            var draft = document.getElementById('site-registration');
            draft.open = true;
            draft.scrollIntoView({block:'start'});
            draft.querySelector('summary').focus({preventScroll:true});
          }
        });
        box.appendChild(button);
      } else { box.appendChild(el('span', 'field-help', '暂不可用')); }
      steps.appendChild(box);
    });
    guide.appendChild(steps);
    document.getElementById('tab-merchant-onboarding').appendChild(guide);    var show = window.showTab;
    window.showTab = function (key, button) { show(key, button); if (key === 'merchant-onboarding') {merchants();setupMerchants();} if (key === 'fee-allocation') fees(); };
    var oldLoad = window.loadMerchants; window.loadMerchants = function () { oldLoad(); merchants();setupMerchants(); };
    window.loadFeeStats = fees;
    document.querySelectorAll('[data-action="loadFeeStats"]').forEach(function (button) { button.removeAttribute('data-action'); button.addEventListener('click', fees); });
    document.querySelectorAll('option[value="MOCK_LOCAL"]').forEach(function (option) { var s = option.parentElement; if (option.selected) s.value = 'UNSUPPORTED'; option.remove(); });
	var bankNotice = document.querySelector('#tab-bank-vba > .alert-banner');
	if (bankNotice) bankNotice.textContent = '请先在银行开通收款账户，再在这里登记。登记账户不会自动开户；真实到账以银行确认和对账结果为准。';
	function removeTrainingChoices() {
		document.querySelectorAll('#adapter-providers-table tr').forEach(function (row) { if (row.cells.length > 1 && /^mock(?:_|$)/i.test(row.cells[0].textContent.trim())) row.remove(); });
		document.querySelectorAll('select option').forEach(function (option) { if (/^Mock (Acquirer|Bank Collection|Crypto Pay|LP|Onchain Wallet|Bank Rail|Exchange)(?:\s|$)/.test(option.textContent.trim())) option.remove(); });
	}
	removeTrainingChoices();
	var providerObserver = new MutationObserver(removeTrainingChoices);
	providerObserver.observe(document.getElementById('tab-universal-adapter'), {childList:true,subtree:true});
    document.querySelectorAll('.advanced-settings summary').forEach(function (e) { e.textContent = '机构连接与通知设置'; });
    document.querySelectorAll('.display-options label').forEach(function (e) { var t = Array.from(e.childNodes).find(function (n) { return n.nodeType === 3; }); if (t) t.textContent = '显示记录编号'; });
    var names = {'new-merchant-name':'例如：品牌名称','new-merchant-legal-name':'填写公司登记名称','new-acq-provider-code':'由支付机构提供','new-acq-channel-code':'填写机构分配的通道名称','new-merchant-webhook-url':'填写销售网站的通知接收地址','new-rfi-order':'填写订单编号'};
    Object.keys(names).forEach(function (id) { var input = document.getElementById(id); if (input) input.placeholder = names[id]; });
    var region = document.getElementById('new-merchant-jurisdiction');
    if (region) { var country = el('select'); country.id = region.id; [['','请选择公司注册地区'],['CN','中国内地'],['HK','中国香港'],['SG','新加坡'],['US','美国'],['GB','英国'],['AE','阿联酋'],['CA','加拿大'],['AU','澳大利亚'],['DE','德国'],['FR','法国'],['JP','日本'],['NZ','新西兰'],['IE','爱尔兰'],['NL','荷兰']].forEach(function (c) { country.add(new Option(c[1],c[0])); }); var regionNames = new Intl.DisplayNames(['zh-CN'], {type:'region'}); var selectedCodes = new Set(Array.from(country.options).map(function(o){return o.value;})); 'AD AE AF AG AI AL AM AO AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ DE DJ DK DM DO DZ EC EE EG ER ES ET FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GT GU GW GY HK HN HR HT HU ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM US UY UZ VA VC VE VG VI VN VU WF WS XK YE YT ZA ZM ZW'.split(' ').map(function(code){return [code,regionNames.of(code)];}).sort(function(a,b){return a[1].localeCompare(b[1],'zh-CN');}).forEach(function(c){if(!selectedCodes.has(c[0])) country.add(new Option(c[1],c[0]));}); region.replaceWith(country); Array.from(country.options).forEach(function(option){setupCountry.add(new Option(option.textContent,option.value));}); var help = document.getElementById(country.id + '-help'); if (help) help.textContent = '选择公司登记文件上的注册地区。其他地区请联系账户负责人登记。'; }
    document.querySelectorAll('.form-group input[placeholder]').forEach(function (input) { if (/UUID|JSON|vault:\/\/|Tier|如:.*(stripe|pool|eth_)|如: LOW/.test(input.placeholder)) input.placeholder = '请填写机构提供的信息'; });
    // Translate product copy only. Never change submitted values, identifiers or merchant data.
    var copy = {'填写收单通道真实凭证':'连接支付账户','填写发卡卡池真实凭证':'连接发卡账户','明文凭证仅经本次传输，服务端 KMS 信封加密后写入 Vault，界面不回显；重复填写自动轮换到新版本并触发运行时重载。':'验证信息会安全保存，保存后不再显示原文。重新填写会更新此账户的连接信息。','法人名称':'公司登记名称','风控模式':'审核方式','风控覆盖记录':'人工调整记录','覆盖审计日志':'调整历史','BIN 码健康滑块':'连续付款失败上限','重载':'更新连接','填凭证':'连接账户','凭证类型:':'账户验证方式','凭证明文:':'账户验证信息','加密保存并绑定':'保存并连接账户','API 密钥 (API_KEY)':'机构访问凭证','密钥 (SECRET)':'机构安全凭证','私钥 (PRIVATE_KEY)':'机构签名凭证','回调密钥 (WEBHOOK_SECRET)':'支付通知验证凭证','全量费用汇总尚未接入。上方显示最近的费用记录，金额需结合币种查看。':'请选择费用明细查看实际费用。','跨组操作记录查询尚未接入。请先查看上方业务组状态。':'跨业务组操作当前不可使用。请联系账户负责人确认业务权限。'};
    function clean(root) {
      var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT), nodes = [], n;
      while ((n = walker.nextNode())) { if (!n.parentElement.closest('script,style,textarea,input')) nodes.push(n); }
      nodes.forEach(function (text) { var key = text.textContent.trim(); if (copy[key]) text.textContent = copy[key]; });
    }
    clean(document.body);
    new MutationObserver(function (mutations) { mutations.forEach(function (m) { m.addedNodes.forEach(function (n) { if (n.nodeType === 1) clean(n); else if (n.nodeType === 3 && copy[n.textContent.trim()]) n.textContent = copy[n.textContent.trim()]; }); }); }).observe(document.body, {childList:true,subtree:true});
    var nativeError = window.errMsg;
    function plainError(error) { var message = nativeError(error); if (/^[\u3400-\u9fff]/.test(message) && !/UUID|JSON|Vault|API|Provider|SQL|参数/.test(message)) return message; if (/forbidden|permission|403/i.test(message)) return '当前账户没有此操作权限，请联系账户负责人。'; if (/already|exists|duplicate/i.test(message)) return '已有相同记录或待审批申请，请先查看审批中心。'; return '操作未完成，请检查所选记录、账户连接和审批状态后重试。'; }
    window.errMsg = plainError;
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start); else start();
}());
