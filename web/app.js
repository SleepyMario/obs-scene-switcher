const scenesElement = document.querySelector('#scenes');
const statusElement = document.querySelector('#status');
const dotElement = document.querySelector('#dot');
const template = document.querySelector('#scene-template');
const refreshButton = document.querySelector('#refresh');
const subtitleStatus = document.querySelector('#subtitle-status');
const subtitleToggle = document.querySelector('#subtitle-toggle');
const subtitleModes = [...document.querySelectorAll('[data-mode]')];
let switching = false;
let changingSubtitles = false;
let subtitleState = 'idle';
const obsStart = document.querySelector('#obs-start');
const obsStop = document.querySelector('#obs-stop');
const obsState = document.querySelector('#obs-state');
const obsResult = document.querySelector('#obs-action-result');
let changingOBS = false;
let obsProcessState = 'unknown';
const previewPanel = document.querySelector('#preview-panel');
const previewImage = document.querySelector('#program-preview');
const previewStatus = document.querySelector('#preview-status');
const previewPlaceholder = document.querySelector('#preview-placeholder');
let previewLoading = false;
let previewObjectURL = '';

function clearPreview(message) {
  if (previewObjectURL) URL.revokeObjectURL(previewObjectURL);
  previewObjectURL = '';
  previewImage.removeAttribute('src');
  previewImage.hidden = true;
  previewPlaceholder.hidden = false;
  previewPlaceholder.textContent = message;
}

async function loadPreview() {
  if (previewLoading || document.hidden || !previewPanel.open) return;
  if (obsProcessState === 'stopped') {
    previewStatus.textContent = 'OBS is closed';
    clearPreview('Start OBS to see the program output.');
    return;
  }
  previewLoading = true;
  try {
    const response = await fetch('/api/preview', { cache: 'no-store' });
    if (!response.ok) {
      const data = await response.json().catch(() => ({}));
      throw new Error(data.detail || data.error || 'Preview unavailable');
    }
    const image = await response.blob();
    const nextURL = URL.createObjectURL(image);
    const previousURL = previewObjectURL;
    previewObjectURL = nextURL;
    previewImage.src = nextURL;
    previewImage.hidden = false;
    previewPlaceholder.hidden = true;
    previewStatus.textContent = `Updated ${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
    if (previousURL) URL.revokeObjectURL(previousURL);
  } catch (error) {
    previewStatus.textContent = 'Preview unavailable';
    clearPreview(error.message);
  } finally {
    previewLoading = false;
  }
}

async function loadOBSStatus() {
  try {
    const response = await fetch('/api/obs', { cache: 'no-store' });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || 'Status unavailable');
    obsProcessState = data.state;
    obsState.textContent = data.message;
    obsStart.disabled = changingOBS || data.state !== 'stopped';
    obsStop.disabled = changingOBS || data.state !== 'running';
  } catch (error) {
    obsState.textContent = error.message;
    obsStart.disabled = obsStop.disabled = true;
  }
}

async function controlOBS(action) {
  if (changingOBS) return;
  if (action === 'stop' && !window.confirm('Close OBS on the laptop? Active streaming or recording will block this.')) return;
  changingOBS = true;
  obsStart.disabled = obsStop.disabled = true;
  obsResult.textContent = action === 'start' ? 'Opening OBS…' : 'Checking outputs before closing…';
  try {
    const response = await fetch(`/api/obs/${action}`, {
      method: 'POST', headers: { 'X-OBS-Control': '1' },
    });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || 'OBS action failed');
    obsResult.textContent = data.message;
  } catch (error) {
    obsResult.textContent = error.message;
  } finally {
    changingOBS = false;
    await loadOBSStatus();
    await loadScenes();
    await loadPreview();
  }
}
obsStart.addEventListener('click', () => controlOBS('start'));
obsStop.addEventListener('click', () => controlOBS('stop'));

function setStatus(text, online) {
  statusElement.textContent = text;
  dotElement.classList.toggle('online', online);
}

async function loadScenes({ quiet = false } = {}) {
  try {
    const response = await fetch('/api/scenes', { cache: 'no-store' });
    const data = await response.json();
    if (!response.ok) throw new Error(data.detail || data.error || 'OBS unavailable');
    render(data.scenes, data.current);
    setStatus(`Live scene: ${data.current}`, true);
  } catch (error) {
    if (!quiet) scenesElement.replaceChildren();
    setStatus(obsProcessState === 'stopped' ? 'OBS is closed · ready to start' : 'Waiting for the OBS connection…', false);
  }
}

function render(scenes, current) {
  const fragment = document.createDocumentFragment();
  for (const name of scenes) {
    const button = template.content.firstElementChild.cloneNode(true);
    button.querySelector('span').textContent = name;
    button.classList.toggle('current', name === current);
    button.querySelector('small').textContent = name === current ? 'Currently live' : 'Tap to switch';
    button.addEventListener('click', () => switchScene(name));
    fragment.append(button);
  }
  scenesElement.replaceChildren(fragment);
}

async function switchScene(name) {
  if (switching) return;
  switching = true;
  for (const button of scenesElement.querySelectorAll('button')) button.disabled = true;
  setStatus(`Switching to ${name}…`, true);
  try {
    const response = await fetch(`/api/scenes/${encodeURIComponent(name)}`, { method: 'POST' });
    const data = await response.json();
    if (!response.ok) throw new Error(data.detail || data.error || 'switch failed');
    await loadScenes();
    await loadPreview();
  } catch (error) {
    setStatus(`Scene switch failed · ${error.message}`, false);
  } finally {
    switching = false;
    for (const button of scenesElement.querySelectorAll('button')) button.disabled = false;
  }
}

refreshButton.addEventListener('click', () => loadScenes());
previewPanel.addEventListener('toggle', () => { if (previewPanel.open) loadPreview(); });
async function loadSubtitleStatus() {
  try {
    const response = await fetch('/api/subtitles', { cache: 'no-store' });
    const data = await response.json();
    const state = data.state || 'unknown';
    subtitleState = state;
    const mode = data.mode || 'remote';
    subtitleStatus.textContent = `${mode === 'remote' ? 'VPS / RunPod' : 'This computer'} · ${data.message || state}`;
    const isOn = ['starting', 'running', 'stopping'].includes(state);
    subtitleToggle.textContent = isOn ? 'Turn subtitles off' : 'Turn subtitles on';
    subtitleToggle.classList.toggle('stop', isOn);
    subtitleToggle.classList.toggle('start', !isOn);
    subtitleToggle.disabled = changingSubtitles || state === 'stopping';
    for (const button of subtitleModes) {
      button.classList.toggle('selected', button.dataset.mode === mode);
      button.disabled = changingSubtitles;
    }
  } catch (error) {
    subtitleStatus.textContent = `Subtitle controller unavailable · ${error.message}`;
    subtitleToggle.disabled = true;
    for (const button of subtitleModes) button.disabled = true;
  }
}

async function changeSubtitles(action) {
  if (changingSubtitles) return;
  changingSubtitles = true;
  subtitleToggle.disabled = true;
  subtitleStatus.textContent = action === 'start' ? 'Starting GPU worker…' : 'Stopping and deleting GPU worker…';
  try {
    const response = await fetch(`/api/subtitles/${action}`, { method: 'POST' });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || `${action} failed`);
  } catch (error) {
    subtitleStatus.textContent = `Subtitle ${action} failed · ${error.message}`;
  } finally {
    changingSubtitles = false;
    await loadSubtitleStatus();
  }
}

async function selectSubtitleMode(mode) {
  if (changingSubtitles) return;
  changingSubtitles = true;
  subtitleStatus.textContent = `Selecting ${mode} subtitles…`;
  try {
    const response = await fetch(`/api/subtitles/mode/${mode}`, { method: 'POST' });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || 'mode change failed');
  } catch (error) {
    subtitleStatus.textContent = `Subtitle mode change failed · ${error.message}`;
  } finally {
    changingSubtitles = false;
    await loadSubtitleStatus();
  }
}

subtitleToggle.addEventListener('click', () => changeSubtitles(['starting', 'running', 'stopping'].includes(subtitleState) ? 'stop' : 'start'));
for (const button of subtitleModes) button.addEventListener('click', () => selectSubtitleMode(button.dataset.mode));
loadScenes();
loadOBSStatus();
loadPreview();
setInterval(() => { if (!changingOBS && !document.hidden) loadOBSStatus(); }, 2500);
loadSubtitleStatus();
setInterval(() => { if (!switching && !document.hidden) loadScenes({ quiet: true }); }, 2500);
setInterval(() => { if (!changingSubtitles && !document.hidden) loadSubtitleStatus(); }, 5000);
setInterval(loadPreview, 2500);
