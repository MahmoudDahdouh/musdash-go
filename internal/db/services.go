package db

import (
	"context"
	"database/sql"
	"strings"

	"github.com/MahmoudDahdouh/musdash-go/internal/secret"
)

// KindService is the resource kind of a service: a Compose stack. In the
// domains table it is the kind of a service's endpoint.
const KindService = "service"

// TemplateCustom is the template of a service whose Compose file a person
// supplied.
const TemplateCustom = "custom"

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
}

// MemberNames returns the Compose service names of the last deployment.
func (s Service) MemberNames() []string {
	if s.Members == "" {
		return nil
	}
	return strings.Split(s.Members, ",")
}

const serviceColumns = `s.id, s.environment_id, s.server_id, s.name, s.template, s.compose, s.variables, s.connect_env,
	s.members, s.status, s.last_error, s.created_at, s.updated_at`

func scanService(row interface{ Scan(...any) error }) (Service, error) {
	var m Service
	err := row.Scan(&m.ID, &m.EnvironmentID, &m.ServerID, &m.Name, &m.Template, &m.Compose, &m.Variables, &m.ConnectEnv,
		&m.Members, &m.Status, &m.LastError, &m.CreatedAt, &m.UpdatedAt)
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
				status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.EnvironmentID, m.ServerID, m.Name, m.Template, m.Compose, m.Variables, m.ConnectEnv, m.Status, m.CreatedAt, m.UpdatedAt)
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

// ServicesOnServer returns every service placed on a server.
func (d *DB) ServicesOnServer(ctx context.Context, serverID string) ([]Service, error) {
	return d.queryServices(ctx, `SELECT `+serviceColumns+` FROM services s WHERE s.server_id = ? ORDER BY s.created_at, s.rowid`, serverID)
}

// UpdateServiceCompose saves the Compose file, the (sealed) variables and
// whether the stack joins the environment's network.
func (d *DB) UpdateServiceCompose(ctx context.Context, teamID string, m Service) error {
	return affected(d.ExecContext(ctx, `UPDATE services SET compose = ?, variables = ?, connect_env = ?, updated_at = ? WHERE id = ?`+teamServices,
		m.Compose, m.Variables, m.ConnectEnv, now(), m.ID, teamID))
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
func (d *DB) SetServiceStatusIf(ctx context.Context, id, status string) error {
	_, err := d.ExecContext(ctx, `UPDATE services SET status = ?, updated_at = ? WHERE id = ? AND status NOT IN (?, ?, ?, ?)`,
		status, now(), id, AppDeploying, AppStopped, AppCreated, status)
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
	// none.
	Host string
	TLS  bool
}

// ListEndpoints returns a service's endpoints with their domains, by name.
func (d *DB) ListEndpoints(ctx context.Context, serviceID string) ([]Endpoint, error) {
	rows, err := d.QueryContext(ctx, `SELECT ep.id, ep.service_id, ep.name, ep.compose_service, ep.port, ep.host_port,
			coalesce(m.host, ''), coalesce(m.tls, 0)
		FROM service_endpoints ep LEFT JOIN domains m ON m.resource_kind = ? AND m.resource_id = ep.id
		WHERE ep.service_id = ? ORDER BY ep.name`, KindService, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		var e Endpoint
		if err := rows.Scan(&e.ID, &e.ServiceID, &e.Name, &e.ComposeService, &e.Port, &e.HostPort, &e.Host, &e.TLS); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SyncEndpoints makes a service's endpoints match the names its Compose
// file uses: missing ones are added, each with the host that newHost
// returns for it, and ones no longer named are removed with their domains.
// It returns the hosts that could not be given because something else
// routes them already.
func (d *DB) SyncEndpoints(ctx context.Context, serviceID string, names []string, newHost func(name string) (host string, tls bool)) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, name FROM service_endpoints WHERE service_id = ?`, serviceID)
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
		_, err := tx.ExecContext(ctx, `INSERT INTO domains (id, resource_kind, resource_id, host, tls, redirect_www, created_at)
			VALUES (?, ?, ?, ?, ?, 0, ?)`, secret.RandomID(), KindService, endpointID, host, tls, now())
		return err
	})
}
