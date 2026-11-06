# camfocus

Turns webcam autofocus off (or back on) on Windows without installing the vendor's software.

I wrote this because my Anker PowerConf C200 keeps hunting for focus during calls, and I can't install Anker's app on my work laptop. The camera is a standard UVC device, so Windows already exposes the focus control. This just flips it.

It's a single `.exe` with no dependencies, and it doesn't need admin rights.

## Download

Grab the exe from [Releases](../../releases):

- `camfocus-amd64.exe` for most laptops (Intel/AMD)
- `camfocus-arm64.exe` for ARM laptops (Snapdragon, Surface Pro X, etc.)

Not sure which? Settings → System → About → "System type".

## Usage

```
camfocus list            show cameras and their current focus mode
camfocus off             disable autofocus, keep the lens where it is
camfocus off -focus 30   disable autofocus and set a fixed focus value
camfocus on              turn autofocus back on
```

By default it only touches cameras with "C200" in the name. For a different camera, run `list` to see its name and pass part of it:

```
camfocus off -camera "logitech"
camfocus off -camera ""          # every camera
```

It runs from WSL too: `./camfocus-amd64.exe off`.

## If autofocus comes back

Most webcams forget their settings when they lose power, so unplugging or rebooting can turn autofocus back on. If that happens, run it at login: press Win+R, type `shell:startup`, and put a shortcut there that runs `camfocus-amd64.exe off`.

Some meeting apps also reset camera settings when they open the camera. If that happens, run `camfocus off` again once the call has started.

## Building

You only need Go on the machine you build on. It cross-compiles from macOS or Linux:

```
GOOS=windows GOARCH=amd64 go build -o dist/camfocus-amd64.exe .
GOOS=windows GOARCH=arm64 go build -o dist/camfocus-arm64.exe .
```

## How it works

It uses DirectShow's `IAMCameraControl` interface, which is what the "Camera Control" tab in the old Windows camera properties dialog uses. It calls COM directly through `syscall`, so it only needs the Go standard library.
