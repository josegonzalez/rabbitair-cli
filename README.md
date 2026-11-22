# rabbitair-cli

A command line tool to discover and control Rabbit Air air purifiers (MinusA2, A3, BioGS) over the local network.

It speaks the same local UDP protocol (port 9009) as the [python-rabbitair](https://github.com/rabbit-air/python-rabbitair) library used by the Home Assistant integration.

## Install

Download the binary for your system from the [latest release](https://github.com/josegonzalez/rabbitair-cli/releases/latest). Binaries are built for Linux, macOS and Windows on amd64 and arm64, and have no dependencies. On Linux and macOS, make the download executable:

```shell
chmod +x rabbitair-cli_*_darwin_arm64
mv rabbitair-cli_*_darwin_arm64 /usr/local/bin/rabbitair-cli
```

macOS may refuse to open a downloaded binary that is not notarized. Clear the quarantine flag with `xattr -d com.apple.quarantine /usr/local/bin/rabbitair-cli`.

Each release lists SHA-256 checksums, and the binaries have signed build provenance, which you can check with the GitHub CLI:

```shell
gh attestation verify rabbitair-cli_*_darwin_arm64 --repo josegonzalez/rabbitair-cli
```

Or build it with Go:

```shell
go install github.com/josegonzalez/rabbitair-cli@latest
```

`rabbitair-cli version` prints the installed version.

## Getting an access token

Every command except `discover` needs the device's access token, a 32 character hex string. You get it from the Rabbit Air mobile app.

Rabbit Air 2 app (per the [Home Assistant docs](https://www.home-assistant.io/integrations/rabbitair/)):

1. Open the app and tap your device to open its control page.
2. Open the three-dot menu in the top right and select "Rename".
3. On the "Rename device" screen, tap the device name to reveal the Thing ID and User key.

The User key is the token. The Thing ID is the device ID shown by `discover`.

Older Rabbit Air app (per the python-rabbitair README):

1. Tap your device to open its control page, then tap "Edit".
2. Quickly tap "Serial Number" several times until two more lines appear. The first is the device ID and the second is the access token.
3. If you see "Tap for setup user key" instead, tap it and follow the prompts to generate a token.

Home Assistant lists first generation MinusA2 units as not supported.

## Usage

Find devices:

```shell
rabbitair-cli discover
```

```
ADDRESS        PORT  ID            DEVICE ID
192.168.1.188  9009  8CAAB5AAE3A4  fd5094f593b5539e799be6eea220
```

The quickest way to get started is `setup`:

```shell
rabbitair-cli setup
```

It finds every purifier on the network and checks which ones are not saved yet or no longer accept their saved key. For each one, it explains where to find the user key in the old and new apps, shows the Thing ID the app should display, asks for the key and checks it against the purifier. If the purifier rejects a key that matches the app, the key saved on the purifier is probably out of date. `setup` then explains how to re-pair it in the app, finds the purifier again (re-pairing changes its Thing ID) and asks for the new key. Saved devices whose address changed are updated automatically. Use `--skip-saved` to skip checking keys that are already saved.

You can also save devices one at a time, so you don't need to pass the host and token every time:

```shell
rabbitair-cli config add bedroom --host 192.168.1.188 --token <token>
rabbitair-cli config add office --host 192.168.1.189 --token <token>
rabbitair-cli config list
```

`config add` checks the token against the device and reads its MAC address before saving. The first device saved becomes the default, and `config default <name>` changes it. Use `--device <name>` (or `RABBITAIR_DEVICE`) to pick another one. `config remove <name>` deletes a device and its token.

Give a device a location with `--location` on `config add`, or later with `config location <name> "Living Room"`. `setup` asks for one too. Locations show up in `config list` and can be used with `--device`, for example `rabbitair-cli state --device kitchen`. Each location can only be used by one device.

Device details are saved in `$XDG_CONFIG_HOME/rabbitair-cli/config.toml` (`~/.config/rabbitair-cli/config.toml` when `XDG_CONFIG_HOME` is unset). Tokens are stored in the OS keychain (macOS Keychain, the Linux Secret Service or Windows Credential Manager) under the service `rabbitair-cli`. When no keychain is usable, or with `--insecure-storage`, the token is saved in the config file instead. The config file is created with `0600` permissions.

You can also set the host and token through flags or the environment, which take precedence over saved devices:

```shell
export RABBITAIR_HOST=192.168.1.188
export RABBITAIR_TOKEN=0123456789ABCDEF0123456789ABCDEF
```

The host can be an IP address or a device ID, which is resolved as `<device id>.local`.

Show the current state, including the model:

```shell
rabbitair-cli state
```

Show firmware, MAC address and uptime:

```shell
rabbitair-cli info
```

Change settings (the new state is printed afterwards):

```shell
rabbitair-cli set --power on --mode manual --speed high
rabbitair-cli set --mode auto --moodlight off
rabbitair-cli set --power off
```

Run `rabbitair-cli set -h` for every setting.

Send a raw command:

```shell
rabbitair-cli raw --cmd 4 --data '{"buzzer":false}'
```

All device commands accept `--json`, `--timeout` and `--retries`. Use `--network tcp` (or `RABBITAIR_NETWORK=tcp`) to talk to the device over TCP instead of UDP.

## Troubleshooting

A device given the wrong token does not reply over UDP, so the command times out. Over TCP, it closes the connection and the command fails with `EOF`.

## Library

The `rabbitair` package can be used on its own:

```go
c, err := rabbitair.NewClient("192.168.1.188", token)
if err != nil {
	return err
}
defer c.Close()

state, err := c.GetState(ctx)
```

## Releasing

Releases are made by the "Release" workflow. In the Actions tab, open "Release", click "Run workflow" on `main` and pick `patch`, `minor` or `major`, or run:

```shell
gh workflow run release.yml --ref main -f bump=patch
```

The workflow checks formatting, runs `go vet` and the tests, and bumps the latest `v*` tag (`v0.0.0` when there is none). It then builds static binaries with `CGO_ENABLED=0` for each platform, attests their build provenance, and creates the tag and a GitHub release with the binaries and a checksum file.

## License

The protocol implementation is based on python-rabbitair, which is licensed under Apache-2.0.
