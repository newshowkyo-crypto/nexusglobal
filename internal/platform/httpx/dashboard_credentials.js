(function () {
  'use strict';

  var fields = {
    API_KEY: ['API Key', '填写机构颁发的访问密钥。'],
    SECRET: ['Secret Key / API Secret', '填写机构颁发的服务端密钥。'],
    WEBHOOK_SECRET: ['Webhook 签名密钥', '填写此通道对应回调端点的签名密钥。']
  };
  var providers = {stripe: 'Stripe', nxtapay: 'Nxtapay', mock: '本地模拟'};
  var channelStatuses = {ACTIVE: '启用', INACTIVE: '停用'};

  function element(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  }

  function credentialForm(type, existing, access, notify, onSaved) {
    var section = element('section', 'credential-item');
    var title = fields[type][0];
    section.appendChild(element('h4', '', title));
    var state = element('p', 'field-help');
    section.appendChild(state);

    function showState(value) {
      state.textContent = value.version ? (value.expired ? '已过期' : '已保存') + ' · 版本 ' + value.version : '未录入';
      if (value.updated_at) state.textContent += ' · 更新于 ' + new Date(value.updated_at).toLocaleString('zh-CN');
      if (value.expires_at) state.textContent += ' · 到期于 ' + new Date(value.expires_at).toLocaleString('zh-CN');
    }
    showState(existing);
    if (!access.canWrite) return section;

    var form = element('form', 'operations-form');
    form.autocomplete = 'off';
    var group = element('div', 'form-group');
    var label = element('label', '', existing.version ? '新凭证' : '凭证');
    var input = element('input', '');
    input.type = 'password';
    input.required = true;
    input.autocomplete = 'new-password';
    input.spellcheck = false;
    label.appendChild(input);
    group.appendChild(label);
    group.appendChild(element('p', 'field-help', fields[type][1]));
    form.appendChild(group);
    var save = element('button', 'submit-btn', existing.version ? '更新凭证' : '加密保存');
    save.type = 'submit';
    form.appendChild(save);
    form.addEventListener('submit', function (event) {
      event.preventDefault();
      if (access.busy || !input.value.trim()) return;
      if (existing.version && !window.confirm('确认更新「' + title + '」？请核对所属商户、通道和机构环境。')) return;
      access.busy = true;
      save.disabled = true;
      var payload = JSON.stringify({credential_type: type, credential_material: input.value.trim(), expected_version: existing.version || 0});
      input.value = '';
      notify('正在加密保存…');
      authFetch(access.url, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: payload})
        .then(function (response) { return response.json(); })
        .then(function (data) {
          existing = {version: data.key_version, updated_at: data.created_at};
          showState(existing);
          onSaved(type, existing);
          save.textContent = '更新凭证';
          label.firstChild.textContent = '新凭证';
          notify('凭证已加密保存。机构连接与支付回调仍待联调。');
          loadAcquirerChannels();
        })
        .catch(function () {
          notify('未确认保存结果。请关闭后重新打开，核对版本，再决定是否重填。');
        })
        .finally(function () { access.busy = false; save.disabled = false; });
    });
    section.appendChild(form);
    return section;
  }

  window.fillAcquirerCredential = function (channelID) {
    var previous = document.activeElement;
    var dialog = element('dialog', 'modal-content credentials-dialog');
    var title = element('h3', '', '收款通道凭证');
    title.id = 'credentials-title';
    dialog.setAttribute('aria-labelledby', title.id);
    dialog.appendChild(title);
    var details = element('p', 'field-help');
    dialog.appendChild(details);
    var progress = element('p', 'field-help');
    dialog.appendChild(progress);
    var status = element('p', 'operation-result', '正在读取凭证状态…');
    status.setAttribute('role', 'status');
    dialog.appendChild(status);
    var list = element('div', 'credential-list');
    dialog.appendChild(list);
    var close = element('button', 'refresh-btn', '关闭');
    close.type = 'button';
    dialog.appendChild(close);
    var access = {url: API_BASE + '/api/v3/channels/' + encodeURIComponent(channelID) + '/credentials', busy: false};

    close.addEventListener('click', function () { if (!access.busy) dialog.close(); });
    dialog.addEventListener('cancel', function (event) { if (access.busy) event.preventDefault(); });
    dialog.addEventListener('close', function () {
      dialog.querySelectorAll('input').forEach(function (input) { input.value = ''; });
      dialog.remove();
      var fallback = document.querySelector('#tab-acquirer-channels h1');
      if (previous && previous.isConnected) previous.focus();
      else if (fallback) fallback.focus();
    });
    document.body.appendChild(dialog);
    dialog.showModal();
    close.focus();

    authFetch(access.url).then(function (response) { return response.json(); }).then(function (data) {
      if (!dialog.open) return;
      details.textContent = [data.merchant_name, providers[data.provider_code] || data.provider_code,
        data.channel_code, channelStatuses[data.channel_status] || data.channel_status].join(' / ');
      access.canWrite = data.can_write;
      status.textContent = data.edit_block || (data.provider_code === 'mock' ? '当前是模拟通道，请使用测试数据。' : '分别填写各项凭证。保存后可查看版本，机构连接仍需联调。');
      var saved = data.credentials || {};
      var required = data.required || [];
      function updateProgress() {
        if (!required.length) return;
        var count = required.filter(function (type) { return saved[type] && saved[type].version && !saved[type].expired; }).length;
        progress.textContent = '录入进度 ' + count + ' / ' + required.length + ' · 机构联调待确认';
      }
      updateProgress();
      required.forEach(function (type) {
        if (fields[type]) list.appendChild(credentialForm(type, saved[type] || {}, access, function (message) {
          status.textContent = message;
        }, function (changedType, metadata) {
          saved[changedType] = metadata;
          updateProgress();
        }));
      });
    }).catch(function () {
      status.textContent = '暂时无法读取凭证状态。请确认通道仍存在、账号有查看权限，然后重新打开。';
    });
  };
}());
