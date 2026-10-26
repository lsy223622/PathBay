# PathBay

A lightweight, self-hosted HTTP route and response server. Configure redirects,
text/HTML, JSON, downloads and static directories from an embedded admin page.
Builtins provide IP, time and random values. A single administrator manages the
instance through the browser or the JSON API.

Settings includes an optional limit for repeated missing-resource requests,
disabled by default. After 20 such responses from one IP within 60 seconds,
public requests from that IP receive `429` with `Retry-After` for 5 minutes.
Access restores automatically; administration remains available. Limited
requests are excluded from access statistics and logs. Counters are kept only
in memory and work independently of detailed logging.

Paste a custom 404 HTML page in Settings to use it for missing public routes,
missing files and disabled builtins. Responses retain HTTP status `404`.
Leave it empty to use the default response. The HTML is included in configuration
JSON exports and can contain up to 1 MiB.

## Run

Download an archive from [Releases](https://github.com/lsy223622/PathBay/releases/latest), extract it and run:

```sh
./pathbay --listen 127.0.0.1:7284 --data-dir ./data
```

Open `/admin/` and create the administrator immediately. The default address is
`127.0.0.1:7284`; the default data directory is `data` under the working directory.
`PATHBAY_LISTEN`, `PATHBAY_DATA_DIR` and `PATHBAY_TRUSTED_PROXIES` override those
deployment defaults. `--upload-limit` sets the maximum file size in bytes
(default 64 MiB). Passwords need at least 12 bytes.

Put Nginx or another trusted reverse proxy in front for HTTPS. Use `proxy_pass`
without a URI suffix; overwrite `Host`, `X-Real-IP` and `X-Forwarded-Proto`.
The default trusted ranges are `127.0.0.1/32,::1/128`. Set `--trusted-proxies`
explicitly for additional proxy addresses. Configure 60-second idle body and
response transfer timeouts, and an upload body limit large enough for multipart
overhead. Backend total read/write timeouts are unset for streaming transfers.

```nginx
location / {
    proxy_pass http://127.0.0.1:7284;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    client_max_body_size 65m;
    client_body_timeout 60s;
    send_timeout 60s;
    proxy_read_timeout 60s;
    proxy_send_timeout 60s;
    proxy_request_buffering off;
}
```

Only publish HTML/JS you trust: public content and the admin page share an origin.
Static directories serve ordinary files and `index.html`, never a directory list.
External local files/directories belong to the administrator and are never deleted
by PathBay. Uploaded files belong to their route.

## API and credentials

The API lives at `/admin/api/v1/`. Browser writes require the session's
`X-CSRF-Token`; automation uses `Authorization: Bearer <token>`. Explicit Bearer
credentials take precedence over cookies. Tokens grant full administrator access
and are displayed only when created.

Resources: `routes` (GET/POST), `routes/{id}` (GET/PATCH/DELETE),
`routes/{id}/file` (PUT multipart file), `builtins` and `settings` (GET/PATCH),
`config/export` (GET), `config/import` (POST), `stats/summary`, `stats/daily`,
`stats/logs` (GET), `tokens` (GET/POST), `tokens/{id}` (DELETE).
Routes use `method` GET/POST/ANY, `match` exact/prefix, `boundary`, `enabled`,
and an `action` with `type` redirect/text/json/file/directory. The admin page is
the reference editor for these fields. Lists default to 50, with `limit` up to
200; routes use `page`, logs return `next_cursor`. Logs accept `hit_id`, RFC 3339
`from`/`to` and `cursor`. Daily statistics accept `hit_id` and `days` (1–366).

Stop the service before local credential commands; they use the same data lock:

```sh
./pathbay password reset --data-dir ./data
./pathbay token create --name automation --data-dir ./data
./pathbay token list --data-dir ./data
./pathbay token revoke TOKEN_ID --data-dir ./data
```

## Data and backups

`app.db` stores committed configuration and credentials; `stats.db` stores visits;
`files/` contains uploads. Public routing reads memory snapshots. Detailed logs
are off by default. Statistics are asynchronous and may lose pending data on
overload, forced termination or power failure; the admin page shows dropped
records and write errors. HEAD does not increase hits. Retention affects detailed
logs only. The first initialization saves the server's time zone and its TZif
rules; restarting or restoring preserves those rules.

For a complete backup, stop PathBay normally, then copy the entire data directory.
Copy external file/directory contents separately. Restore into a stopped instance.
Sessions are in memory and require a fresh login after restart. JSON export holds
configuration and file references, not file contents, credentials, statistics or
the fixed instance time zone. Import merges configuration, allocating new route
IDs; any conflict rejects the whole batch.

## Build and Docker

Go 1.27.3 or later with CGO and a C compiler is required. SQLite is linked from
[go-sqlite3](https://github.com/mattn/go-sqlite3).

```sh
go test ./...
go build ./cmd/pathbay
docker build -t pathbay .
docker run --name pathbay -p 127.0.0.1:7284:7284 -v "$PWD/data:/data" -e TZ=Asia/Shanghai pathbay
```

Linux amd64 is the primary target. CI also builds Linux arm64 and Windows amd64.
Windows initialization maps the system time zone using the CLDR default-region
mapping and rejects custom/unknown rules. Release archives include SHA-256 checksums.
Public Docker images are available as `ghcr.io/lsy223622/pathbay` for Linux amd64
and arm64. The default `latest` tag follows the latest stable release; version
tags such as `ghcr.io/lsy223622/pathbay:v1.0.0` are also available.

### Docker Compose deployment

Use `compose.yaml` in a Linux Docker Compose project. Edit `TZ` in the Compose
file before creating the administrator if your time zone is not `Asia/Shanghai`.
Initialization saves that zone's rules in the data
directory; later environment changes do not change an existing instance's zone.

The Compose service uses host networking and listens only on
`127.0.0.1:7284`. This matches a reverse proxy running on the host or with host
networking: its connection reaches PathBay from loopback, which is the explicit
trusted proxy range. Point the site's reverse proxy to that address and use the
Nginx headers and transfer settings above. A proxy in a separate bridge network
cannot reach this loopback listener; configure that proxy to use host networking
before using this Compose setup. Edit `--listen` in the Compose file to change the local port.

The bind mount preserves the complete instance data when a container or image is
replaced. It runs as UID/GID 1000 by default. Prepare the data directory in the
Compose project's working directory before starting it:

```sh
mkdir -p data
sudo chown 1000:1000 data
docker compose config --quiet
docker compose up -d
```

If you change the data mount or container user in the Compose file, prepare
the directory with matching ownership. Mount any external local files
or static directories at the same absolute container paths referenced by their
routes, preferably read-only.

To update to the latest stable release, pull the image, stop normally, back up
the entire data directory and recreate:

```sh
docker compose pull
docker compose stop
cp -a data "data-backup-$(date +%Y%m%d-%H%M%S)"
docker compose up -d
```

Use your data directory in the backup command if you changed the mount.
Keep the previous image and backup for recovery; restoring requires a stopped
instance. Log in again after recreation.

Time-zone data comes from Go's IANA archive; Windows mappings come from
[Unicode CLDR 48](https://github.com/unicode-org/cldr/tree/release-48), under the
[Unicode license](https://www.unicode.org/license.txt).
