'use strict';
const $ = id => document.getElementById(id);
let csrf = '', initialized = false, routePage = 1, routes = [], editing = null, currentView = 'routes', logCursor = '', instanceZone = 'UTC';
const fieldNames = {ip:'Client IP',user_agent:'User-Agent',referer:'Referer',path:'Request path',method:'Method',status:'Response status'};
async function api(resource, method = 'GET', body) {
  const headers = {};
  if (method !== 'GET') headers['X-CSRF-Token'] = csrf;
  if (body !== undefined && !(body instanceof FormData)) headers['Content-Type'] = 'application/json';
  const response = await fetch('/admin/api/v1/' + resource, {method, headers, body: body instanceof FormData ? body : body === undefined ? undefined : JSON.stringify(body)});
  const data = response.status === 204 ? null : await response.json();
  if (!response.ok) { if (response.status === 401 && resource !== 'login') await start(); throw new Error(data?.error?.message || 'Request failed'); }
  return data;
}
function notice(message) { $('notice').textContent = message; $('notice').hidden = !message; }
function errorIn(form, error) { form.querySelector('.form-error').textContent = error.message; }
function cell(row, text) { const element = document.createElement('td'); element.textContent = text ?? '—'; row.append(element); return element; }
function button(text, handler, style = 'quiet') { const element = document.createElement('button'); element.type = 'button'; element.className = style; element.textContent = text; element.onclick = async () => { try { await handler(); } catch (e) { notice(e.message); } }; return element; }
async function start() {
  const state = await api('session'); csrf = state.csrf; initialized = state.initialized;
  $('app').hidden = !state.authenticated; $('account').hidden = !state.authenticated; $('auth').hidden = state.authenticated;
  $('auth-title').textContent = initialized ? 'Welcome back' : 'Make this bay yours';
  $('auth-description').textContent = initialized ? 'Sign in to manage your routes and responses.' : 'Create the single administrator for this instance. Use at least 12 bytes for your password.';
  $('auth-submit').textContent = initialized ? 'Sign in' : 'Create administrator';
  $('auth-form').elements.password.minLength = initialized ? 1 : 12;
  $('auth-form').elements.password.autocomplete = initialized ? 'current-password' : 'new-password';
  if (state.authenticated) await showView(currentView);
}
async function summary() {
  const data = await api('stats/summary'); instanceZone = data.timezone; $('zone').textContent = data.timezone;
  const warnings = []; if (data.dropped_logs) warnings.push(`${data.dropped_logs} detailed records dropped`); if (data.lost_hits) warnings.push(`${data.lost_hits} pending hits lost`); if (data.write_error) warnings.push('Statistics pending: ' + data.write_error);
  if (warnings.length) notice(warnings.join(' · ')); return data.items;
}
async function showView(view) {
  currentView = view; for (const name of ['routes','builtins','activity','settings']) $('view-'+name).hidden = name !== view;
  document.querySelectorAll('nav button').forEach(element => element.classList.toggle('active', element.dataset.view === view));
  if (view === 'routes') await loadRoutes(); if (view === 'builtins') await loadBuiltins(); if (view === 'activity') await loadActivity(); if (view === 'settings') await loadSettings();
}
async function loadRoutes() {
  const [data, totals] = await Promise.all([api(`routes?page=${routePage}`), summary()]); routes = data.items; const counts = new Map(totals.filter(v=>v.kind==='route').map(v=>[v.hit_id,v])); $('route-list').replaceChildren(); $('routes-empty').hidden = data.total !== 0;
  for (const route of routes) {
    const row = document.createElement('tr'); const pathCell = cell(row,''); const code = document.createElement('code'); code.textContent = route.path; pathCell.append(code); const method = document.createElement('small'); method.textContent = route.method; pathCell.append(method);
    cell(row, route.match === 'exact' ? 'Exact' : route.boundary ? 'Path prefix' : 'Prefix'); cell(row, {redirect:'Redirect',text:route.action.format==='html'?'HTML':'Text',json:'JSON',file:'File',directory:'Static directory'}[route.action.type]);
    cell(row, route.action.target || route.action.content || route.action.file_name || route.action.file_path || 'Upload a file'); cell(row, counts.get(route.id)?.hits || 0); cell(row, counts.get(route.id)?.last_display || '—');
    const status = cell(row,''); const toggle = button(route.enabled?'Enabled':'Disabled',async()=>{await api('routes/'+route.id,'PATCH',{enabled:!route.enabled});await loadRoutes();},'badge'+(route.enabled?'':' off')); status.append(toggle); cell(row,'').append(button('Edit',()=>openRoute(route))); $('route-list').append(row);
  }
  $('routes-page').textContent = `Page ${routePage} · ${data.total} routes`; $('routes-prev').disabled = routePage===1; $('routes-next').disabled = routePage*50>=data.total;
}
function updateAction() {
  const f = $('route-form'), type = f.elements.type.value;
  f.querySelectorAll('[data-action]').forEach(element=>element.hidden=!element.dataset.action.split(' ').includes(type));
  $('boundary-label').hidden = f.elements.match.value !== 'prefix'; $('format-label').hidden = type !== 'text'; $('upload-label').hidden = type !== 'file' || f.elements.source.value !== 'managed';
  $('filepath-section').hidden = !(type === 'directory' || type === 'file' && f.elements.source.value === 'local');
  $('filepath-label').firstChild.textContent = type === 'directory' ? 'Absolute directory path' : 'Absolute local file path';
}
function openRoute(route = null) {
  editing = route; const f = $('route-form'); f.reset(); f.querySelector('.form-error').textContent=''; $('route-title').textContent = route ? 'Edit route' : 'New route'; $('delete-route').hidden = !route;
  $('managed-info').textContent='';
  if (route) {
    for (const key of ['method','match','enabled','boundary']) { const el = f.elements[key]; if (el.type==='checkbox') el.checked=route[key]; else el.value=route[key]; }
    f.elements.path.value = encodeURI(route.path).replaceAll('?','%3F').replaceAll('#','%23');
    for (const key of ['type','content','format','target','status','source','disposition','download_name']) if (route.action[key] !== undefined) f.elements[key].value=route.action[key];
    f.elements.preserve_query.checked=route.action.preserve_query !== false;
    if (route.action.source !== 'managed') f.elements.file_path.value=route.action.file_path || '';
    $('managed-info').textContent=route.action.file_name ? `${route.action.file_name} · ${route.action.file_size} bytes. Choose a file to replace it.` : '';
  }
  updateAction(); $('route-dialog').showModal();
}
async function saveRoute(event) {
  event.preventDefault(); const f=event.currentTarget, e=f.elements; const submit=$('save-route'); submit.disabled=true;
  try {
    const action={type:e.type.value};
    if (action.type==='redirect') Object.assign(action,{target:e.target.value,status:Number(e.status.value),preserve_query:e.preserve_query.checked});
    if (action.type==='text'||action.type==='json') { action.content=e.content.value; if(action.type==='text')action.format=e.format.value; }
    if(action.type==='directory')action.file_path=e.file_path.value;
    if(action.type==='file') { Object.assign(action,{source:e.source.value,disposition:e.disposition.value,download_name:e.download_name.value}); if(action.source==='local')action.file_path=e.file_path.value; else { action.file_path=editing?.action.source==='managed'?editing.action.file_path||'':''; action.file_name=editing?.action.file_name||''; action.file_size=editing?.action.file_size||0; if(!action.file_path&&!e.upload.files.length)throw new Error('Choose a file to upload.'); } }
    const data={path:e.path.value,method:e.method.value,match:e.match.value,boundary:e.boundary.checked,enabled:e.enabled.checked,action};
    let saved=await api(editing?'routes/'+editing.id:'routes',editing?'PATCH':'POST',data); editing=saved;
    if(action.type==='file'&&action.source==='managed'&&e.upload.files.length) { const body=new FormData();body.append('file',e.upload.files[0]);saved=await api('routes/'+saved.id+'/file','PUT',body);editing=saved; }
    $('route-dialog').close(); await loadRoutes(); notice('Route saved.');
  } catch(error) { errorIn(f,error); } finally { submit.disabled=false; }
}
const builtinDetails={ip:['Client IP','Returns the source IP. Query parameters are ignored.'],time:['Current time','RFC 3339 by default. Options: tz=Asia/Shanghai; format=rfc3339, unix, unix_ms, datetime.'],random:['Random value','Options: template=binary, decimal, hex, lower, upper, alnum or uuid; length=1–1024. UUID ignores length.']};
async function loadBuiltins() {
  const data=await api('builtins');$('builtin-list').replaceChildren();
  for(const [name,[title,description]] of Object.entries(builtinDetails)){const card=document.createElement('article');card.className='card';const code=document.createElement('code');code.textContent='/'+name;const heading=document.createElement('h2');heading.textContent=title;const p=document.createElement('p');p.className='muted';p.textContent=description;card.append(code,heading,p,button(data[name]?'Enabled':'Enable',async()=>{await api('builtins','PATCH',{[name]:!data[name]});await loadBuiltins();},data[name]?'':'quiet'));$('builtin-list').append(card);}
}
async function loadSettings() {
  await summary(); const settings=await api('settings'), f=$('settings-form');f.elements.detailed_logs.checked=settings.detailed_logs;f.elements.log_unmatched.checked=settings.log_unmatched;f.elements.retention.value=settings.retention_days===0?'forever':'days';f.elements.retention_days.value=settings.retention_days||30;$('retention-label').hidden=settings.retention_days===0;
  $('log-fields').replaceChildren();for(const [key,name] of Object.entries(fieldNames)){const label=document.createElement('label');label.className='check';const input=document.createElement('input');input.type='checkbox';input.name=key;input.checked=settings.fields[key];label.append(input,document.createTextNode(name));$('log-fields').append(label);}await loadTokens();
}
async function loadTokens() {
  const data=await api('tokens');$('token-list').replaceChildren();for(const token of data){const li=document.createElement('li'),name=document.createElement('span');name.textContent=token.name;li.append(name,button('Revoke',async()=>{if(confirm(`Revoke token “${token.name}”?`)){await api('tokens/'+token.id,'DELETE');await loadTokens();}},'danger'));$('token-list').append(li);}
}
async function loadActivity() {
  const [totals,daily,routeData]=await Promise.all([summary(),api('stats/daily'),api('routes?limit=200')]);
  const selected=$('log-route').value;$('log-route').replaceChildren(new Option('All requests',''));for(const route of routeData.items)$('log-route').add(new Option(route.path+' · '+route.method,route.id));for(const name of Object.keys(builtinDetails))$('log-route').add(new Option('/'+name,name));$('log-route').value=selected;
  const svg=$('trend');svg.replaceChildren();const max=Math.max(1,...daily.map(v=>v.hits));const ns='http://www.w3.org/2000/svg';
  if(daily.length){const poly=document.createElementNS(ns,'polyline');poly.setAttribute('points',daily.map((v,i)=>`${20+i*860/Math.max(1,daily.length-1)},${160-v.hits/max*135}`).join(' '));poly.setAttribute('fill','none');poly.setAttribute('stroke','#246649');poly.setAttribute('stroke-width','3');svg.append(poly);for(let i=0;i<daily.length;i++){const circle=document.createElementNS(ns,'circle');circle.setAttribute('cx',20+i*860/Math.max(1,daily.length-1));circle.setAttribute('cy',160-daily[i].hits/max*135);circle.setAttribute('r','4');circle.setAttribute('fill','#246649');const title=document.createElementNS(ns,'title');title.textContent=daily[i].day+': '+daily[i].hits+' hits';circle.append(title);svg.append(circle);}}
  $('trend-label').textContent=daily.length?`${daily[0].day} – ${daily.at(-1).day} · ${daily.reduce((sum,v)=>sum+v.hits,0)} hits`:'No recorded hits yet.';await loadLogs(false);
}
async function loadLogs(more=false) {
  const q=new URLSearchParams({limit:'50',hit_id:$('log-route').value});for(const name of ['from','to'])if($('log-'+name).value)q.set(name,new Date($('log-'+name).value).toISOString());if(more&&logCursor)q.set('cursor',logCursor);
  const data=await api('stats/logs?'+q);if(!more)$('log-list').replaceChildren();for(const log of data.items){const row=document.createElement('tr');for(const value of [log.time_display,routes.find(v=>v.id===log.hit_id)?.path||log.hit_id||'Unmatched',log.path,log.method,log.status,log.ip,log.user_agent,log.referer])cell(row,value);$('log-list').append(row);}logCursor=data.next_cursor;$('logs-next').hidden=!logCursor;
}
document.querySelectorAll('nav button').forEach(element=>element.onclick=()=>showView(element.dataset.view).catch(e=>notice(e.message)));
$('auth-form').onsubmit=async event=>{event.preventDefault();const f=event.currentTarget;f.querySelector('.form-error').textContent='';try{const result=await api(initialized?'login':'setup','POST',{username:f.elements.username.value,password:f.elements.password.value});csrf=result.csrf;f.reset();notice('');await start();}catch(e){errorIn(f,e);}};
$('logout').onclick=async()=>{try{await api('logout','POST');await start();}catch(e){notice(e.message);}};
$('new-route').onclick=$('empty-new').onclick=()=>openRoute();$('route-form').onsubmit=saveRoute;
for(const name of ['type','source','match'])$('route-form').elements[name].onchange=updateAction;
for(const id of ['close-route','cancel-route'])$(id).onclick=()=>$('route-dialog').close();
$('delete-route').onclick=async()=>{if(editing&&confirm(`Delete ${editing.path} and its statistics?`)){try{await api('routes/'+editing.id,'DELETE');$('route-dialog').close();await loadRoutes();notice('Route deleted.');}catch(e){errorIn($('route-form'),e);}}};
$('routes-prev').onclick=()=>{routePage--;loadRoutes().catch(e=>notice(e.message));};$('routes-next').onclick=()=>{routePage++;loadRoutes().catch(e=>notice(e.message));};
$('settings-form').elements.retention.onchange=event=>$('retention-label').hidden=event.target.value==='forever';
$('settings-form').onsubmit=async event=>{event.preventDefault();const f=event.currentTarget;const fields={};for(const key of Object.keys(fieldNames))fields[key]=f.elements[key].checked;try{await api('settings','PATCH',{detailed_logs:f.elements.detailed_logs.checked,log_unmatched:f.elements.log_unmatched.checked,fields,retention_days:f.elements.retention.value==='forever'?0:Number(f.elements.retention_days.value)});notice('Settings saved.');}catch(e){errorIn(f,e);}};
$('token-form').onsubmit=async event=>{event.preventDefault();const f=event.currentTarget;try{const token=await api('tokens','POST',{name:f.elements.name.value});$('token-value').value=token.token;$('token-secret').hidden=false;f.reset();await loadTokens();}catch(e){notice(e.message);}};
$('dismiss-token').onclick=()=>{$('token-value').value='';$('token-secret').hidden=true;};
$('password-form').onsubmit=async event=>{event.preventDefault();const f=event.currentTarget;try{await api('password','POST',{current_password:f.elements.current_password.value,password:f.elements.password.value});f.reset();await start();notice('Password changed. Sign in with your new password.');}catch(e){errorIn(f,e);}};
$('import-config').onclick=async()=>{try{const file=$('import-file').files[0];if(!file)throw new Error('Choose a configuration JSON file.');if(file.size>16*1024*1024)throw new Error('Import exceeds 16 MiB.');const raw=await file.text();const response=await fetch('/admin/api/v1/config/import',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:raw});const data=await response.json();if(!response.ok)throw new Error(data.error.message);$('import-result').textContent='Configuration imported.';await loadSettings();}catch(e){$('import-result').textContent=e.message;}};
$('refresh-activity').onclick=()=>loadActivity().catch(e=>notice(e.message));$('log-filter').onsubmit=event=>{event.preventDefault();loadLogs().catch(e=>notice(e.message));};$('logs-next').onclick=()=>loadLogs(true).catch(e=>notice(e.message));
start().catch(e=>notice(e.message));
