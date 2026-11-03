package agenttask

// bulkTypes may be queued on many servers at once (fleet batches). Tasks that
// name a per-server object (container, app, site, backup, file path) or whose
// results can carry file contents are deliberately excluded.
var bulkTypes = map[string]bool{
	ServicesList: true, ProcessesList: true, UsersList: true, SSHKeysList: true, CronList: true, TimeInfo: true,
	LogsRead:      true,
	ServiceAction: true, SSHKeysAdd: true, SSHKeysRemove: true, TimezoneSet: true,
	DockerInfo: true, DockerContainers: true, DockerImages: true, DockerVolumes: true, DockerNetworks: true,
	DockerImagePull: true, DockerImagesPrune: true,
	ProxyStatus: true,
}

// Bulk reports whether typ may run as a fleet batch.
func Bulk(typ string) bool { return bulkTypes[typ] }

// BulkTypes lists the types allowed in fleet batches, for clients.
func BulkTypes() []string {
	out := make([]string, 0, len(bulkTypes))
	for t := range bulkTypes {
		out = append(out, t)
	}
	return out
}
