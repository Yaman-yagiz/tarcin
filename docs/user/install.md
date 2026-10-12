# Install

## From the AUR

```sh
yay -S tarcin        # the latest release
yay -S tarcin-git    # the latest commit
```

Any AUR helper works, or `makepkg -si` in a clone of the package. Both
packages build from source with the Arch Go flags and link the system
SQLite. The release package checks the release's PGP signature.

Optional dependencies:

- `xdg-utils`, to open media with your default applications;
- a notification daemon (`dunst`, `mako` or your desktop's own) for
  desktop notifications. Without one, tarcin rings the terminal bell.

## From source

You need Go 1.26 or newer, a C compiler, `git`, and `scdoc` for the man
pages.

```sh
git clone https://github.com/Yaman-yagiz/tarcin.git
cd tarcin
make                           # ./build: the binary (bundled SQLite), man pages, example config
sudo make install PREFIX=/usr  # copies them, the completions and the desktop file into place
```

`make install DESTDIR=… PREFIX=…` installs anywhere; `make uninstall`
removes what it installed. `make help` lists every target.

## Try it without an account

```sh
tarcin --demo
```

The demo runs on simulated chats: it needs no account or network and
keeps nothing. `tarcin --demo --demo-link` shows the link screen.

## Update

Update through your AUR helper. WhatsApp changes its protocol from time
to time; when it stops accepting an old tarcin, tarcin says so, and an
update is needed. Releases follow such changes quickly.
