# Services that are not in the catalogue

Written by `tools/catalog`. The catalogue is filled from Coolify's templates and
Dokploy's blueprints; these are the ones that could not be turned into a
template musdash's rules accept, each with the reason. A service listed here
can still be run from its own Compose file, changed by hand.

| Service | From | Why it is left out |
|---|---|---|
| anythingllm | coolify | it needs the capability SYS_ADMIN, which a stack may not have |
| anythingllm | dokploy | it needs the capability SYS_ADMIN, which a stack may not have |
| anytype | dokploy | it is reached on a port of its own, not through the proxy (a game, a VPN, a peer-to-peer node), and a template publishes none |
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
| beszel | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| beszel | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| beszel-agent | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| blender | dokploy | it uses "runtime": it replaces the container runtime |
| borgitory | dokploy | it needs the capability SYS_ADMIN, which a stack may not have |
| chirpstack | dokploy | with the files it carries it is larger than a Compose file may be (128 KB) |
| clickhouse | dokploy | musdash runs it as a database, with backups and a port of its own: see Databases |
| cloud9 | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| cloudcommander | dokploy | it mounts a directory of the server (/root) |
| cloudflare-ddns | coolify | it uses the server's own network (network_mode: host) |
| cloudflare-ddns | dokploy | it uses the server's own network (network_mode: host) |
| coder | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| coder | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| convertx | dokploy | it needs a generated value musdash has no equal of: ${jwt} |
| creed | dokploy | it builds an image from source, which a template has none of |
| crowdsec | dokploy | it mounts a file next to the Compose file that the template does not carry (../files/acquis.yaml) |
| cup | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| diun | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dockge | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dockroute | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dokploy-prom-monitoring-extension | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dozzle | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dozzle | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dozzle-with-auth | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| dragonfly-db | dokploy | musdash runs it as a database, with backups and a port of its own: see Databases |
| edgedb | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| enshrouded | dokploy | it is reached on a port of its own, not through the proxy (a game, a VPN, a peer-to-peer node), and a template publishes none |
| ente-photos | coolify | its address WEB names two ports, 3002 and 3004 |
| erpnext-v16 | dokploy | the catalogue has it as erpnext |
| esphome | coolify | it uses the server's own network (network_mode: host) |
| espocrm | coolify | it uses "volumes_from": it mounts another container's data |
| evolutioncrm | dokploy | it names an address that no service is routed to (evoia_domain) |
| firecrawl | dokploy | it needs a generated value musdash has no equal of: ${uuid} |
| forgejo-with-runner | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| forgejo-with-runner-with-mariadb | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| forgejo-with-runner-with-mysql | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| forgejo-with-runner-with-postgresql | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| gitea-runner | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| github-runner | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| glances | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| glances | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
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
| macos | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| macos | dokploy | it needs the capability NET_ADMIN, which a stack may not have |
| mautic5 | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| mcsmanager | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| minecraft | coolify | it is reached on a port of its own, not through the proxy (a game, a VPN, a peer-to-peer node), and a template publishes none |
| minepanel | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| minio-community-edition | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| multica | dokploy | it builds an image from source, which a template has none of |
| neon-ws-proxy | coolify | its address NEONPROXY does not say which port it goes to |
| netbird-client | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| netdata | dokploy | it mounts a directory of the server (/etc/passwd) |
| nextcloud-aio | dokploy | the catalogue has it as nextcloud |
| obsidian | coolify | it turns off a protection of its container (seccomp=unconfined) |
| odoo-17 | dokploy | the catalogue has it as odoo |
| odoo-18 | dokploy | the catalogue has it as odoo |
| odoo-19 | dokploy | the catalogue has it as odoo |
| openhands | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| openpanel | coolify | its address OPAPI does not say which service it is for |
| openpanel | dokploy | it serves one address from two services, by path |
| openresty-manager | dokploy | it mounts a directory of the server (/etc/resolv.conf) |
| openstatus | dokploy | it builds an image from source, which a template has none of |
| osticket | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| outline | dokploy | it ships a fixed password, as a hash, that would be the same on every install |
| palworld | coolify | its catalogue says nothing about it |
| paperless-ngx | dokploy | the catalogue has it as paperless |
| peerdb | dokploy | it serves one address from two services, by path |
| photoprism | dokploy | it turns off a protection of its container (seccomp:unconfined) |
| pi-hole | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| picsur | dokploy | it needs a generated value musdash has no equal of: ${jwt} |
| pingvinshare | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| pingvinshare-with-clamav | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| portainer | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| portainer | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| pre0.22.5-supabase | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| pterodactyl-with-wings | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| pulse | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| registry | dokploy | the catalogue has it as docker-registry |
| rustdesk | dokploy | it names an address that no service is routed to (server_domain) |
| satisfactory | coolify | it is reached on a port of its own, not through the proxy (a game, a VPN, a peer-to-peer node), and a template publishes none |
| scrutiny | dokploy | it mounts a directory of the server (/run/udev) |
| sim | dokploy | it serves one address from two services, by path |
| snapp | dokploy | it mounts a file next to the Compose file that the template does not carry (../files/snapp-db) |
| sonarqube | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| spacebot | coolify | it turns off a protection of its container (seccomp=unconfined) |
| sparkyfitness | coolify | its address SPARKYFITNESS does not say which service it is for |
| stirling | dokploy | the catalogue has it as stirling-pdf |
| streamflow | dokploy | it builds an image from source, which a template has none of |
| supabase | coolify | it needs a generated value musdash has no equal of: SERVICE_SUPABASEANON |
| supabase | dokploy | its template.toml could not be read |
| swetrix | coolify | its address SWETRIXAPI does not say which service it is for |
| tailscale-client | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| terraria-server | coolify | it is reached on a port of its own, not through the proxy (a game, a VPN, a peer-to-peer node), and a template publishes none |
| trigger | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| triggerdotdev | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| unifi | dokploy | it is reached on a port of its own, not through the proxy (a game, a VPN, a peer-to-peer node), and a template publishes none |
| wg-easy | dokploy | it mounts a directory of the server (/lib/modules) |
| windmill | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| windmill | dokploy | it mounts the Docker socket or Docker's own files, which hands a container the server |
| windows | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| windows | dokploy | it needs the capability NET_ADMIN, which a stack may not have |
| wings | coolify | it mounts the Docker socket or Docker's own files, which hands a container the server |
| wireguard-easy | coolify | it needs the capability NET_ADMIN, which a stack may not have |
| xsshunter | dokploy | it builds an image from source, which a template has none of |
| zep | coolify | Coolify does not offer it itself (its template is marked to be ignored) |
| zulip | dokploy | it reads its secrets from the environment of where Compose runs, which is musdash itself |
