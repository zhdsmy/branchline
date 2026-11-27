package app

import (
	"net/url"
	"strings"
)

// The hosted-service model: a remote URL of GitHub, GitLab, Bitbucket or
// Gitea parses into the host and repository path its web pages are built
// from. Credentials and ports never survive parsing, so a web URL is always
// plain https on the host itself and nothing the user typed as a secret can
// leak into the browser or an error message.

// hostedRepo is a repository on a known hosting service: the service, its
// host and the repository path without .git, as in "group/sub/repo".
type hostedRepo struct {
	service string
	host    string
	path    string
}

// hostedService names the hosting service of a host, case-insensitively, by
// the substring of its product name: github covers github.com and
// self-hosted GitHub Enterprise, gitlab covers gitlab.com and self-hosted
// instances, bitbucket covers the cloud and the server product, and gitea
// covers Gitea instances and Codeberg.
func hostedService(host string) string {
	switch h := strings.ToLower(host); {
	case strings.Contains(h, "github"):
		return "GitHub"
	case strings.Contains(h, "gitlab"):
		return "GitLab"
	case strings.Contains(h, "bitbucket"):
		return "Bitbucket"
	case strings.Contains(h, "gitea"), strings.Contains(h, "codeberg"):
		return "Gitea"
	}
	return ""
}

// parseHostedRepo parses the fetch URL of a remote into its hosted
// repository, or reports false for anything without a known service: local
// paths, file URLs, other schemes, unknown hosts and URLs without a
// repository path. The result never carries credentials: url.Parse drops
// them out of Hostname, and the SCP form cuts everything before the "@".
func parseHostedRepo(rawurl string) (hostedRepo, bool) {
	rawurl = strings.TrimSpace(rawurl)
	if rawurl == "" {
		return hostedRepo{}, false
	}
	var host, path string
	if strings.Contains(rawurl, "://") {
		u, err := url.Parse(rawurl)
		if err != nil {
			return hostedRepo{}, false
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "ssh", "git":
		default:
			// file: stays local and anything else is not a web page.
			return hostedRepo{}, false
		}
		host, path = u.Hostname(), u.Path
	} else {
		// The SCP form [user@]host:owner/repo; a slash before the colon is a
		// local path, not a remote, and any "@" in the repository path stays
		// part of the path rather than trimming the host.
		h, p, found := strings.Cut(rawurl, ":")
		if !found || strings.ContainsAny(h, "/\\") {
			return hostedRepo{}, false
		}
		if i := strings.LastIndex(h, "@"); i >= 0 {
			h = h[i+1:]
		}
		host, path = h, p
	}
	path = strings.Trim(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	if host == "" || path == "" || strings.ContainsAny(host, " \t\r\n?#") {
		return hostedRepo{}, false
	}
	service := hostedService(host)
	if service == "" {
		return hostedRepo{}, false
	}
	return hostedRepo{service: service, host: host, path: path}, true
}

// escapeBranchPath escapes one branch name for the path segment of a web
// URL: slashes stay — the services read the rest of the path as the branch
// — and spaces, question marks, hashes and percents are percent-encoded.
func escapeBranchPath(branch string) string {
	return strings.ReplaceAll(url.PathEscape(branch), "%2F", "/")
}

// home is the repository's main page, the target of View on '<service>' of
// a remote row.
func (r hostedRepo) home() string {
	return "https://" + r.host + "/" + r.path
}

// branchPage is the page of branch on the hosting service, the target of
// View on '<service>' of a remote branch row.
func (r hostedRepo) branchPage(branch string) string {
	esc := escapeBranchPath(branch)
	switch r.service {
	case "GitLab":
		return r.home() + "/-/tree/" + esc
	case "Bitbucket":
		return r.home() + "/src/" + esc
	case "Gitea":
		return r.home() + "/src/branch/" + esc
	default:
		return r.home() + "/tree/" + esc
	}
}

// pullRequestPage is the page that starts a pull request or merge request
// from branch, the target of the local branch entries.
func (r hostedRepo) pullRequestPage(branch string) string {
	switch r.service {
	case "GitLab":
		return r.home() + "/-/merge_requests/new?merge_request%5Bsource_branch%5D=" + url.QueryEscape(branch)
	case "Bitbucket":
		return r.home() + "/pull-requests/new?source=" + url.QueryEscape(branch)
	case "Gitea":
		return r.home() + "/compare/" + escapeBranchPath(branch)
	default:
		return r.home() + "/pull/new/" + escapeBranchPath(branch)
	}
}
