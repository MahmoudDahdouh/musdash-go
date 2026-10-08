# The API

[← All documentation](../README.md#documentation)

Reading and operating musdash from scripts and pipelines.

For scripts and pipelines. On the Keys & tokens page, under API Tokens, make a token: it asks for your password, is shown once, and is stored as a hash. It can end in 7, 30, 60 or 90 days or a year, or never; it acts as the person who made it, within the permissions it was given, and never does what that person may not. It stops working when they leave the team, change or reset their password, turn on two-step sign-in, or are given a higher role.

| Permission | What a token with it may do |
|---|---|
| Read (`read`) | Every `GET` below |
| Write (`write`) | Start and stop apps, databases and services |
| Deploy (`deploy`) | Start deployments |
| Read sensitive data (`read:sensitive`) | Read, with the values of an app's variables and a database's password in the answers |
| Root (`root`) | All of the above, and what later versions add |

Each permission is its own: a token that only deploys can call none of the `GET` routes, which is what a pipeline wants. (A deploy call still answers with the ids of what it queued, and with `404` for an id that does not exist.) A token made before permissions existed keeps what it could do (`read`, or `read`, `write` and `deploy`).

```bash
curl -H "Authorization: Bearer $MUSDASH_TOKEN" https://musdash.example.com/api/v1/apps
```

```bash
curl -X POST -H "Authorization: Bearer $MUSDASH_TOKEN" "https://musdash.example.com/api/v1/deploy?tag=nightly"
```

| | Route | Token |
|---|---|---|
| Who the token is | `GET /api/v1/me` | read |
| Servers, projects with their environments, tags | `GET /api/v1/servers`, `/projects`, `/tags` | read |
| Apps | `GET /api/v1/apps` (`?tag=`), `/apps/{id}` | read |
| An app's deployments, one deployment | `GET /api/v1/apps/{id}/deployments` (`?limit=`), `/deployments/{id}` | read |
| An app's variables: their names, and their values for `read:sensitive` | `GET /api/v1/apps/{id}/envs` | read |
| Databases and services (a database's `password` for `read:sensitive`) | `GET /api/v1/databases`, `/databases/{id}`, `/services`, `/services/{id}` | read |
| Deploy an app or a service | `POST /api/v1/apps/{id}/deploy`, `/services/{id}/deploy` | deploy |
| Deploy several at once | `POST /api/v1/deploy?uuid=ID,ID&tag=TAG,TAG` | deploy |
| Stop an app or a service | `POST /api/v1/apps/{id}/stop`, `/services/{id}/stop` | write |
| Start or stop a database | `POST /api/v1/databases/{id}/start`, `/databases/{id}/stop` | write |

- Answers are JSON; an error is `{"error": "…"}` with a 4xx or 5xx status. Times are Unix seconds.
- A deploy answers `202` with the deployment's id, which `GET /api/v1/deployments/{id}` follows until its `status` is `success` or `failed`. When a deployment that had not started yet will do what the call asked for, the status is `waiting` and nothing more is queued.
- `POST /api/v1/deploy` deploys nothing when one of its ids is unknown.
- The API never returns a key, a hash or a webhook secret, and returns a variable's value or a database's password only to a token with `read:sensitive`. A value that names a shared variable (`{{team.NAME}}`) is returned as that name. `GET /api/v1/me` lists the token's permissions as `abilities`.
- It reads and operates. Creating and configuring are done in the dashboard.
- Limits: 120 calls a minute for a token and 600 for an address; beyond that the answer is `429` with `Retry-After`.
- The API takes a token and nothing else: a browser's session is not accepted there, and a token is not accepted by the dashboard's pages.
