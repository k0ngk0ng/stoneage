# StoneAge DirectDraw compatibility layer

StoneAge 2.5 renders to an indexed 8-bit DirectDraw surface. Modern macOS and
Windows desktops no longer expose the 256-colour display mode expected by the
client. This profile uses [cnc-ddraw](https://github.com/FunkyFr3sh/cnc-ddraw)
to preserve the client's palette semantics and convert the result for a modern
desktop.

The project is pinned to cnc-ddraw `7.1.0.0` (MIT). Its release archive and
source archive are kept under `vendor/archives/cnc-ddraw/`; extracted source,
license, and the upstream release are under
`vendor/upstream/cnc-ddraw-v7.1.0.0/`.

Run `scripts/prepare-legacy-client.sh` to install the pinned `ddraw.dll` and
this profile into `runtime/legacy-client`. The macOS Wine launcher runs that
preparation automatically and explicitly selects the native DLL override.

