// Package manifest is the shared inventory type written by `dump` and read by
// `supplement` / `verify`. Intentionally verbose rather than clever: this is a
// one-time audit artifact, so every field is explicit and JSON-serializable.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Version bumps when the on-disk format changes incompatibly.
const Version = 1

type Manifest struct {
	Version   int       `json:"version"`
	Source    string    `json:"source"`         // source instance URL
	Target    string    `json:"target"`         // target instance URL
	HarvestAt time.Time `json:"harvest_at"`
	Gitea     Versions  `json:"versions"`

	Users    []User   `json:"users"`
	Orgs     []Org    `json:"orgs"`
	Repos    []Repo   `json:"repos"`
	Packages []Package `json:"packages,omitempty"`

	LoginSources []LoginSource `json:"login_sources,omitempty"` // from DB, not API
}

type Versions struct {
	Source string `json:"source"` // e.g. "1.23.4"
	Target string `json:"target,omitempty"`
}

type User struct {
	ID        int64    `json:"id"`
	Login     string   `json:"login"`
	Email     string   `json:"email"`
	FullName  string   `json:"full_name,omitempty"`
	IsAdmin   bool     `json:"is_admin"`
	LoginType string   `json:"login_type,omitempty"` // password | ldap | oauth2 | ...
	SourceID  int64    `json:"source_id,omitempty"`  // login_source FK
	Created   time.Time `json:"created,omitempty"`
	SSHKeys   []Key    `json:"ssh_keys,omitempty"`
	GPGKeys   []Key    `json:"gpg_keys,omitempty"`
	Emails    []Email  `json:"emails,omitempty"`
	HasPATs   int      `json:"has_pats"`   // count of active PATs (for regen email)
	Has2FA    bool     `json:"has_2fa"`    // from DB
	OAuth2Apps []OAuthApp `json:"oauth2_apps,omitempty"`
}

type Key struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Key   string `json:"key,omitempty"`       // full pubkey for SSH
	KeyID string `json:"key_id,omitempty"`    // GPG key id
}

type Email struct {
	Email    string `json:"email"`
	Verified bool   `json:"verified"`
	Primary  bool   `json:"primary"`
}

type OAuthApp struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
}

type Org struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	FullName   string   `json:"full_name,omitempty"`
	Visibility string   `json:"visibility,omitempty"`
	Teams      []Team   `json:"teams"`
	Members    []string `json:"members"` // usernames (direct org membership)
	Labels     []Label  `json:"labels,omitempty"`
	Webhooks   []Hook   `json:"webhooks,omitempty"`
	Secrets    []string `json:"secrets,omitempty"`   // names only
	Variables  []Variable `json:"variables,omitempty"`
}

type Team struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Permission  string   `json:"permission"`
	Members     []string `json:"members"`
	Repos       []string `json:"repos"` // "owner/name"
}

type Repo struct {
	ID                int64       `json:"id"`
	FullName          string      `json:"full_name"` // owner/name
	Owner             string      `json:"owner"`
	Name              string      `json:"name"`
	Private           bool        `json:"private"`
	Fork              bool        `json:"fork"`
	Mirror            bool        `json:"mirror"`
	DefaultBranch     string      `json:"default_branch"`
	Description       string      `json:"description,omitempty"`
	Website           string      `json:"website,omitempty"`
	Topics            []string    `json:"topics,omitempty"`
	Archived          bool        `json:"archived"`
	HasWiki           bool        `json:"has_wiki"`
	HasIssues         bool        `json:"has_issues"`
	HasPR             bool        `json:"has_pull_requests"`
	HasActions        bool        `json:"has_actions"`
	HasPackages       bool        `json:"has_packages"`
	HasReleases       bool        `json:"has_releases"`
	Size              int64       `json:"size_bytes"` // from API (.size kB * 1024 approx)
	Branches          int         `json:"branches_count"`
	Tags              int         `json:"tags_count"`
	Releases          int         `json:"releases_count"`
	OpenIssues        int         `json:"open_issues_count"`
	ClosedIssues      int         `json:"closed_issues_count"`
	OpenPRs           int         `json:"open_prs_count"`
	ClosedPRs         int         `json:"closed_prs_count"`
	Stars             int         `json:"stars_count"`
	Watchers          int         `json:"watchers_count"`
	BranchProtections []BranchProt `json:"branch_protections,omitempty"`
	Webhooks          []Hook       `json:"webhooks,omitempty"`
	DeployKeys        []Key        `json:"deploy_keys,omitempty"`
	Collaborators     []Collab     `json:"collaborators,omitempty"`
	Labels            []Label      `json:"labels,omitempty"`
	Milestones        []Milestone  `json:"milestones,omitempty"`
	Secrets           []string     `json:"secrets,omitempty"`    // names only
	Variables         []Variable   `json:"variables,omitempty"`
	LFSSize           int64        `json:"lfs_size_bytes,omitempty"`
}

type BranchProt struct {
	Branch                string   `json:"branch"`
	EnablePush            bool     `json:"enable_push"`
	PushWhitelist         []string `json:"push_whitelist,omitempty"`
	RequiredApprovals     int64    `json:"required_approvals"`
	EnableStatusCheck     bool     `json:"enable_status_check"`
	StatusCheckContexts   []string `json:"status_check_contexts,omitempty"`
}

type Hook struct {
	ID          int64             `json:"id"`
	Type        string            `json:"type"`   // gitea | slack | discord | ...
	URL         string            `json:"url"`
	ContentType string            `json:"content_type"`
	Events      []string          `json:"events"`
	Active      bool              `json:"active"`
	Config      map[string]string `json:"config,omitempty"`
}

type Collab struct {
	Login      string `json:"login"`
	Permission string `json:"permission"`
}

type Label struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
	Exclusive   bool   `json:"exclusive,omitempty"`
}

type Milestone struct {
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	State       string     `json:"state"`
	DueOn       *time.Time `json:"due_on,omitempty"`
}

type Variable struct {
	Name  string `json:"name"`
	Value string `json:"value"` // plain-text; no encryption on variables
}

type Package struct {
	Owner   string `json:"owner"`
	Type    string `json:"type"`   // container | npm | maven | generic | rubygems | ...
	Name    string `json:"name"`
	Version string `json:"version"`
}

type LoginSource struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"` // ldap | oauth2 | smtp | ...
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
}

// Save writes the manifest to path atomically.
func (m *Manifest) Save(path string) error {
	m.Version = Version
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a manifest from path.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m Manifest
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		return nil, err
	}
	if m.Version != Version {
		return nil, fmt.Errorf("manifest version %d, want %d", m.Version, Version)
	}
	return &m, nil
}
