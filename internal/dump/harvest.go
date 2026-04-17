// Package dump harvests the source instance into a manifest and (eventually)
// on-disk tarballs.
//
// The Harvest function produces a complete inventory of users, orgs, teams,
// and repos by paging through the Gitea/Forgejo admin and per-resource
// endpoints. It is the read-only part of Phase 1 per the plan — the
// gitea-dump/pg_dump/mc-mirror shells come later.
package dump

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"code.gitea.io/sdk/gitea"

	"github.com/pacnpal/gitea2forgejo/internal/client"
	"github.com/pacnpal/gitea2forgejo/internal/manifest"
)

// pageSize is the per-page item count for list endpoints. 50 is the Gitea
// default MAX_RESPONSE_ITEMS; higher values may be rejected by the server.
const pageSize = 50

// Harvest walks the source instance and returns a populated manifest.
// Any errors are logged (and tallied) rather than fatal so partial harvests
// are still useful for diagnosis.
func Harvest(src *client.Client, log *slog.Logger) (*manifest.Manifest, error) {
	if src.Kind != client.KindSource {
		return nil, fmt.Errorf("Harvest requires a source client, got %s", src.Kind)
	}
	m := &manifest.Manifest{
		Source:    src.URL,
		HarvestAt: time.Now().UTC(),
	}
	ver, _, err := src.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("server version: %w", err)
	}
	m.Gitea.Source = ver
	log.Info("harvest: starting", "source", src.URL, "version", ver)

	h := &harvester{src: src, log: log, m: m}
	h.harvestUsers()
	h.harvestOrgs()
	h.harvestRepos()
	h.harvestPackages()

	log.Info("harvest: complete",
		"users", len(m.Users),
		"orgs", len(m.Orgs),
		"repos", len(m.Repos),
		"packages", len(m.Packages),
		"soft_errors", h.errCount)
	if h.errCount > 0 {
		return m, fmt.Errorf("harvest completed with %d soft errors (see log)", h.errCount)
	}
	return m, nil
}

type harvester struct {
	src      *client.Client
	log      *slog.Logger
	m        *manifest.Manifest
	errCount int
}

// softErr logs an error and bumps the count; used for per-entity failures so
// one bad repo doesn't abort the whole harvest.
func (h *harvester) softErr(what string, err error) {
	if err == nil {
		return
	}
	h.errCount++
	h.log.Warn("harvest error", "what", what, "err", err)
}

// -- users -------------------------------------------------------------------

func (h *harvester) harvestUsers() {
	users := paged(h.log, "admin/users", func(page int) ([]*gitea.User, error) {
		u, _, err := h.src.AdminListUsers(gitea.AdminListUsersOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return u, err
	})
	for _, u := range users {
		mu := manifest.User{
			ID:       u.ID,
			Login:    u.UserName,
			Email:    u.Email,
			FullName: u.FullName,
			IsAdmin:  u.IsAdmin,
			Created:  u.Created,
		}
		mu.SSHKeys = h.userSSHKeys(u.UserName)
		mu.GPGKeys = h.userGPGKeys(u.UserName)
		h.m.Users = append(h.m.Users, mu)
	}
}

func (h *harvester) userSSHKeys(user string) []manifest.Key {
	items := paged(h.log, "users/"+user+"/keys", func(page int) ([]*gitea.PublicKey, error) {
		k, _, err := h.src.ListPublicKeys(user, gitea.ListPublicKeysOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return k, err
	})
	out := make([]manifest.Key, 0, len(items))
	for _, k := range items {
		out = append(out, manifest.Key{ID: k.ID, Title: k.Title, Key: k.Key})
	}
	return out
}

func (h *harvester) userGPGKeys(user string) []manifest.Key {
	items := paged(h.log, "users/"+user+"/gpg_keys", func(page int) ([]*gitea.GPGKey, error) {
		k, _, err := h.src.ListGPGKeys(user, gitea.ListGPGKeysOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return k, err
	})
	out := make([]manifest.Key, 0, len(items))
	for _, k := range items {
		out = append(out, manifest.Key{ID: k.ID, Title: k.KeyID, KeyID: k.KeyID})
	}
	return out
}

// -- orgs --------------------------------------------------------------------

func (h *harvester) harvestOrgs() {
	orgs := paged(h.log, "admin/orgs", func(page int) ([]*gitea.Organization, error) {
		o, _, err := h.src.AdminListOrgs(gitea.AdminListOrgsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return o, err
	})
	for _, o := range orgs {
		mo := manifest.Org{
			ID:         o.ID,
			Name:       o.UserName,
			FullName:   o.FullName,
			Visibility: string(o.Visibility),
		}
		mo.Members = h.orgMembers(o.UserName)
		mo.Teams = h.orgTeams(o.UserName)
		mo.Webhooks = h.orgHooks(o.UserName)
		mo.Secrets = h.orgSecretNames(o.UserName)
		mo.Variables = h.orgVariables(o.UserName)
		h.m.Orgs = append(h.m.Orgs, mo)
	}
}

func (h *harvester) orgMembers(org string) []string {
	items := paged(h.log, "orgs/"+org+"/members", func(page int) ([]*gitea.User, error) {
		u, _, err := h.src.ListOrgMembership(org, gitea.ListOrgMembershipOption{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return u, err
	})
	out := make([]string, 0, len(items))
	for _, u := range items {
		out = append(out, u.UserName)
	}
	return out
}

func (h *harvester) orgTeams(org string) []manifest.Team {
	items := paged(h.log, "orgs/"+org+"/teams", func(page int) ([]*gitea.Team, error) {
		t, _, err := h.src.ListOrgTeams(org, gitea.ListTeamsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return t, err
	})
	out := make([]manifest.Team, 0, len(items))
	for _, t := range items {
		mt := manifest.Team{
			ID:         t.ID,
			Name:       t.Name,
			Permission: string(t.Permission),
		}
		mems := paged(h.log, fmt.Sprintf("teams/%d/members", t.ID), func(page int) ([]*gitea.User, error) {
			u, _, err := h.src.ListTeamMembers(t.ID, gitea.ListTeamMembersOptions{
				ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
			})
			return u, err
		})
		for _, u := range mems {
			mt.Members = append(mt.Members, u.UserName)
		}
		tr := paged(h.log, fmt.Sprintf("teams/%d/repos", t.ID), func(page int) ([]*gitea.Repository, error) {
			r, _, err := h.src.ListTeamRepositories(t.ID, gitea.ListTeamRepositoriesOptions{
				ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
			})
			return r, err
		})
		for _, r := range tr {
			mt.Repos = append(mt.Repos, r.FullName)
		}
		out = append(out, mt)
	}
	return out
}

func (h *harvester) orgHooks(org string) []manifest.Hook {
	items := paged(h.log, "orgs/"+org+"/hooks", func(page int) ([]*gitea.Hook, error) {
		hk, _, err := h.src.ListOrgHooks(org, gitea.ListHooksOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return hk, err
	})
	return convertHooks(items)
}

func (h *harvester) orgSecretNames(org string) []string {
	items := paged(h.log, "orgs/"+org+"/actions/secrets", func(page int) ([]*gitea.Secret, error) {
		s, _, err := h.src.ListOrgActionSecret(org, gitea.ListOrgActionSecretOption{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return s, err
	})
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, s.Name)
	}
	return out
}

func (h *harvester) orgVariables(org string) []manifest.Variable {
	items := paged(h.log, "orgs/"+org+"/actions/variables", func(page int) ([]*gitea.ActionVariable, error) {
		v, _, err := h.src.ListOrgActionVariable(org, gitea.ListOrgActionVariableOption{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return v, err
	})
	out := make([]manifest.Variable, 0, len(items))
	for _, v := range items {
		out = append(out, manifest.Variable{Name: v.Name, Value: v.Data})
	}
	return out
}

// -- repos -------------------------------------------------------------------

func (h *harvester) harvestRepos() {
	// SearchRepos with empty filter + mode=all returns every repo the admin
	// token can see (which is all of them).
	repos := paged(h.log, "repos/search", func(page int) ([]*gitea.Repository, error) {
		r, _, err := h.src.SearchRepos(gitea.SearchRepoOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return r, err
	})
	for _, r := range repos {
		h.m.Repos = append(h.m.Repos, h.repoDetail(r))
	}
}

func (h *harvester) repoDetail(r *gitea.Repository) manifest.Repo {
	mr := manifest.Repo{
		ID:            r.ID,
		FullName:      r.FullName,
		Owner:         r.Owner.UserName,
		Name:          r.Name,
		Private:       r.Private,
		Fork:          r.Fork,
		Mirror:        r.Mirror,
		DefaultBranch: r.DefaultBranch,
		Description:   r.Description,
		Website:       r.Website,
		Archived:      r.Archived,
		HasWiki:       r.HasWiki,
		HasIssues:     r.HasIssues,
		HasPR:         r.HasPullRequests,
		HasActions:    r.HasActions,
		HasPackages:   r.HasPackages,
		HasReleases:   r.HasReleases,
		Size:          int64(r.Size) * 1024, // API returns kB
		OpenIssues:    r.OpenIssues,
		OpenPRs:       r.OpenPulls,
		Releases:      r.Releases,
		Stars:         r.Stars,
		Watchers:      r.Watchers,
	}
	owner, name := r.Owner.UserName, r.Name
	mr.BranchProtections = h.repoBranchProtections(owner, name)
	mr.Webhooks = h.repoHooks(owner, name)
	mr.DeployKeys = h.repoDeployKeys(owner, name)
	mr.Collaborators = h.repoCollaborators(owner, name)
	mr.Topics = h.repoTopics(owner, name)
	mr.Labels = h.repoLabels(owner, name)
	mr.Milestones = h.repoMilestones(owner, name)
	mr.Secrets = h.repoSecretNames(owner, name)
	mr.Variables = h.repoVariables(owner, name)
	mr.Branches = h.repoBranchCount(owner, name)
	mr.Tags = h.repoTagCount(owner, name)
	return mr
}

func (h *harvester) repoBranchProtections(o, n string) []manifest.BranchProt {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/branch_protections", o, n), func(page int) ([]*gitea.BranchProtection, error) {
		bp, _, err := h.src.ListBranchProtections(o, n, gitea.ListBranchProtectionsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return bp, err
	})
	out := make([]manifest.BranchProt, 0, len(items))
	for _, b := range items {
		out = append(out, manifest.BranchProt{
			Branch:              b.BranchName,
			EnablePush:          b.EnablePush,
			PushWhitelist:       b.PushWhitelistUsernames,
			RequiredApprovals:   b.RequiredApprovals,
			EnableStatusCheck:   b.EnableStatusCheck,
			StatusCheckContexts: b.StatusCheckContexts,
		})
	}
	return out
}

func (h *harvester) repoHooks(o, n string) []manifest.Hook {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/hooks", o, n), func(page int) ([]*gitea.Hook, error) {
		hk, _, err := h.src.ListRepoHooks(o, n, gitea.ListHooksOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return hk, err
	})
	return convertHooks(items)
}

func (h *harvester) repoDeployKeys(o, n string) []manifest.Key {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/keys", o, n), func(page int) ([]*gitea.DeployKey, error) {
		k, _, err := h.src.ListDeployKeys(o, n, gitea.ListDeployKeysOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return k, err
	})
	out := make([]manifest.Key, 0, len(items))
	for _, k := range items {
		out = append(out, manifest.Key{ID: k.ID, Title: k.Title, Key: k.Key})
	}
	return out
}

func (h *harvester) repoCollaborators(o, n string) []manifest.Collab {
	users := paged(h.log, fmt.Sprintf("repos/%s/%s/collaborators", o, n), func(page int) ([]*gitea.User, error) {
		u, _, err := h.src.ListCollaborators(o, n, gitea.ListCollaboratorsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return u, err
	})
	out := make([]manifest.Collab, 0, len(users))
	for _, u := range users {
		perm, _, err := h.src.CollaboratorPermission(o, n, u.UserName)
		p := "read"
		if err == nil && perm != nil {
			p = string(perm.Permission)
		} else if err != nil {
			h.softErr(fmt.Sprintf("permission %s/%s:%s", o, n, u.UserName), err)
		}
		out = append(out, manifest.Collab{Login: u.UserName, Permission: p})
	}
	return out
}

func (h *harvester) repoTopics(o, n string) []string {
	t, _, err := h.src.ListRepoTopics(o, n, gitea.ListRepoTopicsOptions{
		ListOptions: gitea.ListOptions{Page: 1, PageSize: pageSize},
	})
	if err != nil {
		h.softErr(fmt.Sprintf("topics %s/%s", o, n), err)
		return nil
	}
	return t
}

func (h *harvester) repoLabels(o, n string) []manifest.Label {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/labels", o, n), func(page int) ([]*gitea.Label, error) {
		l, _, err := h.src.ListRepoLabels(o, n, gitea.ListLabelsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return l, err
	})
	out := make([]manifest.Label, 0, len(items))
	for _, l := range items {
		out = append(out, manifest.Label{
			Name: l.Name, Color: l.Color, Description: l.Description, Exclusive: l.Exclusive,
		})
	}
	return out
}

func (h *harvester) repoMilestones(o, n string) []manifest.Milestone {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/milestones", o, n), func(page int) ([]*gitea.Milestone, error) {
		ms, _, err := h.src.ListRepoMilestones(o, n, gitea.ListMilestoneOption{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return ms, err
	})
	out := make([]manifest.Milestone, 0, len(items))
	for _, ms := range items {
		m := manifest.Milestone{
			Title:       ms.Title,
			Description: ms.Description,
			State:       string(ms.State),
		}
		if ms.Deadline != nil && !ms.Deadline.IsZero() {
			d := *ms.Deadline
			m.DueOn = &d
		}
		out = append(out, m)
	}
	return out
}

func (h *harvester) repoSecretNames(o, n string) []string {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/actions/secrets", o, n), func(page int) ([]*gitea.Secret, error) {
		s, _, err := h.src.ListRepoActionSecret(o, n, gitea.ListRepoActionSecretOption{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return s, err
	})
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, s.Name)
	}
	return out
}

func (h *harvester) repoVariables(o, n string) []manifest.Variable {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/actions/variables", o, n), func(page int) ([]*gitea.RepoActionVariable, error) {
		v, _, err := h.src.ListRepoActionVariable(o, n, gitea.ListRepoActionVariableOption{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return v, err
	})
	out := make([]manifest.Variable, 0, len(items))
	for _, v := range items {
		out = append(out, manifest.Variable{Name: v.Name, Value: v.Value})
	}
	return out
}

func (h *harvester) repoBranchCount(o, n string) int {
	// ListRepoBranches returns up to PageSize per page; we page through.
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/branches", o, n), func(page int) ([]*gitea.Branch, error) {
		b, _, err := h.src.ListRepoBranches(o, n, gitea.ListRepoBranchesOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return b, err
	})
	return len(items)
}

func (h *harvester) repoTagCount(o, n string) int {
	items := paged(h.log, fmt.Sprintf("repos/%s/%s/tags", o, n), func(page int) ([]*gitea.Tag, error) {
		t, _, err := h.src.ListRepoTags(o, n, gitea.ListRepoTagsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
		})
		return t, err
	})
	return len(items)
}

// -- packages ----------------------------------------------------------------

func (h *harvester) harvestPackages() {
	// Enumerate packages per owner (users and orgs). The API is
	// /api/v1/packages/{owner} and returns all types.
	owners := make([]string, 0, len(h.m.Users)+len(h.m.Orgs))
	for _, u := range h.m.Users {
		owners = append(owners, u.Login)
	}
	for _, o := range h.m.Orgs {
		owners = append(owners, o.Name)
	}
	for _, owner := range owners {
		items := paged(h.log, "packages/"+owner, func(page int) ([]*gitea.Package, error) {
			p, _, err := h.src.ListPackages(owner, gitea.ListPackagesOptions{
				ListOptions: gitea.ListOptions{Page: page, PageSize: pageSize},
			})
			return p, err
		})
		for _, p := range items {
			h.m.Packages = append(h.m.Packages, manifest.Package{
				Owner:   owner,
				Type:    string(p.Type),
				Name:    p.Name,
				Version: p.Version,
			})
		}
	}
}

// -- helpers -----------------------------------------------------------------

// paged drives a paginated endpoint until it returns an empty page. It logs
// and tolerates transient errors (returns what it has so far) rather than
// aborting the entire harvest.
func paged[T any](log *slog.Logger, what string, fetch func(page int) ([]T, error)) []T {
	var out []T
	for page := 1; ; page++ {
		var (
			batch []T
			err   error
		)
		for attempt := 0; attempt < 3; attempt++ {
			batch, err = fetch(page)
			if err == nil {
				break
			}
			if isNotFound(err) {
				return out
			}
			backoff := time.Duration(1<<attempt) * time.Second
			log.Warn("paged: retry", "what", what, "page", page, "err", err, "backoff", backoff)
			time.Sleep(backoff)
		}
		if err != nil {
			log.Warn("paged: giving up on this resource", "what", what, "page", page, "err", err)
			return out
		}
		if len(batch) == 0 {
			return out
		}
		out = append(out, batch...)
		if len(batch) < pageSize {
			return out
		}
	}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	// gitea SDK wraps 404 into errors whose message contains "404" — a
	// conservative substring match is fine; callers only use this for "is
	// it safe to treat as empty".
	msg := err.Error()
	return errors.Is(err, errNotFound) ||
		containsFold(msg, "404") ||
		containsFold(msg, "not found")
}

var errNotFound = errors.New("not found")

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func convertHooks(items []*gitea.Hook) []manifest.Hook {
	out := make([]manifest.Hook, 0, len(items))
	for _, hk := range items {
		out = append(out, manifest.Hook{
			ID:          hk.ID,
			Type:        string(hk.Type),
			URL:         hk.Config["url"],
			ContentType: hk.Config["content_type"],
			Events:      hk.Events,
			Active:      hk.Active,
			Config:      hk.Config,
		})
	}
	return out
}
