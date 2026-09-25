# Quidax Clone

This Go application uses MySQL for application data and TigerBeetle for financial transactions.

## Run with Docker Compose

Docker and Docker Compose v2 (either `docker compose` or the standalone v2 `docker-compose`) are required. Legacy Compose 1.x is incompatible with current Docker Engine image metadata.

1. Create local secret files. Do not commit these files:

   ```bash
   mkdir -p secrets
   openssl rand -base64 32 > secrets/mysql_password
   openssl rand -base64 32 > secrets/mysql_root_password
   chmod 700 secrets
   chmod 644 secrets/mysql_password
   chmod 600 secrets/mysql_root_password
   ```

2. Optionally copy `.env.example` to `.env` and change non-secret ports or service settings. Keep password values in the secret files, not in `.env`.

3. Build and start all three services:

   ```bash
   make start
   ```

4. Check the application and service state:

   ```bash
   curl --fail http://127.0.0.1:55059/healthz
   make status
   ```

The API binds to `127.0.0.1:55059` by default. MySQL and TigerBeetle do not publish host ports; they are reachable only by containers on the `quidax_backend` Docker network. MySQL and TigerBeetle data are stored in named Docker volumes.

An application started with `docker run` can join the network directly:

```bash
docker run --network quidax_backend your-image
```

From that container, connect to MySQL at `datadb:3306` and TigerBeetle at `tigerbeetle:3000`. For another Compose project, declare the existing network and attach the relevant service:

```yaml
services:
  other-app:
    image: your-image
    networks:
      - quidax_backend

networks:
  quidax_backend:
    external: true
    name: quidax_backend
```

Set `BACKEND_NETWORK_NAME` in `.env` when a different shared network name is required. Joining this network grants direct network access to the databases, so only attach trusted containers and continue using MySQL authentication.

Use `make logs` to follow logs and `make stop` to stop the stack. Run `docker-compose down --volumes` only when you intentionally want to delete all local database data.

## Run the Go process outside Docker

Copy `env.MAK.sample` to `env.MAK`, ensure MySQL and TigerBeetle are running, then use `make run`. `env.MAK` and all non-example files under `secrets/` are ignored by Git.

## Deploy from GitHub Actions

The workflow in `.github/workflows/deploy.yml` tests the application, publishes commit-SHA-tagged app, MySQL, and TigerBeetle images to GHCR, then deploys `compose.production.yml` over SSH. It runs for pushes to `main` and can also be started manually.

### Prepare the server

Install Docker Engine with Docker Compose v2, add the deployment user to the Docker group, and create a directory that user owns:

```bash
sudo install -d -m 700 -o deploy -g deploy /opt/quidax-go
```

The example assumes a deployment user named `deploy`. Put a reverse proxy on the same server in front of `127.0.0.1:55059`, or deliberately set `APP_BIND_ADDRESS` to `0.0.0.0` and configure the server firewall.

### Configure the `production` GitHub Environment

In **Settings → Environments**, create an environment named `production`. Add approval and protected-branch rules as appropriate.

Add these environment secrets:

| Secret | Purpose |
| --- | --- |
| `DEPLOY_HOST` | Server hostname or IP address |
| `DEPLOY_USER` | SSH deployment user |
| `DEPLOY_SSH_PRIVATE_KEY` | Private key dedicated to deployments |
| `DEPLOY_SSH_KNOWN_HOSTS` | Pinned server host-key line from a trusted source |
| `MYSQL_PASSWORD` | Application MySQL user password |
| `MYSQL_ROOT_PASSWORD` | Separate MySQL root password |
| `GHCR_DEPLOY_TOKEN` | Personal access token (classic) with `read:packages` for pulling private images |

Add these environment variables when their defaults are not suitable:

| Variable | Default | Purpose |
| --- | --- | --- |
| `DEPLOY_PORT` | `22` | SSH port |
| `DEPLOY_PATH` | `/opt/quidax-go` | Server deployment directory |
| `GHCR_USERNAME` | Repository owner | User associated with the GHCR pull token |
| `APP_BIND_ADDRESS` | `127.0.0.1` | Address exposed on the server |
| `APP_PORT` | `55059` | API port on the server |
| `MYSQL_DATABASE` | `quidax-go` | MySQL database name |
| `MYSQL_USER` | `quidax` | MySQL application user |
| `TIGERBEETLE_CACHE_GRID` | `256MiB` | TigerBeetle cache allocation |
| `BACKEND_NETWORK_NAME` | `quidax_backend` | Shared Docker network used by the services |

Optionally set the repository variable `DOCKER_PLATFORMS` if the server is not `linux/amd64`, for example `linux/arm64`.

Passwords are written to server-side secret files and mounted into containers. They are not put in the generated `.env`, image build arguments, container environments, or committed files.

> **Database schema note:** MySQL entrypoint scripts only run when its data volume is first initialized. Updating `db/sql/schema.sql` does not migrate an existing server database; add a migration tool before making post-deployment schema changes.
