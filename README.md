# aqaramcp

[![Go Reference](https://pkg.go.dev/badge/github.com/mshaulsky/aqaramcp.svg)](https://pkg.go.dev/github.com/mshaulsky/aqaramcp)
[![CI](https://github.com/mshaulsky/aqaramcp/actions/workflows/ci.yml/badge.svg)](https://github.com/mshaulsky/aqaramcp/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/mshaulsky/aqaramcp)](https://goreportcard.com/report/github.com/mshaulsky/aqaramcp)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A small, dependency-free Go client that reads an **Aqara Home** account's
devices through the **Aqara MCP server** — the cloud service Aqara built for
language models, which turns out to be the easiest way for a program to get at
your own devices.

```sh
go get github.com/mshaulsky/aqaramcp
```

## Why this exists

Aqara's Open API needs a developer account that is reviewed by hand, and a
personal application can sit in that queue for weeks with no answer. The MCP
server needs nothing of the sort: sign in at
[agent.aqara.com](https://agent.aqara.com/login) with the same account you use
in the app, copy the API key it shows, and that key reads every device you own.
No developer platform, no project, no review.

The service speaks [MCP](https://modelcontextprotocol.io) over streamable HTTP
and describes its tools in Chinese prose, but its answers are structured
tables — which is all a program needs.

* **No third-party dependencies.** Standard library only.
* **`context.Context` on every call**, so timeouts and cancellation work.
* **The session is managed for you** — opened on first use, reopened when the
  server forgets it.
* **Errors carry context** and are matchable with `errors.As`.

## Quick start

```go
client, err := aqaramcp.New(os.Getenv("AQARA_MCP_KEY"))
if err != nil {
    log.Fatal(err)
}

statuses, err := client.Statuses(ctx, aqaramcp.StatusFilter{})
if err != nil {
    log.Fatal(err)
}
for _, s := range statuses {
    switch s.Type {
    case aqaramcp.TypeDoorLock:
        state, _ := s.Status.Get(aqaramcp.KeyLockState)
        log.Printf("%s: lock_state=%s", s.DeviceName, state)
    case aqaramcp.TypeWaterLeakSensor:
        wet, _ := s.Status.Bool(aqaramcp.KeyWaterLeak)
        log.Printf("%s: wet=%t", s.DeviceName, wet)
    }
}
```

One `Statuses` call with an empty filter returns every device in the home with
its current state — the whole dashboard in one round trip.

## Getting the key

1. Open [agent.aqara.com/login](https://agent.aqara.com/login) and sign in
   with your Aqara Home account.
2. Copy the `aqara_api_key` the page shows. It is a credential: keep it out of
   repositories and screenshots. The page also shows an `aqara_mcp_url`, which
   is the default endpoint this package already uses.

Then explore the account with the bundled tool:

```sh
export AQARA_MCP_KEY=...

go run ./examples/poll -devices
go run ./examples/poll -status
go run ./examples/poll -status -watch 2m
```

## Signing in from code

The key the login page hands out expires after some days, and the service has
no refresh token: the only way to a new key is to sign in again. The page does
that with one plain request, and so can this package:

```go
creds := aqaramcp.Credentials{
    Username:    os.Getenv("AQARA_USERNAME"),
    PasswordMD5: os.Getenv("AQARA_PASSWORD_MD5"),
    Region:      aqaramcp.RegionRU,
}
client, err := aqaramcp.New("", aqaramcp.WithLogin(creds))
```

Built this way, the client signs in at first use and again whenever the server
rejects the key it holds; the call that met the rejection is made once more
with the fresh key, so a long-running program never notices an expiry. A
refused sign-in (wrong account, password or region) comes back as a
`*LoginError` and is not retried for an hour, so a mistyped password does not
hammer the account. `WithLoginHook` tells you about every sign-in, and `Login`
alone fetches a key for other uses.

Two things worth knowing:

* **The region** is the one the account was registered in; the login page
  picks it from the country. `RegionEU` covers Europe and Africa, `RegionRU`
  Russia and the CIS (Kazakhstan included), `RegionUS` the Americas,
  `RegionCN` mainland China, `RegionKR` Korea and `RegionSG` the rest of Asia,
  Australia and the Middle East. A wrong region is refused like a wrong
  password.
* **The service only ever sees the MD5 of the password**, and so does this
  package: `Credentials` takes the digest (`printf %s "$password" | md5sum`),
  never the password, so the password need not exist on the machine that
  signs in. The digest is still a credential: anyone holding it can sign in.

`go run ./examples/login -status` signs in once and reads the devices with the
fresh key; with `AQARA_MCP_KEY` set to an earlier key it also tells whether
the new sign-in revoked that key.

## What comes back

`Devices` lists the home; `Statuses` adds each device's state as a flat set of
text values:

| Device type | Status keys seen |
|---|---|
| `DoorLock` | `lock_state` (`"1"` while locked), `online_offline` |
| `WaterLeakSensor` | `water_leak` (`"True"`/`"False"`), `online_offline` |
| `Outlet` | `on_off` (`"on"`/`"off"`), `online_offline` |
| `Button`, `Hub` | `online_offline` only |

`Status.Bool` reads any of those spellings as a flag. Keys not listed here
appear for other device types; `Status` is a plain map, so nothing is lost.

Two things the platform does **not** hand out through this service, as of
September 2026: power and energy readings for outlets (the status carries only
`on_off`), and device events such as button presses or lock openings (the log
tool covers commands sent to devices, not what sensors report). Both are gaps
of the service, not of the client.

## Any other tool

The typed methods cover what a dashboard polls. Everything else the server
offers — scenes, automations, control — is one `Call` away:

```go
tools, _ := client.Tools(ctx)                    // names, descriptions, input schemas
result, err := client.Call(ctx, "scene_base_inquiry", nil)
fmt.Println(result.Text())                       // the JSON envelope, as text
```

Read the answer from `Text()`. The server also fills `StructuredContent`, but
inconsistently — wrapped in a `result` object on success and bare on failure —
so this package ignores it.

## Errors

* `*ToolError` — the platform ran the tool and refused: `Message` is its own
  wording (e.g. `Device control action not supported`) and `TraceID` is what to
  quote to support.
* `*RPCError` — the MCP layer refused the request, e.g. an unknown tool.
* `*HTTPError` — the response was not an MCP answer at all: a rejected key
  arrives as `401 Unauthorized: {"error":"unauthorized or expired"}`.
* `*LoginError` — the service refused a sign-in, in its own words; the
  account, the password or the region is wrong.

## Limits

Aqara publishes no quotas for this service. It is a cloud round trip that in
turn reads the device cloud, so treat it as slow (a second or two) and poll at
the pace of an ambient dashboard — once every minute or two — not once a
second.

## Development

```sh
go test ./...        # no credentials or network required
go generate ./...    # regenerate the mocks
golangci-lint run    # same configuration CI uses
```

The suite runs against an in-process fake of the MCP server (`httptest`) that
checks the headers and the session handshake of every request and answers
either as plain JSON or as an event stream, so both transport shapes are
exercised. Tool answers in the tests are copied from the live server's, down
to the Python dictionary literal it uses for a device's status.

## License

MIT — see [LICENSE](LICENSE).
