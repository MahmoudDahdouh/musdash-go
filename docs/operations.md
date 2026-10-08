# Scheduled tasks, notifications and clean-up

[← All documentation](../README.md#documentation)

What musdash does on its own once it is set up. Database backups are in [Databases](databases.md#backups).

## Scheduled tasks

An app's Tasks tab runs a command in the app's running container on a schedule, with `sh -c`. Each run is listed with its exit status and output; the last 50 are kept. A run that is still going when its next time comes is not started a second time.

## Schedules

Schedules are five cron fields (`0 3 * * *`), or `@hourly`, `@daily`, `@weekly`, `@monthly`, always in UTC. A schedule whose time passed while musdash was not running fires once when it is back, however many of its times were missed.

## Notifications

The **Notifications** page adds channels (Discord, Slack, Mattermost, Telegram, Pushover, a generic webhook, or email over SMTP) and chooses which events each one hears: a deployment finished or failed, a backup finished or failed, a scheduled task failed, a container stopped unexpectedly, the disk is nearly full. A container that keeps crashing is reported once every 15 minutes, not on every restart. A channel's address, and a bucket's endpoint, may not lead to the server's own services, to the private addresses of containers on it, or to a cloud metadata service; what such an address answers is never shown. Other machines on a private network are allowed: a self-hosted chat server or object store usually lives on one.

## Clean-up

Once a day after 03:00 UTC, musdash removes dangling images, build cache that is older than a week or beyond 2 GB (all of it when the disk is 85% full or more), and stopped containers whose app, database or service no longer exists. It never removes volumes, and never images that a stopped app or database still needs.
