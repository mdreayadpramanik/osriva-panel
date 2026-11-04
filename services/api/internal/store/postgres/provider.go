package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

// deleteRestricted deletes one row; a foreign-key RESTRICT violation means the
// row is still referenced (ErrConflict).
func (s *Store) deleteRestricted(ctx context.Context, sql, id string) error {
	tag, err := s.pool.Exec(ctx, sql, id)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return fmt.Errorf("%w: still in use (%s)", store.ErrConflict, pg.ConstraintName)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanAll[T any](rows pgx.Rows, scan func(pgx.Row) (*T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		x, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// ---- data centers and racks ----

func scanDC(row pgx.Row) (*store.Datacenter, error) {
	var d store.Datacenter
	if err := row.Scan(&d.ID, &d.OrganizationID, &d.Name, &d.Location, &d.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &d, nil
}

const dcCols = `id::text, organization_id::text, name, location, created_at`

func (s *Store) CreateDatacenter(ctx context.Context, d *store.Datacenter) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO datacenters (id, organization_id, name, location) VALUES ($1, $2, $3, $4) RETURNING created_at`,
		d.ID, d.OrganizationID, d.Name, d.Location).Scan(&d.CreatedAt))
}

func (s *Store) GetDatacenter(ctx context.Context, id string) (*store.Datacenter, error) {
	return scanDC(s.pool.QueryRow(ctx, `SELECT `+dcCols+` FROM datacenters WHERE id = $1`, id))
}

func (s *Store) ListDatacenters(ctx context.Context, orgID string) ([]store.Datacenter, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+dcCols+` FROM datacenters WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanDC)
}

func (s *Store) DeleteDatacenter(ctx context.Context, id string) error {
	return s.deleteRestricted(ctx, `DELETE FROM datacenters WHERE id = $1`, id)
}

func scanRack(row pgx.Row) (*store.Rack, error) {
	var r store.Rack
	if err := row.Scan(&r.ID, &r.DatacenterID, &r.Name, &r.Units, &r.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &r, nil
}

const rackCols = `id::text, datacenter_id::text, name, units, created_at`

func (s *Store) CreateRack(ctx context.Context, r *store.Rack) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO racks (id, datacenter_id, name, units) VALUES ($1, $2, $3, $4) RETURNING created_at`,
		r.ID, r.DatacenterID, r.Name, r.Units).Scan(&r.CreatedAt))
}

func (s *Store) GetRack(ctx context.Context, id string) (*store.Rack, error) {
	return scanRack(s.pool.QueryRow(ctx, `SELECT `+rackCols+` FROM racks WHERE id = $1`, id))
}

func (s *Store) ListRacks(ctx context.Context, datacenterID string) ([]store.Rack, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+rackCols+` FROM racks WHERE datacenter_id = $1 ORDER BY name`, datacenterID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanRack)
}

func (s *Store) DeleteRack(ctx context.Context, id string) error {
	return s.deleteRestricted(ctx, `DELETE FROM racks WHERE id = $1`, id)
}

// ---- nodes ----

const nodeCols = `id::text, organization_id::text, server_id::text, datacenter_id::text, rack_id::text, hypervisor, network,
	cpu_overcommit, memory_overcommit, enabled, created_at`

func scanNode(row pgx.Row) (*store.Node, error) {
	var n store.Node
	if err := row.Scan(&n.ID, &n.OrganizationID, &n.ServerID, &n.DatacenterID, &n.RackID, &n.Hypervisor, &n.Network,
		&n.CPUOvercommit, &n.MemoryOvercommit, &n.Enabled, &n.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &n, nil
}

func (s *Store) CreateNode(ctx context.Context, n *store.Node) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO nodes (id, organization_id, server_id, datacenter_id, rack_id, hypervisor, network,
			cpu_overcommit, memory_overcommit, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING created_at`,
		n.ID, n.OrganizationID, n.ServerID, n.DatacenterID, n.RackID, n.Hypervisor, n.Network, n.CPUOvercommit, n.MemoryOvercommit, n.Enabled).
		Scan(&n.CreatedAt))
}

func (s *Store) GetNode(ctx context.Context, id string) (*store.Node, error) {
	return scanNode(s.pool.QueryRow(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = $1`, id))
}

func (s *Store) ListNodes(ctx context.Context, orgID string) ([]store.Node, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+nodeCols+` FROM nodes WHERE organization_id = $1 ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanNode)
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	return s.deleteRestricted(ctx, `DELETE FROM nodes WHERE id = $1`, id)
}

// ---- IP pools ----

const poolCols = `id::text, organization_id::text, datacenter_id::text, name, cidr::text, host(gateway), host(first_address),
	host(last_address), dns, created_at`

func scanPool(row pgx.Row) (*store.IPPool, error) {
	var p store.IPPool
	if err := row.Scan(&p.ID, &p.OrganizationID, &p.DatacenterID, &p.Name, &p.CIDR, &p.Gateway, &p.First, &p.Last, &p.DNS, &p.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	if p.DNS == nil {
		p.DNS = []string{}
	}
	return &p, nil
}

func (s *Store) CreateIPPool(ctx context.Context, p *store.IPPool) error {
	if p.DNS == nil {
		p.DNS = []string{}
	}
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO ip_pools (id, organization_id, datacenter_id, name, cidr, gateway, first_address, last_address, dns)
		VALUES ($1, $2, $3, $4, $5::cidr, $6::inet, $7::inet, $8::inet, $9) RETURNING created_at`,
		p.ID, p.OrganizationID, p.DatacenterID, p.Name, p.CIDR, p.Gateway, p.First, p.Last, p.DNS).Scan(&p.CreatedAt))
}

func (s *Store) GetIPPool(ctx context.Context, id string) (*store.IPPool, error) {
	return scanPool(s.pool.QueryRow(ctx, `SELECT `+poolCols+` FROM ip_pools WHERE id = $1`, id))
}

func (s *Store) ListIPPools(ctx context.Context, orgID string) ([]store.IPPool, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+poolCols+` FROM ip_pools WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanPool)
}

func (s *Store) DeleteIPPool(ctx context.Context, id string) error {
	return s.deleteRestricted(ctx, `DELETE FROM ip_pools WHERE id = $1`, id)
}

func (s *Store) ListPoolAddresses(ctx context.Context, poolID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT host(address) FROM ip_addresses WHERE pool_id = $1 ORDER BY address`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- templates ----

const tplCols = `id::text, organization_id::text, name, image_url, image_sha256, min_disk_gb, default_user, created_at`

func scanTemplate(row pgx.Row) (*store.VMTemplate, error) {
	var t store.VMTemplate
	if err := row.Scan(&t.ID, &t.OrganizationID, &t.Name, &t.ImageURL, &t.ImageSHA256, &t.MinDiskGB, &t.DefaultUser, &t.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

func (s *Store) CreateVMTemplate(ctx context.Context, t *store.VMTemplate) error {
	return mapErr(s.pool.QueryRow(ctx, `INSERT INTO vm_templates (id, organization_id, name, image_url, image_sha256, min_disk_gb, default_user)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		t.ID, t.OrganizationID, t.Name, t.ImageURL, t.ImageSHA256, t.MinDiskGB, t.DefaultUser).Scan(&t.CreatedAt))
}

func (s *Store) GetVMTemplate(ctx context.Context, id string) (*store.VMTemplate, error) {
	return scanTemplate(s.pool.QueryRow(ctx, `SELECT `+tplCols+` FROM vm_templates WHERE id = $1`, id))
}

func (s *Store) ListVMTemplates(ctx context.Context, orgID string) ([]store.VMTemplate, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tplCols+` FROM vm_templates WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanTemplate)
}

func (s *Store) DeleteVMTemplate(ctx context.Context, id string) error {
	return s.deleteRestricted(ctx, `DELETE FROM vm_templates WHERE id = $1`, id)
}

// ---- VPS ----

const vpsCols = `id::text, organization_id::text, node_id::text, template_id::text, pool_id::text, name, vcpus, memory_mb, disk_gb,
	host(ip_address), ssh_keys, state, pending_task_id::text, pending_op, last_error, created_by::text, created_at, updated_at, customer_id::text`

func scanVPS(row pgx.Row) (*store.VPS, error) {
	var v store.VPS
	if err := row.Scan(&v.ID, &v.OrganizationID, &v.NodeID, &v.TemplateID, &v.PoolID, &v.Name, &v.VCPUs, &v.MemoryMB, &v.DiskGB,
		&v.IPAddress, &v.SSHKeys, &v.State, &v.PendingTaskID, &v.PendingOp, &v.LastError, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt, &v.CustomerID); err != nil {
		return nil, mapErr(err)
	}
	if v.SSHKeys == nil {
		v.SSHKeys = []string{}
	}
	return &v, nil
}

func (s *Store) CreateVPS(ctx context.Context, v *store.VPS) error {
	if v.SSHKeys == nil {
		v.SSHKeys = []string{}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO vps (id, organization_id, node_id, template_id, pool_id, name, vcpus, memory_mb, disk_gb,
				ip_address, ssh_keys, state, pending_task_id, pending_op, last_error, created_by, customer_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::inet, $11, $12, $13, $14, $15, $16, $17) RETURNING created_at, updated_at`,
			v.ID, v.OrganizationID, v.NodeID, v.TemplateID, v.PoolID, v.Name, v.VCPUs, v.MemoryMB, v.DiskGB, v.IPAddress, v.SSHKeys,
			v.State, v.PendingTaskID, v.PendingOp, v.LastError, v.CreatedBy, v.CustomerID).Scan(&v.CreatedAt, &v.UpdatedAt)
		if err != nil {
			return mapErr(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO ip_addresses (pool_id, address, vps_id) VALUES ($1, $2::inet, $3)`, v.PoolID, v.IPAddress, v.ID)
		return mapErr(err)
	})
}

func (s *Store) GetVPS(ctx context.Context, id string) (*store.VPS, error) {
	return scanVPS(s.pool.QueryRow(ctx, `SELECT `+vpsCols+` FROM vps WHERE id = $1`, id))
}

func (s *Store) ListVPS(ctx context.Context, orgID string) ([]store.VPS, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+vpsCols+` FROM vps WHERE organization_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanVPS)
}

func (s *Store) ListNodeVPS(ctx context.Context, nodeID string) ([]store.VPS, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+vpsCols+` FROM vps WHERE node_id = $1 ORDER BY name`, nodeID)
	if err != nil {
		return nil, err
	}
	return scanAll(rows, scanVPS)
}

func (s *Store) SetVPSState(ctx context.Context, id, state string, pendingTaskID *string, pendingOp, lastError string) error {
	return s.execOne(ctx, `UPDATE vps SET state = $2, pending_task_id = $3, pending_op = $4, last_error = $5, updated_at = now() WHERE id = $1`,
		id, state, pendingTaskID, pendingOp, lastError)
}

func (s *Store) DeleteVPS(ctx context.Context, id string) error {
	return s.execOne(ctx, `DELETE FROM vps WHERE id = $1`, id) // ip_addresses cascade
}
