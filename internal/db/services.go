package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// KindService is the resource kind of a service: a Compose stack. In the
// domains table it is the kind of a service's endpoint.
const KindService = "service"

// TemplateCustom is the template of a service whose Compose file a person
// supplied.
const TemplateCustom = "custom"

// TemplateGit is the template of a service whose Compose file is read from
// a Git repository at every deployment.
const TemplateGit = "git"

// AppDegraded is the status of a service some of whose containers are not
// running.
const AppDegraded = "degraded"

// Service is a stack of containers described by a Compose file. Its
// statuses are the App ones plus AppDegraded.
type Service struct {
	ID            string
	EnvironmentID string
	ServerID      string
	Name          string
	Template      string
	Compose       string
	Variables     string // sealed JSON
	ConnectEnv    bool
	Members       string // Compose service names, comma separated
	Status        string
	LastError     string
	CreatedAt     int64
	UpdatedAt     int64

	// Git source, for a service of the template TemplateGit. Compose then
	// holds the file as it was at the last deployment.
	RepoURL         string
	RepoName        string // "owner/name", lower case
	Branch          string
	ComposePath     string // the Compose file inside the repository
	GitSourceID     string
	SSHKeyID        string
	AutoDeploy      bool
	WebhookSecret   string // sealed
	DeployTokenHash string
	Commit          string // what the running stack was deployed from
	Checkout        string // the checkout directory the running stack uses; "" before the first deployment
	// Layout is what the Compose file holds, as JSON: see StackMember. Empty
	// while no deployment has read the stored text.
	Layout string
}

// FromGit reports whether the service's Compose file comes from a
// repository.
func (s Service) FromGit() bool { return s.Template == TemplateGit }

// MemberNames returns the Compose service names of the last deployment.
func (s Service) MemberNames() []string {
	if s.Members == "" {
		return nil
	}
	return strings.Split(s.Members, ",")
}

const serviceColumns = `s.id, s.environment_id, s.server_id, s.name, s.template, s.compose, s.variables, s.connect_env,
	s.members, s.status, s.last_error, s.created_at, s.updated_at,
	s.repo_url, s.repo_name, s.branch, s.compose_path, s.git_source_id, s.ssh_key_id, s.auto_deploy, s.webhook_secret,
	s.deploy_token_hash, s.commit_sha, s.checkout, s.layout`

func scanService(row interface{ Scan(...any) error }) (Service, error) {
	var m Service
	err := row.Scan(&m.ID, &m.EnvironmentID, &m.ServerID, &m.Name, &m.Template, &m.Compose, &m.Variables, &m.ConnectEnv,
		&m.Members, &m.Status, &m.LastError, &m.CreatedAt, &m.UpdatedAt,
		&m.RepoURL, &m.RepoName, &m.Branch, &m.ComposePath, &m.GitSourceID, &m.SSHKeyID, &m.AutoDeploy, &m.WebhookSecret,
		&m.DeployTokenHash, &m.Commit, &m.Checkout, &m.Layout)
	return m, notFound(err)
}

// CreateService inserts a service into an environment the team owns.
func (d *DB) CreateService(ctx context.Context, teamID string, m Service) (Service, error) {
	if _, err := d.Environment(ctx, teamID, m.EnvironmentID); err != nil {
		return Service{}, err
	}
	if _, err := d.Server(ctx, teamID, m.ServerID); err != nil {
		return Service{}, err
	}
	if err := d.checkSourceOwnership(ctx, teamID, App{GitSourceID: m.GitSourceID, SSHKeyID: m.SSHKeyID}); err != nil {
		return Service{}, err
	}
	m.RepoName = strings.ToLower(m.RepoName)
	m.ID = secret.RandomID()
	m.Status = AppCreated
	m.CreatedAt = now()
	m.UpdatedAt = m.CreatedAt
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		taken, err := nameTaken(ctx, tx, m.EnvironmentID, m.Name, "")
		if err != nil {
			return err
		}
		if taken {
			return ErrNameTaken
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO services (id, environment_id, server_id, name, template, compose, variables, connect_env,
				status, created_at, updated_at, repo_url, repo_name, branch, compose_path, git_source_id, ssh_key_id, auto_deploy)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.EnvironmentID, m.ServerID, m.Name, m.Template, m.Compose, m.Variables, m.ConnectEnv, m.Status, m.CreatedAt, m.UpdatedAt,
			m.RepoURL, m.RepoName, m.Branch, m.ComposePath, m.GitSourceID, m.SSHKeyID, m.AutoDeploy)
		return err
	})
	return m, err
}

const serviceTeamJoin = ` JOIN environments e ON e.id = s.environment_id JOIN projects p ON p.id = e.project_id `

const teamServices = ` AND environment_id IN (SELECT e.id FROM environments e JOIN projects p ON p.id = e.project_id WHERE p.team_id = ?)`

// Service loads one service, scoped to the team.
func (d *DB) Service(ctx context.Context, teamID, id string) (Service, error) {
	return scanService(d.QueryRowContext(ctx, `SELECT `+serviceColumns+` FROM services s`+serviceTeamJoin+`WHERE s.id = ? AND p.team_id = ?`, id, teamID))
}

// ServiceByID loads a service without a team check, for background work.
func (d *DB) ServiceByID(ctx context.Context, id string) (Service, error) {
	return scanService(d.QueryRowContext(ctx, `SELECT `+serviceColumns+` FROM services s WHERE s.id = ?`, id))
}

func (d *DB) queryServices(ctx context.Context, query string, args ...any) ([]Service, error) {
	rows, err := d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		m, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListServices returns the services of one environment. The caller has
// already checked the environment belongs to the team.
func (d *DB) ListServices(ctx context.Context, environmentID string) ([]Service, error) {
	return d.queryServices(ctx, `SELECT `+serviceColumns+` FROM services s WHERE s.environment_id = ? ORDER BY s.name`, environmentID)
}

// TeamServices returns every service of the team, by name.
func (d *DB) TeamServices(ctx context.Context, teamID string) ([]Service, error) {
	return d.queryServices(ctx, `SELECT `+serviceColumns+` FROM services s`+serviceTeamJoin+`WHERE p.team_id = ? ORDER BY s.name, s.id`, teamID)
}

// ServicesOnServer returns every service placed on a server.
func (d *DB) ServicesOnServer(ctx context.Context, serverID string) ([]Service, error) {
	return d.queryServices(ctx, `SELECT `+serviceColumns+` FROM services s WHERE s.server_id = ? ORDER BY s.created_at, s.rowid`, serverID)
}

// UpdateServiceCompose saves the Compose file, the (sealed) variables and
// whether the stack joins the environment's network. The layout goes with a
// text that is not the one it was read from.
func (d *DB) UpdateServiceCompose(ctx context.Context, teamID string, m Service) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET layout = CASE WHEN compose = ? THEN layout ELSE '' END,
			compose = ?, variables = ?, connect_env = ?, updated_at = ? WHERE id = ?`+teamServices,
		m.Compose, m.Compose, m.Variables, m.ConnectEnv, now(), m.ID, teamID))
}

// UpdateServiceSource stores where a Git service's Compose file comes
// from. A GitHub App or deploy key must be the team's own.
func (d *DB) UpdateServiceSource(ctx context.Context, teamID string, m Service) error {
	if err := d.checkSourceOwnership(ctx, teamID, App{GitSourceID: m.GitSourceID, SSHKeyID: m.SSHKeyID}); err != nil {
		return err
	}
	// The layout is that of the file the last deployment read. From another
	// repository, branch or path it is another file.
	return affected(d.ExecContext(ctx, `UPDATE services SET
			layout = CASE WHEN repo_url = ? AND branch = ? AND compose_path = ? THEN layout ELSE '' END,
			repo_url = ?, repo_name = ?, branch = ?, compose_path = ?, git_source_id = ?,
			ssh_key_id = ?, auto_deploy = ?, updated_at = ? WHERE id = ? AND template = ?`+teamServices,
		m.RepoURL, m.Branch, m.ComposePath,
		m.RepoURL, strings.ToLower(m.RepoName), m.Branch, m.ComposePath, m.GitSourceID, m.SSHKeyID, m.AutoDeploy, now(), m.ID, TemplateGit, teamID))
}

// SetServiceComposeRead records the Compose file a deployment read from the
// repository, whether or not that deployment then succeeds: it is what the
// service's pages explain themselves with.
func (d *DB) SetServiceComposeRead(ctx context.Context, id, composeText string) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET layout = CASE WHEN compose = ? THEN layout ELSE '' END,
		compose = ?, updated_at = ? WHERE id = ?`, composeText, composeText, now(), id))
}

// StackMember is one service of a stack's Compose file.
type StackMember struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
	// Ports are the container ports the file names for it, through expose
	// or ports.
	Ports []int `json:"ports,omitempty"`
	Built bool  `json:"built,omitempty"`
}

// StackMembers reads the stored layout. It is nil while there is none.
func (s Service) StackMembers() []StackMember {
	var out []StackMember
	if s.Layout == "" || json.Unmarshal([]byte(s.Layout), &out) != nil {
		return nil
	}
	return out
}

// SetServiceLayout records what a deployment found in the Compose file, if
// the stored text is still the one it read.
func (d *DB) SetServiceLayout(ctx context.Context, id, composeText string, members []StackMember) error {
	raw, err := json.Marshal(members)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE services SET layout = ? WHERE id = ? AND compose = ?`, string(raw), id, composeText)
	return err
}

// SetServiceDeployed records what the stack that is now running came from:
// the commit, and which checkout directory holds its files.
func (d *DB) SetServiceDeployed(ctx context.Context, id, commit, checkout string) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET commit_sha = ?, checkout = ?, updated_at = ? WHERE id = ?`, commit, checkout, now(), id))
}

// SetServiceWebhookSecret stores the (sealed) secret of the service's own
// push webhook.
func (d *DB) SetServiceWebhookSecret(ctx context.Context, teamID, id, sealed string) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET webhook_secret = ?, updated_at = ? WHERE id = ?`+teamServices, sealed, now(), id, teamID))
}

// SetServiceDeployToken stores the hash of the service's deploy token; ""
// revokes it.
func (d *DB) SetServiceDeployToken(ctx context.Context, teamID, id, hash string) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET deploy_token_hash = ?, updated_at = ? WHERE id = ?`+teamServices, hash, now(), id, teamID))
}

// ServiceByDeployToken finds the service whose deploy token has this hash.
func (d *DB) ServiceByDeployToken(ctx context.Context, id, hash string) (Service, error) {
	if hash == "" {
		return Service{}, ErrNotFound
	}
	return scanService(d.QueryRowContext(ctx, `SELECT `+serviceColumns+` FROM services s WHERE s.id = ? AND s.deploy_token_hash = ?`, id, hash))
}

// ServicesForPush returns the services that a push to the given repository
// and branch should redeploy. With sourceID set, only services connected
// through that GitHub App are returned.
func (d *DB) ServicesForPush(ctx context.Context, sourceID, repoName, branch string) ([]Service, error) {
	query := `SELECT ` + serviceColumns + ` FROM services s WHERE s.template = ? AND s.auto_deploy = 1 AND s.repo_name = ? AND s.branch = ?`
	args := []any{TemplateGit, strings.ToLower(repoName), branch}
	if sourceID != "" {
		query += ` AND s.git_source_id = ?`
		args = append(args, sourceID)
	}
	rows, err := d.QueryContext(ctx, query+` ORDER BY s.created_at, s.rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		m, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ServiceDeployWaiting reports whether a deployment of the service is
// queued and has not started: it will deploy whatever a new request wants
// deployed.
func (d *DB) ServiceDeployWaiting(ctx context.Context, id string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT count(*) FROM jobs WHERE kind = 'service' AND status = 'queued' AND json_extract(payload, '$.id') = ?`, id).Scan(&n)
	return n > 0, err
}

// UpdateServiceSettings stores what a person may change about a Git
// service on its Compose tab: its variables and whether it joins the
// environment's network. The file's text is the repository's and is left
// alone.
func (d *DB) UpdateServiceSettings(ctx context.Context, teamID string, m Service) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET variables = ?, connect_env = ?, updated_at = ? WHERE id = ?`+teamServices,
		m.Variables, m.ConnectEnv, now(), m.ID, teamID))
}

// SetServiceVariables stores the sealed variables, for a deployment that
// generated values.
func (d *DB) SetServiceVariables(ctx context.Context, id, sealed string) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET variables = ?, updated_at = ? WHERE id = ?`, sealed, now(), id))
}

// SetServiceMembers records the Compose service names of a deployment.
func (d *DB) SetServiceMembers(ctx context.Context, id string, names []string) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET members = ? WHERE id = ?`, strings.Join(names, ","), id))
}

// SetServiceState records what a service is doing and why it last failed.
func (d *DB) SetServiceState(ctx context.Context, id, status, lastError string) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`, status, lastError, now(), id))
}

// SetServiceStatusIf records a status the monitor observed. It leaves a
// service alone while it is being deployed, after it was stopped on
// purpose, and before it has ever been deployed.
func (d *DB) SetServiceStatusIf(ctx context.Context, serverID, id, status string) error {
	_, err := d.ExecContext(ctx, `UPDATE services SET status = ?, updated_at = ? WHERE id = ? AND server_id = ? AND status NOT IN (?, ?, ?, ?)`,
		status, now(), id, serverID, AppDeploying, AppStopped, AppCreated, status)
	return err
}

// BeginServiceDeploy marks a service as deploying and reports whether it
// was not already.
func (d *DB) BeginServiceDeploy(ctx context.Context, id string) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE services SET status = ?, last_error = '', updated_at = ? WHERE id = ? AND status <> ?`,
		AppDeploying, now(), id, AppDeploying)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ResetStuckServices repairs services left "deploying" by a process that
// died, with no job queued or running to move them on.
func (d *DB) ResetStuckServices(ctx context.Context, reason string) error {
	_, err := d.ExecContext(ctx, `UPDATE services SET status = ?, last_error = ?, updated_at = ?
		WHERE status = ? AND id NOT IN (
			SELECT json_extract(payload, '$.id') FROM jobs WHERE kind = 'service' AND status IN ('queued', 'running'))`,
		AppFailed, reason, now(), AppDeploying)
	return err
}

// NamesOnEnvNetwork returns the names containers answer to on an
// environment's network, apart from those of one service: the apps and
// databases there, and the members of the other services that joined it.
func (d *DB) NamesOnEnvNetwork(ctx context.Context, environmentID, exceptService string) (map[string]string, error) {
	names := map[string]string{}
	rows, err := d.QueryContext(ctx, `SELECT name, 'the app' FROM apps WHERE environment_id = ?
		UNION ALL SELECT name, 'the database' FROM databases WHERE environment_id = ?`, environmentID, environmentID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name, what string
		if err := rows.Scan(&name, &what); err != nil {
			rows.Close()
			return nil, err
		}
		names[name] = what + " " + name
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	others, err := d.queryServices(ctx, `SELECT `+serviceColumns+` FROM services s WHERE s.environment_id = ? AND s.connect_env = 1 AND s.id <> ?`, environmentID, exceptService)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		for _, member := range o.MemberNames() {
			names[member] = "the service " + o.Name
		}
	}
	return names, nil
}

// DeleteService removes a service with its endpoints and their domains.
func (d *DB) DeleteService(ctx context.Context, id string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE resource_kind = ? AND resource_id IN (SELECT id FROM service_endpoints WHERE service_id = ?)`, KindService, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM resource_tags WHERE resource_kind = ? AND resource_id = ?`, KindService, id); err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id))
	})
}

// Endpoint is a port of one container of a service that has a domain.
type Endpoint struct {
	ID             string
	ServiceID      string
	Name           string
	ComposeService string
	Port           int
	HostPort       int
	// Host and TLS are the endpoint's domain; Host is empty when it has
	// none. DomainID is that domain's row.
	Host     string
	TLS      bool
	DomainID string
	// Manual is set for an endpoint a person added on the Domains tab: the
	// Compose file does not name it, and its service and port are theirs.
	Manual bool
}

// MaxManualEndpoints bounds the domains a person may add to one stack.
const MaxManualEndpoints = 50

// ErrTooManyEndpoints is returned by AddEndpoint at the limit.
var ErrTooManyEndpoints = errors.New("the service has as many domains as it may have")

// ListEndpoints returns a service's endpoints with their domains: the ones
// the Compose file names first, by name, then the ones a person added, in
// the order they were added.
func (d *DB) ListEndpoints(ctx context.Context, serviceID string) ([]Endpoint, error) {
	rows, err := d.QueryContext(ctx, `SELECT ep.id, ep.service_id, ep.name, ep.compose_service, ep.port, ep.host_port,
			coalesce(m.host, ''), coalesce(m.tls, 0), coalesce(m.id, ''), ep.manual
		FROM service_endpoints ep LEFT JOIN domains m ON m.resource_kind = ? AND m.resource_id = ep.id
		WHERE ep.service_id = ? ORDER BY ep.manual, CASE WHEN ep.manual = 1 THEN '' ELSE ep.name END, ep.rowid`, KindService, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		var e Endpoint
		if err := rows.Scan(&e.ID, &e.ServiceID, &e.Name, &e.ComposeService, &e.Port, &e.HostPort, &e.Host, &e.TLS, &e.DomainID, &e.Manual); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SyncEndpoints makes a service's endpoints match the names its Compose
// file uses: missing ones are added, each with the host that newHost
// returns for it, and ones no longer named are removed with their domains.
// An endpoint a person added is not the file's and is left as it is.
func (d *DB) SyncEndpoints(ctx context.Context, serviceID string, names []string, newHost func(name string) (host string, tls bool)) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, name FROM service_endpoints WHERE service_id = ? AND manual = 0`, serviceID)
		if err != nil {
			return err
		}
		have := map[string]string{}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			have[name] = id
		}
		if err := rows.Close(); err != nil {
			return err
		}
		want := map[string]bool{}
		for _, name := range names {
			want[name] = true
			if _, ok := have[name]; ok {
				continue
			}
			id := secret.RandomID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO service_endpoints (id, service_id, name) VALUES (?, ?, ?)`, id, serviceID, name); err != nil {
				return err
			}
			if newHost == nil {
				continue
			}
			if host, tls := newHost(name); host != "" {
				// Somebody else's by now: the endpoint is made without an
				// address, as when none was offered.
				if err := hostUnused(ctx, tx, host); errors.Is(err, ErrHostTaken) {
					continue
				} else if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO domains (id, resource_kind, resource_id, host, tls, redirect_www, created_at)
					VALUES (?, ?, ?, ?, ?, 0, ?)`, secret.RandomID(), KindService, id, host, tls, now()); err != nil {
					return err
				}
			}
		}
		for name, id := range have {
			if want[name] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE resource_kind = ? AND resource_id = ?`, KindService, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM service_endpoints WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// AddEndpoint gives a service of a stack a domain on a port a person named.
// The host must be routed by nothing else. Where another endpoint of the
// stack already leads to the same service and port, the new one takes the
// loopback port that one is published on, and so is served at once.
func (d *DB) AddEndpoint(ctx context.Context, teamID, serviceID, composeService string, port int, host string, tls bool) (Endpoint, error) {
	e := Endpoint{ID: secret.RandomID(), ServiceID: serviceID, ComposeService: composeService, Port: port, Host: host, TLS: tls, Manual: true}
	e.Name = "~" + e.ID
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM services s`+serviceTeamJoin+`WHERE s.id = ? AND p.team_id = ?`, serviceID, teamID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM service_endpoints WHERE service_id = ? AND manual = 1`, serviceID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxManualEndpoints {
			return ErrTooManyEndpoints
		}
		if err := hostUnused(ctx, tx, host); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(host_port), 0) FROM service_endpoints
			WHERE service_id = ? AND compose_service = ? AND port = ?`, serviceID, composeService, port).Scan(&e.HostPort); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_endpoints (id, service_id, name, compose_service, port, host_port, manual)
			VALUES (?, ?, ?, ?, ?, ?, 1)`, e.ID, serviceID, e.Name, composeService, port, e.HostPort); err != nil {
			return err
		}
		e.DomainID = secret.RandomID()
		_, err := tx.ExecContext(ctx, `INSERT INTO domains (id, resource_kind, resource_id, host, tls, redirect_www, created_at)
			VALUES (?, ?, ?, ?, ?, 0, ?)`, e.DomainID, KindService, e.ID, host, tls, now())
		return err
	})
	return e, err
}

// DeleteEndpoint removes an endpoint a person added, with its domain. One
// the Compose file names is not removed here: it answers ErrNotFound.
func (d *DB) DeleteEndpoint(ctx context.Context, serviceID, endpointID string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM service_endpoints WHERE id = ? AND service_id = ? AND manual = 1`, endpointID, serviceID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE resource_kind = ? AND resource_id = ?`, KindService, endpointID); err != nil {
			return err
		}
		return affected(tx.ExecContext(ctx, `DELETE FROM service_endpoints WHERE id = ?`, endpointID))
	})
}

// SetEndpointTarget records which Compose service and port an endpoint
// stands for, and the loopback port it is published on.
func (d *DB) SetEndpointTarget(ctx context.Context, id, composeService string, port, hostPort int) error {
	return affected(d.ExecContext(ctx, `UPDATE service_endpoints SET compose_service = ?, port = ?, host_port = ? WHERE id = ?`, composeService, port, hostPort, id))
}

// SetEndpointDomain gives an endpoint its domain, replacing the one it had.
// The endpoint must belong to the service.
func (d *DB) SetEndpointDomain(ctx context.Context, serviceID, endpointID, host string, tls bool) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM service_endpoints WHERE id = ? AND service_id = ?`, endpointID, serviceID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE resource_kind = ? AND resource_id = ?`, KindService, endpointID); err != nil {
			return err
		}
		if err := hostUnused(ctx, tx, host); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO domains (id, resource_kind, resource_id, host, tls, redirect_www, created_at)
			VALUES (?, ?, ?, ?, ?, 0, ?)`, secret.RandomID(), KindService, endpointID, host, tls, now())
		return err
	})
}

// hostUnused reports ErrHostTaken when anything is routed on a host. A
// service's endpoint takes a whole host: since a host can be routed more
// than once, by path, the table no longer refuses a second row for it, and
// the check has to be made in the transaction that writes the row.
func hostUnused(ctx context.Context, tx *sql.Tx, host string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domains WHERE host = ?`, host).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrHostTaken
	}
	return nil
}
