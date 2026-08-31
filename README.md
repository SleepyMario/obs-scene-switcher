# OBS Scene Switcher

A small phone-first remote for changing the current OBS program scene over the private WireGuard network.

## Security model

- The HTTP server refuses to bind outside `10.77.0.0/24`.
- The installed service binds only to Slacktop's WireGuard address, `10.77.0.2:8798`.
- The backend connects to OBS on `127.0.0.1:4455` and reads OBS's existing password locally.
- The OBS password is never sent to or stored on the phone.
- The UI exposes scene selection only. It has no stream, recording, or shutdown controls.

## Use

Start OBS, enable the phone's existing WireGuard tunnel, and open:

    http://10.77.0.2:8798

The scene list and current program scene are retrieved from OBS. The page refreshes automatically and can be added to the Android home screen.

## Install

    go build -o ~/.local/bin/obs-scene-switcher .
    install -Dm644 systemd/obs-scene-switcher.service ~/.config/systemd/user/obs-scene-switcher.service
    systemctl --user daemon-reload
    systemctl --user enable --now obs-scene-switcher.service

The service remains available while OBS is closed; its UI reports `OBS unavailable` until OBS starts.
