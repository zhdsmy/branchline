package git

import (
	"fmt"
	"strings"
)

// FlowConfig is the git-flow branch model stored in the repository.
type FlowConfig struct {
	Main, Develop            string
	Feature, Release, Hotfix string
	Tag                      string
}

func (c FlowConfig) withDefaults() FlowConfig {
	if c.Main == "" {
		c.Main = "main"
	}
	if c.Develop == "" {
		c.Develop = "develop"
	}
	if c.Feature == "" {
		c.Feature = "feature/"
	}
	if c.Release == "" {
		c.Release = "release/"
	}
	if c.Hotfix == "" {
		c.Hotfix = "hotfix/"
	}
	return c
}

// FlowInit writes the git-flow config and creates the develop branch.
func (r *Repo) FlowInit(c FlowConfig) error {
	c = c.withDefaults()
	pairs := [][2]string{
		{"gitflow.branch.master", c.Main},
		{"gitflow.branch.develop", c.Develop},
		{"gitflow.prefix.feature", c.Feature},
		{"gitflow.prefix.release", c.Release},
		{"gitflow.prefix.hotfix", c.Hotfix},
	}
	for _, kv := range pairs {
		if _, err := r.run(quick, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if c.Tag == "" {
		_, _ = r.run(quick, "config", "--unset", "gitflow.prefix.versiontag")
	} else if _, err := r.run(quick, "config", "gitflow.prefix.versiontag", c.Tag); err != nil {
		return err
	}
	if _, err := r.run(quick, "rev-parse", "--verify", "--quiet", "refs/heads/"+c.Develop); err != nil {
		return r.CreateBranch(c.Develop, c.Main, false)
	}
	return nil
}

// FlowFinishOptions are the choices of the finish dialog.
type FlowFinishOptions struct {
	// Kind is feature, release, or hotfix.
	Kind string
	// Branch is the full name, such as feature/payments.
	Branch  string
	Main    string
	Develop string
	// Tag is created on the production branch for a release or hotfix.
	// Empty skips the tag. Message makes it annotated.
	Tag, Message string
	// Rebase rebases a feature onto develop before merging it.
	Rebase bool
	// NoFF records a merge commit for a feature. Release and hotfix
	// always record one.
	NoFF bool
	// Delete removes the finished branch after a clean finish.
	Delete bool
	// BackMerge merges the production branch into develop. A feature
	// ignores it. Hotfixes typically back-merge into develop.
	BackMerge bool
}

// FlowFinish merges a git-flow branch. A conflict stops the finish and
// leaves Git's operation in progress, with the branch still there.
// Local and SSH repositories use the same Git commands.
func (r *Repo) FlowFinish(o FlowFinishOptions) error {
	branch := strings.TrimSpace(o.Branch)
	mainName := strings.TrimSpace(o.Main)
	develop := strings.TrimSpace(o.Develop)
	if branch == "" {
		return fmt.Errorf("finish needs a branch")
	}
	switch o.Kind {
	case "feature":
		if develop == "" {
			return fmt.Errorf("finish needs a develop branch")
		}
		if o.Rebase {
			if err := r.Checkout(branch); err != nil {
				return err
			}
			if err := r.Rebase(develop); err != nil {
				return err
			}
			if r.state() != "" {
				return fmt.Errorf("rebase stopped")
			}
		}
		if err := r.Checkout(develop); err != nil {
			return err
		}
		style := MergeFastForward
		if o.NoFF {
			style = MergeNoFastForward
		}
		if err := r.MergeWith(branch, style); err != nil {
			return err
		}
		if r.state() != "" {
			return fmt.Errorf("merge stopped")
		}
		if o.Delete {
			return r.DeleteBranch(branch, false)
		}
		return nil
	case "release", "hotfix":
		if mainName == "" {
			return fmt.Errorf("finish needs a production branch")
		}
		if err := r.Checkout(mainName); err != nil {
			return err
		}
		if err := r.MergeWith(branch, MergeNoFastForward); err != nil {
			return err
		}
		if r.state() != "" {
			return fmt.Errorf("merge stopped")
		}
		if tag := strings.TrimSpace(o.Tag); tag != "" {
			if err := r.CreateTag(tag, "HEAD", strings.TrimSpace(o.Message)); err != nil {
				return err
			}
		}
		if o.BackMerge {
			if develop == "" {
				return fmt.Errorf("back-merge needs a develop branch")
			}
			if err := r.Checkout(develop); err != nil {
				return err
			}
			if err := r.MergeWith(mainName, MergeNoFastForward); err != nil {
				return err
			}
			if r.state() != "" {
				return fmt.Errorf("merge stopped")
			}
		}
		if o.Delete {
			return r.DeleteBranch(branch, false)
		}
		return nil
	default:
		return fmt.Errorf("unknown flow branch %s", o.Kind)
	}
}

// FlowDeinit removes the git-flow config. The branches stay.
func (r *Repo) FlowDeinit() error {
	for _, key := range []string{
		"gitflow.branch.master", "gitflow.branch.develop", "gitflow.prefix.feature",
		"gitflow.prefix.release", "gitflow.prefix.hotfix", "gitflow.prefix.versiontag",
	} {
		if _, err := r.run(quick, "config", "--unset", key); err != nil && ExitCode(err) != 5 {
			return err
		}
	}
	return nil
}

// FlowConfig reads the git-flow config. Missing keys are empty.
func (r *Repo) FlowConfig() (FlowConfig, error) {
	get := func(key string) string {
		out, err := r.run(quick, "config", "--get", key)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return FlowConfig{
		Main: get("gitflow.branch.master"), Develop: get("gitflow.branch.develop"),
		Feature: get("gitflow.prefix.feature"), Release: get("gitflow.prefix.release"),
		Hotfix: get("gitflow.prefix.hotfix"), Tag: get("gitflow.prefix.versiontag"),
	}, nil
}
