const scenesElement = document.querySelector('#scenes');
const statusElement = document.querySelector('#status');
const dotElement = document.querySelector('#dot');
const template = document.querySelector('#scene-template');
const refreshButton = document.querySelector('#refresh');
const subtitleStatus = document.querySelector('#subtitle-status');
const subtitleStart = document.querySelector('#subtitle-start');
const subtitleStop = document.querySelector('#subtitle-stop');
let switching = false;
let changingSubtitles = false;

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
    subtitleStatus.textContent = data.message || state;
    subtitleStart.disabled = changingSubtitles || !['idle', 'error'].includes(state);
    subtitleStop.disabled = changingSubtitles || ['idle', 'disabled'].includes(state);
  } catch (error) {
    subtitleStatus.textContent = `Subtitle controller unavailable · ${error.message}`;
    subtitleStart.disabled = true;
    subtitleStop.disabled = true;
  }
}

async function changeSubtitles(action) {
  if (changingSubtitles) return;
  changingSubtitles = true;
  subtitleStart.disabled = true;
  subtitleStop.disabled = true;
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

subtitleStart.addEventListener('click', () => changeSubtitles('start'));
subtitleStop.addEventListener('click', () => changeSubtitles('stop'));
loadScenes();
loadSubtitleStatus();
setInterval(() => { if (!switching && !document.hidden) loadScenes({ quiet: true }); }, 2500);
setInterval(() => { if (!changingSubtitles && !document.hidden) loadSubtitleStatus(); }, 5000);
