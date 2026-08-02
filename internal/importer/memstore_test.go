package importer

import "github.com/shady2k/nocx/internal/profile"

// memStore implements both profile.ProfileRepository and
// profile.GroupRepository in memory, for import testing.
type memStore struct {
	profiles []profile.SSHProfile
	groups   []profile.ProfileGroup
}

func newMemStore() *memStore { return &memStore{} }

// --- profile.ProfileRepository ---

func (m *memStore) LoadProfiles() ([]profile.SSHProfile, error) { return m.profiles, nil }

func (m *memStore) CreateProfile(p profile.SSHProfile) error {
	for _, ex := range m.profiles {
		if ex.ID == p.ID {
			return profile.ErrProfileExists
		}
	}
	m.profiles = append(m.profiles, p)
	return nil
}

func (m *memStore) UpdateProfile(p profile.SSHProfile) error {
	for i, ex := range m.profiles {
		if ex.ID == p.ID {
			m.profiles[i] = p
			return nil
		}
	}
	return profile.ErrProfileNotFound
}

func (m *memStore) DeleteProfile(id string) error {
	for i, ex := range m.profiles {
		if ex.ID == id {
			m.profiles = append(m.profiles[:i], m.profiles[i+1:]...)
			return nil
		}
	}
	return nil
}

// --- profile.GroupRepository ---

func (m *memStore) LoadGroups() ([]profile.ProfileGroup, error) { return m.groups, nil }

func (m *memStore) CreateGroup(g profile.ProfileGroup) error {
	for _, ex := range m.groups {
		if ex.ID == g.ID {
			return profile.ErrGroupExists
		}
	}
	m.groups = append(m.groups, g)
	return nil
}

func (m *memStore) UpdateGroup(g profile.ProfileGroup) error {
	for i, ex := range m.groups {
		if ex.ID == g.ID {
			m.groups[i] = g
			return nil
		}
	}
	return profile.ErrGroupNotFound
}

func (m *memStore) DeleteGroup(id string) error {
	for i, ex := range m.groups {
		if ex.ID == id {
			m.groups = append(m.groups[:i], m.groups[i+1:]...)
			return nil
		}
	}
	return nil
}
