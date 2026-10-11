# Test projects

Each folder here is a repository as a person has it, made into a Git
repository by `TestProjectsWithDocker` (`internal/deploy`) and deployed on
real Docker. They are also what to push somewhere and try the dashboard's
Git form with.

| Folder | Deployed as | What it has |
|---|---|---|
| `dockerfile` | An app, Build with **Dockerfile** | A Dockerfile and a page it serves on port 8080 |
| `compose-small` | A stack, Build with **Docker Compose** | One service built from the repository, published on port 80 of the machine, as a file for `docker compose up` does |
| `compose-large` | A stack, Build with **Docker Compose** | Ten services: two built from the repository, a database with a generated password, a job that runs once, health checks and start order, a volume, a file of the repository mounted, a variable that must be given, an extension field shared by five services, and ports 80 and 443 published |

The stacks written for pasting are in `../stacks`.
