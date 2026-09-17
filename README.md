# Virtualis

Virtualis is a single-admin master for managing remote VM and container agents. It ships as a pure-Go binary with an embedded web UI, exposes a versioned HTTP API for automation, and talks to lightweight agents that do the actual work on each node.

- Instances are created on a selected agent, never on the master host.
- Agents report installed drivers and execute lifecycle operations locally.
- QEMU instances expose VNC through the master's noVNC WebSocket proxy.
- Metrics and network inspection are collected from the assigned agent.
- Instance networking supports NAT, bridge, MAC, IPv4, gateway, DNS, and bandwidth settings where the selected driver supports them.
- Fractional CPU limits (e.g. 0.25 cores) are enforced with cgroup bandwidth (`cpu_milli`), with an lxcfs-backed view of `/proc` and the CPU topology inside containers.
- Per-instance dedicated disk pools give real disk quotas and a truthful `df` view inside the instance.

## Quick start

Download the binary for your platform from [Releases](https://github.com/SakuraOpenSource/virtualis/releases), then:

```bash
chmod +x virtualis-os-arch
./virtualis-os-arch -data ./data
```

Open <http://localhost:8080> in your browser to run the installer.

### CLI flags

| Flag | Default | Description |
|---|---|---|
| `-data` | `data` | Data directory holding `config.json` and the SQLite file |
| `-listen` | Value from the config file (initially `:8080`) | Listen address, overrides the config |
| `-debug` | `false` | Enable debug mode |
| `-version` | | Print the version and exit |
| `--reset-password` | | Reset the administrator password and exit |

### Data directory

After installation, the data directory contains `config.json` and the database file (SQLite mode only).

Deleting `config.json` returns the program to the uninstalled state (data in the database is kept).

If the administrator password is forgotten, stop the service and run:

```bash
/opt/virtualis/virtualis -data /var/lib/virtualis --reset-password
```

## HTTP API

Automation uses API keys (`Authorization: Bearer <key>`) against the `/api/v1` group:

```
GET    /api/v1/instances             list instances
POST   /api/v1/instances             create an instance on an agent
GET    /api/v1/instances/:id         instance detail (status, NAT, metrics source)
POST   /api/v1/instances/:id/start|stop|restart
DELETE /api/v1/instances/:id
GET    /api/v1/instances/:id/nat     list NAT port mappings
POST   /api/v1/instances/:id/nat     create a mapping (port 0 = auto-allocate)
DELETE /api/v1/instances/:id/nat/:mid
```

The web UI talks to the same backend through `/api/*` session endpoints (CSRF-protected); VNC connects through `/api/vnc/ws` using short-lived tickets, so agent tokens never reach the browser.

## Building from source

### Requirements

- Go 1.25+
- Node 20+
- pnpm 9+

### Repository layout

```
Virtualis-Project/
├── virtualis/            # this repository (master)
├── virtualis-frontend/   # web UI
└── virtualis-agent/      # agent (may be absent in this checkout)
```

### Build

```bash
git clone https://github.com/SakuraOpenSource/virtualis.git
git clone https://github.com/SakuraOpenSource/virtualis-frontend.git
cd virtualis
make build        # build frontend => copy into internal/web/dist => compile binary
./bin/virtualis -data ./data
```

If the frontend checkout lives somewhere else, use `make build FRONTEND=/path/to/virtualis-frontend`.

| Target | Description |
|---|---|
| `make build` | Frontend + backend, produces `bin/virtualis` |
| `make backend` | Backend only, reuses the existing `internal/web/dist` assets |
| `make frontend` | Builds the frontend and copies it into `internal/web/dist` |
| `make test` / `make vet` / `make fmt` | Tests, static checks, formatting |
| `make clean` | Removes build outputs and resets `internal/web/dist` |

For full multi-platform builds of the master and the agent, use:

```bash
bash build_virtualis.sh --all
```

The backend can run without a frontend build, but only the API is available then.

## Development

```bash
CGO_ENABLED=0 go run ./cmd/virtualis -debug   # terminal 1, listens on :8080
pnpm --dir ../virtualis-frontend dev           # terminal 2, visit http://localhost:5173
```

The Vite dev server proxies `/api` to `http://127.0.0.1:8080`.

## Tests

```bash
make test
```

## License

This project is licensed under GPL-v3.
