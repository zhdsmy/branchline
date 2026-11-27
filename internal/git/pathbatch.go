package git

// pathBatchLimit is the largest the paths of one command may measure, in
// bytes. Each path measures as its shell-quoted length plus one for the
// space joining it to the argument before it. A command reaching a server
// over SSH is one argument of the server's shell, where the kernel allows
// 128 KiB per argument; staying under half of that leaves ample room for
// the cd, the environment and the configuration of the same command line.
// It is a variable only so the package's tests can lower it; nothing else
// should change it.
var pathBatchLimit = 64 * 1024

// runPathBatches runs the command prefix with the paths in as few commands
// as the length limit allows, each command carrying the whole prefix. The
// order of the paths is unchanged and one path is never split: a path that
// alone measures over the limit is one batch of its own. A short list runs
// as the single command an unbatched call would have run.
//
// Batching is not atomic: when a batch fails, the batches before it stay
// applied and its error is the error returned, with the batches after it
// never run.
func (r *Repo) runPathBatches(prefix []string, paths []string) error {
	var batch []string
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := make([]string, 0, len(prefix)+len(batch))
		args = append(args, prefix...)
		args = append(args, batch...)
		batch = nil
		size = 0
		_, err := r.run(quick, args...)
		return err
	}
	for _, p := range paths {
		pl := len(ShellQuote(p)) + 1
		if len(batch) > 0 && size+pl > pathBatchLimit {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, p)
		size += pl
	}
	return flush()
}
