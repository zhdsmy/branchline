package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
)

// The labels of the Git Instance pop-up: the default git of PATH, every
// detected instance as "<path> (<version>)", and the custom choice that
// reveals the path input.
const (
	gitInstanceDefault = "Default (git in PATH)"
	gitInstanceCustom  = "Custom…"
)

// envPathChoices are the labels of the ENV PATH pop-up in the order of the
// settings values "", "parent" and "shell".
var envPathChoices = []string{"Process + Homebrew", "Process PATH", "Login shell"}

// traceLabels are the five verbose Git output switches the master switch
// reveals, in the order of the trace settings.
var traceLabels = []string{"GIT_TRACE", "GIT_TRACE_CURL", "GIT_SSH_COMMAND", "GIT_TRACE_PACKFILE", "GIT_TRACE_PERFORMANCE"}

// settingsCommitGitState holds the drafts and the asynchronous load and save
// bookkeeping of the Commit and Git settings pages. Every field is owned by
// the UI thread; background workers receive captured values only and publish
// through post.
type settingsCommitGitState struct {
	// Commit page drafts, as text: the inputs edit them directly.
	subjectLow, subjectHigh string
	pageGuideOn             bool
	pageGuide               string
	signedOffBy             bool
	commitErr               string

	// Git page drafts. instanceChoice is the pop-up's label, customPath the
	// input of the custom choice, envChoice the ENV PATH label, and verbose
	// the master switch whose turning off clears the five traces.
	instanceChoice string
	choiceTouched  bool
	customPath     string
	envChoice      string
	verbose        bool
	traces         [5]bool

	// instances are the git programs found on this Mac, loaded in the
	// background; nil until the first load completes.
	instances []git.Instance

	// The global identity: loading disables the inputs, a read failure shows
	// the error with a retry and keeps them disabled, and expectedIdentity is
	// the identity the read produced — the write refuses to overwrite a
	// configuration that changed underneath it.
	identityLoading  bool
	identityLoaded   bool
	identityReadErr  error
	name, email      string
	expectedIdentity git.Identity

	// loadGen and saveGen discard stale completions: a page re-entry or a new
	// save supersedes whatever is still in flight.
	loadGen uint64
	saveGen uint64
	saving  bool
	saveErr string
}

// commitGitState returns the app's page state. The pages register through
// registerSettingsPage, so their state cannot live on settingsState; each
// state is owned by its app's UI thread.
func commitGitState(a *app) *settingsCommitGitState {
	return featureState(a, func() *settingsCommitGitState { return &settingsCommitGitState{} })
}

func init() {
	registerSettingsPage(settingsPageDef{
		id:     "commit",
		title:  "Commit",
		order:  20,
		onOpen: (*app).primeCommitSettings,
		view:   (*app).settingsCommitPage,
	})
	registerSettingsPage(settingsPageDef{
		id:     "git",
		title:  "Git",
		order:  30,
		onOpen: (*app).primeGitSettings,
		view:   (*app).settingsGitPage,
	})
}

// parseSettingsInt parses one numeric input of the Commit page.
func parseSettingsInt(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", trimmed)
	}
	return n, nil
}

// primeCommitSettings loads the saved commit message rules into the page's
// drafts: the inputs are plain text, so the numbers read in their decimal
// form, an explicit 0 — the off switch of a limit — included.
func (a *app) primeCommitSettings() {
	st := commitGitState(a)
	prefs := a.store.Settings()
	st.subjectLow = strconv.Itoa(prefs.SubjectLow)
	st.subjectHigh = strconv.Itoa(prefs.SubjectHigh)
	st.pageGuideOn = !prefs.NoPageGuide
	st.pageGuide = strconv.Itoa(prefs.PageGuide)
	st.signedOffBy = prefs.SignedOffBy
	st.commitErr = ""
}

// saveCommitSettings validates and persists the Commit page's drafts. A
// rejected input or a failed write shows the raw error and keeps the drafts
// for editing; the store is never half-written, as saveCommitPrefs validates
// before it writes and the store rolls its own failed write back. A saved
// page repaints the main window so the commit box's warning follows the new
// rules at once.
func (a *app) saveCommitSettings() {
	st := commitGitState(a)
	low, err := parseSettingsInt(st.subjectLow)
	if err != nil {
		st.commitErr = "Subject soft limit: " + err.Error()
		return
	}
	high, err := parseSettingsInt(st.subjectHigh)
	if err != nil {
		st.commitErr = "Subject hard limit: " + err.Error()
		return
	}
	guide, err := parseSettingsInt(st.pageGuide)
	if err != nil {
		st.commitErr = "Page guide column: " + err.Error()
		return
	}
	if err := a.saveCommitPrefs(low, high, guide, !st.pageGuideOn, st.signedOffBy); err != nil {
		st.commitErr = err.Error()
		return
	}
	st.commitErr = ""
	a.redraw()
}

// settingsCommitPage renders the Commit page: the subject length limits, the
// page guide of the description, and whether commits sign off.
func (a *app) settingsCommitPage(c *ui.Context, p palette) {
	st := commitGitState(a)
	ui.Scroll(c.Key("settings-commit-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c.Key("settings-commit")).FillWidth().Gap(10).Padding(0, 0, 8, 0).Children(func() {
			ui.Form(c, func() {
				ui.Field(c, "Subject Soft Limit", func() {
					ui.TextInput(c.Key("commit-subject-low"), &st.subjectLow).Width(72).Label("Subject soft limit")
				})
				ui.Field(c, "Subject Hard Limit", func() {
					ui.TextInput(c.Key("commit-subject-high"), &st.subjectHigh).Width(72).Label("Subject hard limit")
				})
				ui.Field(c, "Page Guide", func() {
					ui.Row(c.Key("commit-page-guide-row")).FillWidth().Justify(ui.Start).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Checkbox(c.Key("commit-page-guide-on"), &st.pageGuideOn, "Page guide at column").Label("Page guide at column")
						ui.TextInput(c.Key("commit-page-guide"), &st.pageGuide).Width(72).Label("Page guide column").Disabled(!st.pageGuideOn)
					})
				})
				ui.Field(c, "Signed-off-by", func() {
					ui.Checkbox(c.Key("commit-signoff"), &st.signedOffBy, "Add Signed-off-by to commits").Label("Add Signed-off-by to commits")
				})
			})
			ui.Row(c.Key("commit-save-row")).FillWidth().Justify(ui.Start).Children(func() {
				if ui.PrimaryButton(c.Key("save-commit-settings"), "Save Commit Settings").Label("Save Commit Settings").Clicked() {
					a.saveCommitSettings()
				}
			})
			if st.commitErr != "" {
				ui.Text(c, st.commitErr).FontSize(11.5).TextColor(p.danger).MaxWidth(400)
			}
		})
	})
}

// gitInstanceLabel is how one detected git program reads in the pop-up.
func gitInstanceLabel(in git.Instance) string {
	return in.Path + " (" + in.Version + ")"
}

// instanceChoiceFor resolves a saved custom git path to its pop-up label: a
// detected instance reads by its label, anything else is the custom choice.
func instanceChoiceFor(gitPath string, instances []git.Instance) string {
	if strings.TrimSpace(gitPath) == "" {
		return gitInstanceDefault
	}
	for _, in := range instances {
		if in.Path == gitPath {
			return gitInstanceLabel(in)
		}
	}
	return gitInstanceCustom
}

// chosenGitPath resolves the pop-up's choice back to the git program it
// names: "" for the default, the detected path of an instance, or the custom
// input.
func (st *settingsCommitGitState) chosenGitPath() string {
	switch st.instanceChoice {
	case gitInstanceDefault:
		return ""
	case gitInstanceCustom:
		return strings.TrimSpace(st.customPath)
	}
	for _, in := range st.instances {
		if gitInstanceLabel(in) == st.instanceChoice {
			return in.Path
		}
	}
	return ""
}

func envPathChoiceFor(envPath string) string {
	switch envPath {
	case "parent":
		return envPathChoices[1]
	case "shell":
		return envPathChoices[2]
	}
	return envPathChoices[0]
}

func envPathForChoice(choice string) string {
	switch choice {
	case envPathChoices[1]:
		return "parent"
	case envPathChoices[2]:
		return "shell"
	}
	return ""
}

// chosenTrace reads the five trace switches: the master switch off means all
// of them off, as turning it off clears them in place.
func (st *settingsCommitGitState) chosenTrace() git.Trace {
	if !st.verbose {
		return git.Trace{}
	}
	return git.Trace{Git: st.traces[0], Curl: st.traces[1], SSH: st.traces[2], Pack: st.traces[3], Perf: st.traces[4]}
}

// instanceOptions are the pop-up's choices in order: the default, every
// detected instance, and the custom choice.
func (st *settingsCommitGitState) instanceOptions() []string {
	out := []string{gitInstanceDefault}
	for _, in := range st.instances {
		out = append(out, gitInstanceLabel(in))
	}
	return append(out, gitInstanceCustom)
}

// primeGitSettings loads the saved toolchain settings into the Git page's
// drafts and starts the background read of the global identity and the git
// programs of this Mac, so the view never runs git itself.
func (a *app) primeGitSettings() {
	st := commitGitState(a)
	prefs := a.store.Settings()
	st.customPath = prefs.GitPath
	st.choiceTouched = false
	st.instanceChoice = instanceChoiceFor(prefs.GitPath, st.instances)
	st.envChoice = envPathChoiceFor(prefs.EnvPath)
	trace := prefs.Trace()
	st.traces = [5]bool{trace.Git, trace.Curl, trace.SSH, trace.Pack, trace.Perf}
	st.verbose = trace.Git || trace.Curl || trace.SSH || trace.Pack || trace.Perf
	st.saveErr = ""
	st.saving = false
	st.saveGen++
	a.startGitSettingsLoad(st)
}

// startGitSettingsLoad reads the global identity and scans the git programs
// in the background: both spawn git, which must never run inside a view. The
// generation counter drops the completion of a superseded load, and the
// pop-up's choice re-resolves only while the user has not touched it.
func (a *app) startGitSettingsLoad(st *settingsCommitGitState) {
	st.loadGen++
	gen := st.loadGen
	st.identityLoading = true
	st.identityLoaded = false
	st.identityReadErr = nil
	prefs := a.store.Settings()
	gitPath := prefs.GitPath
	idRepo := a.localGitForSettings()
	a.spawn(func() {
		identity, readErr := readGlobalIdentityStrict(idRepo)
		instances := gitInstanceChoices(gitPath)
		a.post(func() {
			if st.loadGen != gen {
				return
			}
			st.identityLoading = false
			st.identityLoaded = true
			st.identityReadErr = readErr
			if readErr == nil {
				st.name = identity.Name
				st.email = identity.Email
				st.expectedIdentity = identity
			}
			st.instances = instances
			if !st.choiceTouched {
				st.instanceChoice = instanceChoiceFor(gitPath, instances)
			}
		})
	})
}

// readGlobalIdentityStrict reads the global author identity without mistaking
// a damaged or inaccessible configuration for an empty one. git answers a
// configuration file it cannot read — no permission, for instance — with the
// same exit code 1 as a key that was never set, plus a warning on standard
// error; reading that as an empty identity would let a later write replace
// the configuration the user cannot read. Here anything on standard error or
// any other failure means the state of the file is unknown and the read
// fails.
func readGlobalIdentityStrict(repo *git.Repo) (git.Identity, error) {
	var out git.Identity
	for _, kv := range []struct {
		key  string
		into *string
	}{{"user.name", &out.Name}, {"user.email", &out.Email}} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		stdout, err := repo.Exec.Run(ctx, nil, "config", "--global", "--get", kv.key)
		cancel()
		if err == nil {
			*kv.into = strings.TrimSpace(string(stdout))
			continue
		}
		var ge *git.Error
		if errors.As(err, &ge) && ge.ExitCode == 1 {
			if strings.TrimSpace(ge.Stderr) != "" {
				return git.Identity{}, fmt.Errorf("reading the global Git configuration: %s", strings.TrimSpace(ge.Stderr))
			}
			continue // the key is not set
		}
		return git.Identity{}, err
	}
	return out, nil
}

// saveGitSettings persists the Git page in layers, each failure stopping
// before the next layer and leaving everything it covers untouched:
//
//  1. validation runs in the background — checking a custom program spawns
//     it — and a rejected program or ENV PATH choice writes nothing;
//  2. the settings write back on the UI thread, the store rolling its own
//     failed write back, and a failure neither writes the global identity nor
//     replaces the executor of the open repository;
//  3. the persisted toolchain reaches the open repository at once through
//     RebuildExecutor;
//  4. the global identity — only when it was read cleanly and the draft
//     differs from what was read — writes in the background through the safe
//     write, which refuses to overwrite a configuration that failed to read
//     or changed underneath; a failure keeps the draft for a retry.
//
// The workers use only values captured on the UI thread, and the generation
// counter drops the completion of a superseded save.
func (a *app) saveGitSettings() {
	st := commitGitState(a)
	if st.saving {
		return
	}
	gitPath := st.chosenGitPath()
	envPath := envPathForChoice(st.envChoice)
	trace := st.chosenTrace()
	writeIdentity := false
	var next, expected git.Identity
	var identityReadErr error
	if st.identityLoaded && st.identityReadErr == nil {
		next = git.Identity{Name: strings.TrimSpace(st.name), Email: strings.TrimSpace(st.email)}
		expected = st.expectedIdentity
		identityReadErr = st.identityReadErr
		writeIdentity = next != expected
	}
	st.saving = true
	st.saveGen++
	gen := st.saveGen
	st.saveErr = ""

	a.spawn(func() {
		if err := validateGitSettings(gitPath, envPath); err != nil {
			a.post(func() {
				if st.saveGen != gen {
					return
				}
				st.saving = false
				st.saveErr = err.Error()
			})
			return
		}
		a.post(func() {
			if st.saveGen != gen {
				return
			}
			if err := a.saveGitToolchainSettings(gitPath, envPath, trace); err != nil {
				st.saving = false
				st.saveErr = err.Error()
				return
			}
			a.session.RebuildExecutor()
			a.redraw()
			if !writeIdentity {
				st.saving = false
				return
			}
			writeRepo := git.New(&git.LocalExecutor{Dir: os.TempDir(), Git: gitPath, Path: git.PathMode(envPath)})
			a.spawn(func() {
				err := writeRepo.SafeSetGlobalIdentity(next, expected, identityReadErr)
				a.post(func() {
					if st.saveGen != gen {
						return
					}
					st.saving = false
					if err != nil {
						st.saveErr = err.Error()
						return
					}
					st.saveErr = ""
					st.expectedIdentity = next
				})
			})
		})
	})
}

// settingsGitPage renders the Git page: the global author identity, the git
// program and PATH of this Mac, and the verbose trace switches.
func (a *app) settingsGitPage(c *ui.Context, p palette) {
	st := commitGitState(a)
	identityDisabled := st.identityLoading || st.identityReadErr != nil

	ui.Scroll(c.Key("settings-git-scroll")).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c.Key("settings-git")).FillWidth().Gap(10).Padding(0, 0, 8, 0).Children(func() {
			ui.Text(c, "Global User Information").FontSize(13).FontWeight(600).TextColor(p.text)

			if st.identityLoading {
				ui.Row(c.Key("git-identity-loading")).FillWidth().Justify(ui.Start).Role(ui.RoleStatus).Label("Global Identity Loading").Busy(true).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "Loading global Git identity…").FontSize(12).TextColor(p.muted).SingleLine()
				})
			} else if st.identityReadErr != nil {
				ui.Text(c, st.identityReadErr.Error()).FontSize(11.5).TextColor(p.danger).MaxWidth(400)
				ui.Row(c.Key("git-identity-retry-row")).FillWidth().Justify(ui.Start).Children(func() {
					if ui.Button(c.Key("retry-global-identity"), "Retry Global Identity").Label("Retry Global Identity").Clicked() {
						a.startGitSettingsLoad(st)
					}
				})
			}

			ui.Form(c, func() {
				ui.Field(c, "User Name", func() {
					ui.TextInput(c.Key("git-user-name"), &st.name).FillWidth().Label("Git user name").Disabled(identityDisabled)
				})
				ui.Field(c, "User Email", func() {
					ui.TextInput(c.Key("git-user-email"), &st.email).FillWidth().Label("Git user email").Disabled(identityDisabled)
				})
			})

			ui.Text(c, "Git Toolchain").FontSize(13).FontWeight(600).TextColor(p.text)
			ui.Form(c, func() {
				ui.Field(c, "Git Instance", func() {
					sel := ui.Select(c.Key("git-instance-select"), &st.instanceChoice, st.instanceOptions()).Label("Git Instance")
					if sel.Changed() {
						st.choiceTouched = true
					}
				})
				if st.instanceChoice == gitInstanceCustom {
					ui.Field(c, "Custom Git Binary", func() {
						ui.TextInput(c.Key("git-custom-path"), &st.customPath).FillWidth().Font("monospace").Placeholder("/opt/homebrew/bin/git").Label("Custom Git binary")
					})
				}
				ui.Field(c, "ENV PATH", func() {
					ui.Select(c.Key("git-envpath-select"), &st.envChoice, envPathChoices).Label("ENV PATH")
				})
			})
			ui.Text(c, "Only used for repositories on this Mac; SSH repositories keep using the git on the server.").FontSize(11.5).TextColor(p.muted).MaxWidth(400)

			ui.Form(c, func() {
				ui.Field(c, "Verbose Git Trace", func() {
					master := ui.Checkbox(c.Key("git-verbose-master"), &st.verbose, "Enable verbose Git output").Label("Enable verbose Git output")
					if master.Changed() && !st.verbose {
						for i := range st.traces {
							st.traces[i] = false
						}
					}
					if st.verbose {
						ui.Column(c.Key("git-trace-list")).Gap(4).Padding(4, 0, 0, 20).Children(func() {
							for i, label := range traceLabels {
								ui.Checkbox(c.Key("git-trace-"+label), &st.traces[i], label).Label(label)
							}
						})
					}
				})
			})

			ui.Row(c.Key("git-save-row")).FillWidth().Justify(ui.Start).Children(func() {
				if ui.PrimaryButton(c.Key("save-git-settings"), "Save Git Settings").Label("Save Git Settings").Disabled(st.saving).Clicked() {
					a.saveGitSettings()
				}
			})
			if st.saveErr != "" {
				ui.Text(c, st.saveErr).FontSize(11.5).TextColor(p.danger).MaxWidth(400)
			}
		})
	})
}
