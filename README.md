# OBS Scene Switcher

A small phone-first remote for changing the current OBS program scene over the private WireGuard network.

## Security model

- The HTTP server refuses to bind outside `10.77.0.0/24`.
- The installed service binds only to Slacktop's WireGuard address, `10.77.0.2:8798`.
- The backend connects to OBS on `127.0.0.1:4455` and reads OBS's existing password locally.
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

## Optional GPU subtitles

Copy `subtitles.env.example` to `~/.config/obs-scene-switcher/subtitles.env`, set mode `0600`, and fill in the existing Streamchat bot GUI address/password plus a stable PipeWire/Pulse source name. The page then gains Start and Stop controls. Start asks Streamchat-bot for one temporary worker, waits while the model loads, and launches the local sender. Stop clears the two OBS text files and permanently deletes the worker.

The default output files are:

    ~/.cache/language-subtitles/original.txt
    ~/.cache/language-subtitles/english.txt

For compatibility with the existing OBS subtitle sources, the controller also writes the combined two-line result to `~/.cache/language-subtitles/current.txt`.

The same operation is available locally after installing `/home/ashwin/bin/stream-subtitles`:

    stream-subtitles start
    stream-subtitles status
    stream-subtitles stop
