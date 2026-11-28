// Package store keeps the repositories the app knows, local and on servers
// reached over SSH, in a JSON file.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhdsmy/branchline/internal/git"
)

// Kind is where a repository is.
type Kind string

const (
	Local Kind = "local"
	SSH   Kind = "ssh"
)

// Repo is a repository the app knows.
type Repo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Path is the working tree: of this machine, or of the server.
	Path string `json:"path"`
	// The server of an SSH repository.
	Host     string    `json:"host,omitempty"`
	User     string    `json:"user,omitempty"`
	Port     int       `json:"port,omitempty"`
	Identity string    `json:"identity,omitempty"`
	Opened   time.Time `json:"opened"`
	// Local is this repository's settings, overriding the app's where set.
	Local RepoSettings `json:"local,omitempty"`
	// Workspace groups the repository on the start page. Empty is ungrouped.
	Workspace string `json:"workspace,omitempty"`
}

// RepoSettings are the settings of one repository, overriding the app's
// where set.
type RepoSettings struct {
	// NoAutoFetch lists the remotes that automatic fetching skips. A remote
	// is fetched automatically until it is cleared.
	NoAutoFetch []string `json:"noAutoFetch,omitempty"`

	// SidebarClosed records collapsed reference folders by their stable IDs.
	SidebarClosed []string `json:"sidebarClosed,omitempty"`
	// SidebarSections records which repository sidebar sections are open.
	// Missing keys keep the original defaults.
	SidebarSections SidebarSections `json:"sidebarSections,omitempty"`

	MainBranch    string `json:"mainBranch,omitempty"`
	AuthorName    string `json:"authorName,omitempty"`
	AuthorEmail   string `json:"authorEmail,omitempty"`
	UseGlobalUser bool   `json:"useGlobalUser,omitempty"`
	TabWidth      int    `json:"tabWidth,omitempty"`
	NoFastForward bool   `json:"noFastForward,omitempty"`
	Template      string `json:"template,omitempty"`
	SignedOffBy   bool   `json:"signedOffBy,omitempty"`
	GPGKey        string `json:"gpgKey,omitempty"`
	IssuePattern  string `json:"issuePattern,omitempty"`
	IssueURL      string `json:"issueURL,omitempty"`
	// IssueRules are further tracker rules, tried after IssuePattern.
	IssueRules  []IssueRule `json:"issueRules,omitempty"`
	FlowMain    string      `json:"flowMain,omitempty"`
	FlowDevelop string      `json:"flowDevelop,omitempty"`
	FlowFeature string      `json:"flowFeature,omitempty"`
	FlowRelease string      `json:"flowRelease,omitempty"`
	FlowHotfix  string      `json:"flowHotfix,omitempty"`
	FlowTag     string      `json:"flowTag,omitempty"`
	// NoGlobalCredentials keeps git from the credential helpers of the
	// global configuration, so it asks in Branchline.
	NoGlobalCredentials bool `json:"noGlobalCredentials,omitempty"`
	// TrustShared runs the custom commands of
	// .branchline/custom-commands.json without asking first.
	TrustShared bool `json:"trustShared,omitempty"`
	// HideIssueLinks turns off bug tracker links in this repository.
	HideIssueLinks bool `json:"hideIssueLinks,omitempty"`
	// NoGlobalConfig runs git here without the user's global configuration
	// file, as if "Use global Git configuration file" were unchecked:
	// user.name and user.email are in .git/config.
	NoGlobalConfig bool `json:"noGlobalConfig,omitempty"`
	// RemoteAccounts bind remotes to hosting accounts, whose token answers
	// for their HTTPS URL.
	RemoteAccounts []RemoteAccount `json:"remoteAccounts,omitempty"`
	// Layout remembers the panes of this repository: log height, file
	// width, whether the commit details are hidden, and which detail is shown.
	Layout Layout `json:"layout,omitempty"`
	// Diff remembers the diff header's lines of context, the choices of
	// the Local Changes file lists, and the layout of each diff scope.
	Diff DiffPrefs `json:"diff,omitempty"`

	// HistoryCurrentBranch limits the history to the commits HEAD reaches
	// instead of every branch, remote branch and tag; the zero value shows
	// them all.
	HistoryCurrentBranch bool `json:"historyCurrentBranch,omitempty"`
	// HistoryFirstParent follows only the first parent of a merge in the
	// history, as git log --first-parent does; the zero value walks every
	// parent. It is independent of HistoryCurrentBranch.
	HistoryFirstParent bool `json:"historyFirstParent,omitempty"`
}

// SidebarSections is the expanded state of one repository sidebar.
// A nil pointer means the user has not changed that section.
type SidebarSections struct {
	Branches   *bool `json:"branches,omitempty"`
	Remotes    *bool `json:"remotes,omitempty"`
	Tags       *bool `json:"tags,omitempty"`
	Stashes    *bool `json:"stashes,omitempty"`
	Worktrees  *bool `json:"worktrees,omitempty"`
	Submodules *bool `json:"submodules,omitempty"`
}

// DiffPrefs are the diff and file list choices of one repository.
// Layout and entire-file fields have a saved flag because false is a
// choice: side by side, or only the changed lines.
type DiffPrefs struct {
	// ContextLines is how many unchanged lines show around a change, as
	// git diff -U; ContextSaved marks it chosen, since 0 is a choice.
	ContextLines int  `json:"contextLines,omitempty"`
	ContextSaved bool `json:"contextSaved,omitempty"`
	// FileListTree groups the changed files of Local Changes in folders.
	// The zero value is the flat list.
	FileListTree bool `json:"fileListTree,omitempty"`
	// HistoryFileListTree groups the changed files of a commit in folders
	// on the History page. The zero value is the flat list, and the choice
	// is independent of FileListTree.
	HistoryFileListTree bool `json:"historyFileListTree,omitempty"`
	// FileListCombined puts staged and unstaged changes in one list.
	FileListCombined bool `json:"fileListCombined,omitempty"`
	// FileListSort is "status" to sort files by their change, "" by path.
	FileListSort string `json:"fileListSort,omitempty"`
	// HideUntracked leaves untracked files out of the Unstaged list.
	HideUntracked bool `json:"hideUntracked,omitempty"`
	// PopupUnified shows the diff the spacebar opens in one column; it is
	// side by side otherwise.
	PopupUnified bool `json:"popupUnified,omitempty"`
	// PopupLayoutSaved is true once the spacebar window's layout was chosen.
	PopupLayoutSaved bool `json:"popupLayoutSaved,omitempty"`

	// CommitUnified shows the diff of a commit in one column; it is side
	// by side otherwise. CommitLayoutSaved marks that layout chosen.
	CommitUnified     bool `json:"commitUnified,omitempty"`
	CommitLayoutSaved bool `json:"commitLayoutSaved,omitempty"`
	// HistoryUnified shows the diff of the commit selected in the history
	// in one column. HistoryLayoutSaved marks that layout chosen.
	HistoryUnified     bool `json:"historyUnified,omitempty"`
	HistoryLayoutSaved bool `json:"historyLayoutSaved,omitempty"`
	// SeparateUnified shows a diff in a separate window in one column.
	// SeparateLayoutSaved marks that layout chosen.
	SeparateUnified     bool `json:"separateUnified,omitempty"`
	SeparateLayoutSaved bool `json:"separateLayoutSaved,omitempty"`
	// RevisionUnified shows the diff of one revision, or of the comparison
	// of two revisions, in one column. RevisionLayoutSaved marks that
	// layout chosen.
	RevisionUnified     bool `json:"revisionUnified,omitempty"`
	RevisionLayoutSaved bool `json:"revisionLayoutSaved,omitempty"`

	// CommitEntire shows the entire file rather than only the changed
	// lines in the Changes panes; CommitEntireSaved marks that chosen.
	CommitEntire      bool `json:"commitEntire,omitempty"`
	CommitEntireSaved bool `json:"commitEntireSaved,omitempty"`
	// SeparateEntire shows the entire file in a separate-window diff;
	// SeparateEntireSaved marks that chosen.
	SeparateEntire      bool `json:"separateEntire,omitempty"`
	SeparateEntireSaved bool `json:"separateEntireSaved,omitempty"`

	// HiddenSymbols draws whitespace and other invisible characters in
	// every text diff; HiddenSymbolsSaved marks that chosen.
	HiddenSymbols      bool `json:"hiddenSymbols,omitempty"`
	HiddenSymbolsSaved bool `json:"hiddenSymbolsSaved,omitempty"`
	// ImageHighlight highlights the differing pixels of image diffs;
	// ImageHighlightSaved marks that chosen.
	ImageHighlight      bool `json:"imageHighlight,omitempty"`
	ImageHighlightSaved bool `json:"imageHighlightSaved,omitempty"`
}

// RemoteAccount binds a remote, at the URL it had, to the hosting account
// whose Key is Account.
type RemoteAccount struct {
	Remote  string `json:"remote"`
	URL     string `json:"url,omitempty"`
	Account string `json:"account"`
}

// AccountOf returns the account key bound to remote, or "".
func (r RepoSettings) AccountOf(remote string) string {
	for _, b := range r.RemoteAccounts {
		if b.Remote == remote {
			return b.Account
		}
	}
	return ""
}

// BindRemote binds remote at url to account, or unbinds it when account
// is empty. It returns the bindings, the others kept.
func (r RepoSettings) BindRemote(old, remote, url, account string) []RemoteAccount {
	var out []RemoteAccount
	for _, b := range r.RemoteAccounts {
		if b.Remote != old && b.Remote != remote {
			out = append(out, b)
		}
	}
	if account != "" {
		out = append(out, RemoteAccount{Remote: remote, URL: url, Account: account})
	}
	return out
}

// IssueRule turns text that matches Pattern into a link to URL, where $1 is
// the first group of the match.
type IssueRule struct {
	Name    string `json:"name,omitempty"`
	Pattern string `json:"pattern"`
	URL     string `json:"url"`
}

// Rules returns every issue tracker rule of the repository, the original
// pattern first.
func (r RepoSettings) Rules() []IssueRule {
	var out []IssueRule
	if r.IssuePattern != "" && r.IssueURL != "" {
		out = append(out, IssueRule{Pattern: r.IssuePattern, URL: r.IssueURL})
	}
	for _, rule := range r.IssueRules {
		if rule.Pattern != "" && rule.URL != "" {
			out = append(out, rule)
		}
	}
	return out
}

// Layout is the pane arrangement of one repository.
type Layout struct {
	LogSplit    float32 `json:"logSplit,omitempty"`
	DetailSplit float32 `json:"detailSplit,omitempty"`
	ChangeSplit float32 `json:"changeSplit,omitempty"`
	// SidebarSplit is the width the user gave the repository sidebar.
	// Zero means the sidebar still uses its starting width.
	SidebarSplit float32 `json:"sidebarSplit,omitempty"`
	HideDetails  bool    `json:"hideDetails,omitempty"`
	DetailTab    int     `json:"detailTab,omitempty"`
	Saved        bool    `json:"saved,omitempty"`
	// HistoryColumns are widths the user gave the commit history table, by
	// column ID. Columns not named keep the width the table starts with.
	HistoryColumns map[string]float32 `json:"historyColumns,omitempty"`
	// FileHistoryColumns are widths the user gave the file history table.
	FileHistoryColumns map[string]float32 `json:"fileHistoryColumns,omitempty"`
}

// Executor returns what runs git in the repository, with the default
// toolchain settings: the git of PATH, no traces, no extra SSH keys.
func (r Repo) Executor() git.Executor {
	return r.ExecutorFor(Settings{})
}

// ExecutorFor returns what runs git in the repository with the toolchain
// the settings name: the custom git program, the trace switches, the PATH
// choice, and the active SSH keys offered to every remote using SSH. An
// SSH repository with its own identity key keeps it instead of the active
// keys, as the executor already prefers it.
func (r Repo) ExecutorFor(s Settings) git.Executor {
	if r.Kind == SSH {
		return &git.SSHExecutor{Host: r.Host, User: r.User, Port: r.Port, Identity: r.Identity, Dir: r.Path, Trace: s.Trace(), Path: git.PathMode(s.EnvPath), SSHKeys: slices.Clone(s.SSHKeys)}
	}
	return &git.LocalExecutor{Dir: r.Path, Git: s.GitPath, Trace: s.Trace(), Path: git.PathMode(s.EnvPath), SSHKeys: slices.Clone(s.SSHKeys)}
}

// ExecutorWith returns what runs git. gitPath replaces the git program of
// a repository on this Mac; a server keeps the git on its PATH. trace
// turns on Git's own traces for either. pathMode chooses the PATH of the
// git or ssh program on this Mac; the server keeps its own PATH.
func (r Repo) ExecutorWith(gitPath string, trace git.Trace, pathMode string) git.Executor {
	if r.Kind == SSH {
		return &git.SSHExecutor{Host: r.Host, User: r.User, Port: r.Port, Identity: r.Identity, Dir: r.Path, Trace: trace, Path: git.PathMode(pathMode)}
	}
	return &git.LocalExecutor{Dir: r.Path, Git: gitPath, Trace: trace, Path: git.PathMode(pathMode)}
}

// Location says where the repository is: a path, or user@host:path.
func (r Repo) Location() string {
	if r.Kind != SSH {
		return r.Path
	}
	return r.Address()
}

// Address returns the SSH address of the repository, as ParseAddress
// reads it. The path is written as it is: the ssh:// form's path is not a
// URL path, so it carries no escapes.
func (r Repo) Address() string {
	host := r.Host
	// scp style cannot carry a host with a colon, so an IPv6 host uses the
	// ssh:// form even without a port; there its colons need brackets.
	sshForm := r.Port > 0 || strings.Contains(r.Host, ":")
	if sshForm && strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if r.User != "" {
		host = r.User + "@" + host
	}
	if sshForm {
		p := r.Path
		if strings.HasPrefix(p, "~") {
			p = "/" + p
		}
		if r.Port > 0 {
			return "ssh://" + host + ":" + strconv.Itoa(r.Port) + p
		}
		return "ssh://" + host + p
	}
	return host + ":" + r.Path
}

// Same reports whether r and o are one repository.
func (r Repo) Same(o Repo) bool {
	if r.Kind != o.Kind || r.Path != o.Path {
		return false
	}
	return r.Kind == Local || r.Host == o.Host && r.User == o.User && r.Port == o.Port
}

// ParseAddress reads where a repository of a server is, as git writes
// remotes: user@host:path (scp style, path relative to the home unless it
// starts with /), or ssh://user@host:port/path. A path of ~ or ~/… is
// under the home either way.
//
// The path of an ssh:// address is not a URL path: the user writes it as
// it is on the server, so after the authority it is read as typed — a % is
// a percent, not an escape, and # or ? never end the path.
func ParseAddress(s string) (Repo, error) {
	s = strings.TrimSpace(s)
	r := Repo{Kind: SSH}
	if strings.HasPrefix(s, "ssh://") {
		rest := s[len("ssh://"):]
		var authority string
		if slash := strings.IndexByte(rest, '/'); slash < 0 {
			authority = rest
			r.Path = ""
		} else {
			authority, r.Path = rest[:slash], rest[slash:]
		}
		var hostport string
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			r.User, hostport = authority[:at], authority[at+1:]
		} else {
			hostport = authority
		}
		if strings.HasPrefix(hostport, "[") {
			end := strings.IndexByte(hostport, ']')
			if end < 0 {
				return r, fmt.Errorf("cannot read %q: no ] in the host", s)
			}
			r.Host = hostport[1:end]
			if rest := hostport[end+1:]; rest != "" {
				if !strings.HasPrefix(rest, ":") {
					return r, fmt.Errorf("cannot read %q: garbage after ]", s)
				}
				if p := rest[1:]; p != "" {
					port, err := parseSSHPort(p)
					if err != nil {
						return r, err
					}
					r.Port = port
				}
			}
		} else if host, port, ok := strings.Cut(hostport, ":"); ok {
			r.Host = host
			if port != "" {
				var err error
				if r.Port, err = parseSSHPort(port); err != nil {
					return r, err
				}
			}
		} else {
			r.Host = hostport
		}
		if strings.HasPrefix(r.Path, "/~") {
			r.Path = r.Path[1:]
		}
	} else {
		host, p, ok := strings.Cut(s, ":")
		if !ok {
			return r, errors.New("write the address as user@host:/path or ssh://user@host:port/path")
		}
		if u, h, ok := strings.Cut(host, "@"); ok {
			r.User, host = u, h
		}
		r.Host = host
		r.Path = p
		if r.Path != "" && !strings.HasPrefix(r.Path, "/") && !strings.HasPrefix(r.Path, "~") {
			r.Path = "~/" + r.Path
		}
	}
	if r.Path == "" {
		r.Path = "~"
	}
	if r.Path != "~" && r.Path != "/" {
		r.Path = strings.TrimSuffix(r.Path, "/")
	}
	ex := git.SSHExecutor{Host: r.Host, User: r.User, Port: r.Port, Dir: r.Path}
	if err := ex.Validate(); err != nil {
		return r, err
	}
	return r, nil
}

// parseSSHPort reads a decimal port in 1..65535; signs and non-digits are rejected.
func parseSSHPort(p string) (int, error) {
	if p == "" {
		return 0, fmt.Errorf("bad port %q", p)
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return 0, fmt.Errorf("bad port %q", p)
		}
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("bad port %q", p)
	}
	return port, nil
}

// DefaultName names a repository after its directory.
func DefaultName(p string) string {
	p = strings.TrimSuffix(p, "/")
	name := path.Base(filepath.ToSlash(p))
	if name == "" || name == "." || name == "/" || name == "~" {
		return p
	}
	return name
}

// Store is the repositories, saved in a file as they change.
type Store struct {
	mu   sync.Mutex
	path string
	data file
	// reposRev advances on every save, saves unrelated to the repository
	// list and saves that roll back included, so a reader caching the
	// sorted list can tell it went stale without comparing the
	// repositories themselves. A revision spent on an unrelated or
	// rolled-back save only costs the cache one extra copy. It is
	// runtime-only: a store read back from disk starts at zero again.
	reposRev uint64
}

type file struct {
	Appearance string   `json:"appearance,omitempty"`
	Settings   Settings `json:"settings,omitempty"`
	// SettingsSaved distinguishes an explicit all-off choice from a store
	// that has never saved Preferences. Older stores infer it from values.
	SettingsSaved bool   `json:"settingsSaved,omitempty"`
	Repos         []Repo `json:"repos"`
	// Open are the IDs of the repositories open in tabs, in their order.
	Open []string `json:"open"`
	// Active is the repository shown. ActiveSet keeps an explicit start
	// page apart from a store saved before it was remembered.
	Active    string `json:"active,omitempty"`
	ActiveSet bool   `json:"activeSet,omitempty"`
	// Accounts are hosting services used to browse and clone repositories.
	Accounts []Account `json:"accounts,omitempty"`
	// Welcomed is set once the welcome window has been through.
	Welcomed bool `json:"welcomed,omitempty"`
	// Deleted are the local paths removed from the list, which a rescan
	// of the source folders leaves out.
	Deleted []string `json:"deleted,omitempty"`
	// Sheets are the check boxes the git sheets remember, by key.
	Sheets map[string]bool `json:"sheets,omitempty"`
	// Workspaces are the workspaces made in Configure Workspaces, which
	// exist without a repository in them.
	Workspaces []string `json:"workspaces,omitempty"`
}

// Account is a hosting service the user can browse and clone from.
// Token is stored as typed and is never written to logs.
type Account struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Host    string `json:"host,omitempty"`
	Token   string `json:"token,omitempty"`
	// Email goes with a Bitbucket Cloud API token, which is used with it.
	Email string `json:"email,omitempty"`
	// URL is the account page on the service, as the service gave it.
	URL string `json:"url,omitempty"`
	// NoNotifications keeps the account notifications out of the
	// repository windows.
	NoNotifications bool `json:"noNotifications,omitempty"`
	// Auth is "oauth" when a GitHub account was authorized with the
	// device flow. Empty means a personal access token, including an
	// account saved before the field existed.
	Auth string `json:"auth,omitempty"`
}

// Key names the account in bindings: its service, host and name.
func (a Account) Key() string { return a.Service + "|" + a.Host + "|" + a.Name }

// CustomCommand is a git command shown in the repository menu.
type CustomCommand struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Scope is "repository" when the command belongs to one repository.
	// An empty scope is an app-wide command.
	Scope string `json:"scope,omitempty"`
	Repo  string `json:"repo,omitempty"`
	// Kind is what Command is: "git" arguments (the default), a "bash"
	// script, a "process" to start, or an "url" to open.
	Kind string `json:"kind,omitempty"`
	// Target is where the command is offered: "repository" (the default),
	// "branch", "commit", "file", "tag" or "stash".
	Target string `json:"target,omitempty"`
	// ShowOutput shows what the command printed; Confirm asks first.
	ShowOutput bool `json:"showOutput,omitempty"`
	Confirm    bool `json:"confirm,omitempty"`
	// WaitForExit waits for a started process to end; one that shows its
	// output always waits.
	WaitForExit bool `json:"waitForExit,omitempty"`
	// OS is where the command is offered: "" or "any" everywhere, "mac"
	// or "windows".
	OS string `json:"os,omitempty"`
	// UI, when set, is a dialog shown before the command runs, whose
	// controls give the command more ${name} variables.
	UI *CommandUI `json:"ui,omitempty"`
	// Shared is a command read from the repository's
	// .branchline/custom-commands.json, never saved in Settings.
	Shared bool `json:"-"`
}

// CommandUI is the dialog of a custom command.
type CommandUI struct {
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Controls    []CommandControl `json:"controls,omitempty"`
	// Buttons are the dialog's buttons, the first the default. None are
	// Run, which runs the command, and Cancel.
	Buttons []CommandButton `json:"buttons,omitempty"`
}

// CommandControl is a field of a custom command's dialog. Name is the
// variable it sets.
type CommandControl struct {
	// Kind is "text", "path", "checkbox" or "select".
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Default is the text first shown, or the choice first chosen.
	Default string `json:"default,omitempty"`
	// A checkbox gives CheckedValue or UncheckedValue, and starts Checked.
	Checked        bool   `json:"checked,omitempty"`
	CheckedValue   string `json:"checkedValue,omitempty"`
	UncheckedValue string `json:"uncheckedValue,omitempty"`
	// A select offers Options, or the references under the prefixes of
	// Filter, such as refs/heads/ refs/tags/.
	Options []string `json:"options,omitempty"`
	Filter  string   `json:"filter,omitempty"`
	// A path's Choose button opens DialogType: "openFile" (the default),
	// "openDirectory" or "saveFile", with Filename filled for a save.
	DialogType string `json:"dialogType,omitempty"`
	Filename   string `json:"filename,omitempty"`
}

// CommandButton is a button of a custom command's dialog. Kind "close"
// closes the dialog; empty runs the command; another kind runs Command
// as that kind instead.
type CommandButton struct {
	Title       string `json:"title"`
	Kind        string `json:"kind,omitempty"`
	Command     string `json:"command,omitempty"`
	ShowOutput  bool   `json:"showOutput,omitempty"`
	WaitForExit bool   `json:"waitForExit,omitempty"`
}

// Settings are the preferences that change how the app works.
type Settings struct {
	AutoFetch    bool `json:"autoFetch,omitempty"`
	FetchMinutes int  `json:"fetchMinutes,omitempty"`
	FetchTags    bool `json:"fetchTags,omitempty"`
	// LocalBranchSort and RemoteBranchSort are the orders of the sidebar Sort Branches menu.
	LocalBranchSort  string `json:"localBranchSort,omitempty"`
	RemoteBranchSort string `json:"remoteBranchSort,omitempty"`
	// TagSort is the order of the sidebar Sort Tags menu.
	TagSort          string `json:"tagSort,omitempty"`
	UpdateSubmodules bool   `json:"updateSubmodules,omitempty"`
	PushAfterCommit  bool   `json:"pushAfterCommit,omitempty"`
	IgnoreSpace      bool   `json:"ignoreSpace,omitempty"`
	// DiffChangesOnly limits file diffs to the changed lines and their
	// context. Unset, the diff header shows the entire file, so a store that
	// predates the choice needs no migration.
	DiffChangesOnly bool `json:"diffChangesOnly,omitempty"`
	// DiffContextLines is how many unchanged lines show around a change, as
	// git diff -U; DiffContextSaved marks it chosen, since 0 is a choice.
	DiffContextLines int  `json:"diffContextLines,omitempty"`
	DiffContextSaved bool `json:"diffContextSaved,omitempty"`
	// DiffWrap wraps long diff lines rather than scrolling them sideways.
	DiffWrap         bool   `json:"diffWrap,omitempty"`
	DateOrder        bool   `json:"dateOrder,omitempty"`
	BranchSpace      string `json:"branchSpace,omitempty"` // "-", "_", or "" to leave spaces
	RememberCheckout bool   `json:"rememberCheckout,omitempty"`
	MarkDirtyTabs    bool   `json:"markDirtyTabs,omitempty"`
	CompactLabels    bool   `json:"compactLabels,omitempty"`
	SubjectLow       int    `json:"subjectLow,omitempty"`
	SubjectLowSaved  bool   `json:"subjectLowSaved,omitempty"`
	SubjectHigh      int    `json:"subjectHigh,omitempty"`
	SubjectHighSaved bool   `json:"subjectHighSaved,omitempty"`
	PageGuide        int    `json:"pageGuide,omitempty"`
	// NoPageGuide turns the page guide off, so a long description line
	// does not warn.
	NoPageGuide bool `json:"noPageGuide,omitempty"`
	// ChangeMarks shows + and - before changed lines instead of bars.
	ChangeMarks bool   `json:"changeMarks,omitempty"`
	SpellCheck  bool   `json:"spellCheck,omitempty"`
	AuthorName  string `json:"authorName,omitempty"`
	AuthorEmail string `json:"authorEmail,omitempty"`
	SignedOffBy bool   `json:"signedOffBy,omitempty"`
	GPGKey      string `json:"gpgKey,omitempty"`
	GitPath     string `json:"gitPath,omitempty"`
	// EnvPath is the PATH of git and ssh on this Mac: "" adds Homebrew,
	// "parent" keeps the process PATH, and "shell" uses the login shell.
	EnvPath string `json:"envPath,omitempty"`
	// VerboseGit is the old single trace switch; Settings reads it as all
	// five traces on.
	VerboseGit bool   `json:"verboseGit,omitempty"`
	TraceGit   bool   `json:"traceGit,omitempty"`
	TraceCurl  bool   `json:"traceCurl,omitempty"`
	TraceSSH   bool   `json:"traceSSH,omitempty"`
	TracePack  bool   `json:"tracePack,omitempty"`
	TracePerf  bool   `json:"tracePerf,omitempty"`
	Terminal   string `json:"terminal,omitempty"`
	DiffTool   string `json:"diffTool,omitempty"`
	MergeTool  string `json:"mergeTool,omitempty"`
	// CustomDiff and CustomMerge are the programs the Custom choice of the
	// External Diff Tool and Merge Tool pop-ups runs.
	CustomDiff   ToolSetup `json:"customDiff,omitempty"`
	CustomMerge  ToolSetup `json:"customMerge,omitempty"`
	DiffFont     string    `json:"diffFont,omitempty"`
	DiffFontSize float64   `json:"diffFontSize,omitempty"`
	// DiffTabWidth is how many columns a tab expands to in the diff viewer:
	// 0 keeps the framework's own default of 4, and 2, 4 or 8 are the
	// choices the settings offer. Anything else reads back as 0.
	DiffTabWidth  int      `json:"diffTabWidth,omitempty"`
	SourceFolders []string `json:"sourceFolders,omitempty"`
	// SSHKeyFiles are keys opened from outside ~/.ssh, kept in the list
	// when they are not active.
	SSHKeyFiles []string `json:"sshKeyFiles,omitempty"`
	// SSHKeys are the private key files marked active in the SSH Keys
	// settings; git and ssh offer them in every repository.
	SSHKeys         []string `json:"sshKeys,omitempty"`
	GitHubAvatars   bool     `json:"githubAvatars,omitempty"`
	CommitPrompt    string   `json:"commitPrompt,omitempty"`
	ReviewPrompt    string   `json:"reviewPrompt,omitempty"`
	HandleGitHub    bool     `json:"handleGitHub,omitempty"`
	HandleBitbucket bool     `json:"handleBitbucket,omitempty"`
	UpdateFeed      string   `json:"updateFeed,omitempty"`
	// SupportEmail is where Contact Support writes to; empty uses the
	// issue tracker of the project.
	SupportEmail    string          `json:"supportEmail,omitempty"`
	LastUpdateCheck string          `json:"lastUpdateCheck,omitempty"`
	Commands        []CustomCommand `json:"commands,omitempty"`
	// MergeTools are merge tools defined here, run as a command with
	// $BASE, $LOCAL, $REMOTE and $MERGED filled in.
	MergeTools []MergeTool `json:"mergeTools,omitempty"`
	// NoIssueLinks turns off links to a bug tracker in commit messages.
	NoIssueLinks bool `json:"noIssueLinks,omitempty"`
	// DisableStatusUpdate turns the automatic status refresh off;
	// StatusUpdateSeconds is its interval. Zero seconds means no interval
	// was ever set.
	DisableStatusUpdate bool `json:"disableStatusUpdate,omitempty"`
	StatusUpdateSeconds int  `json:"statusUpdateSeconds,omitempty"`
	// AutoCheckUpdates and UpdateInterval ("hourly", "daily", "weekly")
	// control checking the update feed on launch; UpdateChannel is
	// "stable" or "beta".
	AutoCheckUpdates    bool   `json:"autoCheckUpdates,omitempty"`
	AutoDownloadUpdates bool   `json:"autoDownloadUpdates,omitempty"`
	UpdateInterval      string `json:"updateInterval,omitempty"`
	UpdateChannel       string `json:"updateChannel,omitempty"`
	// NoSyntaxHighlight shows diffs and files without syntax colors.
	NoSyntaxHighlight bool `json:"noSyntaxHighlight,omitempty"`
	// ScanDepth is the repository scan depth chosen in the Rescan sheet.
	// Zero means the sheet has not saved one: a fresh store receives the
	// registered depth of 6, while an older store keeps the historical
	// fallback of 4.
	ScanDepth int `json:"scanDepth,omitempty"`
}

// ToolSetup is a program and its arguments, with $LOCAL, $REMOTE, $BASE and
// $MERGED in them.
type ToolSetup struct {
	Path string `json:"path,omitempty"`
	Args string `json:"args,omitempty"`
}

// MergeTool is a custom merge tool: Name is what Settings and git call it,
// Command is run through the shell with $BASE, $LOCAL, $REMOTE and $MERGED.
type MergeTool struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

// UnmarshalJSON distinguishes an explicit 0 subject limit in JSON from a
// legacy or fresh store that omitted the field.
func (s *Settings) UnmarshalJSON(data []byte) error {
	type rawSettings Settings
	var raw struct {
		rawSettings
		SubjectLow  *int `json:"subjectLow"`
		SubjectHigh *int `json:"subjectHigh"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = Settings(raw.rawSettings)
	if raw.SubjectLow != nil {
		s.SubjectLow = *raw.SubjectLow
		if *raw.SubjectLow == 0 {
			s.SubjectLowSaved = true
		}
	}
	if raw.SubjectHigh != nil {
		s.SubjectHigh = *raw.SubjectHigh
		if *raw.SubjectHigh == 0 {
			s.SubjectHighSaved = true
		}
	}
	return nil
}

func settingsUnset(v Settings) bool {
	return v.LocalBranchSort == "" && v.RemoteBranchSort == "" && v.TagSort == "" && !v.AutoFetch && v.FetchMinutes == 0 && !v.FetchTags && !v.UpdateSubmodules &&
		!v.PushAfterCommit && !v.IgnoreSpace && !v.DiffChangesOnly && v.DiffContextLines == 0 && !v.DiffContextSaved && !v.DiffWrap && !v.DateOrder && v.BranchSpace == "" &&
		!v.RememberCheckout && !v.MarkDirtyTabs && !v.CompactLabels &&
		v.SubjectLow == 0 && !v.SubjectLowSaved && v.SubjectHigh == 0 && !v.SubjectHighSaved && v.PageGuide == 0 && !v.NoPageGuide && !v.ChangeMarks && !v.SpellCheck &&
		v.AuthorName == "" && v.AuthorEmail == "" && !v.SignedOffBy && v.GitPath == "" && v.EnvPath == "" && !v.VerboseGit && !v.TraceGit && !v.TraceCurl && !v.TraceSSH && !v.TracePack && !v.TracePerf && v.Terminal == "" &&
		v.DiffTool == "" && v.MergeTool == "" && v.CustomDiff == (ToolSetup{}) && v.CustomMerge == (ToolSetup{}) && v.DiffFont == "" && v.DiffFontSize == 0 && v.DiffTabWidth == 0 && len(v.SourceFolders) == 0 && len(v.SSHKeys) == 0 && len(v.SSHKeyFiles) == 0 &&
		!v.GitHubAvatars && !v.HandleGitHub && !v.HandleBitbucket && v.CommitPrompt == "" && v.ReviewPrompt == "" && v.UpdateFeed == "" && v.SupportEmail == "" && v.LastUpdateCheck == "" && len(v.Commands) == 0 &&
		len(v.MergeTools) == 0 && !v.NoIssueLinks && !v.DisableStatusUpdate && v.StatusUpdateSeconds == 0 && !v.AutoCheckUpdates && !v.AutoDownloadUpdates && v.UpdateInterval == "" && v.UpdateChannel == "" && !v.NoSyntaxHighlight && v.ScanDepth == 0
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsLocked()
}

func (s *Store) settingsLocked() Settings {
	out := cloneSettings(s.data.Settings)
	if !validDiffTabWidth(out.DiffTabWidth) {
		out.DiffTabWidth = 0
	}
	if out.VerboseGit {
		out.VerboseGit = false
		out.TraceGit, out.TraceCurl, out.TraceSSH, out.TracePack, out.TracePerf = true, true, true, true, true
	}
	// Decide whether settings are absent before filling numeric defaults.
	// Otherwise FetchMinutes=10 makes settingsUnset false on a fresh store.
	if !s.data.SettingsSaved && settingsUnset(out) {
		out.RememberCheckout = true
		out.MarkDirtyTabs = true
		out.CompactLabels = true
		out.SpellCheck = true
		out.UpdateSubmodules = true
		out.AutoFetch = true
		out.LocalBranchSort = "backward"
		out.RemoteBranchSort = "alpha-folders"
		out.ScanDepth = 6
	}
	if out.FetchMinutes <= 0 {
		out.FetchMinutes = 10
	}
	if out.StatusUpdateSeconds == 0 {
		out.StatusUpdateSeconds = 60
	}
	if out.DiffTool == "" {
		out.DiffTool = "FileMerge"
	}
	if out.MergeTool == "" {
		out.MergeTool = "FileMerge"
	}
	if out.SubjectLow < 0 {
		out.SubjectLow, out.SubjectLowSaved = 50, false
	} else if out.SubjectLow == 0 && !out.SubjectLowSaved {
		out.SubjectLow = 50
	}
	if out.SubjectHigh < 0 {
		out.SubjectHigh, out.SubjectHighSaved = 70, false
	} else if out.SubjectHigh == 0 && !out.SubjectHighSaved {
		out.SubjectHigh = 70
	}
	if out.PageGuide <= 0 {
		out.PageGuide = 72
	}
	if out.BranchSpace == "" {
		out.BranchSpace = "-"
	}
	if out.BranchSpace == "keep" {
		out.BranchSpace = ""
	}
	return out
}

// Trace is the Git traces the settings turn on.
func (s Settings) Trace() git.Trace {
	return git.Trace{Git: s.TraceGit, Curl: s.TraceCurl, SSH: s.TraceSSH, Pack: s.TracePack, Perf: s.TracePerf}
}

func (s *Store) SetSettings(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, saved := s.data.Settings, s.data.SettingsSaved
	s.data.Settings = cloneSettings(v)
	s.data.SettingsSaved = true
	if err := s.save(); err != nil {
		s.data.Settings, s.data.SettingsSaved = previous, saved
		return err
	}
	return nil
}

// SetDiffFont saves only the font, leaving other Settings drafts untouched.
func (s *Store) SetDiffFont(font string, size float64) error {
	if size <= 0 || math.IsNaN(size) || math.IsInf(size, 0) {
		return fmt.Errorf("font size must be a finite positive number")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, saved := s.data.Settings, s.data.SettingsSaved
	if !saved && settingsUnset(previous) {
		s.data.Settings = s.settingsLocked()
	}
	s.data.Settings.DiffFont = strings.TrimSpace(font)
	s.data.Settings.DiffFontSize = size
	s.data.SettingsSaved = true
	if err := s.save(); err != nil {
		s.data.Settings, s.data.SettingsSaved = previous, saved
		return err
	}
	return nil
}

// validDiffTabWidth reports whether width is one of the diff viewer's tab
// widths: 0 keeps the framework default, and 2, 4 and 8 are the choices.
func validDiffTabWidth(width int) bool {
	return width == 0 || width == 2 || width == 4 || width == 8
}

// SetDiffTabWidth saves only the diff viewer's tab width, leaving other
// Settings drafts untouched. Widths outside the choices are rejected without
// changing the store, and the store rolls its own failed write back.
func (s *Store) SetDiffTabWidth(width int) error {
	if !validDiffTabWidth(width) {
		return fmt.Errorf("diff tab width must be 0, 2, 4 or 8, not %d", width)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, saved := s.data.Settings, s.data.SettingsSaved
	if !saved && settingsUnset(previous) {
		s.data.Settings = s.settingsLocked()
	}
	s.data.Settings.DiffTabWidth = width
	s.data.SettingsSaved = true
	if err := s.save(); err != nil {
		s.data.Settings, s.data.SettingsSaved = previous, saved
		return err
	}
	return nil
}

// Appearance returns the saved appearance override: "system", "light" or
// "dark". A store that has never saved one, or holds a value written by a
// version that offered other choices, follows the system.
func (s *Store) Appearance() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.data.Appearance {
	case "light", "dark":
		return s.data.Appearance
	default:
		return "system"
	}
}

func (s *Store) SetAppearance(appearance string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.Appearance
	s.data.Appearance = appearance
	if err := s.save(); err != nil {
		s.data.Appearance = previous
		return err
	}
	return nil
}

// Path returns the JSON file this store writes, or an empty path for an in-memory store.
func (s *Store) Path() string { return s.path }

// Open reads the store saved at path, or starts an empty one.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return s, fmt.Errorf("%s: %v", path, err)
	}
	return s, nil
}

// Repos returns the repositories, the last opened first.
func (s *Store) Repos() []Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reposSortedLocked()
}

// reposSortedLocked copies the repositories sorted by the last opened
// first. The caller holds s.mu.
func (s *Store) reposSortedLocked() []Repo {
	list := make([]Repo, len(s.data.Repos))
	for i, r := range s.data.Repos {
		list[i] = cloneRepo(r)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Opened.After(list[j].Opened) })
	return list
}

// ReposWithRev returns the repositories as Repos does, with the revision
// they were copied at in the same lock: a reader comparing a later
// ReposRev against it knows whether the copy can still be used.
func (s *Store) ReposWithRev() ([]Repo, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reposSortedLocked(), s.reposRev
}

// ReposRev returns the revision of the repository list, which every save
// advances. Reading it copies and sorts nothing, so a view can ask every
// frame and copy the list only when the revision moved.
func (s *Store) ReposRev() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reposRev
}

// Get returns the repository with id.
func (s *Store) Get(id string) (Repo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.data.Repos {
		if r.ID == id {
			return cloneRepo(r), true
		}
	}
	return Repo{}, false
}

// Add adds r, or returns the repository it is already as, which it
// updates with r's name and identity. Either is marked opened now.
func (s *Store) Add(r Repo) (Repo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Name == "" {
		r.Name = DefaultName(r.Path)
	}
	r.Workspace = cleanWorkspace(r.Workspace)
	r.Opened = time.Now()
	r = cloneRepo(r)
	for i, o := range s.data.Repos {
		if o.Same(r) {
			r.ID = o.ID
			r.Local = o.Local
			if r.Workspace == "" {
				r.Workspace = o.Workspace
			}
			s.data.Repos[i] = r
			if err := s.save(); err != nil {
				s.data.Repos[i] = o
				return Repo{}, err
			}
			return cloneRepo(r), nil
		}
	}
	r.ID = newID()
	previous := s.data.Repos
	s.data.Repos = append(s.data.Repos, r)
	if err := s.save(); err != nil {
		s.data.Repos = previous
		return Repo{}, err
	}
	return cloneRepo(r), nil
}

// Touch marks a repository opened now.
func (s *Store) Touch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Repos {
		if s.data.Repos[i].ID == id {
			previous := s.data.Repos[i].Opened
			s.data.Repos[i].Opened = time.Now()
			if err := s.save(); err != nil {
				s.data.Repos[i].Opened = previous
				return err
			}
			return nil
		}
	}
	return s.save()
}

// Remove forgets a repository; its files stay.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(id, false)
}

func (s *Store) removeLocked(id string, recordDeleted bool) error {
	previousRepos := append([]Repo(nil), s.data.Repos...)
	previousOpen := append([]string(nil), s.data.Open...)
	previousActive, previousSet := s.data.Active, s.data.ActiveSet
	previousDeleted := append([]string(nil), s.data.Deleted...)
	var kept []Repo
	for _, r := range s.data.Repos {
		if r.ID == id {
			if recordDeleted && r.Kind == Local {
				clean := filepath.Clean(r.Path)
				if !slices.Contains(s.data.Deleted, clean) {
					s.data.Deleted = append(s.data.Deleted, clean)
				}
			}
			continue
		}
		kept = append(kept, r)
	}
	s.data.Repos = kept
	var open []string
	for _, o := range s.data.Open {
		if o != id {
			open = append(open, o)
		}
	}
	s.data.Open = open
	if s.data.Active == id {
		s.data.Active, s.data.ActiveSet = "", true
	}
	if err := s.save(); err != nil {
		s.data.Repos, s.data.Open = previousRepos, previousOpen
		s.data.Active, s.data.ActiveSet = previousActive, previousSet
		s.data.Deleted = previousDeleted
		return err
	}
	return nil
}

// SetWorkspace names the group a repository appears in. An empty name is ungrouped.
func (s *Store) SetWorkspace(id, workspace string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace = cleanWorkspace(workspace)
	for i := range s.data.Repos {
		if s.data.Repos[i].ID == id {
			previous := s.data.Repos[i].Workspace
			s.data.Repos[i].Workspace = workspace
			if err := s.save(); err != nil {
				s.data.Repos[i].Workspace = previous
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("unknown repository %s", id)
}

// UpdateLocal saves the settings of one repository.
func (s *Store) UpdateLocal(id string, local RepoSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Repos {
		if s.data.Repos[i].ID == id {
			previous := s.data.Repos[i].Local
			s.data.Repos[i].Local = cloneRepoSettings(local)
			if err := s.save(); err != nil {
				s.data.Repos[i].Local = previous
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("unknown repository %s", id)
}

// OpenTabs returns the IDs of the repositories left open in tabs.
func (s *Store) OpenTabs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.data.Open...)
}

// ActiveTab returns the repository left shown. set is false for a store
// saved before the choice was remembered.
func (s *Store) ActiveTab() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Active, s.data.ActiveSet
}

// Accounts returns the hosting accounts, in the order they were saved.
func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Account(nil), s.data.Accounts...)
}

// SaveAccounts replaces the hosting accounts.
func (s *Store) SaveAccounts(list []Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]Account(nil), s.data.Accounts...)
	s.data.Accounts = append([]Account(nil), list...)
	if err := s.save(); err != nil {
		s.data.Accounts = previous
		return err
	}
	return nil
}

// SetOpenTabs saves the IDs of the repositories open in tabs.
func (s *Store) SetOpenTabs(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := append([]string(nil), s.data.Open...)
	s.data.Open = append([]string(nil), ids...)
	if err := s.save(); err != nil {
		s.data.Open = previous
		return err
	}
	return nil
}

// SetTabSession saves the open repositories and the one shown. An empty
// active with set saves the start page; without set it leaves the saved
// choice as it was.
func (s *Store) SetTabSession(ids []string, active string, set bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previousOpen := append([]string(nil), s.data.Open...)
	previousActive, previousSet := s.data.Active, s.data.ActiveSet
	s.data.Open = append([]string(nil), ids...)
	if set {
		s.data.Active, s.data.ActiveSet = active, true
	}
	if err := s.save(); err != nil {
		s.data.Open = previousOpen
		s.data.Active, s.data.ActiveSet = previousActive, previousSet
		return err
	}
	return nil
}

// save writes the store, advancing the revision of the repository list in
// the same lock the caller made its edits in, so a failed write rolled back
// still leaves the revision ahead of any copy a caching reader took.
func (s *Store) save() error {
	s.reposRev++
	if s.path == "" {
		return nil // in memory, as in tests
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AutoFetches reports whether automatic fetching includes the remote.
func (l RepoSettings) AutoFetches(remote string) bool {
	for _, r := range l.NoAutoFetch {
		if r == remote {
			return false
		}
	}
	return true
}

// WithAutoFetch returns the settings with the remote included in, or left
// out of, automatic fetching.
func (l RepoSettings) WithAutoFetch(remote string, on bool) RepoSettings {
	var rest []string
	for _, r := range l.NoAutoFetch {
		if r != remote {
			rest = append(rest, r)
		}
	}
	if !on {
		rest = append(rest, remote)
	}
	l.NoAutoFetch = rest
	return l
}
