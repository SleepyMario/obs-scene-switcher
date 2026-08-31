const scenesElement = document.querySelector('#scenes');
const statusElement = document.querySelector('#status');
const dotElement = document.querySelector('#dot');
const template = document.querySelector('#scene-template');
const refreshButton = document.querySelector('#refresh');
let switching = false;

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
loadScenes();
setInterval(() => { if (!switching && !document.hidden) loadScenes({ quiet: true }); }, 2500);
