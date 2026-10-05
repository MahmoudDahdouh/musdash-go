package deploy

import (
	"context"
	"errors"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
)

// DeployTag queues a deployment of every app and service of the team that
// has the tag, and reports how many it queued and how many have the tag.
// Each is treated as a push would treat it: nothing is queued behind a
// deployment that is already waiting, since that one will deploy whatever
// this call wanted deployed.
func (d *Deployer) DeployTag(ctx context.Context, teamID, tag, trigger string) (queued, tagged int, err error) {
	apps, services, err := d.DB.Tagged(ctx, teamID, tag)
	if err != nil {
		return 0, 0, err
	}
	tagged = len(apps) + len(services)
	for _, app := range apps {
		if _, err := d.DB.QueuedDeployment(ctx, app.ID); err == nil {
			continue
		} else if !errors.Is(err, db.ErrNotFound) {
			return queued, tagged, err
		}
		if _, err := d.Enqueue(ctx, app, trigger); err != nil {
			return queued, tagged, err
		}
		queued++
	}
	for _, svc := range services {
		waiting, err := d.DB.ServiceDeployWaiting(ctx, svc.ID)
		if err != nil {
			return queued, tagged, err
		}
		if waiting {
			continue
		}
		// "again": a deployment that is running has already read the
		// service as it was.
		if err := d.EnqueueService(ctx, svc, true); err != nil {
			return queued, tagged, err
		}
		queued++
	}
	return queued, tagged, nil
}
