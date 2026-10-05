package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MahmoudDahdouh/musdash-go/internal/db"
	"github.com/MahmoudDahdouh/musdash-go/internal/jobs"
	"github.com/MahmoudDahdouh/musdash-go/internal/source"
)

// A preview is the deployment of a pull request: an app of its own, the
// child of the app whose repository the pull request is in, built from the
// pull request's branch and reachable at an address of its own. It is made
// when the pull request is opened, deployed again on every push to it, and
// removed when it is closed.
//
// A preview runs code that is not merged yet with its parent's variables.
// The one rule that makes this safe is in SyncPreview: the code must be in
// the parent's own repository, where only people who may push can put it.

// JobClosePreview removes a preview.
const JobClosePreview = "preview-close"

// TriggerPullRequest is the trigger of a deployment a pull request made.
const TriggerPullRequest = "pull request"

// ErrNoPreview is returned when a pull request does not get a preview.
var ErrNoPreview = errors.New("this pull request does not get a preview")

// PullRequestComments writes the comment that tells a pull request where
// its preview is.
type PullRequestComments interface {
	CommentOnPullRequest(ctx context.Context, appID int64, key []byte, owner, repo string, number int, commentID int64, text string) (int64, error)
}

// PreviewName is the name of the preview of a pull request: its address on
// the environment's network, next to the app's own.
func PreviewName(parent string, number int) string {
	return parent + "-pr-" + strconv.Itoa(number)
}

// PreviewHost is the address a preview is served at: under the domain the
// app sets aside for previews, or one that needs no DNS.
func PreviewHost(parent db.App, server db.Server, number int) string {
	label := "pr-" + strconv.Itoa(number) + "-" + parent.Name
	if parent.PreviewDomain != "" {
		return label + "." + parent.PreviewDomain
	}
	return label + "-" + GeneratedDomain(server)
}

// wantsPreview reports whether a pull request gets a preview of the app.
func wantsPreview(parent db.App, pr source.PullRequest) bool {
	switch {
	case parent.Source != db.SourceGit || !parent.Previews || parent.IsPreview():
		return false
	case pr.Number <= 0 || !source.ValidBranch(pr.Branch):
		return false
	// The code must be in the app's own repository. A pull request from a
	// fork is somebody else's code, and a preview would build and run it
	// with this app's variables.
	case pr.FromFork() || !strings.EqualFold(pr.Repo, parent.RepoName):
		return false
	// A preview shows what the app would become: a pull request into
	// another branch is not about this app.
	case pr.BaseBranch != parent.Branch:
		return false
	}
	return true
}

// SyncPreview creates the preview of a pull request if there is none yet,
// and queues a deployment of the pull request's branch. It returns
// ErrNoPreview for a pull request that does not get one, and
// db.ErrPreviewLimit when the app has as many as it may have.
func (d *Deployer) SyncPreview(ctx context.Context, parent db.App, pr source.PullRequest) (db.App, error) {
	if !wantsPreview(parent, pr) {
		return db.App{}, ErrNoPreview
	}
	child, err := d.DB.Preview(ctx, parent.ID, pr.Number)
	if errors.Is(err, db.ErrNotFound) {
		if child, err = d.createPreview(ctx, parent, pr); errors.Is(err, db.ErrNameTaken) {
			// Created by another delivery of the same event in the meantime.
			child, err = d.DB.Preview(ctx, parent.ID, pr.Number)
		}
	}
	if err != nil {
		return db.App{}, err
	}
	if child.Branch != pr.Branch {
		if err := d.DB.SetAppBranch(ctx, child.ID, pr.Branch); err != nil {
			return db.App{}, err
		}
		child.Branch = pr.Branch
	}
	// One waiting deployment builds whatever the branch holds when it
	// starts; a second would build the same.
	if _, err := d.DB.QueuedDeployment(ctx, child.ID); errors.Is(err, db.ErrNotFound) {
		_, err = d.Enqueue(ctx, child, TriggerPullRequest)
		return child, err
	} else if err != nil {
		return child, err
	}
	return child, nil
}

func (d *Deployer) createPreview(ctx context.Context, parent db.App, pr source.PullRequest) (db.App, error) {
	child, err := d.DB.CreatePreview(ctx, parent, pr.Number, PreviewName(parent.Name, pr.Number), pr.Branch)
	if err != nil {
		return db.App{}, err
	}
	// Its address. A preview without one still builds and runs, which is
	// worth having: the failure is logged, not fatal.
	server, err := d.DB.ServerByID(ctx, parent.ServerID)
	if err != nil {
		return child, err
	}
	teamID, err := d.DB.TeamOfEnvironment(ctx, parent.EnvironmentID)
	if err != nil {
		return child, err
	}
	host := PreviewHost(parent, server, pr.Number)
	// Never the dashboard's own address, whatever the app's preview domain
	// and name add up to.
	if instance, _ := d.DB.Setting(ctx, db.SettingInstanceDomain); instance != "" && instance == host {
		d.Log.Warn("a preview has no address: it would be the dashboard's own", "app", parent.Name, "host", host)
		return child, nil
	}
	_, err = d.DB.AddDomain(ctx, teamID, parent.ServerID, db.Domain{ResourceKind: db.KindApp, ResourceID: child.ID,
		Host: host, TLS: parent.PreviewDomain != ""})
	if err != nil {
		d.Log.Warn("a preview has no address", "app", parent.Name, "pull_request", pr.Number, "host", host, "err", err)
	}
	return child, nil
}

type closePayload struct {
	AppID string `json:"app_id"`
}

// ClosePreview removes a preview. The removal is queued behind whatever
// deployment of the preview is running, under the same lock.
func (d *Deployer) ClosePreview(ctx context.Context, preview db.App) error {
	if !preview.IsPreview() {
		return ErrNoPreview
	}
	_, err := d.Queue.Enqueue(ctx, JobClosePreview, closePayload{AppID: preview.ID}, jobs.WithLockKey(deployLock(preview)), jobs.WithMaxAttempts(5))
	return err
}

func (d *Deployer) runClosePreview(ctx context.Context, raw []byte) error {
	var p closePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobs.Permanent(err)
	}
	app, err := d.DB.AppByID(ctx, p.AppID)
	if errors.Is(err, db.ErrNotFound) {
		return nil // removed already
	}
	if err != nil {
		return err
	}
	// Only ever a preview: the payload is a row in a table, and an app's
	// id in it must not be a way to delete the app.
	if !app.IsPreview() {
		return jobs.Permanent(fmt.Errorf("app %s is not a preview", app.ID))
	}
	err = d.Destroy(ctx, app.ID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err == nil {
		d.announce(ctx, app, "The preview of this pull request was removed when the pull request was closed.")
	}
	// ErrBusy and the like: tried again after a while.
	return err
}

// announcePreview tells the pull request where its preview is. Best
// effort: it needs a GitHub App that may write to pull requests, and a
// preview is no less deployed without the comment.
func (d *Deployer) announcePreview(ctx context.Context, app db.App, dep db.Deployment) {
	domains, err := d.DB.ListDomains(ctx, db.KindApp, app.ID)
	if err != nil || len(domains) == 0 {
		return
	}
	scheme := "http"
	if domains[0].TLS {
		scheme = "https"
	}
	text := "This pull request is deployed at " + scheme + "://" + domains[0].Host + domains[0].Path
	if len(dep.CommitSHA) >= 7 {
		text += "\n\nCommit `" + dep.CommitSHA[:7] + "`, deployed " + time.Now().UTC().Format("2006-01-02 15:04") + " UTC."
	}
	d.announce(ctx, app, text)
}

// announce writes or rewrites the one comment a preview keeps on its pull
// request. The text is made here and holds nothing a person typed.
func (d *Deployer) announce(ctx context.Context, app db.App, text string) {
	if d.Comments == nil || app.GitSourceID == "" || app.PRNumber <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	repo, err := source.ParseRepo(app.RepoURL)
	if err != nil || repo.Host != "github.com" {
		return
	}
	src, err := d.DB.GitSourceByID(ctx, app.GitSourceID)
	if err != nil {
		return
	}
	key, err := d.Box.Open(src.PrivateKey)
	if err != nil {
		return
	}
	id, err := d.Comments.CommentOnPullRequest(ctx, src.AppID, key, repo.Owner, repo.Name, app.PRNumber, app.PRCommentID, text)
	if err != nil {
		// Usually an App made before previews existed, which may not
		// write to pull requests.
		d.Log.Info("no comment on the pull request", "app", app.Name, "pull_request", app.PRNumber, "err", err)
		return
	}
	if id != app.PRCommentID {
		if err := d.DB.SetPRComment(ctx, app.ID, id); err != nil {
			d.Log.Warn("record a pull request comment", "app", app.Name, "err", err)
		}
	}
}
