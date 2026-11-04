package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/provider"
	"github.com/mdreayadpramanik/osriva-panel/services/api/internal/store"
)

var errProviderDisabled = errors.New("Provider Mode is not available")

type datacenterDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Location  string `json:"location"`
	CreatedAt string `json:"created_at"`
}

type rackDTO struct {
	ID           string `json:"id"`
	DatacenterID string `json:"datacenter_id"`
	Name         string `json:"name"`
	Units        int    `json:"units"`
}

type nodeDTO struct {
	ID               string  `json:"id"`
	ServerID         string  `json:"server_id"`
	ServerName       string  `json:"server_name"`
	Connection       string  `json:"connection"`
	DatacenterID     string  `json:"datacenter_id"`
	RackID           *string `json:"rack_id"`
	Hypervisor       string  `json:"hypervisor"`
	Network          string  `json:"network"`
	CPUOvercommit    float64 `json:"cpu_overcommit"`
	MemoryOvercommit float64 `json:"memory_overcommit"`
	Enabled          bool    `json:"enabled"`
	VCPUs            int     `json:"vcpus"`
	MemoryMB         int     `json:"memory_mb"`
	UsedVCPUs        int     `json:"used_vcpus"`
	UsedMemoryMB     int     `json:"used_memory_mb"`
	VPSCount         int     `json:"vps_count"`
}

func toNodeDTO(v *provider.NodeView) nodeDTO {
	n := v.Node
	return nodeDTO{ID: n.ID, ServerID: n.ServerID, ServerName: v.ServerName, Connection: v.Connection, DatacenterID: n.DatacenterID,
		RackID: n.RackID, Hypervisor: n.Hypervisor, Network: n.Network, CPUOvercommit: n.CPUOvercommit, MemoryOvercommit: n.MemoryOvercommit,
		Enabled: n.Enabled, VCPUs: v.VCPUs, MemoryMB: v.MemoryMB, UsedVCPUs: v.UsedVCPUs, UsedMemoryMB: v.UsedMemoryMB, VPSCount: v.VPSCount}
}

type poolDTO struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	DatacenterID *string  `json:"datacenter_id"`
	CIDR         string   `json:"cidr"`
	Gateway      string   `json:"gateway"`
	First        string   `json:"first"`
	Last         string   `json:"last"`
	DNS          []string `json:"dns"`
	Size         int      `json:"size"`
	Allocated    []string `json:"allocated"`
}

func toPoolDTO(v *provider.PoolView) poolDTO {
	p := v.Pool
	return poolDTO{ID: p.ID, Name: p.Name, DatacenterID: p.DatacenterID, CIDR: p.CIDR, Gateway: p.Gateway, First: p.First, Last: p.Last,
		DNS: p.DNS, Size: v.Size, Allocated: v.Allocated}
}

type templateDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ImageURL    string `json:"image_url"`
	ImageSHA256 string `json:"image_sha256"`
	MinDiskGB   int    `json:"min_disk_gb"`
	DefaultUser string `json:"default_user"`
}

type vpsDTO struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	NodeID        string   `json:"node_id"`
	TemplateID    *string  `json:"template_id"`
	PoolID        string   `json:"pool_id"`
	VCPUs         int      `json:"vcpus"`
	MemoryMB      int      `json:"memory_mb"`
	DiskGB        int      `json:"disk_gb"`
	IPAddress     string   `json:"ip_address"`
	SSHKeys       []string `json:"ssh_keys"`
	State         string   `json:"state"`
	PendingTaskID *string  `json:"pending_task_id"`
	PendingOp     string   `json:"pending_op"`
	LastError     string   `json:"last_error"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	CustomerID    *string  `json:"customer_id"`
}

func toVPSDTO(v *store.VPS) vpsDTO {
	op, _, _ := cutColon(v.PendingOp)
	return vpsDTO{ID: v.ID, Name: v.Name, NodeID: v.NodeID, TemplateID: v.TemplateID, PoolID: v.PoolID, VCPUs: v.VCPUs, MemoryMB: v.MemoryMB,
		DiskGB: v.DiskGB, IPAddress: v.IPAddress, SSHKeys: v.SSHKeys, State: v.State, PendingTaskID: v.PendingTaskID, PendingOp: op,
		LastError: v.LastError, CreatedAt: ts(v.CreatedAt), UpdatedAt: ts(v.UpdatedAt), CustomerID: v.CustomerID}
}

func cutColon(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func (s *Server) providerOr503(w http.ResponseWriter, r *http.Request) bool {
	if s.provider == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "unavailable", errProviderDisabled.Error(), "")
		return false
	}
	return true
}

// list writes {key: [...]} after mapping each element.
func list[T, D any](s *Server, w http.ResponseWriter, r *http.Request, key string, items []T, err error, conv func(*T) D) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]D, len(items))
	for i := range items {
		out[i] = conv(&items[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{key: out})
}

func one[T, D any](s *Server, w http.ResponseWriter, r *http.Request, code int, item *T, err error, conv func(*T) D) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, code, conv(item))
}

func (s *Server) noContent(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dcConv(d *store.Datacenter) datacenterDTO {
	return datacenterDTO{ID: d.ID, Name: d.Name, Location: d.Location, CreatedAt: ts(d.CreatedAt)}
}
func rackConv(x *store.Rack) rackDTO {
	return rackDTO{ID: x.ID, DatacenterID: x.DatacenterID, Name: x.Name, Units: x.Units}
}
func tplConv(t *store.VMTemplate) templateDTO {
	return templateDTO{ID: t.ID, Name: t.Name, ImageURL: t.ImageURL, ImageSHA256: t.ImageSHA256, MinDiskGB: t.MinDiskGB, DefaultUser: t.DefaultUser}
}

func (s *Server) providerRoutes(r chi.Router) {
	p := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.providerOr503(w, r) {
				h(w, r)
			}
		}
	}
	r.Get("/provider/datacenters", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provider.ListDatacenters(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "datacenters", items, err, dcConv)
	}))
	r.Post("/provider/datacenters", p(func(w http.ResponseWriter, r *http.Request) {
		var in provider.DatacenterInput
		if s.decode(w, r, &in) {
			d, err := s.provider.CreateDatacenter(r.Context(), principalFrom(r.Context()), in)
			one(s, w, r, http.StatusCreated, d, err, dcConv)
		}
	}))
	r.Delete("/provider/datacenters/{dcId}", p(func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.provider.DeleteDatacenter(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "dcId")))
	}))
	r.Get("/provider/datacenters/{dcId}/racks", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provider.ListRacks(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "dcId"))
		list(s, w, r, "racks", items, err, rackConv)
	}))
	r.Post("/provider/racks", p(func(w http.ResponseWriter, r *http.Request) {
		var in provider.RackInput
		if s.decode(w, r, &in) {
			x, err := s.provider.CreateRack(r.Context(), principalFrom(r.Context()), in)
			one(s, w, r, http.StatusCreated, x, err, rackConv)
		}
	}))
	r.Delete("/provider/racks/{rackId}", p(func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.provider.DeleteRack(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "rackId")))
	}))
	r.Get("/provider/nodes", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provider.ListNodes(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "nodes", items, err, toNodeDTO)
	}))
	r.Post("/provider/nodes", p(func(w http.ResponseWriter, r *http.Request) {
		var in provider.NodeInput
		if s.decode(w, r, &in) {
			v, err := s.provider.CreateNode(r.Context(), principalFrom(r.Context()), in)
			one(s, w, r, http.StatusCreated, v, err, toNodeDTO)
		}
	}))
	r.Delete("/provider/nodes/{nodeId}", p(func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.provider.DeleteNode(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "nodeId")))
	}))
	r.Get("/provider/ip-pools", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provider.ListPools(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "pools", items, err, toPoolDTO)
	}))
	r.Post("/provider/ip-pools", p(func(w http.ResponseWriter, r *http.Request) {
		var in provider.PoolInput
		if s.decode(w, r, &in) {
			v, err := s.provider.CreatePool(r.Context(), principalFrom(r.Context()), in)
			one(s, w, r, http.StatusCreated, v, err, toPoolDTO)
		}
	}))
	r.Delete("/provider/ip-pools/{poolId}", p(func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.provider.DeletePool(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "poolId")))
	}))
	r.Get("/provider/templates", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provider.ListTemplates(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "templates", items, err, tplConv)
	}))
	r.Post("/provider/templates", p(func(w http.ResponseWriter, r *http.Request) {
		var in provider.TemplateInput
		if s.decode(w, r, &in) {
			t, err := s.provider.CreateTemplate(r.Context(), principalFrom(r.Context()), in)
			one(s, w, r, http.StatusCreated, t, err, tplConv)
		}
	}))
	r.Delete("/provider/templates/{templateId}", p(func(w http.ResponseWriter, r *http.Request) {
		s.noContent(w, r, s.provider.DeleteTemplate(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "templateId")))
	}))
	r.Get("/provider/vps", p(func(w http.ResponseWriter, r *http.Request) {
		items, err := s.provider.ListVPS(r.Context(), principalFrom(r.Context()))
		list(s, w, r, "vps", items, err, toVPSDTO)
	}))
	r.Post("/provider/vps", p(func(w http.ResponseWriter, r *http.Request) {
		var in provider.VPSInput
		if s.decode(w, r, &in) {
			v, err := s.provider.CreateVPS(r.Context(), principalFrom(r.Context()), in)
			one(s, w, r, http.StatusAccepted, v, err, toVPSDTO)
		}
	}))
	r.Get("/provider/vps/{vpsId}", p(func(w http.ResponseWriter, r *http.Request) {
		v, err := s.provider.GetVPS(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "vpsId"))
		one(s, w, r, http.StatusOK, v, err, toVPSDTO)
	}))
	r.Post("/provider/vps/{vpsId}/actions", p(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action string `json:"action"`
		}
		if s.decode(w, r, &in) {
			v, err := s.provider.Action(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "vpsId"), in.Action)
			one(s, w, r, http.StatusAccepted, v, err, toVPSDTO)
		}
	}))
	r.Delete("/provider/vps/{vpsId}", p(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Confirm string `json:"confirm"`
		}
		if s.decode(w, r, &in) {
			v, err := s.provider.Delete(r.Context(), principalFrom(r.Context()), chi.URLParam(r, "vpsId"), in.Confirm)
			one(s, w, r, http.StatusAccepted, v, err, toVPSDTO)
		}
	}))
}
