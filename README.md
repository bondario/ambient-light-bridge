# Ambient Light Bridge

Ambient Light Bridge is one Go binary that reads the Apple Studio Display
ambient-light sensor through IOKit HID and serves readings to
[Lunar](https://lunar.fyi) at `127.0.0.1:5890`.

## Requirements

macOS, Go 1.24 or newer, Xcode command line tools for cgo, and Lunar.

## Install

Run `make install` from the repo checkout. The LaunchAgent plist at
`~/Library/LaunchAgents/com.bondario.ambient-light-bridge.plist` points to
`bin/ambient-light-bridge` in the checkout. If you move the checkout, run
`make install` again from the new location.

In Lunar, choose **Adaptive Brightness > Automatic > External sensor (DIY)**.
Set the host to `127.0.0.1`, the port to `5890`, and leave the path prefix empty.
Select the monitors Lunar should control.

## Operations

- `make status` shows the LaunchAgent state and queries the current reading.
- `make logs` follows `~/Library/Logs/ambient-light-bridge/bridge.log`.
- `make restart` restarts the LaunchAgent.
- `make uninstall` unloads the LaunchAgent and removes its plist.
- `make build` builds the binary, and `make test` runs vet and the tests.
- `make run` builds and runs the bridge in the foreground. The port must be free.

## Endpoints

The endpoints follow [lunarsensor](https://github.com/alin23/lunarsensor).

- `/sensor/ambient_light` returns HTTP 200 with the value or 503 when unavailable.
- `/events` streams readings as server-sent events (SSE).
- `/sensor/ambient_color_temperature` returns HTTP 404, as lunarsensor does for a sensor without colour. The bridge does not expose colour temperature.

## Sensor selection

The bridge matches HID devices with usage page `0x20` and usage `0x41`. It
prefers external devices over built-in devices, then picks the lowest location
ID. A missing location ID counts as 0, and the choice among devices with
equal location IDs is unspecified. There is no fallback to another device when
the selected one fails to read.

The bridge logs the source with the first reading, when readings resume after
a failure, and when the source changes. If the match count is greater than
one, the bridge logs a warning whenever that count changes.

## Reading and restart behaviour

The bridge polls every 2 s. A reading older than 10 s stops being served as
fresh. The bridge holds the last value for up to 30 s from the last time it was
served fresh. Once the hold expires, `/sensor/ambient_light` returns 503 and
`/events` stops sending readings until data resumes.

If a sensor read hangs for 30 s, the process exits and launchd restarts it.

## Licence

This repo is MIT-licensed.
