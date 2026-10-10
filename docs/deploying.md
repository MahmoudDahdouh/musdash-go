# Deploying apps

[← All documentation](../README.md#documentation)

What happens when an app is deployed, and what you can do around a deployment: roll back, add domains, deploy from a Git repository, and preview pull requests.

## How a deploy works

1. The image is pulled; output goes to the deployment's log, which the page streams live.
2. The app's variables are written to a private env file, and its file mounts to disk.
3. A new container starts on the environment's Docker network, published on a loopback port musdash picks.
4. musdash waits for the health check: the configured path, the configured command, or the port accepting connections.
5. The proxy's routes are rewritten and reloaded, moving traffic to the new container.
6. The previous container is stopped and removed.

If any step before 5 fails, the new container is removed and the previous one keeps serving.

Apps in one environment reach each other by name (`web:80`) on that network. From outside, the only way in is the proxy.

## Rolling back

Every deployment's image stays on the server under a name of that deployment's own, the newest five of them. Open an earlier deployment and choose **Roll back**: that image runs again through the same health check and switch. Nothing is pulled or built, so a tag such as `nginx:latest` that has moved since does not matter. The app's settings and variables stay as they are today; only the image goes back.

## Domains, paths and passwords

A domain of your own (one you bought, such as `example.com`) needs one thing done outside musdash: a DNS record at your provider that leads it to the server, an `A` record with the server's public IP address (`AAAA` for an IPv6 address). The app's **Domains** tab names the record and its value. Add the domain there with `https://`; its certificate is ordered from Let's Encrypt at the first request once the record is in place. Tick the www box to have `www.example.com` redirect to it; that name needs a record too. **Check DNS** in a domain's row looks the name up and says whether it leads to the server, somewhere else, or nowhere yet.

A domain can be limited to a path: with `/api`, the app answers `app.example.com/api` and what is below it, and another app of yours on the same server can take the rest of the domain. The longest path that matches wins, on whole segments (`/api` is not `/apix`). The path can be removed before the request is passed on, for apps that expect to live at `/`.

A domain can also ask for a user name and password before anything reaches the app. The password is stored as a hash. Over plain HTTP it travels unencrypted, so use it with HTTPS. When one app is routed both openly and, under a path, behind a password, anything an app might read as that path asks for the password too: `/Admin` as well as `/admin`.

A domain can lead to another port of the app's container than the app's own: the Add domain dialog has the port beside the domain, filled with the app's. A deployment publishes every port the app's domains name, so a domain with a new port is served from the next deployment on. The dialog's **Generate domain** button makes an address that needs no DNS (`<name>.<server's IP>.sslip.io`), served over plain HTTP.

A Compose stack has a Domains tab of its own, where a domain is given to one of its services: see [Services](services.md#domains).

Both a path and a password need a proxy of this version. A proxy that was running before the upgrade answers "nothing is deployed" for such a domain until it is restarted (`systemctl restart musdash-proxy`; the install script does it) or, on another server, installed again from the Servers page. The dashboard says so when such a domain is added while an earlier proxy is running, and on the Servers page.

## Deploying from Git

An app can be built from a repository instead of pulling an image. Step 1 above becomes: clone the branch, then `docker build`. Builds run one at a time per server.

| Repository | How musdash reads it |
|---|---|
| Public | Its `https://` address, nothing to set up |
| Private, on GitHub | A GitHub App: create one under **Sources**, then install it on the repositories |
| Private, on GitLab | An access token: connect GitLab under **Sources** with a personal, group or project token that has the `read_api` and `read_repository` scopes. gitlab.com or your own instance, over HTTPS |
| Private, anywhere | A deploy key: generate one under **Sources** and add its public half to the repository |

Through a GitHub App or a GitLab token the form asks the host what there is: the repository is chosen from the ones the source can read (one the list does not show is named by typing `owner/name` into the menu's field), and the branch from the repository's own, starting at its default branch. With a public repository or a deploy key there is nobody to ask, and the address and the branch are typed.

Four build packs. Through a source, the form looks at the folder the app is built from and chooses one, naming the file that decided: a `Dockerfile` is the Dockerfile build, a `railpack.json` Railpack and a `nixpacks.toml` Nixpacks, a file of a language Railpack builds (`package.json`, `go.mod`, `requirements.txt`, …) Railpack, and an `index.html` with nothing to build a static site. It looks again when the repository, the branch or the base directory changes; what you choose yourself stays until then. Only the names of the files are read.

- **Dockerfile**: a path inside the repository.
- **Static site**: a directory served by nginx, with an optional single-page-app fallback.
- **Nixpacks** and **Railpack**: for a repository without a Dockerfile. The builder reads the code, works out how to build it, and the app listens on the port you gave it, which it is told as `PORT`. Their own settings go in as build-time variables (`NIXPACKS_START_CMD`, `RAILPACK_BUILD_CMD`, …) or in the repository's `nixpacks.toml` / `railpack.json`.

Variables marked build-time on the Environment tab are passed as build arguments (with Railpack, as build secrets). The tab lists variables by name: their values are sent to the page only when you choose **Show values**, and are changed in the tab's editor. Shared variables and a service's variables are kept the same way.

**Builds cannot reach the network.** If a build fails where it downloads something ("Temporary failure in name resolution", "Could not resolve host", `EAI_AGAIN`) while containers on the same server reach the network, the steps of a build have no DNS on that server: seen with Docker 29 on Ubuntu 24.04. Give Docker's builds a resolver in `/etc/docker/daemon.json`, for example `{"dns": ["1.1.1.1", "8.8.8.8"]}`, and restart Docker. musdash adds this to the error of a build that failed after such a line.

Neither builder is installed on a server. The first build that needs one makes a small image for it (`musdash/nixpacks:<version>`, `musdash/railpack:<version>`) from the project's release file, which Docker fetches from github.com and refuses unless it matches the checksum pinned in musdash. The builder then runs in a container that sees the checkout and nothing else of the server: no Docker socket, no capabilities, and for Nixpacks no network (Railpack asks the network which versions of a language exist). It needs Docker 24 or newer. The first build with either downloads its base images and takes minutes.

A push deploys the app when auto-deploy is on:

- Through a GitHub App, pushes arrive on their own; nothing to add.
- Through a GitLab source they do not: a token only reads. Add the webhook as below.
- Otherwise add a webhook to the repository. On the **Keys & tokens** page, under **API Tokens**, **Webhook secret** makes the secret for an app or a service; its row has the address and shows the secret, and the page says where each host wants them.

| Host | The secret goes in | Events to send |
|---|---|---|
| GitHub, Gitea, Forgejo | Secret, with content type `application/json` | Push (and pull requests, for previews) |
| GitLab | Secret token. A signing token is generated by GitLab and cannot be used here | Push events (and merge request events) |
| Bitbucket Cloud | Secret | Repository push (and pull request created, updated, merged, declined) |

Private repositories on these hosts are read with a deploy key (Bitbucket calls it an access key). Bitbucket Data Center is not supported.

A CI pipeline can start a deployment with the app's deploy token, made on the same page with **Deploy token**:

```bash
curl -X POST -H "Authorization: Bearer $MUSDASH_DEPLOY_TOKEN" "https://musdash.example.com/api/v1/deploy?uuid=APP_ID"
```

That token deploys this one app and nothing else. A person's API token does more: see [The API](api.md).

## A preview for every pull request

Switch it on under the app's Settings. Each pull request into the app's branch then gets a deployment of its own, built from the pull request's branch, updated on every push to it and removed when it is closed.

- **Address.** Set a domain for previews, such as `preview.example.com`, and point `*.preview.example.com` at the server: pull request 12 of the app `web` is served at `pr-12-web.preview.example.com` over HTTPS. Without one, each preview gets a generated address over plain HTTP.
- **What it runs with.** The app's variables and files, as they are when the preview is deployed, plus `MUSDASH_PREVIEW=1` and `MUSDASH_PULL_REQUEST=<number>` so the app can tell. It gets none of the app's volumes or server directories. Unless the app looks at `MUSDASH_PREVIEW`, a preview talks to the same database as the app.
- **Who can get one.** Only pull requests whose branch is in the repository itself. One from a fork gets none: its code would be built and run with your variables. Anyone who can push a branch to the repository can read those variables through a preview.
- **Events.** Through a GitHub App they arrive on their own. A webhook added by hand must also send pull request events (merge request events on GitLab). The comment on the pull request is written on GitHub only.
- **The comment.** Through a GitHub App, the preview's address is written to the pull request. An App created before previews existed may lack the permission: grant "Pull requests: read and write" in the App's settings on GitHub.
- **Limit.** Ten previews per app at once.
