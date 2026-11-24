# Brand

The mark is a barn in outline under a gambrel roof that floats a little above it: the roof being raised, as neighbours raise a barn together. Every file here, the panel's icons and the desktop app's icons are built by [`scripts/brand/build.py`](../../scripts/brand/build.py) from one geometry. Change the script and run `make brand`, never the files.

## Files

| File | Use it for |
| --- | --- |
| `svg/logo-on-dark.svg`, `svg/logo-on-light.svg` | The logo: the mark and the name, on a transparent background |
| `svg/logo-stacked-on-dark.svg`, `svg/logo-stacked-on-light.svg` | The same, with the name under the mark, for square spaces |
| `svg/mark.svg`, `svg/mark-on-light.svg` | The mark alone |
| `svg/mark-white.svg`, `svg/mark-black.svg` | One colour, for print, stamps and photos |
| `svg/icon.svg`, `png/icon-1024.png` | The app icon: the mark on its dark plate |
| `svg/icon-small.svg` | The app icon drawn for 32 to 40 px |
| `png/icon-macos-1024.png` | The app icon on Apple's grid (824 px shape, room for the shadow) |
| `png/social-preview.png` | GitHub's social preview (Settings → General → Social preview) |

The wordmark is outlined, so the files do not need the font.

## Where the build puts the rest

| Platform | Files | Notes |
| --- | --- | --- |
| Panel (browser) | `web/public/`: `favicon.svg`, `favicon.ico` (16, 32, 48), `apple-touch-icon.png` (180, opaque), `icon-192.png`, `icon-512.png`, `icon-maskable-512.png`, `manifest.webmanifest` | The daemon serves them with their types (`internal/adapters/in/panel`) |
| macOS | `desktop/build/appicon.png`, `desktop/build/darwin/iconfile.icns` | Wails makes the bundle's icon from `appicon.png`; `make desktop` replaces it with the `.icns` drawn for every size |
| Windows | `desktop/build/windows/icon.ico` (16 to 256) | Embedded in the `.exe` with its version information (`info.json`) |
| Linux | `desktop/build/linux/`: icons in the hicolor tree (16 to 512 and scalable), `.desktop` entry, AppStream metainfo | `make desktop-install` installs them for the user |

The desktop app's id is `io.github.leandrodaf.barnraise` on every platform: bundle identifier, Windows manifest, `.desktop` file, Wayland app id and X11 window class.

## Colours

| Name | Hex | Where |
| --- | --- | --- |
| Roof on dark | `#FFC857` | |
| Barn on dark | `#F5A524` | Also the roof in the panel |
| Roof on light | `#E8930C` | |
| Barn on light, text on light | `#1C1D29` | |
| Text on dark | `#EDEBE6` | |
| Plate | `#13141C` | Behind the app icon |

The name is set in Inter Medium, lower case, tracked −3 %.

## Small sizes

Thin lines vanish when they are small, so the build draws the mark in four weights and picks one for each size: regular from 128 px, medium from 48 px, heavy at 32 and 40 px, and at 24 px and below a simpler barn with no hayloft and no brace. Use `icon-small.svg` rather than scaling `icon.svg` down.

## Don't

- Stretch, rotate or outline the mark, or join the roof to the walls: the gap is the idea.
- Recolour it outside the table above, or put the on-dark version on a light background.
- Set the name in another font or in capitals.
- Leave less clear space around it than the height of the roof.
