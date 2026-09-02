# OBS Scene Switcher

A small phone-first remote for changing the current OBS program scene over the private WireGuard network.

## Security model

- The HTTP server refuses to bind outside `10.77.0.0/24`.
- The installed service binds only to Slacktop's WireGuard address, `10.77.0.2:8798`.
- The backend connects to OBS on Slacktop's WireGuard address, `10.77.0.2:4455`, and reads OBS's existing password locally.
- The OBS password is never sent to or stored on the phone.
- The UI exposes scene selection and an optional GPU-subtitle session control. It has no stream, recording, or shutdown controls.
- Subtitle provider credentials stay on the VPS. The temporary worker token is handled by the Slacktop backend and is never returned to the phone browser.
- Microphone audio travels directly from Slacktop to the temporary worker, not through the VPS.

## Use

Start OBS, enable the phone's existing WireGuard tunnel, and open:

    http://10.77.0.2:8798

The scene list and current program scene are retrieved from OBS. The page refreshes automatically and can be added to the Android home screen.

## Install

    go build -o ~/.local/bin/obs-scene-switcher .
    install -Dm755 scripts/stream-subtitles ~/bin/stream-subtitles
    install -Dm644 systemd/obs-scene-switcher.service ~/.config/systemd/user/obs-scene-switcher.service
    systemctl --user daemon-reload
    systemctl --user enable --now obs-scene-switcher.service

The service remains available while OBS is closed; its UI reports `OBS unavailable` until OBS starts.

The OBS endpoint is currently fixed to Slacktop's WireGuard address. A later
controller update may expose this as a saved setting for Windows or another
WireGuard-connected streaming machine.

## Optional GPU subtitles

Copy `subtitles.env.example` to `~/.config/obs-scene-switcher/subtitles.env`, set mode `0600`, and fill in the existing Streamchat bot GUI address/password plus a stable PipeWire/Pulse source name. The phone page has independent on/off and local/remote controls. Remote is the normal VPS/RunPod route. Local starts the already-installed `~/bin/subtitles` service and is retained as an explicit fallback; it is never selected automatically. Changing engines stops the current engine and leaves subtitles off.

The default output files are:

    ~/.cache/language-subtitles/original.txt
    ~/.cache/language-subtitles/english.txt
    ~/.cache/language-subtitles/chinese.txt

For compatibility with the existing OBS subtitle source, the controller also writes the combined result to `~/.cache/language-subtitles/current.txt`: original speech, English, then Simplified Chinese. English or Chinese originals suppress their duplicate translation line, so those cases use two lines.

The same operation is available locally after installing `/home/ashwin/bin/stream-subtitles`:

    stream-subtitles on
    stream-subtitles off
    stream-subtitles remote
    stream-subtitles local
    stream-subtitles status

## IRL input visibility

The local SRT bridge reports whether the GoPro input is live through
`POST /api/irl/input/live` and `POST /api/irl/input/offline`. The controller
shows or hides `VPS-MMTX` in `IRL - VPS` accordingly. This prevents OBS from
leaving the final UDP frame frozen after the camera disconnects; the existing
BRB layer underneath is revealed instead.

Install the tracked bridge and user service with:

    install -Dm755 scripts/irl-srt-bridge ~/bin/irl-srt-bridge
    install -Dm644 systemd/irl-srt-bridge.service ~/.config/systemd/user/irl-srt-bridge.service
    systemctl --user daemon-reload
    systemctl --user enable --now irl-srt-bridge.service

Its protected environment file remains at
`~/.config/irl-srt-bridge/env`; `IRL_SRT_URL` is required and must not be
committed. `IRL_OBS_CONTROL_URL` is optional and defaults to the controller on
Slacktop's WireGuard address.
