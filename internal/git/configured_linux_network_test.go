package git

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestConfiguredLinuxSSHNetworkRecovery(t *testing.T) {
	target := resolveConfiguredLinuxSSHTarget(t)

	if quick != 30*time.Second || slow != 10*time.Minute {
		t.Fatalf("product default timeouts changed: quick=%s (want 30s), slow=%s (want 10m0s)", quick, slow)
	}

	t.Run("FetchDisconnectRetry", func(t *testing.T) {
		fix := newNetworkRecoveryCaseFixture(t, target, "fetch-disc")
		fix.populateWorkSentinels(t)

		// Advance peer and push a new commit + tag to bare so origin/main is ahead of work.
		advanceScript := "cd " + ShellQuote(fix.peerDir) + "\n" +
			"printf 'fetched payload from peer v2\\n' > remote-new.txt\n" +
			"git add remote-new.txt\n" +
			"git commit -q -m 'peer advance for fetch'\n" +
			"new_oid=$(git rev-parse HEAD)\n" +
			"git tag v1.0-fetch \"$new_oid\"\n" +
			"git push -q origin refs/heads/main:refs/heads/main refs/tags/v1.0-fetch:refs/tags/v1.0-fetch\n" +
			"printf '%s\\n' \"$new_oid\"\n"
		wantRemoteOID := strings.TrimSpace(fix.sshOut(t, advanceScript))
		if !isHex(wantRemoteOID) || wantRemoteOID == fix.baseCommit {
			t.Fatalf("unexpected wantRemoteOID %q (base=%q)", wantRemoteOID, fix.baseCommit)
		}

		fix.armUploadPackBarrier(t)
		beforeWork := fix.captureWorkSnapshot(t)
		beforeBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "FetchDisconnectRetry/before", beforeWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "FetchDisconnectRetry/before", beforeBare)
		if beforeBare.MainOID != wantRemoteOID {
			t.Fatalf("bare refs/heads/main = %q, want %q", beforeBare.MainOID, wantRemoteOID)
		}

		proxy := startSSHTCPProxy(t, target.targetAddr)
		client := newProxySSHClient(t, target, proxy)
		cmdLog := NewCommandLog()
		repo := New(client.Executor(target.hostAlias, fix.workDir)).WithCommandLog(cmdLog)

		fetchDone := make(chan error, 1)
		go func() {
			fetchDone <- repo.FetchOpts(FetchOptions{Remote: "origin", Prune: true, Tags: true})
		}()

		ev := fix.waitUploadPackBarrier(t, 15*time.Second)
		select {
		case earlyErr := <-fetchDone:
			t.Fatalf("FetchOpts returned before disconnect at upload-pack barrier: %v", earlyErr)
		default:
		}
		totalBefore, activeBefore, _, c2sBefore, s2cBefore := proxy.Stats()
		if totalBefore < 1 || activeBefore < 1 {
			t.Fatalf("proxy stats at barrier: total=%d active=%d, want >= 1", totalBefore, activeBefore)
		}
		t.Logf("FetchDisconnectRetry barrier reached: marker=%q git_pid=%d(%s) wrapper_pid=%d stage_bytes=%d stage_hex=%s proxy_listen=%s->%s (total=%d active=%d c2s=%d s2c=%d)",
			ev.ReadyMarker, ev.GitPID, ev.GitComm, ev.WrapperPID, ev.StageBytes, ev.StageHex,
			proxy.listenAddr, target.targetAddr, totalBefore, activeBefore, c2sBefore, s2cBefore)

		discStart := time.Now()
		closedCount := proxy.DisconnectActive()
		if closedCount < 1 {
			t.Fatalf("DisconnectActive closed %d connections, want >= 1", closedCount)
		}

		var fetchErr error
		var discElapsed time.Duration
		select {
		case fetchErr = <-fetchDone:
			discElapsed = time.Since(discStart)
		case <-time.After(5 * time.Second):
			discElapsed = time.Since(discStart)
			t.Errorf("FetchOpts did not return within 5s after proxy TCP disconnect (elapsed=%s)", discElapsed)
			fix.releaseBarrierAndWaitPIDs(t, ev.GitPID, ev.WrapperPID)
			t.FailNow()
		}
		if fetchErr == nil {
			t.Fatal("FetchOpts succeeded across disconnected proxy TCP stream, want error")
		}
		t.Logf("FetchDisconnectRetry disconnect result: elapsed=%s err=%q", discElapsed, fetchErr.Error())

		if err := proxy.WaitForZeroActive(3 * time.Second); err != nil {
			t.Fatalf("proxy active connections after disconnect: %v", err)
		}
		client.VerifyAllLocalSSHPIDsExited(t, 3*time.Second)
		fix.releaseBarrierAndWaitPIDs(t, ev.GitPID, ev.WrapperPID)

		afterFaultWork := fix.captureWorkSnapshot(t)
		afterFaultBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "FetchDisconnectRetry/afterFault", afterFaultWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "FetchDisconnectRetry/afterFault", afterFaultBare)

		originAfterFault := strings.TrimSpace(fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" rev-parse refs/remotes/origin/main"))
		if originAfterFault != fix.baseCommit && originAfterFault != wantRemoteOID {
			t.Fatalf("afterFault origin/main = %q, want either old %q or new %q", originAfterFault, fix.baseCommit, wantRemoteOID)
		}
		fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" cat-file -e "+ShellQuote(originAfterFault+"^{commit}"))

		// Restore proxy and retry on the exact same Repo instance.
		proxy.Restore()
		if retryErr := repo.FetchOpts(FetchOptions{Remote: "origin", Prune: true, Tags: true}); retryErr != nil {
			t.Fatalf("FetchOpts Retry after proxy recovery failed: %v", retryErr)
		}

		afterRetryWork := fix.captureWorkSnapshot(t)
		afterRetryBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "FetchDisconnectRetry/afterRetry", afterRetryWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "FetchDisconnectRetry/afterRetry", afterRetryBare)

		originAfterRetry := strings.TrimSpace(fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" rev-parse refs/remotes/origin/main"))
		tagAfterRetry := strings.TrimSpace(fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" rev-parse refs/tags/v1.0-fetch"))
		if originAfterRetry != wantRemoteOID || tagAfterRetry != wantRemoteOID {
			t.Fatalf("afterRetry origin/main=%q tag=%q, want both %q", originAfterRetry, tagAfterRetry, wantRemoteOID)
		}
		fetchedBlob := fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" show refs/remotes/origin/main:remote-new.txt")
		if fetchedBlob != "fetched payload from peer v2\n" {
			t.Fatalf("fetched blob = %q", fetchedBlob)
		}

		records, dropped := cmdLog.Snapshot()
		if dropped != 0 || len(records) != 2 {
			t.Fatalf("CommandLog records=%d dropped=%d, want 2 records and 0 dropped: %+v", len(records), dropped, records)
		}
		if records[0].ExitCode == 0 || records[1].ExitCode != 0 {
			t.Fatalf("CommandLog exit codes = (%d, %d), want (non-zero, 0)", records[0].ExitCode, records[1].ExitCode)
		}
		invocations := client.Invocations(t)
		if len(invocations) != 2 {
			t.Fatalf("proxy ssh wrapper invocations = %d, want 2: %q", len(invocations), invocations)
		}
		totalAfter, activeAfter, _, c2sAfter, s2cAfter := proxy.Stats()
		fix.verifyFetchRefOracleCounterexamples(t, beforeWork, beforeBare, wantRemoteOID)
		t.Logf("FetchDisconnectRetry state evidence:\n  before: HEAD=%s(%s) refs=[%s] bare_refs=[%s] stages=[%s] locks=%q\n  afterFault: HEAD=%s(%s) refs=[%s] bare_refs=[%s] stages=[%s] locks=%q\n  afterRetry: HEAD=%s(%s) refs=[%s] bare_refs=[%s] stages=[%s] locks=%q\n  sentinels: staged=%q tracked=(%q,%q) shared=(%q,%q) untracked=%q\n  cmdLog[0]: cmd=%q exit=%d err=%q\n  cmdLog[1]: cmd=%q exit=%d",
			beforeWork.HeadSymref, beforeWork.HeadOID, strings.ReplaceAll(beforeWork.Refs, "\n", "; "), strings.ReplaceAll(beforeBare.Refs, "\n", "; "), strings.ReplaceAll(beforeWork.IndexStages, "\n", "; "), beforeWork.LockAndTempFiles,
			afterFaultWork.HeadSymref, afterFaultWork.HeadOID, strings.ReplaceAll(afterFaultWork.Refs, "\n", "; "), strings.ReplaceAll(afterFaultBare.Refs, "\n", "; "), strings.ReplaceAll(afterFaultWork.IndexStages, "\n", "; "), afterFaultWork.LockAndTempFiles,
			afterRetryWork.HeadSymref, afterRetryWork.HeadOID, strings.ReplaceAll(afterRetryWork.Refs, "\n", "; "), strings.ReplaceAll(afterRetryBare.Refs, "\n", "; "), strings.ReplaceAll(afterRetryWork.IndexStages, "\n", "; "), afterRetryWork.LockAndTempFiles,
			strings.TrimSpace(afterRetryWork.StagedWorktree), strings.TrimSpace(afterRetryWork.TrackedIndex), strings.TrimSpace(afterRetryWork.TrackedWorktree),
			strings.TrimSpace(afterRetryWork.SharedIndex), strings.TrimSpace(afterRetryWork.SharedWorktree), strings.TrimSpace(afterRetryWork.UntrackedWorktree),
			records[0].Command, records[0].ExitCode, records[0].Stderr, records[1].Command, records[1].ExitCode)
		t.Logf("FetchDisconnectRetry completed: origin_before=%s origin_after_fault=%s origin_after_retry=%s head=%s proxy_total=%d proxy_active=%d bytes=(%d,%d)",
			fix.baseCommit, originAfterFault, originAfterRetry, afterRetryWork.HeadOID, totalAfter, activeAfter, c2sAfter, s2cAfter)
	})

	t.Run("PushDisconnectRetry", func(t *testing.T) {
		fix := newNetworkRecoveryCaseFixture(t, target, "push-disc")

		// Create a committed change in work to push, then populate uncommitted sentinels on top.
		commitScript := "cd " + ShellQuote(fix.workDir) + "\n" +
			"printf 'pushed commit payload v2\\n' > push-item.txt\n" +
			"git add push-item.txt\n" +
			"git commit -q -m 'work commit for push recovery'\n" +
			"git rev-parse HEAD\n"
		wantPushOID := strings.TrimSpace(fix.sshOut(t, commitScript))
		if !isHex(wantPushOID) || wantPushOID == fix.baseCommit {
			t.Fatalf("unexpected wantPushOID %q (base=%q)", wantPushOID, fix.baseCommit)
		}
		fix.populateWorkSentinels(t)

		beforeWork := fix.captureWorkSnapshot(t)
		beforeBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "PushDisconnectRetry/before", beforeWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "PushDisconnectRetry/before", beforeBare)
		if beforeWork.HeadOID != wantPushOID || beforeBare.MainOID != fix.baseCommit {
			t.Fatalf("before push: work HEAD=%q (want %q), bare main=%q (want %q)",
				beforeWork.HeadOID, wantPushOID, beforeBare.MainOID, fix.baseCommit)
		}

		proxy := startSSHTCPProxy(t, target.targetAddr)
		client := newProxySSHClient(t, target, proxy)
		cmdLog := NewCommandLog()
		repo := New(client.Executor(target.hostAlias, fix.workDir)).WithCommandLog(cmdLog)

		// Phase 1: PushNote disconnects at pre-receive barrier (which aborts after disconnect so bare stays at old OID), then Retry via PushNote updates bare to wantPushOID.
		fix.armPreReceiveBarrier(t, true)
		type pushResult struct {
			note string
			err  error
		}
		pushDone := make(chan pushResult, 1)
		go func() {
			note, err := repo.PushNote("origin", "main", "", false, false, false)
			pushDone <- pushResult{note: note, err: err}
		}()

		ev1 := fix.waitPreReceiveBarrier(t, "refs/heads/main", 15*time.Second)
		select {
		case early := <-pushDone:
			t.Fatalf("PushNote returned before disconnect at pre-receive barrier: %+v", early)
		default:
		}
		wantStdin1 := fix.baseCommit + " " + wantPushOID + " refs/heads/main"
		if ev1.StdinRefs != wantStdin1 || ev1.RefWhilePaused != fix.baseCommit {
			t.Fatalf("PushNote pre-receive stage: stdin=%q (want %q), refWhilePaused=%q (want %q)",
				ev1.StdinRefs, wantStdin1, ev1.RefWhilePaused, fix.baseCommit)
		}
		t.Logf("PushDisconnectRetry/PushNote barrier reached: marker=%q hook_pid=%d rp_pid=%d(%s) stdin=%q ref_while_paused=%s",
			ev1.ReadyMarker, ev1.HookPID, ev1.ReceivePackPID, ev1.ReceiveComm, ev1.StdinRefs, ev1.RefWhilePaused)

		discStart1 := time.Now()
		if closed := proxy.DisconnectActive(); closed < 1 {
			t.Fatalf("PushNote DisconnectActive closed %d connections, want >= 1", closed)
		}
		var res1 pushResult
		var discElapsed1 time.Duration
		select {
		case res1 = <-pushDone:
			discElapsed1 = time.Since(discStart1)
		case <-time.After(5 * time.Second):
			discElapsed1 = time.Since(discStart1)
			t.Errorf("PushNote did not return within 5s after proxy TCP disconnect (elapsed=%s)", discElapsed1)
			fix.releaseBarrierAndWaitPIDs(t, ev1.HookPID, ev1.ReceivePackPID)
			t.FailNow()
		}
		if res1.err == nil {
			t.Fatalf("PushNote succeeded across disconnected proxy TCP stream (note=%q), want error", res1.note)
		}
		t.Logf("PushDisconnectRetry/PushNote disconnect result: elapsed=%s err=%q", discElapsed1, res1.err.Error())

		if err := proxy.WaitForZeroActive(3 * time.Second); err != nil {
			t.Fatalf("proxy active connections after PushNote disconnect: %v", err)
		}
		client.VerifyAllLocalSSHPIDsExited(t, 3*time.Second)
		fix.releaseBarrierAndWaitPIDs(t, ev1.HookPID, ev1.ReceivePackPID)

		midWork := fix.captureWorkSnapshot(t)
		midBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "PushDisconnectRetry/afterPushNoteFault", midWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "PushDisconnectRetry/afterPushNoteFault", midBare)
		if midBare.MainOID != fix.baseCommit && midBare.MainOID != wantPushOID {
			t.Fatalf("bare main after PushNote fault = %q, want %q or %q", midBare.MainOID, fix.baseCommit, wantPushOID)
		}
		fix.sshOut(t, "git -C "+ShellQuote(fix.bareDir)+" cat-file -e "+ShellQuote(midBare.MainOID+"^{commit}"))

		proxy.Restore()
		retryNote1, retryErr1 := repo.PushNote("origin", "main", "", false, false, false)
		if retryErr1 != nil {
			t.Fatalf("PushNote Retry after proxy recovery failed: %v", retryErr1)
		}
		afterNoteRetryWork := fix.captureWorkSnapshot(t)
		afterNoteRetryBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "PushDisconnectRetry/afterPushNoteRetry", afterNoteRetryWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "PushDisconnectRetry/afterPushNoteRetry", afterNoteRetryBare)
		if afterNoteRetryBare.MainOID != wantPushOID {
			t.Fatalf("bare main after PushNote Retry = %q, want %q", afterNoteRetryBare.MainOID, wantPushOID)
		}
		upToDateNote, upToDateErr := repo.PushNote("origin", "main", "", false, false, false)
		if upToDateErr != nil || upToDateNote != UpToDatePush {
			t.Fatalf("idempotent PushNote = (%q, %v), want (%q, nil)", upToDateNote, upToDateErr, UpToDatePush)
		}

		// Phase 2: PushTo custom branch ("published/main") disconnects at pre-receive barrier (which exits 0 on release, allowing receive-pack to finish after disconnect), then Retry via PushTo converges cleanly.
		fix.armPreReceiveBarrier(t, false)
		pushToDone := make(chan error, 1)
		go func() {
			pushToDone <- repo.PushTo("origin", "main", "published/main", false, false, false)
		}()

		ev2 := fix.waitPreReceiveBarrier(t, "refs/heads/published/main", 15*time.Second)
		select {
		case earlyErr := <-pushToDone:
			t.Fatalf("PushTo returned before disconnect at pre-receive barrier: %v", earlyErr)
		default:
		}
		zeroOID := "0000000000000000000000000000000000000000"
		wantStdin2 := zeroOID + " " + wantPushOID + " refs/heads/published/main"
		if ev2.StdinRefs != wantStdin2 || ev2.RefWhilePaused != "missing" {
			t.Fatalf("PushTo pre-receive stage: stdin=%q (want %q), refWhilePaused=%q (want missing)",
				ev2.StdinRefs, wantStdin2, ev2.RefWhilePaused)
		}
		t.Logf("PushDisconnectRetry/PushTo barrier reached: marker=%q hook_pid=%d rp_pid=%d(%s) stdin=%q ref_while_paused=%s",
			ev2.ReadyMarker, ev2.HookPID, ev2.ReceivePackPID, ev2.ReceiveComm, ev2.StdinRefs, ev2.RefWhilePaused)

		discStart2 := time.Now()
		if closed := proxy.DisconnectActive(); closed < 1 {
			t.Fatalf("PushTo DisconnectActive closed %d connections, want >= 1", closed)
		}
		var pushToErr error
		var discElapsed2 time.Duration
		select {
		case pushToErr = <-pushToDone:
			discElapsed2 = time.Since(discStart2)
		case <-time.After(5 * time.Second):
			discElapsed2 = time.Since(discStart2)
			t.Errorf("PushTo did not return within 5s after proxy TCP disconnect (elapsed=%s)", discElapsed2)
			fix.releaseBarrierAndWaitPIDs(t, ev2.HookPID, ev2.ReceivePackPID)
			t.FailNow()
		}
		if pushToErr == nil {
			t.Fatal("PushTo succeeded across disconnected proxy TCP stream, want error")
		}
		t.Logf("PushDisconnectRetry/PushTo disconnect result: elapsed=%s err=%q", discElapsed2, pushToErr.Error())

		if err := proxy.WaitForZeroActive(3 * time.Second); err != nil {
			t.Fatalf("proxy active connections after PushTo disconnect: %v", err)
		}
		client.VerifyAllLocalSSHPIDsExited(t, 3*time.Second)
		fix.releaseBarrierAndWaitPIDs(t, ev2.HookPID, ev2.ReceivePackPID)

		afterPushToFaultWork := fix.captureWorkSnapshot(t)
		afterPushToFaultBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "PushDisconnectRetry/afterPushToFault", afterPushToFaultWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "PushDisconnectRetry/afterPushToFault", afterPushToFaultBare)
		pubAfterFault := strings.TrimSpace(fix.sshOut(t, "git -C "+ShellQuote(fix.bareDir)+" rev-parse --verify -q refs/heads/published/main || printf 'missing'"))
		if pubAfterFault != "missing" && pubAfterFault != wantPushOID {
			t.Fatalf("published/main after PushTo fault = %q, want missing or %q", pubAfterFault, wantPushOID)
		}

		proxy.Restore()
		if err := repo.PushTo("origin", "main", "published/main", false, false, false); err != nil {
			t.Fatalf("PushTo Retry after proxy recovery failed: %v", err)
		}

		afterAllWork := fix.captureWorkSnapshot(t)
		afterAllBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "PushDisconnectRetry/afterAllRetry", afterAllWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "PushDisconnectRetry/afterAllRetry", afterAllBare)

		pubAfterRetry := strings.TrimSpace(fix.sshOut(t, "git -C "+ShellQuote(fix.bareDir)+" rev-parse refs/heads/published/main"))
		if afterAllBare.MainOID != wantPushOID || pubAfterRetry != wantPushOID {
			t.Fatalf("afterAllRetry bare main=%q published/main=%q, want both %q", afterAllBare.MainOID, pubAfterRetry, wantPushOID)
		}
		pushedBlob := fix.sshOut(t, "git -C "+ShellQuote(fix.bareDir)+" show refs/heads/published/main:push-item.txt")
		if pushedBlob != "pushed commit payload v2\n" {
			t.Fatalf("bare pushed blob = %q", pushedBlob)
		}

		records, dropped := cmdLog.Snapshot()
		if dropped != 0 || len(records) != 5 {
			t.Fatalf("CommandLog records=%d dropped=%d, want 5 records and 0 dropped: %+v", len(records), dropped, records)
		}
		invocations := client.Invocations(t)
		if len(invocations) != 5 {
			t.Fatalf("proxy ssh wrapper invocations = %d, want 5", len(invocations))
		}
		totalAfter, activeAfter, _, c2sAfter, s2cAfter := proxy.Stats()
		fix.verifyPushRefOracleCounterexamples(t, beforeWork, beforeBare, wantPushOID)
		t.Logf("PushDisconnectRetry state evidence:\n  work_before: HEAD=%s(%s) refs=[%s] stages=[%s] locks=%q\n  work_after_fault: HEAD=%s(%s) refs=[%s] stages=[%s] locks=%q\n  work_after_retry: HEAD=%s(%s) refs=[%s] stages=[%s] locks=%q\n  bare_refs_before=[%s] bare_refs_mid=[%s] bare_refs_after=[%s] bare_locks=%q\n  cmdLog[0]: cmd=%q exit=%d err=%q\n  cmdLog[1]: cmd=%q exit=%d\n  cmdLog[2]: cmd=%q exit=%d\n  cmdLog[3]: cmd=%q exit=%d err=%q\n  cmdLog[4]: cmd=%q exit=%d",
			beforeWork.HeadSymref, beforeWork.HeadOID, strings.ReplaceAll(beforeWork.Refs, "\n", "; "), strings.ReplaceAll(beforeWork.IndexStages, "\n", "; "), beforeWork.LockAndTempFiles,
			midWork.HeadSymref, midWork.HeadOID, strings.ReplaceAll(midWork.Refs, "\n", "; "), strings.ReplaceAll(midWork.IndexStages, "\n", "; "), midWork.LockAndTempFiles,
			afterAllWork.HeadSymref, afterAllWork.HeadOID, strings.ReplaceAll(afterAllWork.Refs, "\n", "; "), strings.ReplaceAll(afterAllWork.IndexStages, "\n", "; "), afterAllWork.LockAndTempFiles,
			strings.ReplaceAll(beforeBare.Refs, "\n", "; "), strings.ReplaceAll(midBare.Refs, "\n", "; "), strings.ReplaceAll(afterAllBare.Refs, "\n", "; "), afterAllBare.LockAndTempFiles,
			records[0].Command, records[0].ExitCode, records[0].Stderr,
			records[1].Command, records[1].ExitCode,
			records[2].Command, records[2].ExitCode,
			records[3].Command, records[3].ExitCode, records[3].Stderr,
			records[4].Command, records[4].ExitCode)
		t.Logf("PushDisconnectRetry completed: bare_main=%s->%s->%s(note=%q) published=%s->%s proxy_total=%d proxy_active=%d bytes=(%d,%d)",
			fix.baseCommit, midBare.MainOID, afterAllBare.MainOID, retryNote1, pubAfterFault, pubAfterRetry, totalAfter, activeAfter, c2sAfter, s2cAfter)
	})

	t.Run("InFlightDeadlineRecovery", func(t *testing.T) {
		fix := newNetworkRecoveryCaseFixture(t, target, "deadline")
		fix.populateWorkSentinels(t)

		advanceScript := "cd " + ShellQuote(fix.peerDir) + "\n" +
			"printf 'deadline recovery payload v2\\n' > deadline-item.txt\n" +
			"git add deadline-item.txt\n" +
			"git commit -q -m 'peer commit for deadline recovery'\n" +
			"new_oid=$(git rev-parse HEAD)\n" +
			"git push -q origin refs/heads/main:refs/heads/main\n" +
			"printf '%s\\n' \"$new_oid\"\n"
		wantRemoteOID := strings.TrimSpace(fix.sshOut(t, advanceScript))
		if !isHex(wantRemoteOID) || wantRemoteOID == fix.baseCommit {
			t.Fatalf("unexpected wantRemoteOID %q (base=%q)", wantRemoteOID, fix.baseCommit)
		}

		fix.armUploadPackBarrier(t)
		beforeWork := fix.captureWorkSnapshot(t)
		beforeBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "InFlightDeadlineRecovery/before", beforeWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "InFlightDeadlineRecovery/before", beforeBare)

		proxy := startSSHTCPProxy(t, target.targetAddr)
		client := newProxySSHClient(t, target, proxy)
		cmdLog := NewCommandLog()
		repo := New(client.Executor(target.hostAlias, fix.workDir)).WithCommandLog(cmdLog)

		testTimeout := 1500 * time.Millisecond
		t.Logf("InFlightDeadlineRecovery timeout parameters: test_context_timeout=%s vs product_defaults=(quick=%s, slow=%s)",
			testTimeout, quick, slow)

		callStart := time.Now()
		deadlineDone := make(chan error, 1)
		go func() {
			_, err := repo.runContext(context.Background(), testTimeout, nil, "fetch", "--prune", "--tags", "origin")
			deadlineDone <- err
		}()

		ev := fix.waitUploadPackBarrier(t, 10*time.Second)
		select {
		case earlyErr := <-deadlineDone:
			t.Fatalf("runContext returned before upload-pack barrier pause: %v", earlyErr)
		default:
		}
		pausedActive := proxy.PauseForwarding()
		if pausedActive < 1 {
			t.Fatalf("PauseForwarding active connections = %d, want >= 1", pausedActive)
		}
		t.Logf("InFlightDeadlineRecovery barrier reached & proxy forwarding paused (connection held open): marker=%q git_pid=%d(%s) wrapper_pid=%d stage_bytes=%d active_conns=%d elapsed_at_pause=%s",
			ev.ReadyMarker, ev.GitPID, ev.GitComm, ev.WrapperPID, ev.StageBytes, pausedActive, time.Since(callStart))

		var timeoutErr error
		var callElapsed time.Duration
		select {
		case timeoutErr = <-deadlineDone:
			callElapsed = time.Since(callStart)
		case <-time.After(testTimeout + 5*time.Second):
			callElapsed = time.Since(callStart)
			t.Errorf("runContext did not return within bounded window after %s deadline (elapsed=%s)", testTimeout, callElapsed)
			proxy.ResumeForwarding()
			fix.releaseBarrierAndWaitPIDs(t, ev.GitPID, ev.WrapperPID)
			t.FailNow()
		}

		wantTimeoutMsg := "git fetch: timed out after " + testTimeout.String()
		if timeoutErr == nil || !strings.Contains(timeoutErr.Error(), wantTimeoutMsg) {
			t.Fatalf("runContext error = %v, want substring %q", timeoutErr, wantTimeoutMsg)
		}
		if callElapsed < testTimeout-100*time.Millisecond {
			t.Fatalf("runContext returned too early: elapsed=%s, want >= %s", callElapsed, testTimeout)
		}

		// Verify child ssh process Wait completed and proxy goroutines for the timed-out connection were recycled.
		localPIDs := client.VerifyAllLocalSSHPIDsExited(t, 3*time.Second)
		if len(localPIDs) != 1 {
			t.Fatalf("recorded local ssh PIDs = %v, want 1", localPIDs)
		}
		if err := proxy.WaitForZeroActive(3 * time.Second); err != nil {
			t.Fatalf("proxy goroutine recycling after context deadline: %v", err)
		}
		t.Logf("InFlightDeadlineRecovery deadline result: elapsed=%s err=%q local_ssh_pid_exited=%d proxy_active=0",
			callElapsed, timeoutErr.Error(), localPIDs[0])

		// Resume proxy forwarding and release remote barrier worker.
		proxy.ResumeForwarding()
		fix.releaseBarrierAndWaitPIDs(t, ev.GitPID, ev.WrapperPID)

		afterTimeoutWork := fix.captureWorkSnapshot(t)
		afterTimeoutBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "InFlightDeadlineRecovery/afterTimeout", afterTimeoutWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "InFlightDeadlineRecovery/afterTimeout", afterTimeoutBare)

		originAfterTimeout := strings.TrimSpace(fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" rev-parse refs/remotes/origin/main"))
		if originAfterTimeout != fix.baseCommit {
			t.Fatalf("origin/main after timeout = %q, want unchanged %q", originAfterTimeout, fix.baseCommit)
		}

		// Verify product read operations (TopLevel, Status) and Retry (FetchOpts) on the exact same Repo instance succeed.
		top, err := repo.TopLevel()
		if err != nil || top != fix.workDir {
			t.Fatalf("TopLevel after recovery = (%q, %v), want (%q, nil)", top, err, fix.workDir)
		}
		st, err := repo.Status()
		if err != nil || st.Branch != "main" || st.Head != fix.baseCommit {
			t.Fatalf("Status after recovery = (%+v, %v), want branch=main head=%s", st, err, fix.baseCommit)
		}
		if len(st.Staged) != 2 || len(st.Unstaged) != 3 {
			t.Fatalf("Status sentinel counts after recovery: staged=%d unstaged=%d, want 2/3 (%+v)",
				len(st.Staged), len(st.Unstaged), st)
		}

		if retryErr := repo.FetchOpts(FetchOptions{Remote: "origin", Prune: true, Tags: true}); retryErr != nil {
			t.Fatalf("FetchOpts Retry after deadline recovery failed: %v", retryErr)
		}

		afterRetryWork := fix.captureWorkSnapshot(t)
		afterRetryBare := fix.captureBareSnapshot(t)
		fix.assertWorkSentinelsAndCleanLocks(t, "InFlightDeadlineRecovery/afterRetry", afterRetryWork, beforeWork)
		fix.assertBarePeerAndCleanLocks(t, "InFlightDeadlineRecovery/afterRetry", afterRetryBare)

		originAfterRetry := strings.TrimSpace(fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" rev-parse refs/remotes/origin/main"))
		if originAfterRetry != wantRemoteOID {
			t.Fatalf("origin/main after Retry = %q, want %q", originAfterRetry, wantRemoteOID)
		}
		fetchedBlob := fix.sshOut(t, "git -C "+ShellQuote(fix.workDir)+" show refs/remotes/origin/main:deadline-item.txt")
		if fetchedBlob != "deadline recovery payload v2\n" {
			t.Fatalf("fetched blob after Retry = %q", fetchedBlob)
		}

		client.VerifyAllLocalSSHPIDsExited(t, 3*time.Second)
		if err := proxy.WaitForZeroActive(3 * time.Second); err != nil {
			t.Fatalf("proxy active connections at end of InFlightDeadlineRecovery: %v", err)
		}
		records, dropped := cmdLog.Snapshot()
		if dropped != 0 || len(records) < 4 {
			t.Fatalf("CommandLog records=%d dropped=%d, want >= 4 records and 0 dropped", len(records), dropped)
		}
		totalAfter, activeAfter, _, c2sAfter, s2cAfter := proxy.Stats()
		t.Logf("InFlightDeadlineRecovery state evidence:\n  before: HEAD=%s(%s) refs=[%s] bare_refs=[%s] stages=[%s] locks=%q\n  afterTimeout: HEAD=%s(%s) refs=[%s] bare_refs=[%s] stages=[%s] locks=%q\n  afterRetry: HEAD=%s(%s) refs=[%s] bare_refs=[%s] stages=[%s] locks=%q\n  cmdLog[0](timeout): cmd=%q exit=%d err=%q\n  cmdLog[last](retry): cmd=%q exit=%d",
			beforeWork.HeadSymref, beforeWork.HeadOID, strings.ReplaceAll(beforeWork.Refs, "\n", "; "), strings.ReplaceAll(beforeBare.Refs, "\n", "; "), strings.ReplaceAll(beforeWork.IndexStages, "\n", "; "), beforeWork.LockAndTempFiles,
			afterTimeoutWork.HeadSymref, afterTimeoutWork.HeadOID, strings.ReplaceAll(afterTimeoutWork.Refs, "\n", "; "), strings.ReplaceAll(afterTimeoutBare.Refs, "\n", "; "), strings.ReplaceAll(afterTimeoutWork.IndexStages, "\n", "; "), afterTimeoutWork.LockAndTempFiles,
			afterRetryWork.HeadSymref, afterRetryWork.HeadOID, strings.ReplaceAll(afterRetryWork.Refs, "\n", "; "), strings.ReplaceAll(afterRetryBare.Refs, "\n", "; "), strings.ReplaceAll(afterRetryWork.IndexStages, "\n", "; "), afterRetryWork.LockAndTempFiles,
			records[0].Command, records[0].ExitCode, records[0].Stderr, records[len(records)-1].Command, records[len(records)-1].ExitCode)
		t.Logf("InFlightDeadlineRecovery completed: origin=%s->%s->%s top=%s proxy_total=%d proxy_active=%d bytes=(%d,%d)",
			fix.baseCommit, originAfterTimeout, originAfterRetry, top, totalAfter, activeAfter, c2sAfter, s2cAfter)
	})
}

func TestNetworkRecoveryRefMapOracleCounterexamples(t *testing.T) {
	const (
		baseOID     = "1111111111111111111111111111111111111111"
		peerKeepOID = "2222222222222222222222222222222222222222"
		wantNewOID  = "3333333333333333333333333333333333333333"
		rogueOID    = "4444444444444444444444444444444444444444"
	)

	fix := &networkRecoveryCaseFixture{
		baseCommit:          baseOID,
		peerKeepCommit:      peerKeepOID,
		baselineBareMainOID: wantNewOID,
		baselineWorkHeadOID: wantNewOID,
	}

	workBeforeFetch := formatExactRefMap(map[string]string{
		"refs/heads/local-keep":         baseOID,
		"refs/heads/main":               baseOID,
		"refs/remotes/origin/main":      baseOID,
		"refs/remotes/origin/peer-keep": peerKeepOID,
		"refs/tags/v0.1-keep":           baseOID,
	})
	bareBeforeFetch := formatExactRefMap(map[string]string{
		"refs/heads/main":      wantNewOID,
		"refs/heads/peer-keep": peerKeepOID,
		"refs/tags/v0.1-keep":  baseOID,
		"refs/tags/v1.0-fetch": wantNewOID,
	})

	workAfterFetchNew := formatExactRefMap(map[string]string{
		"refs/heads/local-keep":         baseOID,
		"refs/heads/main":               baseOID,
		"refs/remotes/origin/main":      wantNewOID,
		"refs/remotes/origin/peer-keep": peerKeepOID,
		"refs/tags/v0.1-keep":           baseOID,
		"refs/tags/v1.0-fetch":          wantNewOID,
	})

	// Legal old & new states for Fetch fault, and required new state for Fetch retry.
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", workBeforeFetch, workBeforeFetch); err != nil {
		t.Fatalf("expected legal complete old work refs to pass afterFault: %v", err)
	}
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", workBeforeFetch, workAfterFetchNew); err != nil {
		t.Fatalf("expected legal complete new work refs to pass afterFault: %v", err)
	}
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", workBeforeFetch, workAfterFetchNew); err != nil {
		t.Fatalf("expected legal complete new work refs to pass afterRetry: %v", err)
	}
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", workBeforeFetch, workBeforeFetch); err == nil {
		t.Fatal("expected stale old work refs to fail afterRetry")
	}

	// Extra branch (exact B review reproduction: refs/heads/unexpected-review) and extra tag in work and bare.
	withExtraBranch := workBeforeFetch + "\nrefs/heads/unexpected-review " + baseOID
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterFault", workBeforeFetch, withExtraBranch); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/heads/unexpected-review"`) {
		t.Fatalf("expected extra branch refs/heads/unexpected-review to fail afterFault, got: %v", err)
	}
	withExtraBranchAfterRetry := workAfterFetchNew + "\nrefs/heads/unexpected-review " + baseOID
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", workBeforeFetch, withExtraBranchAfterRetry); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/heads/unexpected-review"`) {
		t.Fatalf("expected extra branch refs/heads/unexpected-review to fail afterRetry, got: %v", err)
	}
	withExtraTag := workAfterFetchNew + "\nrefs/tags/unexpected-tag " + baseOID
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", workBeforeFetch, withExtraTag); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/tags/unexpected-tag"`) {
		t.Fatalf("expected extra tag refs/tags/unexpected-tag to fail afterRetry, got: %v", err)
	}
	bareWithExtraBranch := bareBeforeFetch + "\nrefs/heads/unexpected-bare " + baseOID
	if err := fix.checkBareRefsForPhase("FetchDisconnectRetry/afterFault", bareBeforeFetch, bareWithExtraBranch); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/heads/unexpected-bare"`) {
		t.Fatalf("expected extra bare branch to fail, got: %v", err)
	}
	bareWithExtraTag := bareBeforeFetch + "\nrefs/tags/unexpected-bare-tag " + baseOID
	if err := fix.checkBareRefsForPhase("FetchDisconnectRetry/afterRetry", bareBeforeFetch, bareWithExtraTag); err == nil || !strings.Contains(err.Error(), `unexpected extra ref "refs/tags/unexpected-bare-tag"`) {
		t.Fatalf("expected extra bare tag to fail, got: %v", err)
	}

	// Deletion or OID mutation of existing non-target refs in work and bare.
	workDeletedKeep := formatExactRefMap(map[string]string{
		"refs/heads/main":               baseOID,
		"refs/remotes/origin/main":      wantNewOID,
		"refs/remotes/origin/peer-keep": peerKeepOID,
		"refs/tags/v0.1-keep":           baseOID,
		"refs/tags/v1.0-fetch":          wantNewOID,
	})
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", workBeforeFetch, workDeletedKeep); err == nil || !strings.Contains(err.Error(), `non-target ref "refs/heads/local-keep"`) {
		t.Fatalf("expected deleted work non-target ref to fail, got: %v", err)
	}
	workMutatedKeepTag := formatExactRefMap(map[string]string{
		"refs/heads/local-keep":         baseOID,
		"refs/heads/main":               baseOID,
		"refs/remotes/origin/main":      wantNewOID,
		"refs/remotes/origin/peer-keep": peerKeepOID,
		"refs/tags/v0.1-keep":           rogueOID,
		"refs/tags/v1.0-fetch":          wantNewOID,
	})
	if err := fix.checkWorkRefsForPhase("FetchDisconnectRetry/afterRetry", workBeforeFetch, workMutatedKeepTag); err == nil || !strings.Contains(err.Error(), `non-target ref "refs/tags/v0.1-keep" mutated`) {
		t.Fatalf("expected mutated work non-target tag to fail, got: %v", err)
	}
	bareDeletedTag := formatExactRefMap(map[string]string{
		"refs/heads/main":      wantNewOID,
		"refs/heads/peer-keep": peerKeepOID,
		"refs/tags/v1.0-fetch": wantNewOID,
	})
	if err := fix.checkBareRefsForPhase("FetchDisconnectRetry/afterRetry", bareBeforeFetch, bareDeletedTag); err == nil || !strings.Contains(err.Error(), `non-target ref "refs/tags/v0.1-keep"`) {
		t.Fatalf("expected deleted bare non-target tag to fail, got: %v", err)
	}
	bareMutatedPeer := formatExactRefMap(map[string]string{
		"refs/heads/main":      wantNewOID,
		"refs/heads/peer-keep": rogueOID,
		"refs/tags/v0.1-keep":  baseOID,
		"refs/tags/v1.0-fetch": wantNewOID,
	})
	if err := fix.checkBareRefsForPhase("FetchDisconnectRetry/afterRetry", bareBeforeFetch, bareMutatedPeer); err == nil || !strings.Contains(err.Error(), `non-target ref "refs/heads/peer-keep" mutated`) {
		t.Fatalf("expected mutated bare peer-keep to fail, got: %v", err)
	}
}
