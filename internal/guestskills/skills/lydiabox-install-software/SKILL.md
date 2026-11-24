---
name: lydiabox-install-software
description: Rules for installing software in the LydiaBox VM so that it persists across VM restarts and LydiaBox updates. Use before you install a program, a package, a CLI, or a desktop application with apt, pip, uv, npm, or a downloaded binary.
---

<!-- Managed by LydiaBox; replaced at each harness start. -->

# Install software in the LydiaBox VM

## What persists

- `/home/agent` (your home, `~`) and `/workspace` persist on all LydiaBox
  VM types. Put programs that must stay in the home.
- `/tmp` is not storage. Do not keep files there.
- The rest of the root file system (`/usr`, `/etc`, `/opt`, and the apt
  packages) persists in a different way on each VM type:
  - Cloud Hypervisor VM: the changes persist (a root overlay on the
    persistent storage).
  - Docker VM: the changes persist across a stop and a start. They are
    lost when LydiaBox recreates the container (a new guest image, new VM
    settings, or a LydiaBox update) or resets the root.
  - InstaBox machine (LydiaBox Cloud): the root disk persists across a
    stop and a start of the machine. A new machine starts from the image
    again.
- So an apt install can be lost. Prefer a user-level install in the home.
  Use apt for system libraries and desktop applications that have no
  user-level install.

## Root

- You are the user `agent` (UID 1000). On InstaBox, UID 1000 has a
  different user name; the home is still `/home/agent`.
- `sudo` works without a password: `sudo apt-get update` and then
  `sudo apt-get install -y <package>`.

## User-level installs (preferred)

`~/.local/bin` is on `PATH` in harness sessions, terminals, and desktop
programs that LydiaBox starts (at the end, so the image's programs come
first).

- Python CLI: `uv tool install <package>` (the command goes to
  `~/.local/bin`). `uv tool upgrade <package>` updates it.
- Python libraries for a project: `uv venv` or `python3 -m venv .venv` in
  the project, then install into that venv.
- Do not use `pip install --user` or `sudo pip install` with the system
  Python. The system Python is "externally managed" (PEP 668) and refuses
  them. Do not use `--break-system-packages`.
- npm CLI: `npm install -g --prefix ~/.local <package>` (the command goes
  to `~/.local/bin`). A plain `npm install -g` writes to the root-owned
  `/usr` and fails without sudo.
- A single binary or an AppImage: put it in `~/.local/bin` and make it
  executable (`chmod +x`). If an AppImage does not start (no FUSE), run it
  with `--appimage-extract-and-run`.
- CLIs for desktop applications: use `cli-hub` (skill `lydiabox-cli-hub`).
- Do not use snap or flatpak. They are not installed.

## Desktop (GUI) applications

- The VM desktop is X display `:0` (1920x1080, Xfce). `DISPLAY=:0` is set
  in harness sessions and terminals. The user sees this desktop on the
  Desktop page.
- Start a GUI program in the background so that your shell does not
  wait: `nohup <program> >/tmp/<program>.log 2>&1 &`. Then use
  `lbx computer screenshot` to see it.
- `lbx computer launch chrome|blender|terminal` starts the programs that
  the image has.
- An application that apt installs usually adds a menu entry (a
  `.desktop` file in `/usr/share/applications`) to the Xfce Applications
  menu. For a program in the home, put its `.desktop` file in
  `~/.local/share/applications`, which persists.
