package app

import "github.com/leandrodaf/barnraise/internal/domain"

// VersionStatus says which build this machine runs and whether a machine of
// the cluster runs a newer one.
type VersionStatus struct {
	Self domain.Build
	// Newest is the newest build of the cluster when it is newer than Self.
	NewestMachine string
	Newest        domain.Build
	Outdated      bool
	// SelfUpdate: the daemon takes the newer binary by itself, between sessions.
	SelfUpdate bool
}

// Versions compares this machine's build with every machine the projects know.
func (s *Service) Versions() VersionStatus {
	v := VersionStatus{Self: domain.Build{Version: s.d.Version, Time: s.d.BuildTime}, SelfUpdate: s.d.SelfUpdate}
	for _, name := range s.Projects() {
		p, err := s.Project(name)
		if err != nil {
			continue
		}
		for _, m := range p.Snapshot().Machines {
			if m == nil || m.Name == s.d.Machine || m.Left {
				continue
			}
			b := domain.Build{Version: m.Version, Time: m.BuildTime}
			if b.Newer(v.Self) && (!v.Outdated || b.Newer(v.Newest)) {
				v.Newest, v.NewestMachine, v.Outdated = b, m.Name, true
			}
		}
	}
	return v
}
