// Remote file editing of SSH repositories on the Local Changes page: Open
// for Editing downloads one file of the server's working tree into a private
// copy on this machine and opens it in the default application, Save to
// Server writes the edited copy back only while the server file is still
// what was downloaded, and Discard Local Copy removes the copy. A file with
// a copy shows an "Edited Locally" badge on its row. The copies are
// remembered by repository and path for as long as the app runs, surviving
// repository switches; nothing is written back without an explicit Save.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/zhdsmy/branchline/internal/git"
	"github.com/zhdsmy/branchline/internal/store"
)

// fallbackPencil is the geometric fallback of the Edited Locally badge icon,
// a small diagonal pencil, kept here so ui_kit.go stays limited to shared
// components.
var fallbackPencil = mustSVG(`<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 3.5l3 3L6 13H3v-3l6.5-6.5z"/><path d="M8.5 4.5l3 3"/></svg>`)

// remoteEditKey identifies one remembered copy: the repository it belongs
// to and the file's slash-separated path inside it. The key is the
// repository's store ID, so it survives switches away and back within the
// run; nothing is remembered across launches.
type remoteEditKey struct {
	repoID string
	path   string
}

// remoteEditKeyFor keys the copies of one repository. A repository that was
// never persisted has no store ID, and two of them would share the empty key
// and each other's copies, so they key by their address instead.
func remoteEditKeyFor(meta store.Repo, path string) remoteEditKey {
	id := meta.ID
	if id == "" {
		id = meta.Address()
	}
	return remoteEditKey{repoID: id, path: path}
}

// remoteEditEntry is the state of one remote-edited file: the copy itself
// once downloaded, the SHA-256 of its content as first downloaded (the
// baseline Discard Local Copy compares edits against), and the in-flight
// flags rejecting concurrent downloads and discards of the same file. Saves
// need no flag of their own: they are writes of the session, whose busy
// state rejects the re-entry for exactly the write's duration.
type remoteEditEntry struct {
	copy         git.RemoteCopy
	hasCopy      bool
	downloadHash string
	downloading  bool
	discarding   bool
}

// remoteEditState is the per-app state of remote file editing. All fields
// are owned by the UI thread; the injected seams (openPath, showInFolder,
// cacheDir) survive every reset because they never reset: tests install
// them once and capture every open, reveal and cache placement instead of
// ever launching an editor, a Finder window, or touching the real cache.
// copyKey and conflictPath remember which entry the discard confirmation
// and the server-changed notice belong to.
type remoteEditState struct {
	entries map[remoteEditKey]*remoteEditEntry

	swept bool

	openPath     func(string) error
	showInFolder func(string)
	cacheDir     func() (string, error)

	confirmingDiscard bool
	copyKey           remoteEditKey

	conflictOpen bool
	conflictPath string
}

// remoteEditUI returns the app's remote editing state, creating it on first
// use — the first time anything of remote editing is used — and starting the
// cache sweep exactly then, once: edit-* directories older than seven days
// under the cache directory are removed in the background. Only directories
// inside the resolved cache directory are ever considered.
func remoteEditUI(a *app) *remoteEditState {
	st := featureState(a, func() *remoteEditState {
		return &remoteEditState{entries: map[remoteEditKey]*remoteEditEntry{}}
	})
	if !st.swept {
		st.swept = true
		a.spawn(sweepRemoteEditCache(st.cacheDir))
	}
	return st
}

// setRemoteEditOpener injects how copies open and reveal for the app, nil
// restoring the production defaults of mygo.Shell.OpenPath and
// mygo.Shell.ShowItemInFolder. Tests inject captures that never launch an
// editor or the file manager.
func setRemoteEditOpener(a *app, open func(string) error, reveal func(string)) {
	st := featureState(a, func() *remoteEditState {
		return &remoteEditState{entries: map[remoteEditKey]*remoteEditEntry{}}
	})
	st.openPath = open
	st.showInFolder = reveal
}

// setRemoteEditCacheDir injects where copies are cached for the app, nil
// restoring the production default of the remote-edit directory inside
// mygo.App.Path(mygo.PathCache). Tests inject t.TempDir so nothing of theirs
// lands in the user's cache.
func setRemoteEditCacheDir(a *app, dir func() (string, error)) {
	featureState(a, func() *remoteEditState {
		return &remoteEditState{entries: map[remoteEditKey]*remoteEditEntry{}}
	}).cacheDir = dir
}

// remoteEditCacheDir resolves where copies download to: the injected
// directory in tests, or the private remote-edit directory inside the
// application's cache directory in production.
func remoteEditCacheDir(injected func() (string, error)) (string, error) {
	if injected != nil {
		return injected()
	}
	base, err := mygo.App.Path(mygo.PathCache)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "remote-edit"), nil
}

// sweepRemoteEditCache removes edit-* directories of the cache directory
// whose modification time is older than seven days. Every error is dropped:
// the sweep is housekeeping of a cache, never worth an error in the UI, and
// a directory it cannot read or remove is left exactly as it is.
func sweepRemoteEditCache(cacheDirFn func() (string, error)) func() {
	return func() {
		dir, err := remoteEditCacheDir(cacheDirFn)
		if err != nil {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "edit-") {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// openRemoteEditCopy hands the copy's local path to the default application:
// the injected opener in tests, mygo.Shell.OpenPath otherwise. OpenPath is
// safe to call on the UI thread: mygo's onMain runs the body inline when it
// is already the main thread, which the UI thread is, and opening a file is
// a fast launch request to the workspace. A failure shows as the dismissible
// operation error.
func (a *app) openRemoteEditCopy(local string) {
	st := remoteEditUI(a)
	var err error
	if st.openPath != nil {
		err = st.openPath(local)
	} else {
		err = mygo.Shell.OpenPath(local)
	}
	if err != nil {
		a.session.ClearActionErr()
		syncStateFor(a).syncErr = git.RedactText(err.Error())
	}
}

// showRemoteEditCopy reveals the copy in the file manager through the
// injected seam in tests or mygo.Shell.ShowItemInFolder in production, which
// is safe on the UI thread for the same reason OpenPath is.
func (a *app) showRemoteEditCopy(local string) {
	st := remoteEditUI(a)
	if st.showInFolder != nil {
		st.showInFolder(local)
		return
	}
	mygo.Shell.ShowItemInFolder(local)
}

// remoteEditMenuItems appends the remote editing entries to the context menu
// of a Local Changes file row, in SSH repositories only. Open for Editing is
// offered for files present in the working tree — never for deleted or
// conflicted ones — and stays disabled while its download is in flight, so a
// repeated choice cannot start a second download. Save to Server and Discard
// Local Copy exist only for a file with a copy, disabled while a write of
// the repository or an operation on the copy itself is in flight.
func (a *app) remoteEditMenuItems(m *ui.Menu, f git.FileChange) {
	if a.session.Meta().Kind != store.SSH {
		return
	}
	st := remoteEditUI(a)
	key := remoteEditKeyFor(a.session.Meta(), f.Path)
	entry := st.entries[key]
	hasCopy := entry != nil && entry.hasCopy
	openOK := f.Status != 'D' && f.Status != 'U'
	if !openOK && !hasCopy {
		return
	}
	m.Separator()
	if openOK {
		downloading := entry != nil && entry.downloading
		if m.Item("Open for Editing").ID("remote-edit-open").Disabled(downloading).Chosen() {
			a.openRemoteEdit(st, key, entry)
		}
	}
	if hasCopy {
		// Save to Server is a write of the session: while any write is in
		// flight the item is disabled. Discarding the copy underneath a save
		// that is still reading it would corrupt the upload, so the discard
		// waits for the write too; the copy's own removal keeps its flag.
		if m.Item("Save to Server").ID("remote-edit-save").Disabled(a.session.Busy()).Chosen() {
			a.saveRemoteEdit(key, entry)
		}
		if m.Item("Discard Local Copy").ID("remote-edit-discard").Disabled(a.session.Busy() || entry.discarding).Chosen() {
			a.discardRemoteEdit(key, entry)
		}
	}
}

// openRemoteEdit starts editing path of the repository key belongs to. A
// file whose copy already exists opens that same copy again without
// downloading; otherwise the file downloads in the background and opens once
// the copy exists. The copy and the error are published keyed by repository
// rather than generation: switching away and back keeps both the entry and
// the in-flight download, but opening the editor or showing an error only
// happens while the repository is still the open one. The worker reads
// nothing of the app's state: the repository, cache directory and key were
// captured on the UI thread.
func (a *app) openRemoteEdit(st *remoteEditState, key remoteEditKey, entry *remoteEditEntry) {
	if entry != nil && entry.hasCopy {
		a.openRemoteEditCopy(entry.copy.Local)
		return
	}
	if entry != nil && entry.downloading {
		return
	}
	if entry == nil {
		entry = &remoteEditEntry{}
		st.entries[key] = entry
	}
	entry.downloading = true

	repo, gen := a.syncRepo()
	if repo == nil {
		entry.downloading = false
		return
	}
	cacheDirFn := st.cacheDir
	path := key.path
	a.spawn(func() {
		cacheDir, err := remoteEditCacheDir(cacheDirFn)
		var copy git.RemoteCopy
		if err == nil {
			copy, err = repo.DownloadForEdit(context.Background(), path, cacheDir)
		}
		a.post(func() {
			entry.downloading = false
			if err != nil {
				if !dropIfStale(a, gen) {
					a.session.ClearActionErr()
					syncStateFor(a).syncErr = git.RedactText(err.Error())
				}
				if !entry.hasCopy {
					delete(st.entries, key)
				}
				return
			}
			entry.copy = copy
			entry.hasCopy = true
			// The baseline Discard Local Copy compares edits against: the
			// hash of what was downloaded, which DownloadForEdit verified
			// against the server's metadata before writing the copy.
			entry.downloadHash = copy.Meta.SHA256
			if !dropIfStale(a, gen) {
				a.openRemoteEditCopy(copy.Local)
			}
		})
	})
}

// saveRemoteEdit writes the copy back to the server through the session's
// ordinary write operation, exactly like the sync actions: the write takes
// the busy state for its duration, so no other write starts underneath it,
// the menu keeps the item disabled for exactly that time, and the session's
// own reload publishes what the write changed. On success the copy's
// metadata moves to what the server reported, so the next edit and save
// starts from the new baseline. A repository switch drops the completion
// with its generation: the write may still have landed, and the copy keeps
// its pre-save metadata, so the next save re-verifies against the server
// instead of assuming — a conservative re-read, never a silent overwrite.
// When the server file is no longer what was downloaded, nothing was
// written: the notice says so and offers revealing the local copy, never an
// overwrite. Every other failure shows as the dismissible operation error
// with the copy kept.
func (a *app) saveRemoteEdit(key remoteEditKey, entry *remoteEditEntry) {
	if entry == nil || !entry.hasCopy {
		return
	}
	repo, _ := a.syncRepo()
	if repo == nil {
		return
	}
	copy := entry.copy
	path := key.path
	var meta git.RemoteFileMeta
	a.session.doThen("Save to Server", func(*git.Repo) error {
		var err error
		meta, err = repo.SaveEdited(context.Background(), copy)
		return err
	}, func(err error) {
		// doThen drops this completion when the repository generation moved
		// on, so nothing of the save is published to another repository, and
		// the copy's metadata deliberately stays where it is.
		if err == nil {
			entry.copy.Meta = meta
			return
		}
		if errors.Is(err, git.ErrRemoteChanged) {
			a.session.ClearActionErr()
			st := remoteEditUI(a)
			st.conflictOpen = true
			st.conflictPath = path
			return
		}
		a.session.ClearActionErr()
		syncStateFor(a).syncErr = git.RedactText(err.Error())
	})
}

// discardRemoteEdit starts removing the copy of key. The comparison with
// what was downloaded runs in the same background task as the removal: an
// unchanged copy's directory is removed right there, so no round trip to
// the UI thread sits between the read that proved it unchanged and its
// deletion. One window remains and cannot close without locking the copy:
// bytes written between the read and the removal are still lost. Changed —
// or unreadable, which must never delete unconfirmed — the task posts back
// and the discard confirmation asks first; its confirm runs the deletion
// path of its own, so a copy that cannot be read is never silently removed
// either way.
func (a *app) discardRemoteEdit(key remoteEditKey, entry *remoteEditEntry) {
	if entry == nil || !entry.hasCopy || entry.discarding {
		return
	}
	entry.discarding = true
	copy := entry.copy
	baseline := entry.downloadHash
	a.spawn(func() {
		changed := true
		if content, err := os.ReadFile(copy.Local); err == nil {
			sum := sha256.Sum256(content)
			changed = hex.EncodeToString(sum[:]) != baseline
		}
		if !changed {
			// The copy holds exactly what was downloaded: nothing of the
			// user is in it, so it leaves without asking, right here in the
			// task that proved it unchanged.
			err := os.RemoveAll(filepath.Dir(copy.Local))
			a.post(func() {
				entry.discarding = false
				if err == nil {
					delete(remoteEditUI(a).entries, key)
				}
			})
			return
		}
		a.post(func() {
			entry.discarding = false
			st := remoteEditUI(a)
			st.confirmingDiscard = true
			st.copyKey = key
		})
	})
}

// deleteRemoteEditCopy removes the copy's directory in the background and
// forgets the entry only once the removal succeeded. A removal failure is
// not worth an operation error — the copy simply stays and the badge with
// it, and the entry can be discarded again.
func (a *app) deleteRemoteEditCopy(key remoteEditKey, entry *remoteEditEntry) {
	if entry == nil || !entry.hasCopy || entry.discarding {
		return
	}
	entry.discarding = true
	dir := filepath.Dir(entry.copy.Local)
	a.spawn(func() {
		err := os.RemoveAll(dir)
		a.post(func() {
			entry.discarding = false
			if err == nil {
				delete(remoteEditUI(a).entries, key)
			}
		})
	})
}

// submitRemoteEditDiscard confirms the discard confirmation: the copy is
// deleted while it is still the one the confirmation was opened for.
func (a *app) submitRemoteEditDiscard() {
	st := remoteEditUI(a)
	key := st.copyKey
	st.confirmingDiscard = false
	st.copyKey = remoteEditKey{}
	entry := st.entries[key]
	if entry == nil || !entry.hasCopy {
		return
	}
	a.deleteRemoteEditCopy(key, entry)
}

// cancelRemoteEditDiscard closes the discard confirmation without deleting.
func (a *app) cancelRemoteEditDiscard() {
	st := remoteEditUI(a)
	st.confirmingDiscard = false
	st.copyKey = remoteEditKey{}
}

// closeRemoteEditConflict closes the server-changed notice and forgets
// which file it belonged to.
func (a *app) closeRemoteEditConflict() {
	st := remoteEditUI(a)
	st.conflictOpen = false
	st.conflictPath = ""
}

// remoteEditBadge draws the small badge of a file whose server copy is
// being edited locally: nothing at all for every other file, so the rows of
// local repositories and of unedited files keep exactly their shape. The
// badge carries the accessible name "Edited Locally". It reads the state
// without counting as using remote editing, so the cache sweep waits for
// the first real interaction.
func (a *app) remoteEditBadge(c *ui.Context, f git.FileChange) {
	p := a.pal
	st := featureState(a, func() *remoteEditState {
		return &remoteEditState{entries: map[remoteEditKey]*remoteEditEntry{}}
	})
	key := remoteEditKeyFor(a.session.Meta(), f.Path)
	entry := st.entries[key]
	if entry == nil || !entry.hasCopy {
		return
	}
	ui.Box(c).Width(20).Height(20).Shrink(0).Radius(5).Center().
		Role(ui.RoleImage).
		Label("Edited Locally").Tooltip("Edited Locally").
		Background(p.accent.Alpha(0.16)).
		Children(func() {
			symbolIcon(c, "pencil", fallbackPencil, 10, p.accent)
		})
}

// remoteEditDialogs renders the remote editing dialogs once, at the level
// of the Local Changes page exactly like the page's own confirmations: they
// belong to entries and paths of the state, not to any file row, so the
// server-changed notice and the discard confirmation appear even while the
// file's row is scrolled away, filtered out, or left in a list that no
// longer renders it.
func (a *app) remoteEditDialogs(c *ui.Context) {
	p := a.pal
	st := featureState(a, func() *remoteEditState {
		return &remoteEditState{entries: map[remoteEditKey]*remoteEditEntry{}}
	})

	if st.confirmingDiscard {
		open := true
		if ui.Modal(c.Key("remote-edit-discard-modal"), &open, func() {
			ui.Column(c.Key("remote-edit-discard-body")).Width(430).Padding(18, 18, 14, 18).Gap(10).Children(func() {
				ui.Text(c, "Discard local copy?").FontSize(17).FontWeight(650).TextColor(p.text)
				ui.Text(c, "The copy of "+st.copyKey.path+" has been edited. Discarding it permanently loses those edits; the file on the server is untouched.").FontSize(13).TextColor(p.muted).MaxWidth(390).Wrap()
				ui.Row(c.Key("remote-edit-discard-actions")).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c.Key("remote-edit-discard-cancel"), "Cancel").Label("Cancel").Clicked() {
						a.cancelRemoteEditDiscard()
					}
					if ui.PrimaryButton(c.Key("remote-edit-discard-confirm"), "Discard Local Copy").Label("Confirm Discard Local Copy").Clicked() {
						a.submitRemoteEditDiscard()
					}
				})
			})
		}).Dismissed() {
			a.cancelRemoteEditDiscard()
		}
	}

	if st.conflictOpen {
		open := true
		key := remoteEditKeyFor(a.session.Meta(), st.conflictPath)
		if ui.Modal(c.Key("remote-edit-conflict-modal"), &open, func() {
			ui.Column(c.Key("remote-edit-conflict-body")).Width(430).Padding(18, 18, 14, 18).Gap(10).Children(func() {
				ui.Text(c, "File changed on the server").FontSize(17).FontWeight(650).TextColor(p.text)
				ui.Text(c, "The file on the server changed after it was opened for editing. Nothing was written; your edits are still in the local copy.").FontSize(13).TextColor(p.muted).MaxWidth(390).Wrap()
				ui.Row(c.Key("remote-edit-conflict-actions")).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c.Key("remote-edit-conflict-cancel"), "Close").Label("Close").Clicked() {
						a.closeRemoteEditConflict()
					}
					if ui.PrimaryButton(c.Key("remote-edit-show-copy"), "Show Local Copy").Label("Show Local Copy").Clicked() {
						a.closeRemoteEditConflict()
						if entry := st.entries[key]; entry != nil && entry.hasCopy {
							a.showRemoteEditCopy(entry.copy.Local)
						}
					}
				})
			})
		}).Dismissed() {
			a.closeRemoteEditConflict()
		}
	}
}
