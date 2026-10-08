# Services that are not in the catalogue

Written by `tools/catalog`. The catalogue is filled from Coolify's templates and
Dokploy's blueprints; these are the ones that could not be turned into a
template musdash's rules accept, each with the reason. A service listed here
can still be run from its own Compose file, changed by hand.

| Service | From | Why it is left out |
|---|---|---|
| alltube | dokploy | its repository is archived, and nothing has changed since April 2023 |
| anonupload | dokploy | its project is given up: nothing has changed since February 2023, and its site is for sale |
| anythingllm | coolify | it needs the capability SYS_ADMIN, which a stack may not have |
| anythingllm | dokploy | it needs the capability SYS_ADMIN, which a stack may not have |
| appwrite | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| appwrite | dokploy | it serves one address from two services, by path |
| arche | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| authelia | dokploy | it ships a fixed password, as a hash, that would be the same on every install |
| authentik | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| authentik | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| autobase | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| autobase | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| azimutt | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| backrest | dokploy | it mounts a directory of the server (/) |
| barrage | dokploy | its project is given up: nothing has changed since January 2023 |
| beszel | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| beszel | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| beszel-agent | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| blender | dokploy | it uses "runtime": it replaces the container runtime |
| borgitory | dokploy | it needs the capability SYS_ADMIN, which a stack may not have |
| botpress | dokploy | Botpress ended v12, the version that is self-hosted: it is no longer to be downloaded or deployed |
| budge | coolify | its project is given up: nothing has changed since June 2022 |
| chirpstack | dokploy | with the files it carries it is larger than a Compose file may be (128 KB) |
| clickhouse | dokploy | musdash runs it as a database, with backups and a port of its own: see Databases |
| cloud9 | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| cloudcommander | dokploy | it mounts a directory of the server (/root) |
| cloudflare-ddns | coolify | it uses the server's own network (network_mode: host) |
| cloudflare-ddns | dokploy | it uses the server's own network (network_mode: host) |
| coder | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| coder | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| commento | dokploy | its project is given up: nothing has changed since February 2021, and its site is gone |
| commentoplusplus | dokploy | its project is given up: nothing has changed since October 2022 |
| confluence | dokploy | Atlassian ended Confluence Server in February 2024, and its image has not been built since |
| creed | dokploy | it builds an image from source, which a template has none of |
| crowdsec | dokploy | it mounts a file next to the Compose file that the template does not carry (../files/acquis.yaml) |
| cup | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| diun | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dockge | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dockroute | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dokploy-prom-monitoring-extension | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| doublezero | dokploy | its project is given up: nothing has changed since September 2024 |
| dozzle | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dozzle | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dozzle-with-auth | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dragonfly-db | dokploy | musdash runs it as a database, with backups and a port of its own: see Databases |
| edgedb | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| embystat | coolify | its image is deprecated and its repository archived: EmbyStat is no longer developed |
| ente-photos | coolify | its address WEB names two ports, 3002 and 3004 |
| erpnext-v16 | dokploy | the catalogue has it as erpnext |
| esphome | coolify | it uses the server's own network (network_mode: host) |
| espocrm | coolify | it uses "volumes_from": it mounts another container's data |
| evolutioncrm | dokploy | it names an address that no service is routed to (evoia_domain) |
| firecrawl | dokploy | it needs a generated value musdash has no equal of: ${uuid} |
| focalboard | dokploy | its makers say that it is not maintained |
| forgejo-with-runner | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| forgejo-with-runner-with-mariadb | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| forgejo-with-runner-with-mysql | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| forgejo-with-runner-with-postgresql | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| gel | dokploy | its makers closed down in December 2025 |
| gitea-runner | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| github-runner | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| glances | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| glances | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| hoarder | dokploy | it is Karakeep now, which the catalogue has |
| home-assistant | coolify | it mounts a directory of the server (/run/dbus) |
| homeassistant | dokploy | it mounts a directory of the server (/run/dbus) |
| jenkins | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| jenkins | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| kestra | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| keydb | dokploy | musdash runs it as a database, with backups and a port of its own: see Databases |
| kokoro-tts | dokploy | it builds an image from source, which a template has none of |
| kuzzle | coolify | it needs the capability SYS_PTRACE, which a stack may not have |
| litequeen | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| livekit | dokploy | it needs the capability SYS_ADMIN, which a stack may not have |
| lodestone | dokploy | its project is given up: nothing has been released since June 2024 |
| macos | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| macos | dokploy | it needs the capability NET_ADMIN, which a stack may not have |
| marketing-dashboard | dokploy | its repository is gone |
| mautic5 | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| maybe | coolify | its repository is archived (July 2025); Sure, which the catalogue has, carries it on |
| maybe | dokploy | its repository is archived (July 2025); Sure, which the catalogue has, carries it on |
| mcsmanager | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| minepanel | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| minio-community-edition | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| moltbot | dokploy | it is OpenClaw now, which the catalogue has |
| morphos | dokploy | its repository is archived (January 2026) |
| mulesoft-esb | dokploy | the repository of Mule Community Edition is archived (September 2026) |
| multica | dokploy | it builds an image from source, which a template has none of |
| neon-ws-proxy | coolify | as written it relays a visitor to any address (ALLOW_ADDR_REGEX=.*), and a template gives it a public one |
| netbird-client | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| netdata | dokploy | it mounts a directory of the server (/etc/passwd) |
| nextcloud-aio | dokploy | the catalogue has it as nextcloud |
| obsidian | coolify | it turns off a protection of its container (seccomp=unconfined) |
| odoo-17 | dokploy | the catalogue has it as odoo |
| odoo-18 | dokploy | the catalogue has it as odoo |
| odoo-19 | dokploy | the catalogue has it as odoo |
| openhands | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| openresty-manager | dokploy | it mounts a directory of the server (/etc/resolv.conf) |
| openstatus | dokploy | it builds an image from source, which a template has none of |
| osticket | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| outline | dokploy | it ships a fixed password, as a hash, that would be the same on every install |
| overseerr | coolify | its repository is archived (February 2026); it goes on as Seerr |
| palmr | dokploy | its maker says that it is no longer maintained |
| paperless-ngx | dokploy | the catalogue has it as paperless |
| peerdb | dokploy | it serves one address from two services, by path |
| peppermint | coolify | its repository is archived (July 2026) |
| peppermint | dokploy | its repository is archived (July 2026) |
| photoprism | dokploy | it turns off a protection of its container (seccomp:unconfined) |
| pi-hole | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| picsur | dokploy | its maker says that it is not maintained |
| pingvinshare | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| pingvinshare-with-clamav | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| portainer | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| portainer | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| postgresus | dokploy | it is Databasus now, which the catalogue has |
| pre0.22.5-supabase | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| pterodactyl-with-wings | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| pulse | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| pyrodactyl | dokploy | its repository is archived (July 2026); it goes on as Hydrodactyl |
| registry | dokploy | the catalogue has it as docker-registry |
| scrutiny | dokploy | it mounts a directory of the server (/run/udev) |
| sim | dokploy | it serves one address from two services, by path |
| snapdrop | coolify | its image is deprecated and its repository archived (February 2025); PairDrop, which the catalogue has, took its place |
| snapp | dokploy | it mounts a file next to the Compose file that the template does not carry (../files/snapp-db) |
| soketi | coolify | its project is given up: nothing has changed since March 2024 |
| soketi | dokploy | its project is given up: nothing has changed since March 2024 |
| sonarqube | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| spacebot | coolify | it turns off a protection of its container (seccomp=unconfined) |
| statusnook | coolify | its project is given up: nothing has changed since June 2024 |
| stirling | dokploy | the catalogue has it as stirling-pdf |
| streamflow | dokploy | it builds an image from source, which a template has none of |
| supabase | coolify | it needs a generated value musdash has no equal of: SERVICE_SUPABASEANON |
| supabase | dokploy | its template.toml could not be read |
| swetrix | coolify | it needs the capability SYS_NICE, which a stack may not have |
| tailscale-client | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| trigger | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| triggerdotdev | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| trilium | dokploy | it is TriliumNext now, which the catalogue has |
| wg-easy | dokploy | it mounts a directory of the server (/lib/modules) |
| whoogle | coolify | its repository is archived (2026) |
| windmill | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| windmill | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| windows | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| windows | dokploy | it needs the capability NET_ADMIN, which a stack may not have |
| wings | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| wireguard-easy | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| xsshunter | dokploy | it builds an image from source, which a template has none of |
| yt-dlp-webui | dokploy | its repository is archived (2026) |
| zep | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| zulip | dokploy | it reads its secrets from the environment of where Compose runs, which is musdash itself |
