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
    setStatus(`OBS unavailable · ${error.message}`, false);
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
  } catch (error) {
    setStatus(`Scene switch failed · ${error.message}`, false);
  } finally {
    switching = false;
    for (const button of scenesElement.querySelectorAll('button')) button.disabled = false;
  }
}

refreshButton.addEventListener('click', () => loadScenes());
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
loadSubtitleStatus();
setInterval(() => { if (!switching && !document.hidden) loadScenes({ quiet: true }); }, 2500);
setInterval(() => { if (!changingSubtitles && !document.hidden) loadSubtitleStatus(); }, 5000);
